// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Defaults and limits (PLAN.md section 8).
const (
	DefaultTimeout   = 60 * time.Second
	MaxTimeout       = 600 * time.Second
	MinTimeout       = 100 * time.Millisecond
	DefaultMaxOutput = 64 << 10 // per stream: 48 KiB of the head and 16 KiB of the tail
	MinMaxOutput     = 1 << 10
	MaxMaxOutput     = 128 << 10 // a response carries both streams in its header, which is limited to 1 MiB
	DefaultGrace     = 2 * time.Second
	// DefaultHardLimit is how much one stream may grow before the command is stopped: a runaway writer must not fill
	// the guest's disk while the caller waits for its timeout.
	DefaultHardLimit = 256 << 20
	// DefaultDir is where commands run unless told otherwise.
	DefaultDir = "/home/cs/work"
)

// Options describe one command.
type Options struct {
	Command string
	// Dir is the working directory (default DefaultDir, or the home directory when that does not exist).
	Dir   string
	Stdin string
	// Timeout is how long the command may run; at the end its process group gets SIGTERM, and SIGKILL after Grace.
	Timeout time.Duration
	// MaxOutput caps each stream returned: the first three quarters and the last quarter are kept.
	MaxOutput int
	Grace     time.Duration
	// HardLimit stops the command when one stream grows past it.
	HardLimit int64
	// Shell is the program that runs the command (default bash, as a login shell).
	Shell string
	// ExtraEnv is added to the agent's own environment, after the defaults of this package.
	ExtraEnv []string
}

// Result is what a command did.
type Result struct {
	ExitCode  int // 128 plus the signal when the shell was killed by one, 124 on a timeout
	Duration  time.Duration
	Stdout    string
	Stderr    string
	Truncated bool
	TimedOut  bool
}

// ErrBadDir means the working directory is not usable.
type ErrBadDir struct{ Dir, Reason string }

func (e *ErrBadDir) Error() string { return fmt.Sprintf("working directory %q: %s", e.Dir, e.Reason) }

// ErrUnsupported is returned on systems without process groups (the agent runs in a Linux guest).
var ErrUnsupported = errors.New("running commands needs a Unix system")

// defaultEnv is added to every command's environment: no pager, no prompts, a terminal that has no escape codes.
var defaultEnv = []string{"TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "DEBIAN_FRONTEND=noninteractive"}

func (o *Options) fill() error {
	if strings.TrimSpace(o.Command) == "" {
		return errors.New("the command is empty")
	}
	if strings.ContainsRune(o.Command, 0) {
		return errors.New("the command contains a NUL byte")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxOutput <= 0 {
		o.MaxOutput = DefaultMaxOutput
	}
	if o.Grace <= 0 {
		o.Grace = DefaultGrace
	}
	if o.HardLimit <= 0 {
		o.HardLimit = DefaultHardLimit
	}
	if o.Shell == "" {
		o.Shell = "bash"
	}
	if o.Dir == "" {
		o.Dir = DefaultDir
		if fi, err := os.Stat(o.Dir); err != nil || !fi.IsDir() {
			if home, err := os.UserHomeDir(); err == nil {
				o.Dir = home
			} else {
				o.Dir = "/"
			}
		}
	}
	if !filepath.IsAbs(o.Dir) {
		return &ErrBadDir{Dir: o.Dir, Reason: "it must be an absolute path"}
	}
	if fi, err := os.Stat(o.Dir); err != nil {
		return &ErrBadDir{Dir: o.Dir, Reason: "it does not exist"}
	} else if !fi.IsDir() {
		return &ErrBadDir{Dir: o.Dir, Reason: "it is not a directory"}
	}
	return nil
}

// Run runs the command with bash as a login shell, in its own process group, and waits for it. Its output goes to
// private files, not pipes: a program the command starts in the background (a file manager, a browser) then neither
// keeps Run waiting nor dies of a broken pipe when Run returns. Only the process the shell started is waited for; what
// it left running in the background is not touched, unless the command timed out or was canceled, when the whole group
// is stopped.
//
// When ctx ends, the group is stopped (SIGTERM, then SIGKILL after Grace) and Run returns ctx's error together with
// what the command had written so far.
func Run(ctx context.Context, o Options) (Result, error) {
	if err := o.fill(); err != nil {
		return Result{}, err
	}
	tmp, err := os.MkdirTemp("", "cs-exec-")
	if err != nil {
		return Result{}, fmt.Errorf("making a scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	outFile, err := os.Create(filepath.Join(tmp, "stdout")) //nolint:gosec // inside our own private directory
	if err != nil {
		return Result{}, err
	}
	defer outFile.Close()
	errFile, err := os.Create(filepath.Join(tmp, "stderr")) //nolint:gosec // inside our own private directory
	if err != nil {
		return Result{}, err
	}
	defer errFile.Close()

	cmd := exec.CommandContext(context.WithoutCancel(ctx), o.Shell, "-lc", o.Command) //nolint:gosec // running the caller's command is the point; the host's policy decides what is allowed
	cmd.Dir = o.Dir
	cmd.Env = append(append(os.Environ(), defaultEnv...), o.ExtraEnv...)
	cmd.Stdout, cmd.Stderr = outFile, errFile
	if o.Stdin != "" {
		in, err := os.Create(filepath.Join(tmp, "stdin")) //nolint:gosec // inside our own private directory
		if err != nil {
			return Result{}, err
		}
		defer in.Close()
		if _, err := io.WriteString(in, o.Stdin); err != nil {
			return Result{}, err
		}
		if _, err := in.Seek(0, io.SeekStart); err != nil {
			return Result{}, err
		}
		cmd.Stdin = in
	} // else the command reads /dev/null
	if err := configure(cmd); err != nil {
		return Result{}, err
	}

	begin := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("starting %s: %w", o.Shell, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var (
		timedOut, flooded bool
		stopped           = false
		killAt            <-chan time.Time
	)
	timeout := time.NewTimer(o.Timeout)
	defer timeout.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	stop := func() { // the first call asks politely, the grace timer then ends it for good
		if stopped {
			return
		}
		stopped = true
		signalGroup(cmd, syscall.SIGTERM)
		killAt = time.After(o.Grace)
	}
	var waitErr error
wait:
	for {
		select {
		case waitErr = <-done:
			break wait
		case <-timeout.C:
			timedOut = true
			stop()
		case <-ctx.Done():
			stop()
		case <-poll.C:
			if size(outFile) > o.HardLimit || size(errFile) > o.HardLimit {
				flooded = true
				stop()
			}
		case <-killAt:
			signalGroup(cmd, syscall.SIGKILL)
			killAt = nil
		}
	}
	if stopped { // whatever the shell left in the group goes with it
		signalGroup(cmd, syscall.SIGKILL)
	}

	res := Result{Duration: time.Since(begin), TimedOut: timedOut}
	res.ExitCode = exitCode(cmd.ProcessState, waitErr)
	if timedOut {
		res.ExitCode = 124
	}
	var t1, t2 bool
	res.Stdout, t1 = readCapped(outFile, o.MaxOutput)
	res.Stderr, t2 = readCapped(errFile, o.MaxOutput)
	res.Truncated = t1 || t2
	if flooded {
		res.Truncated = true
		res.Stderr += fmt.Sprintf("\n[the command wrote more than %d MiB to one stream and was stopped]\n", o.HardLimit>>20)
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, nil
}

func size(f *os.File) int64 {
	fi, err := f.Stat()
	if err != nil {
		return 0
	}
	return fi.Size()
}
