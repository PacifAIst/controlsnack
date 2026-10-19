// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package qemu

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

// reaperHelper is the `reaper` mode of TestProcessHelper: what the hidden `controlsnack __reaper` command does.
func reaperHelper(args []string) {
	// args: --parent N -- argv...
	parent, _ := strconv.Atoi(args[1])
	code := RunReaper(parent, args[3:], os.NewFile(3, "report"), os.Stdout, os.Stderr)
	os.Exit(code)
}

// dispatchReaper makes the test binary the reaper when it is started as one. On macOS every start goes through the reaper, as
// `<this executable> __reaper --parent N -- argv...`, which in the program is the hidden command of that name; in a test binary
// nothing answered to it, and every test that starts a process there failed.
func dispatchReaper() {
	if len(os.Args) > 1 && os.Args[1] == ReaperCommand {
		reaperHelper(os.Args[2:])
	}
}

func reaperOpts() StartOptions {
	return StartOptions{ForceReaper: true, ReaperCmd: helperArgs("reaper")}
}

func TestReaperRunsTheCommandAndPassesItsStatusOn(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("print"), reaperOpts())
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 10*time.Second)
	if out := p.Output(); !strings.Contains(out, "out-line") || !strings.Contains(out, "err-line") {
		t.Errorf("the command's output passes through the reaper: %q", out)
	}
	if code, _ := p.Result(); code != 7 {
		t.Errorf("the reaper ends with the command's status, got %d", code)
	}
	if p.pid == p.cmd.Process.Pid {
		t.Error("the controller must hold QEMU's pid, not the reaper's")
	}
}

func TestKillUnderTheReaperEndsQEMUItself(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("sleep"), reaperOpts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	pi := platform.OSProcesses{}
	qemuPid, reaperPid := p.Pid(), p.cmd.Process.Pid
	if qemuPid == reaperPid || !pi.Alive(qemuPid) || !pi.Alive(reaperPid) {
		t.Fatalf("qemu %d reaper %d", qemuPid, reaperPid)
	}
	start := time.Now()
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 5*time.Second)
	t.Logf("Kill -> reaper and QEMU gone: %v", time.Since(start).Round(time.Millisecond))
	if pi.Alive(qemuPid) {
		t.Error("QEMU itself must be gone, not only the wrapper")
	}
}

func TestReaperReportsAProgramThatCannotStart(t *testing.T) {
	_, err := Start(context.Background(), []string{"/nonexistent/qemu-system-x86_64"}, reaperOpts())
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("%v", err)
	}
}

func TestReaperRefusesAnOrphanedStart(t *testing.T) {
	var report strings.Builder
	// The pid claimed as parent is not this process's parent: the controller is already gone.
	code := RunReaper(os.Getppid()+1, []string{"/bin/true"}, &report, os.Stdout, os.Stderr)
	if code == 0 || !strings.Contains(report.String(), "error=") {
		t.Errorf("%d %q", code, report.String())
	}
	report.Reset()
	if code := RunReaper(os.Getppid(), nil, &report, os.Stdout, os.Stderr); code == 0 || !strings.Contains(report.String(), "error=") {
		t.Errorf("no command: %d %q", code, report.String())
	}
}
