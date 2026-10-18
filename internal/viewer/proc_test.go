// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test: it is what the tests below start as their "browser", in the way the standard library tests
// its own process handling. With CS_HELPER unset it does nothing.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("CS_HELPER") {
	case "sleeper":
		time.Sleep(time.Hour)
	case "quick":
		os.Exit(0)
	case "parent":
		// Like Edge: start the real browser, tell where it is, and exit at once.
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestHelperProcess") //nolint:gosec // this test binary
		cmd.Env = append(os.Environ(), "CS_HELPER=sleeper")
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(os.Getenv("CS_HELPER_PIDFILE"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		os.Exit(0)
	}
}

func helperArgs() []string { return []string{"-test.run=TestHelperProcess"} }

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestKillEndsTheWholeTreeEvenWhenTheFirstProcessIsGone(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("CS_HELPER", "parent")
	t.Setenv("CS_HELPER_PIDFILE", pidfile)
	p, err := startProcess(os.Args[0], helperArgs())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })

	var child int
	waitFor(t, "the child's number", 15*time.Second, func() bool {
		b, err := os.ReadFile(pidfile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	if !processAlive(child) {
		t.Fatal("the child is not running")
	}
	// The first process has exited; the tree has not. A wait on the first process alone would say the window was closed.
	time.Sleep(700 * time.Millisecond)
	select {
	case <-p.Done():
		t.Fatal("Done closed while a process of the tree was still running")
	default:
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done did not close after Kill")
	}
	waitFor(t, "the child to end", 10*time.Second, func() bool { return !processAlive(child) })
	if err := p.Kill(); err != nil { // and a second Kill is fine
		t.Errorf("a second Kill: %v", err)
	}
}

func TestDoneClosesWhenTheWholeTreeEndsByItself(t *testing.T) {
	t.Setenv("CS_HELPER", "quick")
	p, err := startProcess(os.Args[0], helperArgs())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("Done did not close for a program that has ended")
	}
	if err := p.Kill(); err != nil {
		t.Errorf("Kill after the end: %v", err)
	}
}

func TestAProgramThatIsNotThereCannotBeStarted(t *testing.T) {
	if _, err := startProcess(filepath.Join(t.TempDir(), "no-such-browser"), nil); err == nil {
		t.Error("started a program that does not exist")
	}
}

func TestAWindowEndsItsWholeTreeAndRemovesItsProfile(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("CS_HELPER", "parent")
	t.Setenv("CS_HELPER_PIDFILE", pidfile)
	profile := filepath.Join(t.TempDir(), "viewer-profile")
	w, err := Launch(LaunchOptions{
		Mode: ModeWindow, URL: "http://127.0.0.1:43211/auth?t=x", ProfileDir: profile, Browser: os.Args[0],
		Start: func(path string, _ []string) (Process, error) { return startProcess(path, helperArgs()) },
	})
	if err != nil || w.Kind != "app" {
		t.Fatalf("%+v, %v", w, err)
	}
	var child int
	waitFor(t, "the child's number", 15*time.Second, func() bool {
		b, err := os.ReadFile(pidfile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	// A file in the profile, held by nothing: the profile is removed with the window.
	if err := os.WriteFile(filepath.Join(profile, "Cookies"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the child to end", 10*time.Second, func() bool { return !processAlive(child) })
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("the profile is still there: %v", err)
	}
	select {
	case <-w.Closed():
	default:
		t.Error("Closed is not closed")
	}
}
