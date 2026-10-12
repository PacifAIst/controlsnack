// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// StartOptions configure how the QEMU process is started.
type StartOptions struct {
	// Dir is the working directory (the session directory).
	Dir string
	// Env replaces the minimal environment (PATH, HOME and the Windows system variables, with a fixed locale).
	Env []string
	// RingSize is how much output is kept for error messages; default DefaultRingSize.
	RingSize int
	// ReaperCmd is the command that runs the reaper wrapper (macOS, where the OS has no way to end a child with its
	// parent). Default: this executable with the hidden `__reaper` command. ForceReaper makes any Unix use it,
	// which is how it is tested on Linux.
	ReaperCmd   []string
	ForceReaper bool
}

// Process is a running QEMU. It satisfies the kill switch's view of the VM (Kill, Exited).
//
// The process dies with the controller: Pdeathsig on Linux, a Job Object with KILL_ON_JOB_CLOSE on Windows, a
// reaper wrapper on macOS (PLAN.md section 7.4). It runs in its own process group (or console process group) so
// that a Ctrl+C aimed at the controller reaches only the controller, which then stops QEMU through the kill switch.
type Process struct {
	cmd    *exec.Cmd
	pid    int // the pid to kill: QEMU itself, even when a reaper wraps it
	ring   *Ring
	exited chan struct{}

	reportW *os.File // reaper mode: the write end of the pid report pipe, closed once the child has it

	mu     sync.Mutex
	err    error
	code   int
	reaped atomic.Bool
}

// Start launches argv (argv[0] is the QEMU binary, as returned by the command-line builders).
//
// ctx ends the process: when it is done, QEMU is killed. The controller's kill switch cancels the session context
// after it has killed QEMU itself, so this is a second line, not the first.
func Start(ctx context.Context, argv []string, opt StartOptions) (*Process, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("starting QEMU: empty command line")
	}
	if !isAbs(argv[0]) {
		return nil, fmt.Errorf("starting QEMU: %q is not an absolute path", argv[0])
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := &Process{ring: NewRing(opt.RingSize), exited: make(chan struct{})}
	env := opt.Env
	if env == nil {
		env = probeEnv()
	}

	reaper := runtime.GOOS == "darwin" || opt.ForceReaper
	var report *os.File // the reaper tells us QEMU's pid on this pipe
	var cmd *exec.Cmd
	if reaper {
		var err error
		if cmd, report, err = p.reaperCmd(argv, opt); err != nil {
			return nil, err
		}
	} else {
		// The process outlives any one request, and the watcher below kills the whole process group when ctx ends;
		// exec's own handling would kill only the lead process.
		cmd = exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...) //nolint:gosec // argv comes from the command-line builders
	}
	cmd.Dir, cmd.Env = opt.Dir, env
	cmd.Stdout, cmd.Stderr = p.ring, p.ring
	cmd.WaitDelay = 2 * time.Second
	prepareCmd(cmd, reaper)
	p.cmd = cmd

	// The goroutine that starts the process also waits for it, with its OS thread locked: Linux ends a child with
	// Pdeathsig when the *thread* that created it exits, and Go may retire threads at any time.
	started := make(chan error, 1)
	go p.run(started, report)
	if err := <-started; err != nil {
		return nil, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Kill()
		case <-p.exited:
		}
	}()
	return p, nil
}

func (p *Process) run(started chan<- error, report *os.File) {
	runtime.LockOSThread() // not unlocked: when the child is gone the thread may go with this goroutine
	err := p.cmd.Start()
	if p.reportW != nil {
		_ = p.reportW.Close() // the child has its own copy now
	}
	// abort ends a child that was started but cannot be used, and collects it.
	abort := func() {
		_ = p.cmd.Process.Kill()
		if p.pid != p.cmd.Process.Pid {
			_ = killTree(p.pid, p.cmd.Process, &p.reaped)
		}
		_ = p.cmd.Wait()
	}
	var release func()
	if err == nil {
		p.pid = p.cmd.Process.Pid
		if release, err = adopt(p.pid); err != nil {
			abort()
		}
	}
	if report != nil {
		if err == nil {
			if err = p.readReaperReport(report); err != nil {
				abort()
			}
		}
		_ = report.Close()
	}
	if err != nil {
		started <- err
		close(p.exited)
		return
	}
	defer release()
	started <- nil

	waitErr := p.cmd.Wait()
	p.reaped.Store(true)
	p.mu.Lock()
	p.err = waitErr
	p.code = p.cmd.ProcessState.ExitCode()
	p.mu.Unlock()
	close(p.exited)
}

// Pid returns the process id of QEMU.
func (p *Process) Pid() int { return p.pid }

// Exited is closed when the process has ended and its output has been collected.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// Output returns the last output QEMU wrote to stdout and stderr (up to the ring size).
func (p *Process) Output() string { return p.ring.String() }

// Result reports how the process ended; it is only meaningful after Exited is closed. The code is -1 when the
// process was killed by a signal. The error is nil for a clean exit.
func (p *Process) Result() (code int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, p.err
}

// Kill ends QEMU at once (SIGKILL; TerminateProcess on Windows), along with anything it started. It is safe to
// call more than once and after the process has exited.
func (p *Process) Kill() error {
	select {
	case <-p.exited:
		return nil
	default:
	}
	err := killTree(p.pid, p.cmd.Process, &p.reaped)
	if p.pid != p.cmd.Process.Pid { // a reaper wraps QEMU: it exits on its own once QEMU is gone; make sure
		time.AfterFunc(time.Second, func() { _ = p.cmd.Process.Kill() })
	}
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// Stop shuts the VM down politely and then firmly: quit is called first (QMP `quit`, which lets QEMU flush its
// disk); if the process is still there after grace (PLAN.md section 7.4: 3 s), it is killed.
func (p *Process) Stop(ctx context.Context, quit func(context.Context) error, grace time.Duration) error {
	if quit != nil {
		qctx, cancel := context.WithTimeout(ctx, grace)
		_ = quit(qctx) // a failure here only means the kill below does the work
		cancel()
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.exited:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	err := p.Kill()
	select {
	case <-p.exited:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("QEMU (pid %d) did not exit after being killed", p.pid)
	}
}
