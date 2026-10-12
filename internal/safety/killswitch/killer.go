// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Source says what pulled the trigger. It is recorded in the audit log (lifecycle.kill).
type Source string

// The kill sources of PLAN.md section 6.8 and D-19.
const (
	SourceButton   Source = "button"        // the big red button in the viewer
	SourceHotkey   Source = "hotkey"        // Ctrl+Shift+Esc or Esc x3 in the viewer
	SourceAPI      Source = "api"           // POST /api/kill with the viewer cookie
	SourceCLI      Source = "cli"           // `controlsnack kill` from another terminal
	SourceSignal   Source = "signal"        // Ctrl+C, SIGTERM or SIGHUP in the controlling terminal
	SourceDeadman  Source = "deadman"       // the viewer stayed away past the grace period
	SourcePanic    Source = "panic"         // an unexpected panic in the controller
	SourceAudit    Source = "audit_failure" // the audit log could not be written
	SourceInternal Source = "internal"      // a safety check inside the controller tripped
)

// QEMU is the VM process as the kill switch needs to see it.
type QEMU interface {
	// Kill ends the process at once (SIGKILL; TerminateProcess on Windows).
	Kill() error
	// Exited is closed when the process has exited.
	Exited() <-chan struct{}
}

// Hook is a teardown step that runs after QEMU has been killed: close the agent and QMP connections, tell the viewer
// and close it, kill the browser window, finalize the audit log, delete the session directory, release the lock.
type Hook struct {
	Name string
	Fn   func(ctx context.Context) error
}

// HookError is a teardown step that failed or ran out of time.
type HookError struct {
	Name string
	Err  error
}

// Result describes what a kill did.
type Result struct {
	Source Source
	Reason string
	// QEMUKilled is false if there was no VM yet or the kill call failed.
	QEMUKilled bool
	QEMUErr    error
	// QEMUGoneAfter is the time from the trigger until QEMU was seen to have exited; the target is under 500 ms.
	QEMUGoneAfter time.Duration
	// TeardownTook is the time the hooks needed.
	TeardownTook time.Duration
	HookErrors   []HookError
}

// Options configure a Killer.
type Options struct {
	// Cancel cancels the root context of the session; everything that waits on it stops.
	Cancel context.CancelFunc
	// HookTimeout bounds each teardown step (default 2 s). A step that overruns is abandoned, not waited for.
	HookTimeout time.Duration
	// QEMUWait bounds how long to wait for QEMU to disappear after the kill call (default 2 s).
	QEMUWait time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// Killer is the kill switch. There is exactly one per session, it cannot be switched off, and Trigger is
// idempotent: the first call does the work, every later or concurrent call waits for it and returns the same
// Result (PLAN.md section 6.8, invariant 5).
//
// Order of events: the killed flag is set first (the dispatcher checks it before every action and after every
// model reply), then QEMU is killed, then the root context is canceled, then the teardown steps run in the order
// they were added, each with its own timeout.
type Killer struct {
	opts   Options
	killed atomic.Bool
	once   sync.Once

	mu       sync.Mutex
	qemu     QEMU
	hooks    []Hook
	finished bool

	qemuGone chan struct{}
	done     chan struct{}
	result   Result
}

// New creates the kill switch of a session.
func New(o Options) *Killer {
	if o.HookTimeout <= 0 {
		o.HookTimeout = 2 * time.Second
	}
	if o.QEMUWait <= 0 {
		o.QEMUWait = 2 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Killer{opts: o, qemuGone: make(chan struct{}), done: make(chan struct{})}
}

// Killed reports whether the kill switch has fired. It is cheap and safe to call from anywhere.
func (k *Killer) Killed() bool { return k.killed.Load() }

// SetQEMU registers the VM process once it has been launched. If the kill switch has already fired the process is
// killed on the spot: a kill that raced a launch must not leave a VM behind.
func (k *Killer) SetQEMU(q QEMU) {
	k.mu.Lock()
	k.qemu = q
	late := k.killed.Load()
	k.mu.Unlock()
	if late && q != nil {
		_ = q.Kill()
	}
}

// AddHook registers a teardown step. Steps run in the order they were added. A step added after the kill has
// finished runs immediately, so a resource created during a race with the kill is still cleaned up; its error,
// if any, is returned (a queued step reports through Result.HookErrors instead).
func (k *Killer) AddHook(name string, fn func(ctx context.Context) error) error {
	k.mu.Lock()
	if k.finished {
		k.mu.Unlock()
		return k.runHook(Hook{Name: name, Fn: fn})
	}
	k.hooks = append(k.hooks, Hook{Name: name, Fn: fn})
	k.mu.Unlock()
	return nil
}

// Done is closed when the kill, including teardown, has finished.
func (k *Killer) Done() <-chan struct{} { return k.done }

// QEMUGone is closed as soon as QEMU is confirmed gone (or immediately if there was none), before teardown ends.
// The `controlsnack kill` handler answers on this.
func (k *Killer) QEMUGone() <-chan struct{} { return k.qemuGone }

// Result returns what the kill did; ok is false until it has finished.
func (k *Killer) Result() (r Result, ok bool) {
	select {
	case <-k.done:
		return k.result, true
	default:
		return Result{}, false
	}
}

// TriggerAsync fires the kill switch without waiting for it to finish.
func (k *Killer) TriggerAsync(src Source, reason string) {
	k.killed.Store(true) // visible to the dispatcher right now, before the goroutine is scheduled
	go k.Trigger(src, reason)
}

// Trigger fires the kill switch and returns once teardown is complete. It must not be called from a teardown hook
// (the hook would wait for itself).
func (k *Killer) Trigger(src Source, reason string) Result {
	k.killed.Store(true)
	k.once.Do(func() { k.run(src, reason) })
	<-k.done
	return k.result
}

// OnPanic is for `defer killer.OnPanic()` at the top of a goroutine that must not die silently: it fires the kill
// switch with the panic as the reason, then lets the panic continue so the crash stays visible.
func (k *Killer) OnPanic() {
	if r := recover(); r != nil {
		k.Trigger(SourcePanic, fmt.Sprint(r))
		panic(r)
	}
}

func (k *Killer) run(src Source, reason string) {
	start := k.opts.Now()
	res := Result{Source: src, Reason: reason}

	k.mu.Lock()
	q := k.qemu
	k.mu.Unlock()

	// 1. kill QEMU
	if q != nil {
		if err := q.Kill(); err != nil {
			res.QEMUErr = err
		} else {
			res.QEMUKilled = true
		}
	}
	// 2. cancel the root context
	if k.opts.Cancel != nil {
		k.opts.Cancel()
	}
	// 3. confirm QEMU is gone, bounded
	if q != nil {
		select {
		case <-q.Exited():
			res.QEMUGoneAfter = k.opts.Now().Sub(start)
		case <-time.After(k.opts.QEMUWait):
			if res.QEMUErr == nil {
				res.QEMUErr = fmt.Errorf("QEMU still running %s after the kill", k.opts.QEMUWait)
			}
			res.QEMUGoneAfter = k.opts.Now().Sub(start)
		}
	}
	close(k.qemuGone)

	// 4. teardown, in order, each step bounded
	teardownStart := k.opts.Now()
	for i := 0; ; i++ {
		k.mu.Lock()
		if i >= len(k.hooks) {
			k.finished = true
			k.mu.Unlock()
			break
		}
		h := k.hooks[i]
		k.mu.Unlock()
		if err := k.runHook(h); err != nil {
			res.HookErrors = append(res.HookErrors, HookError{Name: h.Name, Err: err})
		}
	}
	res.TeardownTook = k.opts.Now().Sub(teardownStart)

	k.result = res
	close(k.done)
}

// runHook runs one step with a timeout and panic protection. A step that does not finish in time is abandoned.
func (k *Killer) runHook(h Hook) error {
	ctx, cancel := context.WithTimeout(context.Background(), k.opts.HookTimeout)
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errc <- fmt.Errorf("panic: %v", r)
			}
		}()
		errc <- h.Fn(ctx)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timed out after %s", k.opts.HookTimeout)
	}
}
