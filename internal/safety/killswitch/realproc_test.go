// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

// TestHelperProcess is the body of the child these tests start, standing in for QEMU.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("CONTROLSNACK_TEST_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

// osProcess adapts an exec.Cmd to the QEMU interface the way the VM supervisor will.
type osProcess struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func startOSProcess(t *testing.T) *osProcess {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$") //nolint:gosec // re-executes the test binary
	cmd.Env = append(os.Environ(), "CONTROLSNACK_TEST_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &osProcess{cmd: cmd, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return p
}

func (p *osProcess) Kill() error             { return p.cmd.Process.Kill() }
func (p *osProcess) Exited() <-chan struct{} { return p.exited }

func TestKillingARealProcessIsFast(t *testing.T) {
	proc := startOSProcess(t)
	pid := proc.cmd.Process.Pid
	pi := platform.OSProcesses{}
	if !pi.Alive(pid) {
		t.Fatal("the stand-in for QEMU must be running")
	}
	var canceled bool
	k := New(Options{Cancel: func() { canceled = true }})
	k.SetQEMU(proc)

	res := k.Trigger(SourceButton, "real process")
	if !res.QEMUKilled || res.QEMUErr != nil {
		t.Fatalf("%+v", res)
	}
	if res.QEMUGoneAfter > 500*time.Millisecond {
		t.Errorf("trigger to QEMU gone took %v; the target of PLAN.md section 6.8 is under 500 ms", res.QEMUGoneAfter)
	}
	t.Logf("trigger -> process gone: %v", res.QEMUGoneAfter)
	if !canceled {
		t.Error("the root context must be canceled")
	}
	if pi.Alive(pid) {
		t.Error("the process must be gone")
	}
}

func TestControlClientEndsARealOrphan(t *testing.T) {
	// The controller died long ago; its QEMU (a real process here) is still running and must be ended on the next
	// start, identified by pid and start time together.
	proc := startOSProcess(t)
	pid := proc.cmd.Process.Pid
	pi := platform.OSProcesses{}
	start, err := pi.StartTime(pid)
	if err != nil || start == "" {
		t.Skipf("no process start times on this platform (%v)", err)
	}
	reg := NewRegistry(t.TempDir(), pi)
	e := entry("s_orphan", 2147483000) // a controller pid that cannot exist
	e.QEMUPID, e.QEMUStarted = pid, start
	if _, _, err := reg.Acquire(e); err != nil {
		t.Fatal(err)
	}
	reaped, err := reg.ReapStale()
	if err != nil || reaped == nil || !reaped.KilledQEMU {
		t.Fatalf("%v %+v", err, reaped)
	}
	select {
	case <-proc.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphaned process was not ended")
	}
}
