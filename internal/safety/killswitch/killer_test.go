// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package killswitch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeQEMU records the order of events around the kill.
type fakeQEMU struct {
	k          *Killer
	log        *eventLog
	killErr    error
	exitAfter  time.Duration
	neverExits bool
	exited     chan struct{}
	killCalls  atomic.Int32
	sawKilled  atomic.Bool
}

func newFakeQEMU(log *eventLog) *fakeQEMU { return &fakeQEMU{log: log, exited: make(chan struct{})} }

func (q *fakeQEMU) Kill() error {
	q.killCalls.Add(1)
	if q.k != nil {
		q.sawKilled.Store(q.k.Killed())
	}
	q.log.add("qemu.kill")
	if q.killErr != nil {
		return q.killErr
	}
	if !q.neverExits {
		go func() {
			time.Sleep(q.exitAfter)
			select {
			case <-q.exited:
			default:
				close(q.exited)
			}
		}()
	}
	return nil
}

func (q *fakeQEMU) Exited() <-chan struct{} { return q.exited }

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) { l.mu.Lock(); l.events = append(l.events, e); l.mu.Unlock() }
func (l *eventLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func TestOrderOfEvents(t *testing.T) {
	log := &eventLog{}
	ctx, cancel := context.WithCancel(context.Background())
	k := New(Options{Cancel: func() { log.add("cancel"); cancel() }})
	q := newFakeQEMU(log)
	q.k = k
	q.exitAfter = 20 * time.Millisecond
	k.SetQEMU(q)
	addHook(t, k, "agent", func(context.Context) error { log.add("hook:agent"); return nil })
	addHook(t, k, "viewer", func(context.Context) error { log.add("hook:viewer"); return nil })
	addHook(t, k, "audit", func(context.Context) error {
		if ctx.Err() == nil {
			t.Error("the root context must be canceled before the teardown steps run")
		}
		log.add("hook:audit")
		return nil
	})
	addHook(t, k, "lock", func(context.Context) error { log.add("hook:lock"); return nil })

	if k.Killed() {
		t.Fatal("not killed yet")
	}
	res := k.Trigger(SourceButton, "the user pressed it")

	if !q.sawKilled.Load() {
		t.Error("the killed flag must be set before QEMU is killed, so the dispatcher stops at once")
	}
	want := []string{"qemu.kill", "cancel", "hook:agent", "hook:viewer", "hook:audit", "hook:lock"}
	if got := log.list(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
	if res.Source != SourceButton || res.Reason != "the user pressed it" || !res.QEMUKilled || res.QEMUErr != nil || len(res.HookErrors) != 0 {
		t.Errorf("%+v", res)
	}
	if res.QEMUGoneAfter < 20*time.Millisecond || res.QEMUGoneAfter > 500*time.Millisecond {
		t.Errorf("QEMU gone after %v; the target is well under 500 ms", res.QEMUGoneAfter)
	}
	select {
	case <-k.QEMUGone():
	default:
		t.Error("QEMUGone must be closed")
	}
	if r, ok := k.Result(); !ok || r.Source != SourceButton {
		t.Errorf("Result() = %+v %v", r, ok)
	}
	if !k.Killed() {
		t.Error("Killed() stays true")
	}
}

func TestTriggerIsIdempotentUnderConcurrency(t *testing.T) {
	log := &eventLog{}
	k := New(Options{})
	q := newFakeQEMU(log)
	k.SetQEMU(q)
	var hookRuns atomic.Int32
	addHook(t, k, "once", func(context.Context) error { hookRuns.Add(1); time.Sleep(30 * time.Millisecond); return nil })

	var wg sync.WaitGroup
	results := make([]Result, 20)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := SourceButton
			if i%2 == 1 {
				src = SourceSignal
			}
			results[i] = k.Trigger(src, "r")
		}(i)
	}
	wg.Wait()
	if q.killCalls.Load() != 1 || hookRuns.Load() != 1 {
		t.Fatalf("QEMU killed %d times, hook ran %d times; both must be exactly once", q.killCalls.Load(), hookRuns.Load())
	}
	for i, r := range results {
		if r.Source != results[0].Source {
			t.Fatalf("call %d returned source %v, first returned %v: every caller gets the first trigger's result", i, r.Source, results[0].Source)
		}
		if r.TeardownTook < 30*time.Millisecond {
			t.Errorf("call %d returned before teardown finished (%v)", i, r.TeardownTook)
		}
	}
	// A later trigger does nothing and returns the same result.
	again := k.Trigger(SourceCLI, "later")
	if again.Source != results[0].Source || q.killCalls.Load() != 1 {
		t.Errorf("a repeat trigger changed something: %+v", again)
	}
}

func TestHooksAreBoundedAndIsolated(t *testing.T) {
	k := New(Options{HookTimeout: 60 * time.Millisecond})
	var ran []string
	var mu sync.Mutex
	rec := func(s string) { mu.Lock(); ran = append(ran, s); mu.Unlock() }
	addHook(t, k, "hangs", func(ctx context.Context) error { rec("hangs"); time.Sleep(5 * time.Second); return nil })
	addHook(t, k, "panics", func(context.Context) error { rec("panics"); panic("boom") })
	addHook(t, k, "fails", func(context.Context) error { rec("fails"); return errors.New("disk full") })
	addHook(t, k, "sees-timeout", func(ctx context.Context) error {
		rec("sees-timeout")
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("a hook must get a deadline")
		}
		return nil
	})
	addHook(t, k, "last", func(context.Context) error { rec("last"); return nil })

	start := time.Now()
	res := k.Trigger(SourceInternal, "test")
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a hanging hook held the kill for %v", took)
	}
	mu.Lock()
	got := strings.Join(ran, ",")
	mu.Unlock()
	if got != "hangs,panics,fails,sees-timeout,last" {
		t.Errorf("every step must run, in order, whatever happened before: %s", got)
	}
	errs := map[string]string{}
	for _, he := range res.HookErrors {
		errs[he.Name] = he.Err.Error()
	}
	if !strings.Contains(errs["hangs"], "timed out") || !strings.Contains(errs["panics"], "panic: boom") || errs["fails"] != "disk full" {
		t.Errorf("hook errors: %v", errs)
	}
	if _, bad := errs["sees-timeout"]; bad || len(errs) != 3 {
		t.Errorf("only the three failing steps are reported: %v", errs)
	}
}

func TestQEMUFailuresDoNotStopTeardown(t *testing.T) {
	log := &eventLog{}
	k := New(Options{})
	q := newFakeQEMU(log)
	q.killErr = errors.New("access denied")
	k.SetQEMU(q)
	hook := false
	addHook(t, k, "h", func(context.Context) error { hook = true; return nil })
	res := k.Trigger(SourceAPI, "x")
	if res.QEMUKilled || res.QEMUErr == nil || !hook {
		t.Fatalf("a failed kill is reported, and teardown still runs: %+v hook=%v", res, hook)
	}
}

func TestQEMUThatNeverExitsIsReported(t *testing.T) {
	k := New(Options{QEMUWait: 50 * time.Millisecond})
	q := newFakeQEMU(&eventLog{})
	q.neverExits = true
	k.SetQEMU(q)
	start := time.Now()
	res := k.Trigger(SourceSignal, "x")
	if time.Since(start) > 2*time.Second {
		t.Fatal("waiting for QEMU must be bounded")
	}
	if res.QEMUErr == nil || !strings.Contains(res.QEMUErr.Error(), "still running") {
		t.Errorf("%+v", res)
	}
}

func TestKillBeforeAnyVMExists(t *testing.T) {
	canceled := false
	k := New(Options{Cancel: func() { canceled = true }})
	res := k.Trigger(SourceSignal, "ctrl-c during boot")
	if res.QEMUKilled || res.QEMUErr != nil || !canceled {
		t.Errorf("%+v canceled=%v", res, canceled)
	}
	select {
	case <-k.QEMUGone():
	default:
		t.Error("with no VM, QEMUGone is closed immediately")
	}
	// The launch that was in flight registers its VM afterwards: it must be killed on the spot.
	log := &eventLog{}
	late := newFakeQEMU(log)
	k.SetQEMU(late)
	if late.killCalls.Load() != 1 {
		t.Error("a VM registered after the kill must be killed immediately")
	}
}

func TestHookAddedAfterTheKillRunsAtOnce(t *testing.T) {
	k := New(Options{})
	k.Trigger(SourceButton, "x")
	ran := false
	if err := k.AddHook("late", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("a hook added after the kill finished must run immediately: ran=%v err=%v", ran, err)
	}
	if err := k.AddHook("late-fails", func(context.Context) error { return errors.New("nope") }); err == nil {
		t.Error("the error of an immediately run hook is returned")
	}
}

func TestTriggerAsyncSetsTheFlagImmediately(t *testing.T) {
	block := make(chan struct{})
	k := New(Options{})
	addHook(t, k, "slow", func(context.Context) error { <-block; return nil })
	k.TriggerAsync(SourceHotkey, "x")
	if !k.Killed() {
		t.Fatal("the flag must be visible as soon as TriggerAsync returns")
	}
	if _, ok := k.Result(); ok {
		t.Fatal("teardown is still running")
	}
	close(block)
	select {
	case <-k.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("never finished")
	}
}

func TestOnPanicKillsThenRepanics(t *testing.T) {
	k := New(Options{})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		defer k.OnPanic()
		panic("controller bug")
	}()
	if recovered != "controller bug" {
		t.Errorf("the panic must continue after the kill, got %v", recovered)
	}
	r, ok := k.Result()
	if !ok || r.Source != SourcePanic || !strings.Contains(r.Reason, "controller bug") {
		t.Errorf("%+v %v", r, ok)
	}
	// Without a panic OnPanic does nothing.
	k2 := New(Options{})
	func() { defer k2.OnPanic() }()
	if k2.Killed() {
		t.Error("no panic, no kill")
	}
}

func TestEverySourceHasADistinctName(t *testing.T) {
	seen := map[Source]bool{}
	for _, s := range []Source{SourceButton, SourceHotkey, SourceAPI, SourceCLI, SourceSignal, SourceDeadman, SourcePanic, SourceAudit, SourceInternal} {
		if s == "" || seen[s] {
			t.Errorf("source %q is empty or duplicated", s)
		}
		seen[s] = true
	}
}

// addHook registers a teardown step that is expected to be queued, not run at once.
func addHook(t testing.TB, k *Killer, name string, fn func(ctx context.Context) error) {
	t.Helper()
	if err := k.AddHook(name, fn); err != nil {
		t.Fatalf("AddHook(%s): %v", name, err)
	}
}
