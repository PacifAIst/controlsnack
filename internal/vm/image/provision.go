// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack/internal/vm/qmp"
)

// Provisioning timeouts (PLAN.md section 7.5, step 5); under tcg they are multiplied by tcgFactor.
const (
	DefaultProvisionTimeout = 30 * time.Minute
	// DefaultProvisionIdle is how long the console may stay silent. apt prints all the time, so a silent console
	// means the VM is hung.
	DefaultProvisionIdle = 10 * time.Minute
	tcgFactor            = 4
	// connectTimeout is how long QEMU has to open its control channel (it then waits for the build to connect).
	connectTimeout = time.Minute
)

// ProvisionRun describes one provisioning boot.
type ProvisionRun struct {
	Install   qemu.Install
	Accel     string // kvm, whpx, hvf or tcg, as resolved by the caller
	CPUs      int
	MemoryMiB int
	Disk      string // the provisioning overlay
	Seed      string // the NoCloud ISO
	Dir       string // the build directory: serial.log and the UEFI variable store go here

	Timeout time.Duration // default DefaultProvisionTimeout (x4 under tcg)
	Idle    time.Duration // default DefaultProvisionIdle (x4 under tcg)
	Poll    time.Duration // how often the console log is read; default 250 ms
	// ConnectTimeout is how long QEMU has to come up far enough for the control channel to answer (default one minute, x4 under tcg).
	ConnectTimeout time.Duration
	// Progress receives the text of every CS-PROVISION line as it appears.
	Progress func(text string)
}

// RunProvision boots the provisioning VM, follows its serial console and returns when the VM has powered off. It
// succeeds only if the VM said "done" and exited cleanly; anything else is an error that carries the last lines of
// the console and of QEMU's own output.
func RunProvision(ctx context.Context, r ProvisionRun) error {
	factor := time.Duration(1)
	if r.Accel == qemu.AccelTCG {
		factor = tcgFactor
	}
	timeout, idle := r.Timeout, r.Idle
	if timeout <= 0 {
		timeout = DefaultProvisionTimeout
	}
	if idle <= 0 {
		idle = DefaultProvisionIdle
	}
	timeout, idle = timeout*factor, idle*factor
	connect := r.ConnectTimeout
	if connect <= 0 {
		connect = connectTimeout
	}
	progress := r.Progress
	if progress == nil {
		progress = func(string) {}
	}

	sockDir, cleanSock, err := qemu.NewSocketDir()
	if err != nil {
		return err
	}
	defer cleanSock()
	eps, err := qemu.AllocateEndpoints(ctx, runtime.GOOS, sockDir, "qmp0")
	if err != nil {
		return err
	}
	serial := filepath.Join(r.Dir, "serial.log")
	if err := os.WriteFile(serial, nil, 0o600); err != nil {
		return err
	}
	vars := ""
	if r.Install.Arch == qemu.ArchAarch64 {
		fw, err := r.Install.RequireFirmware()
		if err != nil {
			return err
		}
		vars = filepath.Join(r.Dir, "vars.fd")
		if err := copyFile(fw.Vars, vars); err != nil {
			return fmt.Errorf("copying the UEFI variable store: %w", err)
		}
	}
	argv, err := qemu.ProvisionArgv(qemu.ProvisionSpec{
		Install: r.Install, Accel: r.Accel, Name: "provision", CPUs: r.CPUs, MemoryMiB: r.MemoryMiB,
		Disk: r.Disk, Seed: r.Seed, Vars: vars, Serial: serial, QMP: eps[0],
		AllowedDirs: []string{r.Dir, sockDir, filepath.Dir(r.Seed)},
	})
	if err != nil {
		return err
	}

	proc, err := qemu.Start(ctx, argv, qemu.StartOptions{Dir: r.Dir})
	if err != nil {
		return err
	}
	defer func() { _ = proc.Kill() }()

	// QEMU holds still until the controller connects to its control channel (wait=on).
	dctx, dcancel := context.WithTimeout(ctx, connect*factor)
	network, addr, err := eps[0].NetAddr()
	if err != nil {
		dcancel()
		return err
	}
	// A QEMU that died at startup (a refused option, a missing file) must not be waited for until the timeout.
	go func() {
		select {
		case <-proc.Exited():
			dcancel()
		case <-dctx.Done():
		}
	}()
	q, err := qmp.Dial(dctx, network, addr, qmp.DialOptions{})
	dcancel()
	if err != nil {
		select {
		case <-proc.Exited():
			return fmt.Errorf("QEMU exited before it could be reached: %s", tailExcerpt(proc.Output()))
		default:
		}
		return fmt.Errorf("connecting to the provisioning VM: %w", err)
	}
	defer q.Close()

	// Follow the console.
	tail := NewLineTail(40)
	var (
		mu       sync.Mutex
		last     string // the last progress text
		failedAt time.Time
		sawDone  atomic.Bool
	)
	fctx, fcancel := context.WithCancel(ctx)
	followed := make(chan struct{})
	go func() {
		defer close(followed)
		_ = Follow(fctx, serial, r.Poll, func(line string) {
			tail.Add(line)
			p, ok := ParseProgress(line)
			if !ok {
				return
			}
			mu.Lock()
			last = p.Text
			if p.Failed && failedAt.IsZero() {
				failedAt = time.Now()
			}
			mu.Unlock()
			if p.Done {
				sawDone.Store(true)
			}
			progress(p.Text)
		})
	}()

	start := time.Now()
	lastGrowth, lastSize := start, int64(0)
	check := time.NewTicker(time.Second)
	defer check.Stop()
	var reason string
wait:
	for {
		select {
		case <-proc.Exited():
			break wait
		case <-ctx.Done():
			reason = "interrupted"
			_ = proc.Kill()
			<-proc.Exited()
			break wait
		case now := <-check.C:
			// select picks at random among ready cases: a VM that powered off just as a limit ran out has exited, not hung.
			select {
			case <-proc.Exited():
				break wait
			default:
			}
			if fi, err := os.Stat(serial); err == nil && fi.Size() != lastSize {
				lastSize, lastGrowth = fi.Size(), now
			}
			mu.Lock()
			failed := !failedAt.IsZero() && now.Sub(failedAt) > failedGrace
			mu.Unlock()
			switch {
			case failed:
				reason = "a provisioning step failed and the VM did not power off"
			case now.Sub(start) > timeout:
				reason = fmt.Sprintf("provisioning took longer than %s", timeout)
			case now.Sub(lastGrowth) > idle:
				reason = fmt.Sprintf("the console was silent for %s: the VM is hung", idle)
			}
			if reason != "" {
				_ = proc.Kill()
				<-proc.Exited()
				break wait
			}
		}
	}
	fcancel()
	<-followed

	if reason == "interrupted" {
		return ctx.Err()
	}
	code, _ := proc.Result()
	if reason == "" && code == 0 && sawDone.Load() {
		return nil
	}
	mu.Lock()
	lastText := last
	mu.Unlock()
	var b strings.Builder
	switch {
	case reason != "":
		b.WriteString(reason)
	case code != 0:
		fmt.Fprintf(&b, "QEMU exited with status %d", code)
	default:
		b.WriteString("the VM powered off without saying it was done")
	}
	if lastText != "" {
		fmt.Fprintf(&b, " (last step: %s)", lastText)
	}
	b.WriteString("\nlast lines of the console:")
	lines := tail.Lines()
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	for _, l := range lines {
		b.WriteString("\n  | " + l)
	}
	if out := tailExcerpt(proc.Output()); out != "" {
		b.WriteString("\nQEMU said: " + out)
	}
	return errors.New(b.String())
}

func tailExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 1024 {
		s = "..." + s[len(s)-1024:]
	}
	return s
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // a firmware file found by discovery
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the build directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// failedGrace is how long a VM that has reported FAILED gets to power itself off before it is killed. It is a
// variable only so that tests need not wait it out.
var failedGrace = 15 * time.Second
