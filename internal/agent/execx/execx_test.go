// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package execx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func needBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
}

func run(t *testing.T, command string, mod ...func(*Options)) Result {
	t.Helper()
	needBash(t)
	o := Options{Command: command, Dir: t.TempDir(), Grace: 300 * time.Millisecond}
	for _, m := range mod {
		m(&o)
	}
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("%q: %v", command, err)
	}
	return res
}

func TestOutputAndExitCodes(t *testing.T) {
	res := run(t, "echo hello; echo oops >&2")
	if res.Stdout != "hello\n" || res.Stderr != "oops\n" || res.ExitCode != 0 || res.Truncated || res.TimedOut {
		t.Errorf("%+v", res)
	}
	if res.Duration <= 0 {
		t.Error("the duration is measured")
	}
	for command, want := range map[string]int{"exit 7": 7, "exit 255": 255, "false": 1, "true": 0, "kill -TERM $$": 143, "kill -KILL $$": 137, "nosuchcommand_xyz": 127} {
		if got := run(t, command).ExitCode; got != want {
			t.Errorf("%q: exit code %d, want %d", command, got, want)
		}
	}
	// stderr is kept apart from stdout.
	res = run(t, "echo out; echo err >&2; echo out2")
	if res.Stdout != "out\nout2\n" || res.Stderr != "err\n" {
		t.Errorf("%+v", res)
	}
}

func TestEnvironment(t *testing.T) {
	res := run(t, `echo "$TERM|$PAGER|$GIT_PAGER|$DEBIAN_FRONTEND|$MARK"`, func(o *Options) { o.ExtraEnv = []string{"MARK=extra"} })
	if res.Stdout != "dumb|cat|cat|noninteractive|extra\n" {
		t.Errorf("%q", res.Stdout)
	}
	if res := run(t, "command -v ls"); res.ExitCode != 0 {
		t.Errorf("a login shell has a PATH: %+v", res)
	}
}

func TestWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	want, _ := filepath.EvalSymlinks(dir)
	res := run(t, "pwd -P", func(o *Options) { o.Dir = dir })
	if strings.TrimSpace(res.Stdout) != want {
		t.Errorf("%q, want %q", res.Stdout, want)
	}
	// Without a directory the default is used, or the home directory where that does not exist.
	res = run(t, "pwd -P", func(o *Options) { o.Dir = "" })
	if res.ExitCode != 0 || !strings.HasPrefix(res.Stdout, "/") {
		t.Errorf("%+v", res)
	}
	file := filepath.Join(dir, "afile")
	_ = os.WriteFile(file, nil, 0o600)
	for name, d := range map[string]string{"missing": filepath.Join(dir, "nope"), "relative": "work", "a file": file} {
		_, err := Run(context.Background(), Options{Command: "true", Dir: d})
		var bd *ErrBadDir
		if !errors.As(err, &bd) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestStdin(t *testing.T) {
	if res := run(t, "cat", func(o *Options) { o.Stdin = "hello from stdin\n" }); res.Stdout != "hello from stdin\n" {
		t.Errorf("%+v", res)
	}
	big := strings.Repeat("x", 64<<10)
	if res := run(t, "wc -c", func(o *Options) { o.Stdin = big }); strings.TrimSpace(res.Stdout) != "65536" {
		t.Errorf("%q", res.Stdout)
	}
	// Without input the command reads end-of-file at once instead of waiting for a keyboard that does not exist.
	start := time.Now()
	if res := run(t, "cat; echo done"); res.Stdout != "done\n" || time.Since(start) > 5*time.Second {
		t.Errorf("%+v after %v", res, time.Since(start))
	}
	// A command that never reads its input does not hang.
	if res := run(t, "echo ignored", func(o *Options) { o.Stdin = big }); res.Stdout != "ignored\n" {
		t.Errorf("%+v", res)
	}
}

func TestLongOutputKeepsTheHeadAndTheTail(t *testing.T) {
	res := run(t, "seq 1 100000")
	if !res.Truncated || res.TimedOut {
		t.Fatalf("%+v", res.Truncated)
	}
	if len(res.Stdout) < 64<<10 || len(res.Stdout) > 64<<10+80 {
		t.Errorf("a stream is capped near 64 KiB, got %d bytes", len(res.Stdout))
	}
	if !strings.HasPrefix(res.Stdout, "1\n2\n3\n") || !strings.HasSuffix(res.Stdout, "99999\n100000\n") {
		t.Errorf("head and tail: %q ... %q", res.Stdout[:12], res.Stdout[len(res.Stdout)-16:])
	}
	if !strings.Contains(res.Stdout, "bytes omitted") {
		t.Error("the cut says how much was left out")
	}
	idx := strings.Index(res.Stdout, "\n[... ")
	if idx < 48<<10-8 || idx > 48<<10+8 {
		t.Errorf("48 KiB of head came first: the marker is at %d", idx)
	}

	res = run(t, "echo short; seq 1 3000 >&2")
	if res.Stdout != "short\n" || res.Truncated || !strings.HasSuffix(res.Stderr, "3000\n") {
		t.Errorf("%+v", res)
	}
	if res = run(t, "head -c 1000 /dev/zero | tr '\\0' a", func(o *Options) { o.MaxOutput = 100 }); !res.Truncated || len(res.Stdout) > 100+60 {
		t.Errorf("%d bytes, truncated %v", len(res.Stdout), res.Truncated)
	}
	if res = run(t, "head -c 1000 /dev/zero | tr '\\0' a", func(o *Options) { o.MaxOutput = 1000 }); res.Truncated || len(res.Stdout) != 1000 {
		t.Errorf("exactly the cap is not truncated: %d %v", len(res.Stdout), res.Truncated)
	}
}

func TestOutputIsAlwaysValidUTF8(t *testing.T) {
	res := run(t, `printf 'ok \xff\xfe\xc3( \xe6\x97 end'`)
	if !strings.HasPrefix(res.Stdout, "ok ") || !strings.HasSuffix(res.Stdout, " end") {
		t.Errorf("%q", res.Stdout)
	}
	for _, r := range res.Stdout {
		if r == 0xFFFD {
			return
		}
	}
	t.Errorf("invalid bytes are replaced, not passed on: %q", res.Stdout)
}

func TestTimeoutStopsTheWholeProcessGroup(t *testing.T) {
	needBash(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	start := time.Now()
	res, err := Run(context.Background(), Options{Command: "sleep 300 & echo $! > " + pidFile + "; echo started; wait",
		Dir: dir, Timeout: 700 * time.Millisecond, Grace: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != 124 || res.Stdout != "started\n" {
		t.Errorf("%+v", res)
	}
	if took := time.Since(start); took < 600*time.Millisecond || took > 5*time.Second {
		t.Errorf("took %v", took)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	gone := false
	for i := 0; i < 50 && !gone; i++ {
		gone = syscall.Kill(pid, 0) == syscall.ESRCH
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the child of the command (pid %d) is still running after the timeout", pid)
	}
}

func TestACommandThatIgnoresSIGTERMIsKilledAfterTheGrace(t *testing.T) {
	needBash(t)
	start := time.Now()
	res, err := Run(context.Background(), Options{Command: "trap '' TERM; echo ready; sleep 300", Dir: t.TempDir(),
		Timeout: 500 * time.Millisecond, Grace: 700 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if !res.TimedOut || res.Stdout != "ready\n" {
		t.Errorf("%+v", res)
	}
	if took < 1100*time.Millisecond || took > 6*time.Second {
		t.Errorf("SIGTERM first, SIGKILL after the grace: took %v (timeout 500 ms + grace 700 ms)", took)
	}
}

func TestCancellationStopsTheCommand(t *testing.T) {
	needBash(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	res, err := Run(ctx, Options{Command: "echo begun; sleep 300", Dir: t.TempDir(), Grace: 300 * time.Millisecond})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if res.Stdout != "begun\n" || res.TimedOut || time.Since(start) > 5*time.Second {
		t.Errorf("what it wrote so far comes back: %+v after %v", res, time.Since(start))
	}
}

func TestBackgroundProgramsNeitherHangNorDie(t *testing.T) {
	needBash(t)
	dir := t.TempDir()
	flag := filepath.Join(dir, "flag")
	start := time.Now()
	// A program started in the background keeps the command's output open, writes after the command is over and
	// leaves a flag. With pipes it would either keep Run waiting or die of SIGPIPE when Run closed them.
	res, err := Run(context.Background(), Options{Command: "(sleep 1; echo late-stdout; echo late-stderr >&2; echo done > " + flag + ") & echo started", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "started\n" || time.Since(start) > 900*time.Millisecond {
		t.Errorf("Run does not wait for background programs: %+v after %v", res, time.Since(start))
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(flag); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the background program died: it must outlive the command and be able to write")
}

func TestAFloodOfOutputIsStopped(t *testing.T) {
	start := time.Now()
	res := run(t, "yes", func(o *Options) { o.HardLimit = 1 << 20; o.Timeout = 30 * time.Second })
	if !res.Truncated || res.TimedOut || !strings.Contains(res.Stderr, "was stopped") {
		t.Errorf("%d bytes, %+v", len(res.Stdout), res.Truncated)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("a runaway writer is stopped quickly, not at the timeout: %v", time.Since(start))
	}
	if len(res.Stdout) > 70<<10 {
		t.Errorf("the output is capped as usual: %d", len(res.Stdout))
	}
}

func TestRefusals(t *testing.T) {
	for name, o := range map[string]Options{
		"empty":      {Command: ""},
		"blank":      {Command: "  \n\t"},
		"a NUL byte": {Command: "echo a\x00b"},
	} {
		if _, err := Run(context.Background(), o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Run(context.Background(), Options{Command: "true", Shell: "/nonexistent/shell", Dir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Errorf("%v", err)
	}
}

func TestDefaultsAndLimits(t *testing.T) {
	o := Options{Command: "true", Dir: t.TempDir()}
	if err := o.fill(); err != nil {
		t.Fatal(err)
	}
	if o.Timeout != 60*time.Second || o.MaxOutput != 64<<10 || o.Grace != 2*time.Second || o.HardLimit != 256<<20 || o.Shell != "bash" {
		t.Errorf("%+v", o)
	}
	if MaxTimeout != 600*time.Second || MinTimeout != 100*time.Millisecond || MaxMaxOutput != 128<<10 {
		t.Error("the limits of the protocol")
	}
}

func TestManyAtOnce(t *testing.T) {
	needBash(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := Run(context.Background(), Options{Command: "echo " + strconv.Itoa(i) + "; seq 1 2000 >&2", Dir: t.TempDir()})
			if err != nil || res.Stdout != strconv.Itoa(i)+"\n" || res.ExitCode != 0 {
				t.Errorf("%d: %v %+v", i, err, res)
			}
		}()
	}
	wg.Wait()
}

func TestScratchFilesAreRemoved(t *testing.T) {
	needBash(t)
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "cs-exec-*"))
	run(t, "echo x", func(o *Options) { o.Stdin = "in" })
	_, _ = Run(context.Background(), Options{Command: "sleep 300", Dir: t.TempDir(), Timeout: 200 * time.Millisecond, Grace: 100 * time.Millisecond})
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "cs-exec-*"))
	if len(after) > len(before) {
		t.Errorf("scratch directories are left behind: %v", after)
	}
}
