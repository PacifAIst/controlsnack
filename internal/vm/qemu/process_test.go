// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
)

var _ killswitch.QEMU = (*Process)(nil) // the supervisor is what the kill switch kills

// helperArgs makes the test binary behave as a stand-in for QEMU: TestProcessHelper reads the mode after "--".
func helperArgs(mode string, extra ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestProcessHelper$", "--", mode}, extra...)
}

// TestProcessHelper is not a test: it is the body of the processes the tests below start. It does nothing in a
// normal run.
func TestProcessHelper(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || i+1 >= len(os.Args) {
		return
	}
	mode, rest := os.Args[i+1], os.Args[i+2:]
	switch mode {
	case "print":
		fmt.Fprintln(os.Stdout, "out-line")
		fmt.Fprintln(os.Stderr, "err-line")
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exit-on-file": // exits cleanly once the named file exists: a stand-in for QMP quit
		for {
			if _, err := os.Stat(rest[0]); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "flood":
		chunk := strings.Repeat("x", 1023) + "\n"
		for range 300 { // 300 KiB
			fmt.Fprint(os.Stdout, chunk)
		}
		fmt.Fprintln(os.Stdout, "THE-END")
		os.Exit(0)
	case "env":
		fmt.Fprint(os.Stdout, strings.Join(os.Environ(), "\n"))
		os.Exit(0)
	case "pwd":
		wd, _ := os.Getwd()
		fmt.Fprint(os.Stdout, wd)
		os.Exit(0)
	case "controller", "controller-reaper":
		// A controller: starts a long-lived child through the supervisor, tells the test its pid, and waits to be killed.
		opt := StartOptions{}
		if mode == "controller-reaper" {
			opt.ForceReaper = true
			opt.ReaperCmd = helperArgs("reaper")
		}
		p, err := Start(context.Background(), helperArgs("sleep"), opt)
		if err != nil {
			fmt.Fprintln(os.Stdout, "error", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "pid", p.Pid())
		time.Sleep(time.Minute)
		os.Exit(0)
	case "reaper": // helperArgs("reaper") is the ReaperCmd prefix: the arguments that follow are --parent N -- argv...
		reaperHelper(rest)
	}
	os.Exit(9)
}

// --- starting, output, results ---------------------------------------------------------------------------------

func TestStartCollectsOutputAndExitStatus(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("print"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 10*time.Second)
	out := p.Output()
	if !strings.Contains(out, "out-line") || !strings.Contains(out, "err-line") {
		t.Errorf("stdout and stderr both go to the ring: %q", out)
	}
	code, err := p.Result()
	var exit *exec.ExitError
	if code != 7 || !errors.As(err, &exit) {
		t.Errorf("exit status %d, %v", code, err)
	}
	if p.Pid() <= 1 {
		t.Errorf("pid %d", p.Pid())
	}
	if err := p.Kill(); err != nil {
		t.Errorf("killing a process that has exited is not an error: %v", err)
	}
}

func TestOutputRingKeepsTheEnd(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("flood"), StartOptions{RingSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 10*time.Second)
	out := p.Output()
	if len(out) > 4096 || !strings.HasSuffix(out, "THE-END\n") {
		t.Errorf("the ring keeps the last 4096 bytes, got %d ending %q", len(out), out[max(0, len(out)-12):])
	}
}

func TestStartEnvironmentAndDirectory(t *testing.T) {
	t.Setenv("CS_SECRET_TOKEN", "must-not-leak")
	dir := t.TempDir()
	p, err := Start(context.Background(), helperArgs("env"), StartOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 10*time.Second)
	if env := p.Output(); strings.Contains(env, "CS_SECRET_TOKEN") || !strings.Contains(env, "LC_ALL=C") {
		t.Errorf("QEMU runs with a minimal environment: %q", env)
	}

	p, err = Start(context.Background(), helperArgs("env"), StartOptions{Env: []string{"ONLY=this", "SystemRoot=" + os.Getenv("SystemRoot")}})
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 10*time.Second)
	if env := p.Output(); !strings.Contains(env, "ONLY=this") || strings.Contains(env, "LC_ALL") {
		t.Errorf("an explicit environment replaces the default: %q", env)
	}

	p, err = Start(context.Background(), helperArgs("pwd"), StartOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 10*time.Second)
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(p.Output())); !strings.EqualFold(got, want) {
		t.Errorf("working directory %q, want %q", got, want)
	}
}

func TestStartRefusesWhatItCannotRun(t *testing.T) {
	ctx := context.Background()
	if _, err := Start(ctx, nil, StartOptions{}); err == nil {
		t.Error("empty command line")
	}
	if _, err := Start(ctx, []string{""}, StartOptions{}); err == nil {
		t.Error("empty program")
	}
	if _, err := Start(ctx, []string{"qemu-system-x86_64", "-S"}, StartOptions{}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("a program found through PATH is not allowed: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "no-such-qemu")
	if runtime.GOOS == "windows" {
		missing += ".exe"
	}
	if _, err := Start(ctx, []string{missing}, StartOptions{}); err == nil || !strings.Contains(err.Error(), "no-such-qemu") {
		t.Errorf("%v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Start(canceled, helperArgs("sleep"), StartOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}

// --- killing ---------------------------------------------------------------------------------------------------

func TestKillIsImmediateAndIdempotent(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("sleep"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	pi := platform.OSProcesses{}
	if !pi.Alive(p.Pid()) {
		t.Fatal("the process is running")
	}
	start := time.Now()
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, p, 5*time.Second)
	t.Logf("Kill -> Exited: %v", time.Since(start).Round(time.Millisecond))
	if pi.Alive(p.Pid()) {
		t.Error("the process must be gone")
	}
	if code, err := p.Result(); code == 0 || err == nil {
		t.Errorf("a killed process did not exit cleanly: %d %v", code, err)
	}
	if err := p.Kill(); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

func TestContextCancelKills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, helperArgs("sleep"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	cancel()
	waitExit(t, p, 5*time.Second)
}

func TestKillSwitchKillsTheSupervisedProcess(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("sleep"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	canceled := false
	k := killswitch.New(killswitch.Options{Cancel: func() { canceled = true }})
	k.SetQEMU(p)
	res := k.Trigger(killswitch.SourceCLI, "test")
	if !res.QEMUKilled || res.QEMUErr != nil || !canceled {
		t.Fatalf("%+v", res)
	}
	if res.QEMUGoneAfter > 500*time.Millisecond {
		t.Errorf("trigger -> QEMU gone: %v; the target of PLAN.md section 6.8 is under 500 ms", res.QEMUGoneAfter)
	}
	t.Logf("kill switch -> supervised process gone: %v", res.QEMUGoneAfter)
	if platform.OSProcesses.Alive(platform.OSProcesses{}, p.Pid()) {
		t.Error("the process must be gone")
	}
}

// --- stopping politely -----------------------------------------------------------------------------------------

func TestStopPrefersTheGracefulWay(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "quit")
	p, err := Start(context.Background(), helperArgs("exit-on-file", flag), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	start := time.Now()
	quit := func(context.Context) error { return os.WriteFile(flag, []byte("x"), 0o600) }
	if err := p.Stop(context.Background(), quit, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if code, err := p.Result(); code != 0 || err != nil {
		t.Errorf("QEMU quit by itself and exited cleanly: %d %v", code, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("a cooperative process is not made to wait out the grace period (%v)", time.Since(start))
	}
}

func TestStopKillsAfterTheGracePeriod(t *testing.T) {
	p, err := Start(context.Background(), helperArgs("sleep"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	called := false
	quit := func(context.Context) error { called = true; return errors.New("QMP is not answering") }
	start := time.Now()
	if err := p.Stop(context.Background(), quit, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("quit was not tried first")
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 5*time.Second {
		t.Errorf("killed after %v, want about the grace period", d)
	}
	select {
	case <-p.Exited():
	default:
		t.Error("the process must be gone when Stop returns")
	}
	// No quit function at all: straight to the kill after the grace period.
	p2, err := Start(context.Background(), helperArgs("sleep"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p2.Kill() })
	if err := p2.Stop(context.Background(), nil, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// A canceled context cuts the wait short.
	p3, err := Start(context.Background(), helperArgs("sleep"), StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p3.Kill() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	if err := p3.Stop(ctx, nil, time.Minute); err != nil || time.Since(start) > 5*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

// --- dying with the controller (the point of PLAN.md section 7.4) ----------------------------------------------

func TestChildDiesWithTheController(t *testing.T) {
	modes := []string{"controller"}
	if runtime.GOOS != "windows" {
		modes = append(modes, "controller-reaper") // the macOS mechanism, exercised on any Unix
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ctrl := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestProcessHelper$", "--", mode) //nolint:gosec // re-executes the test binary
			out, err := ctrl.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ctrl.Process.Kill(); _ = ctrl.Wait() })

			line, err := bufio.NewReader(out).ReadString('\n')
			if err != nil || !strings.HasPrefix(line, "pid ") {
				t.Fatalf("the controller said %q (%v)", line, err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid ")))
			if err != nil || pid <= 1 {
				t.Fatalf("%q", line)
			}
			pi := platform.OSProcesses{}
			t.Cleanup(func() { _ = pi.Kill(pid) })
			if !pi.Alive(pid) {
				t.Fatal("the child must be running while its controller lives")
			}

			_ = ctrl.Process.Kill() // the controller dies without any chance to clean up
			_ = ctrl.Wait()

			deadline := time.Now().Add(10 * time.Second)
			for pi.Alive(pid) {
				if time.Now().After(deadline) {
					t.Fatalf("pid %d survived its controller", pid)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

func waitExit(t *testing.T, p *Process, d time.Duration) {
	t.Helper()
	select {
	case <-p.Exited():
	case <-time.After(d):
		_ = p.Kill()
		t.Fatalf("the process did not exit within %v", d)
	}
}
