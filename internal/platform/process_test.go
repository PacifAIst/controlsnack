// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestHelperProcess is not a test: it is the body of the child process the tests below start.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("CONTROLSNACK_TEST_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func startChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$") //nolint:gosec // re-executes the test binary
	cmd.Env = append(os.Environ(), "CONTROLSNACK_TEST_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

func TestOwnProcess(t *testing.T) {
	var pi ProcessInspector = OSProcesses{}
	pid := os.Getpid()
	if !pi.Alive(pid) {
		t.Fatal("this process must be alive")
	}
	start, err := pi.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		if start == "" {
			t.Fatalf("%s must report start times", runtime.GOOS)
		}
	}
	again, _ := pi.StartTime(pid)
	if start != again {
		t.Errorf("the start time must be stable: %q then %q", start, again)
	}
	if start != "" && !SameProcess(pi, pid, start) {
		t.Error("SameProcess must recognize this process by its own start time")
	}
	if SameProcess(pi, pid, "") {
		t.Error("an unknown recorded start time must never match: nothing is killed on a PID alone")
	}
	if SameProcess(pi, pid, "linux:1") {
		t.Error("a different start time is a different process")
	}
}

func TestChildProcessLifecycle(t *testing.T) {
	var pi ProcessInspector = OSProcesses{}
	child := startChild(t)
	pid := child.Process.Pid
	if !pi.Alive(pid) {
		t.Fatal("the child must be alive")
	}
	childStart, err := pi.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	if own, _ := pi.StartTime(os.Getpid()); childStart != "" && childStart == own {
		t.Error("two different processes must not share a start time")
	}
	if err := pi.Kill(pid); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	done := make(chan struct{})
	go func() { _, _ = child.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the killed child did not exit")
	}
	if pi.Alive(pid) && runtime.GOOS != "windows" {
		// On Windows a handle held by Wait may keep the PID visible for a moment; elsewhere it is gone.
		t.Error("a killed and reaped child must not be alive")
	}
	if err := pi.Kill(pid); err == nil {
		t.Log("killing a reaped pid reported success (the pid may have been reused or the OS keeps it briefly)")
	}
}

func TestNonexistentProcess(t *testing.T) {
	var pi ProcessInspector = OSProcesses{}
	for _, pid := range []int{0, -1, -42} {
		if pi.Alive(pid) {
			t.Errorf("pid %d cannot be alive", pid)
		}
		if _, err := pi.StartTime(pid); !errors.Is(err, ErrNoSuchProcess) {
			t.Errorf("StartTime(%d) = %v", pid, err)
		}
		if err := pi.Kill(pid); !errors.Is(err, ErrNoSuchProcess) {
			t.Errorf("Kill(%d) = %v", pid, err)
		}
	}
}
