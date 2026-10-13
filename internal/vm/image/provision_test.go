// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// TestMain lets the test binary play QEMU: RunProvision starts Install.System with a real QEMU command line, and a
// command line that names the VM "controlsnack-provision" is answered by fakeQEMU instead of by the tests.
func TestMain(m *testing.M) {
	if i := slices.Index(os.Args, "controlsnack-provision"); i > 0 && os.Args[i-1] == "-name" {
		os.Exit(fakeQEMU(os.Args[1:]))
	}
	os.Exit(m.Run())
}

var (
	reSerial  = regexp.MustCompile(`file:(.+)$`)
	reQMPPath = regexp.MustCompile(`path=([^,]+)`)
	reQMPPort = regexp.MustCompile(`host=127\.0\.0\.1,port=(\d+)`)
)

// fakeQEMU behaves as the provisioning QEMU: it opens the control channel, waits for the controller (wait=on),
// writes the console log and exits, as the scenario in the first line of the disk file says.
func fakeQEMU(args []string) int {
	var serial, qmpNet, qmpAddr, disk string
	for i, a := range args {
		if i+1 >= len(args) {
			break
		}
		next := args[i+1]
		switch a {
		case "-serial":
			if m := reSerial.FindStringSubmatch(next); m != nil {
				serial = m[1]
			}
		case "-chardev":
			if strings.HasPrefix(next, "socket,id=qmp0") {
				if m := reQMPPath.FindStringSubmatch(next); m != nil {
					qmpNet, qmpAddr = "unix", m[1]
				} else if m := reQMPPort.FindStringSubmatch(next); m != nil {
					qmpNet, qmpAddr = "tcp", "127.0.0.1:"+m[1]
				}
			}
		case "-drive":
			if strings.Contains(next, "format=qcow2") {
				for _, kv := range strings.Split(next, ",") {
					if v, ok := strings.CutPrefix(kv, "file="); ok {
						disk = v
					}
				}
			}
		}
	}
	data, _ := os.ReadFile(disk)
	scenario := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])

	line := func(s string) {
		f, err := os.OpenFile(serial, os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, _ = f.WriteString(s + "\r\n")
			_ = f.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if scenario == "exit-early" {
		fmt.Fprintln(os.Stderr, "qemu-system-x86_64: boom: cannot do that")
		return 1
	}
	if scenario == "noqmp" {
		time.Sleep(time.Minute)
		return 0
	}

	// wait=on: nothing proceeds until the controller has connected to the control channel.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), qmpNet, qmpAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake qemu: cannot listen:", err)
		return 2
	}
	conn, err := ln.Accept()
	if err != nil {
		return 2
	}
	go func() {
		fmt.Fprintln(conn, `{"QMP": {"version": {"qemu": {"micro": 0, "minor": 0, "major": 8}, "package": "fake"}, "capabilities": []}}`)
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			var req struct{ ID json.RawMessage }
			_ = json.Unmarshal(sc.Bytes(), &req)
			fmt.Fprintf(conn, `{"return": {}, "id": %s}`+"\n", req.ID)
		}
	}()

	switch scenario {
	case "ok":
		line("[    0.000000] Linux version 6.12 (kernel noise)")
		line("CS-PROVISION: start (x86_64, Debian GNU/Linux 13 (trixie))")
		line("CS-PROVISION: installing packages (32 requested)")
		line("CS-PROVISION: done")
		return 0
	case "failed":
		line("CS-PROVISION: start")
		line("CS-PROVISION: installing packages (32 requested)")
		line("CS-PROVISION: FAILED at line 9: apt-get update")
		time.Sleep(150 * time.Millisecond)
		return 0 // the script powers the VM off
	case "failed-hang":
		line("CS-PROVISION: FAILED at line 9: apt-get update")
		time.Sleep(time.Minute)
	case "silent":
		time.Sleep(time.Minute)
	case "forever":
		for {
			line("CS-PROVISION: still going")
		}
	case "crash":
		line("CS-PROVISION: start")
		return 3
	case "nodone":
		line("CS-PROVISION: start")
		return 0
	}
	return 0
}

// provisionRun prepares a build directory whose disk file names the scenario.
func provisionRun(t *testing.T, scenario string) (ProvisionRun, *[]string) {
	t.Helper()
	dir := t.TempDir()
	disk := filepath.Join(dir, "work.qcow2")
	if err := os.WriteFile(disk, []byte(scenario+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedISO := filepath.Join(dir, "seed.iso")
	if err := os.WriteFile(seedISO, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	return ProvisionRun{
		Install: qemu.Install{Arch: qemu.ArchX86_64, System: os.Args[0], Img: "unused"},
		Accel:   qemu.AccelKVM, CPUs: 2, MemoryMiB: 2048, Disk: disk, Seed: seedISO, Dir: dir,
		Timeout: 5 * time.Second, Idle: 5 * time.Second, Poll: 10 * time.Millisecond, ConnectTimeout: 5 * time.Second,
		Progress: func(s string) { mu.Lock(); seen = append(seen, s); mu.Unlock() },
	}, &seen
}

func TestProvisionSucceeds(t *testing.T) {
	r, seen := provisionRun(t, "ok")
	if err := RunProvision(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	want := []string{"start (x86_64, Debian GNU/Linux 13 (trixie))", "installing packages (32 requested)", "done"}
	if !slices.Equal(*seen, want) {
		t.Errorf("progress %q, want %q", *seen, want)
	}
	if data, err := os.ReadFile(filepath.Join(r.Dir, "serial.log")); err != nil || !strings.Contains(string(data), "kernel noise") {
		t.Errorf("the console log stays in the build directory: %v", err)
	}
}

func TestProvisionFailures(t *testing.T) {
	defer func(d time.Duration) { failedGrace = d }(failedGrace)
	failedGrace = 2500 * time.Millisecond // the first check is a second in; a race-enabled test binary starts slowly

	tests := []struct {
		scenario string
		tune     func(*ProvisionRun)
		want     []string
	}{
		{"failed", nil, []string{"powered off without saying it was done", "last step: FAILED at line 9: apt-get update"}},
		{"failed-hang", nil, []string{"a provisioning step failed and the VM did not power off", "last step: FAILED at line 9"}},
		{"silent", func(r *ProvisionRun) { r.Idle = 600 * time.Millisecond }, []string{"console was silent for", "the VM is hung"}},
		{"forever", func(r *ProvisionRun) { r.Timeout = 700 * time.Millisecond }, []string{"took longer than", "last step: still going"}},
		{"crash", nil, []string{"QEMU exited with status 3", "last step: start"}},
		{"nodone", nil, []string{"powered off without saying it was done"}},
		{"exit-early", nil, []string{"QEMU exited before it could be reached", "boom: cannot do that"}},
		{"noqmp", func(r *ProvisionRun) { r.ConnectTimeout = 400 * time.Millisecond }, []string{"connecting to the provisioning VM"}},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			r, _ := provisionRun(t, tt.scenario)
			if tt.tune != nil {
				tt.tune(&r)
			}
			start := time.Now()
			err := RunProvision(context.Background(), r)
			if err == nil {
				t.Fatal("accepted")
			}
			msg := err.Error()
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message lacks %q:\n%s", w, msg)
				}
			}
			if time.Since(start) > 20*time.Second {
				t.Errorf("took %v", time.Since(start))
			}
		})
	}
}

func TestProvisionErrorShowsTheConsole(t *testing.T) {
	r, _ := provisionRun(t, "failed")
	err := RunProvision(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "last lines of the console:") || !strings.Contains(err.Error(), "  | CS-PROVISION: installing packages") {
		t.Errorf("%v", err)
	}
}

func TestProvisionCanceled(t *testing.T) {
	r, _ := provisionRun(t, "forever")
	r.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	err := RunProvision(ctx, r)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 10*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

func TestProvisionTimeoutsGrowUnderSoftwareEmulation(t *testing.T) {
	// 700 ms of silence ends a hardware-accelerated run; under tcg the same setting means 2.8 s.
	r, _ := provisionRun(t, "silent")
	r.Idle = 700 * time.Millisecond
	r.Accel = qemu.AccelTCG
	start := time.Now()
	err := RunProvision(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "silent for 2.8s") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) < 2500*time.Millisecond {
		t.Errorf("gave up after %v", time.Since(start))
	}
}

func TestProvisionRefusesWhatCannotBeBuilt(t *testing.T) {
	r, _ := provisionRun(t, "ok")
	r.Install.Arch = qemu.ArchAarch64 // no firmware known
	if err := RunProvision(context.Background(), r); err == nil || !strings.Contains(err.Error(), "UEFI") {
		t.Errorf("%v", err)
	}
	r, _ = provisionRun(t, "ok")
	r.Accel = "auto"
	if err := RunProvision(context.Background(), r); err == nil || !strings.Contains(err.Error(), "unknown accelerator") {
		t.Errorf("%v", err)
	}
	r, _ = provisionRun(t, "ok")
	r.Install.System = "qemu-system-x86_64" // not an absolute path
	if err := RunProvision(context.Background(), r); err == nil {
		t.Error("a program found through PATH is not allowed")
	}
}
