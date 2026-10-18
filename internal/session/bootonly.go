// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
	"github.com/PacifAIst/controlsnack-local/internal/vm"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/overlay"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// machine is the part of *vm.Machine the runner uses; tests substitute a fake.
type machine interface {
	Proc() vm.Process
	Report() vm.BootReport
	Screendump(ctx context.Context, path string) error
	StopAgent()
	// Agent is the connection to the agent, and Monitor QEMU's own channel.
	Agent() AgentLink
	Monitor() Monitor
	// Shutdown presses the guest's power button and waits for it to power off; an error says it did not in time.
	Shutdown(ctx context.Context) error
	// Close ends the VM in order: the guest is asked to stop, and QEMU is ended if it does not.
	Close(ctx context.Context) error
	// NetPort is the connection to the network port and the way to make another; nil without --allow-net.
	NetPort() (net.Conn, func(context.Context) (net.Conn, error))
}

// realMachine is a running *vm.Machine as the runner uses it.
type realMachine struct{ *vm.Machine }

func (m realMachine) Agent() AgentLink { return m.Client }
func (m realMachine) Monitor() Monitor { return m.QMP }
func (m realMachine) NetPort() (net.Conn, func(context.Context) (net.Conn, error)) {
	if m.Net == nil {
		return nil, nil
	}
	return m.Net, m.DialNet
}

// deps are the runner's connections to the machine it runs on.
type deps struct {
	dirs     func() (platform.Dirs, error)
	discover func(ctx context.Context, qemuDir string) (qemu.Install, error)
	image    func(dirs platform.Dirs, spec, arch string) (image.Image, error)
	agent    func(arch string) (agentbin.Agent, error)
	boot     func(ctx context.Context, o vm.BootOptions) (machine, error)
	// launch opens the viewer window (viewer.Launch on the real machine).
	launch func(viewer.LaunchOptions) (*viewer.Window, error)
	// library is the directory of kept VMs.
	library func(dirs platform.Dirs) overlay.Library
	// afterFunc starts the dead-man switch's timers (time.AfterFunc when nil), and assembled hears of the session once it is joined.
	afterFunc func(d time.Duration, f func()) (stop func() bool)
	assembled func(*Session)
	procs     platform.ProcessInspector
	pid       func() int
	random    io.Reader
	// models makes what finds the model (the real one reads the cache of earlier probes and asks the servers).
	models func(cfg config.Config, dirs platform.Dirs) ModelFinder
}

func realDeps() deps {
	return deps{
		dirs: platform.DefaultDirs,
		discover: func(ctx context.Context, dir string) (qemu.Install, error) {
			return qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{Dir: dir})
		},
		image: func(dirs platform.Dirs, spec, arch string) (image.Image, error) {
			store := image.Store{Dir: dirs.ImagesDir()}
			im, err := store.Resolve(spec, arch)
			if err != nil {
				return image.Image{}, err
			}
			return im, store.QuickCheck(im)
		},
		agent: agentbin.For,
		boot: func(ctx context.Context, o vm.BootOptions) (machine, error) {
			m, err := vm.Boot(ctx, o)
			if err != nil {
				return nil, err // not a typed nil inside the interface
			}
			return realMachine{m}, nil
		},
		launch:  viewer.Launch,
		library: func(dirs platform.Dirs) overlay.Library { return overlay.Library{Dir: dirs.VMsDir()} },
		procs:   platform.OSProcesses{},
		pid:     os.Getpid,
		random:  rand.Reader,
		models: func(cfg config.Config, dirs platform.Dirs) ModelFinder {
			return detect.New(detect.Options{CacheDir: dirs.Cache, NewClient: func(name string) *httpx.Client {
				c := httpx.New(name)
				if t := cfg.LLM.FirstTimeout.D(); t > 0 {
					c.First = t
				}
				if t := cfg.LLM.Timeout.D(); t > 0 {
					c.Timeout = t
				}
				return c
			}})
		},
	}
}

// hold is the end of `run --no-llm --viewer none` (P3-T11): the VM side of a session with no model and no viewer. The machine is
// up and the screen can be read; hold waits until the user stops it (Ctrl+C or `controlsnack kill`), the session timeout passes,
// or the guest shuts down. Whatever ends it goes through the kill switch.
func (r *runner) hold(l *live) error {
	ctx, killer, mach, cfg, rec, end, teardown := l.ctx, l.killer, l.mach, l.cfg, l.rec, l.end, l.teardown
	fmt.Fprintln(r.out, "Running. Press Ctrl+C to stop the VM, or run `controlsnack kill` from another terminal.")
	var timeout <-chan time.Time
	if t := cfg.Safety.Timeout.D(); t > 0 {
		timer := time.NewTimer(t)
		defer timer.Stop()
		timeout = timer.C
	}

	timedOut := false
	select {
	case <-ctx.Done(): // Ctrl+C, SIGTERM or SIGHUP, or a kill that has already canceled the context
	case <-timeout:
		timedOut = true
	case <-mach.Proc().Exited():
	}
	// Who ended it? The kill switch sets its flag before it kills QEMU, so a kill that came from outside
	// (`controlsnack kill`) is recognized even when QEMU's exit is seen before the context ends. Only a QEMU that went
	// away with no kill switch fired is the guest's own doing.
	if !killer.Killed() {
		select {
		case <-mach.Proc().Exited():
			code, _ := mach.Proc().Result()
			_ = teardown(ctx)
			var outcome error
			if code != 0 {
				outcome = fmt.Errorf("QEMU stopped by itself with status %d", code)
			} else {
				fmt.Fprintln(r.out, "The guest shut down.")
			}
			end(map[string]any{"qemu_status": code})
			return outcome
		default:
		}
		if timedOut {
			killer.Trigger(killswitch.SourceInternal, "session timeout")
		} else {
			killer.Trigger(killswitch.SourceSignal, "interrupted")
		}
	}
	<-killer.Done()
	res, _ := killer.Result()
	outcome := exitcode.New(exitcode.Killed, fmt.Errorf("stopped (%s)", res.Source))
	if timedOut && res.Source == killswitch.SourceInternal {
		outcome = exitcode.New(exitcode.Timeout, fmt.Errorf("the session timeout of %s passed", cfg.Safety.Timeout.D()))
	}

	if res.QEMUKilled {
		fmt.Fprintf(r.out, "The VM was stopped %d ms after the kill (%s).\n", res.QEMUGoneAfter.Milliseconds(), res.Source)
	}
	_ = rec(audit.Record{Kind: audit.KindLifecycle, Event: audit.EventKill, Actor: "system", Reason: res.Reason, Data: audit.ToRaw(map[string]any{
		"source": res.Source, "qemu_killed": res.QEMUKilled, "qemu_gone_ms": res.QEMUGoneAfter.Milliseconds(), "teardown_ms": res.TeardownTook.Milliseconds(),
	})})
	end(nil)
	if !res.QEMUKilled || res.QEMUErr != nil {
		cause := res.QEMUErr
		if cause == nil {
			cause = errors.New("the kill did not reach the VM")
		}
		return fmt.Errorf("the VM may still be running: %w", cause)
	}
	return outcome
}

// printReady tells the user what came up and proves the screen can be read.
func (r *runner) printReady(ctx context.Context, m machine, rep vm.BootReport, sessionDir string) {
	fmt.Fprintf(r.out, "Ready: agent %s (sha256 %.12s) running as %s, screen %dx%d.\n",
		rep.Agent.AgentVersion, rep.Agent.AgentSHA256, rep.Agent.User, rep.Agent.Display.W, rep.Agent.Display.H)
	fmt.Fprintf(r.out, "Boot with %s: QEMU running after %s, desktop up after %s, agent connected after %s.\n", rep.Accel.Chosen,
		rep.Timings.Launch.Round(time.Millisecond), rep.Timings.PortOpen.Round(time.Second), rep.Timings.Connected.Round(time.Second))
	shot := filepath.Join(sessionDir, "ready.png")
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := m.Screendump(sctx, shot); err != nil {
		fmt.Fprintf(r.out, "Warning: the screen could not be read through QMP: %v\n", err)
		return
	}
	f, err := os.Open(shot) //nolint:gosec // a file QEMU just wrote in the session directory
	if err != nil {
		fmt.Fprintf(r.out, "Warning: the screendump is missing: %v\n", err)
		return
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	fi, _ := f.Stat()
	if err != nil || fi == nil {
		fmt.Fprintf(r.out, "Warning: the screendump is not a PNG: %v\n", err)
		return
	}
	fmt.Fprintf(r.out, "Screendump works: %dx%d PNG, %s.\n", cfg.Width, cfg.Height, image.HumanSize(fi.Size()))
}

// terminalEvents prints the accelerator decisions (the audit log gets them through vm.AuditEvents).
type terminalEvents struct{ out io.Writer }

func (t terminalEvents) AccelSelected(a vm.AccelResult) {
	fmt.Fprintf(t.out, "  accelerator: %s\n", a.Chosen)
	if a.Chosen == vm.AccelTCG {
		fmt.Fprintln(t.out, "  WARNING: no hardware acceleration, so the guest runs slowly (see `controlsnack doctor`)")
	}
}

func (t terminalEvents) AccelFallback(f vm.Fallback) {
	fmt.Fprintf(t.out, "  WARNING: %s failed to start (%s); falling back to software emulation, which is slow\n", f.From, f.Reason)
}

func newID(r io.Reader, prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("making a session id: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

func qemuStart(p platform.ProcessInspector, pid int) string {
	s, _ := p.StartTime(pid)
	return s
}

// agentState tells the user when the agent connection changes after the first connect.
func (r *runner) agentState(s agentclient.State, err error) {
	switch s {
	case agentclient.StateDown:
		fmt.Fprintf(r.out, "  agent: connection lost (%v); reconnecting\n", err)
	case agentclient.StateUp:
		fmt.Fprintln(r.out, "  agent: connected")
	}
}

// vmName turns a session id (s_1a2b3c4d) into a name QEMU accepts: ids may contain underscores, VM names may not.
func vmName(id string) string { return strings.ReplaceAll(id, "_", "-") }

// runningMarker is the file that marks a session directory as owned by a running session.
const runningMarker = ".running"

// sweepRunDirs removes the directories of sessions that were killed without a chance to clean up (the marker is still
// there). Directories without a marker are what --keep left on purpose.
func sweepRunDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, runningMarker)); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
}
