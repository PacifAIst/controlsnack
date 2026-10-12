// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qmp"
)

// Launch timings (PLAN.md section 7.9). Under tcg the window is multiplied by AccelResult.TimeoutFactor.
const (
	// DefaultWindow is how long a launch may take: QEMU must accept the chardev connections, answer QMP, reach
	// "running" and stay running.
	DefaultWindow = 30 * time.Second
	// DefaultStable is how long the VM must stay running before the launch counts as successful.
	DefaultStable = 10 * time.Second
	// defaultPoll is how often the run state is read during the stability check.
	defaultPoll = 250 * time.Millisecond
	// excerptBytes caps the stderr excerpt in errors and audit records.
	excerptBytes = 1024
)

// killWait is how long a failed QEMU gets to disappear after being killed; if it does not, there is no retry. It is
// a variable only so that tests need not wait it out.
var killWait = 5 * time.Second

// Process is a running QEMU as the launcher uses it; *qemu.Process is the real one.
type Process interface {
	Pid() int
	Kill() error
	Exited() <-chan struct{}
	Result() (code int, err error)
	Output() string
	Stop(ctx context.Context, quit func(context.Context) error, grace time.Duration) error
}

// QMP is the control channel as the launcher and the session use it; *qmp.Client is the real one.
type QMP interface {
	QueryStatus(ctx context.Context) (qmp.Status, error)
	Quit(ctx context.Context) error
	Screendump(ctx context.Context, path, format string) error
	InputSendEvent(ctx context.Context, events []qmp.InputEvent) error
	WaitPortOpen(ctx context.Context, id string) error
	WaitEvent(ctx context.Context, match func(qmp.Event) bool) (qmp.Event, error)
	Done() <-chan struct{}
	Err() error
	Close() error
}

// Backend starts QEMU and reaches its chardevs; RealBackend is the production one, tests substitute their own.
type Backend struct {
	Start      func(ctx context.Context, argv []string, opt qemu.StartOptions) (Process, error)
	DialQMP    func(ctx context.Context, ep qemu.Endpoint) (QMP, error)
	DialStream func(ctx context.Context, ep qemu.Endpoint) (net.Conn, error)
}

// Prepared is what one launch attempt runs with.
type Prepared struct {
	// Spec is the VM's command line, without the accelerator (the launcher sets it).
	Spec qemu.RuntimeSpec
	// Dir is QEMU's working directory (the session directory).
	Dir string
	// Cleanup removes what this attempt created (sockets, a copy of the variable store...). The overlay of a kept VM
	// is not the attempt's to remove.
	Cleanup func()
}

// PrepareFunc creates the resources of launch attempt n (1, then 2 after a fallback): a fresh socket directory or
// ports, a fresh overlay from the base image (a kept VM from --vm is reused as it is), and for aarch64 a copy of
// the UEFI variable store.
type PrepareFunc func(ctx context.Context, attempt int) (Prepared, error)

// Fallback describes a launch that fell back to software emulation (PLAN.md section 7.9, step 5).
type Fallback struct {
	Requested string `json:"requested"`
	From      string `json:"from"`
	Used      string `json:"used"`
	Reason    string `json:"reason"`
	Stderr    string `json:"stderr,omitempty"` // at most 1 KiB of what QEMU said
}

// Events receives what the launcher decides, for the audit log, the terminal and the viewer.
type Events interface {
	AccelSelected(AccelResult)
	AccelFallback(Fallback)
}

// LaunchOptions configure Launch.
type LaunchOptions struct {
	// Resolve picks the accelerator (Detector.Resolve with the --accel and --slow values bound).
	Resolve func(ctx context.Context) (AccelResult, error)
	Prepare PrepareFunc
	Backend Backend
	Events  Events // optional

	// Window and Stable default to DefaultWindow and DefaultStable; Poll is the status polling interval.
	Window, Stable, Poll time.Duration
}

// VM is a launched, running virtual machine.
type VM struct {
	Process Process
	QMP     QMP
	// Agent is the connection to the agent's virtio-serial chardev, Net the one to the network port (nil without
	// --allow-net). Both are already connected: QEMU waits for the controller before it starts the guest.
	Agent net.Conn
	Net   net.Conn
	// Accel is the accelerator in use, after a fallback if there was one.
	Accel AccelResult
	// Fallback is set when the first launch failed and tcg was used instead.
	Fallback *Fallback
	// Spec and Argv are what the running VM was started with.
	Spec qemu.RuntimeSpec
	Argv []string

	cleanup func()
	closer  sync.Once
}

// Stop shuts the VM down politely and then firmly (QMP quit, 3 s, kill) and releases its resources.
func (v *VM) Stop(ctx context.Context) error {
	err := v.Process.Stop(ctx, v.QMP.Quit, 3*time.Second)
	v.release()
	return err
}

// Kill ends the VM at once.
func (v *VM) Kill() error {
	err := v.Process.Kill()
	<-v.Process.Exited()
	v.release()
	return err
}

func (v *VM) release() {
	v.closer.Do(func() {
		_ = v.QMP.Close()
		_ = v.Agent.Close()
		if v.Net != nil {
			_ = v.Net.Close()
		}
		if v.cleanup != nil {
			v.cleanup()
		}
	})
}

// LaunchError is one failed launch attempt.
type LaunchError struct {
	Accel  string
	Reason string
	Stderr string // at most 1 KiB of QEMU's output
	cause  error
}

func (e *LaunchError) Error() string {
	s := fmt.Sprintf("QEMU with %s did not start: %s", e.Accel, e.Reason)
	if e.Stderr != "" {
		s += "\n  QEMU said: " + e.Stderr
	}
	return s
}

func (e *LaunchError) Unwrap() error { return e.cause }

// Launch chooses the accelerator, starts QEMU and waits until the VM is up (PLAN.md section 7.9). When a hardware
// accelerator fails, QEMU is started once more with tcg; a failure of tcg is final. If both fail the error carries
// exit code 3 and both excerpts.
func Launch(ctx context.Context, o LaunchOptions) (*VM, error) {
	if o.Resolve == nil || o.Prepare == nil || o.Backend.Start == nil || o.Backend.DialQMP == nil || o.Backend.DialStream == nil {
		return nil, errors.New("launching the VM: the options are incomplete (Resolve, Prepare and the whole Backend are required)")
	}
	o.defaults()
	res, err := o.Resolve(ctx)
	if err != nil {
		return nil, exitcode.New(exitcode.Environment, err)
	}
	if o.Events != nil {
		o.Events.AccelSelected(res)
	}

	vm, first := o.attempt(ctx, res.Chosen, 1)
	if first == nil {
		vm.Accel = res
		return vm, nil
	}
	if res.Chosen == AccelTCG || ctx.Err() != nil || first.fatal {
		return nil, launchFailure(ctx, first, nil)
	}

	fb := Fallback{Requested: res.Requested, From: res.Chosen, Used: AccelTCG, Reason: first.Reason, Stderr: first.Stderr}
	if o.Events != nil {
		o.Events.AccelFallback(fb)
	}
	vm, second := o.attempt(ctx, AccelTCG, 2)
	if second != nil {
		return nil, launchFailure(ctx, first, second)
	}
	vm.Accel = AccelResult{Requested: res.Requested, Chosen: AccelTCG, Slow: true,
		Tried: append(append([]AccelTry(nil), res.Tried...),
			AccelTry{Accel: res.Chosen, Reason: "failed at launch: " + first.Reason},
			AccelTry{Accel: AccelTCG, Usable: true, Reason: "fallback after a failed launch"})}
	vm.Fallback = &fb
	return vm, nil
}

func (o *LaunchOptions) defaults() {
	if o.Window <= 0 {
		o.Window = DefaultWindow
	}
	if o.Stable <= 0 {
		o.Stable = DefaultStable
	}
	if o.Poll <= 0 {
		o.Poll = defaultPoll
	}
}

func launchFailure(ctx context.Context, first, second *attemptError) error {
	if err := ctx.Err(); err != nil {
		return err // the caller gave up; this is not an environment problem
	}
	if second == nil {
		if first.internal {
			return first.LaunchError // a bug or a full disk of ours, not something doctor can explain
		}
		return exitcode.New(exitcode.Environment, fmt.Errorf("%w\nrun 'controlsnack doctor' to see what this machine can do", first.LaunchError))
	}
	if second.internal {
		return second.LaunchError
	}
	return exitcode.New(exitcode.Environment, fmt.Errorf("%w\nthe fallback to software emulation failed as well:\n%w\nrun 'controlsnack doctor' to see what this machine can do",
		first.LaunchError, second.LaunchError))
}

// attemptError is a failed attempt; fatal means a second attempt must not be made (a failed QEMU that would not die,
// or an attempt that could not even be prepared).
type attemptError struct {
	*LaunchError
	fatal    bool
	internal bool // our own files or command line were wrong: not an environment problem (exit code 1)
}

// attempt starts QEMU once with the accelerator and waits through the launch window.
func (o *LaunchOptions) attempt(ctx context.Context, accel string, n int) (*VM, *attemptError) {
	fail := func(reason string, cause error) *attemptError {
		return &attemptError{LaunchError: &LaunchError{Accel: accel, Reason: reason, cause: cause}, fatal: true}
	}
	prep, err := o.Prepare(ctx, n)
	if err != nil {
		e := fail("preparing the VM's files failed: "+err.Error(), err)
		e.internal = true
		return nil, e
	}
	cleanup := prep.Cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	spec := prep.Spec
	spec.Accel = accel
	argv, err := qemu.RuntimeArgv(spec)
	if err != nil {
		cleanup()
		e := fail(err.Error(), err)
		e.internal = true
		return nil, e
	}

	factor := 1
	if accel == AccelTCG {
		factor = slowFactor
	}
	wctx, cancel := context.WithTimeout(ctx, o.Window*time.Duration(factor))
	defer cancel()

	proc, err := o.Backend.Start(ctx, argv, qemu.StartOptions{Dir: prep.Dir})
	if err != nil {
		cleanup()
		return nil, fail("QEMU could not be started: "+err.Error(), err)
	}

	var conns launchConns
	abort := func(reason string, cause error) (*VM, *attemptError) {
		_ = proc.Kill()
		stderr := tailExcerpt(proc.Output(), excerptBytes)
		gone := true
		select {
		case <-proc.Exited():
		case <-time.After(killWait):
			gone = false
		}
		conns.close()
		cleanup()
		e := &attemptError{LaunchError: &LaunchError{Accel: accel, Reason: reason, Stderr: stderr, cause: cause}}
		if !gone {
			e.Reason += "; the process could not be stopped, so no second attempt is made"
			e.fatal = true
		}
		return nil, e
	}

	if err := o.connect(wctx, proc, spec, &conns); err != nil {
		return abort(err.Error(), err)
	}
	if err := o.waitRunning(wctx, proc, conns.qmp); err != nil {
		return abort(err.Error(), err)
	}
	return &VM{Process: proc, QMP: conns.qmp, Agent: conns.agent, Net: conns.net, Spec: spec, Argv: argv, cleanup: cleanup}, nil
}

type launchConns struct {
	qmp   QMP
	agent net.Conn
	net   net.Conn
}

func (c *launchConns) close() {
	if c.qmp != nil {
		_ = c.qmp.Close()
	}
	if c.agent != nil {
		_ = c.agent.Close()
	}
	if c.net != nil {
		_ = c.net.Close()
	}
}

// connect dials every chardev at once and waits for all of them. QEMU creates them in order and waits for each
// connection before it goes on (wait=on), so dialing one after another would work too, but dialing together cannot
// deadlock whichever order QEMU uses. When one fails the others are canceled and joined before connect returns, so
// nothing is still writing to out when the caller cleans up.
func (o *LaunchOptions) connect(ctx context.Context, proc Process, spec qemu.RuntimeSpec, out *launchConns) error {
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, 3)
	var wg sync.WaitGroup
	started := 0
	start := func(name string, dial func(context.Context) error) {
		started++
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- result{name, dial(dctx)}
		}()
	}
	start("QMP", func(c context.Context) (err error) { out.qmp, err = o.Backend.DialQMP(c, spec.QMP); return })
	start("the agent port", func(c context.Context) (err error) { out.agent, err = o.Backend.DialStream(c, spec.Agent); return })
	if spec.Net != nil {
		start("the network port", func(c context.Context) (err error) { out.net, err = o.Backend.DialStream(c, *spec.Net); return })
	}

	var firstErr error
	for i := 0; i < started && firstErr == nil; i++ {
		select {
		case r := <-results:
			switch {
			case r.err == nil:
			case errors.Is(r.err, context.DeadlineExceeded):
				firstErr = fmt.Errorf("%s was not ready within the launch window", r.name)
			case errors.Is(r.err, context.Canceled):
				firstErr = r.err
			default:
				firstErr = fmt.Errorf("could not reach %s: %w", r.name, r.err)
			}
		case <-proc.Exited():
			firstErr = exitedError(proc, "before it could be reached")
		}
	}
	cancel()
	wg.Wait()
	return firstErr
}

// waitRunning waits for the VM to be running and to stay running for the stability period. Failure is: QEMU
// exiting, the QMP connection ending, the window expiring, or the state turning to internal-error or shutdown.
func (o *LaunchOptions) waitRunning(ctx context.Context, proc Process, q QMP) error {
	var runningSince time.Time
	tick := time.NewTicker(o.Poll)
	defer tick.Stop()
	for {
		st, err := q.QueryStatus(ctx)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return windowExpired(runningSince)
		case isGone(proc):
			return exitedError(proc, "while starting")
		default:
			return fmt.Errorf("QMP stopped answering: %w", err)
		}
		switch st.Status {
		case "internal-error", "shutdown", "guest-panicked", "io-error", "watchdog":
			return fmt.Errorf("the VM entered the %q state", st.Status)
		}
		if st.Running {
			if runningSince.IsZero() {
				runningSince = time.Now()
			}
			if time.Since(runningSince) >= o.Stable {
				return nil
			}
		} else if !runningSince.IsZero() {
			return fmt.Errorf("the VM stopped running (state %q)", st.Status)
		}
		select {
		case <-tick.C:
		case <-proc.Exited():
			return exitedError(proc, "while starting")
		case <-q.Done():
			if isGone(proc) {
				return exitedError(proc, "while starting")
			}
			return fmt.Errorf("the QMP connection ended: %w", q.Err())
		case <-ctx.Done():
			return windowExpired(runningSince)
		}
	}
}

func isGone(p Process) bool {
	select {
	case <-p.Exited():
		return true
	case <-time.After(50 * time.Millisecond): // QEMU closes QMP an instant before the process is reaped
		return false
	}
}

func exitedError(p Process, when string) error {
	code, _ := p.Result()
	return fmt.Errorf("QEMU exited with status %d %s", code, when)
}

// tailExcerpt returns the end of a program's output on one line, at most n bytes: what a program says last is
// what went wrong.
func tailExcerpt(out string, n int) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		// QEMU announces every chardev it waits on ("info: QEMU waiting for connection on: ..."); that is progress,
		// not the reason for a failure, and would push the reason out of the excerpt.
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "QEMU waiting for connection on:") {
			lines = append(lines, l)
		}
	}
	s := strings.Join(lines, " | ")
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return "..." + s
}

// windowExpired is the error for a launch window that ran out; which one depends only on how far the VM got.
func windowExpired(runningSince time.Time) error {
	if runningSince.IsZero() {
		return errors.New("the VM did not reach the running state within the launch window")
	}
	return errors.New("the VM did not stay running within the launch window")
}
