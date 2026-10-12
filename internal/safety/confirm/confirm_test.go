// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package confirm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeViewer records what it was asked to show.
type fakeViewer struct {
	mu        sync.Mutex
	presented []Request
	resolved  map[string]Outcome
	failWith  error
	shown     chan Request
}

func newViewer() *fakeViewer {
	return &fakeViewer{resolved: map[string]Outcome{}, shown: make(chan Request, 16)}
}

func (v *fakeViewer) Present(_ context.Context, r Request) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.failWith != nil {
		return v.failWith
	}
	v.presented = append(v.presented, r)
	v.shown <- r
	return nil
}

func (v *fakeViewer) Resolved(id string, o Outcome) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.resolved[id] = o
}

func (v *fakeViewer) outcome(id string) (Outcome, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	o, ok := v.resolved[id]
	return o, ok
}

// manualTimer lets a test fire the timeout by hand.
type manualTimer struct {
	mu      sync.Mutex
	chans   []chan time.Time
	stopped int
	last    time.Duration
}

func (m *manualTimer) New(d time.Duration) (<-chan time.Time, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := make(chan time.Time, 1)
	m.chans = append(m.chans, c)
	m.last = d
	return c, func() { m.mu.Lock(); m.stopped++; m.mu.Unlock() }
}

// fire triggers the i-th timer, waiting until Ask has actually created it (Present runs first).
func (m *manualTimer) fire(i int) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		if i < len(m.chans) {
			c := m.chans[i]
			m.mu.Unlock()
			c <- time.Now()
			return
		}
		m.mu.Unlock()
		if time.Now().After(deadline) {
			panic("the timer was never created")
		}
		time.Sleep(time.Millisecond)
	}
}

type asked struct {
	res chan Result
}

func ask(b *Broker, ctx context.Context, req Request) asked {
	a := asked{res: make(chan Result, 1)}
	go func() { a.res <- b.Ask(ctx, req) }()
	return a
}

func (a asked) wait(t *testing.T) Result {
	t.Helper()
	select {
	case r := <-a.res:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return")
		return Result{}
	}
}

func newBroker(t *testing.T) (*Broker, *fakeViewer, *manualTimer) {
	t.Helper()
	v := newViewer()
	mt := &manualTimer{}
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	b := New(Options{Timeout: 120 * time.Second, Now: func() time.Time { return now }, NewTimer: mt.New})
	b.SetPresenter(v)
	return b, v, mt
}

func sample() Request {
	return Request{ActionID: "t7.1", Tool: "exec", Args: map[string]any{"command": "rm -r /tmp/x"}, Reason: "clean up",
		Rules: []RuleInfo{{ID: "CONFIRM-DELETE", Explanation: "This deletes files."}}}
}

func TestApproveAndReject(t *testing.T) {
	b, v, _ := newBroker(t)
	for _, tt := range []struct {
		approve bool
		want    Outcome
	}{{true, Approved}, {false, Rejected}} {
		a := ask(b, context.Background(), sample())
		shown := <-v.shown
		if shown.ID == "" || !strings.HasPrefix(shown.ID, "c_") || len(shown.ID) < 10 {
			t.Fatalf("the broker must assign an unguessable id, got %q", shown.ID)
		}
		if shown.ActionID != "t7.1" || shown.Tool != "exec" || len(shown.Rules) != 1 || shown.Reason != "clean up" {
			t.Errorf("the presenter must get the whole request: %+v", shown)
		}
		if want := time.Date(2026, 10, 12, 9, 2, 0, 0, time.UTC); !shown.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", shown.ExpiresAt, want)
		}
		if err := b.Resolve(shown.ID, tt.approve, "go ahead\x00 but careful"); err != nil {
			t.Fatal(err)
		}
		r := a.wait(t)
		if r.Outcome != tt.want || r.Approved() != tt.approve {
			t.Errorf("outcome %v, want %v", r.Outcome, tt.want)
		}
		if r.Note != "go ahead but careful" {
			t.Errorf("note = %q (control characters must be dropped)", r.Note)
		}
		if o, ok := v.outcome(shown.ID); !ok || o != tt.want {
			t.Errorf("the viewer must be told the request is over: %v %v", o, ok)
		}
	}
	if len(b.Pending()) != 0 {
		t.Error("nothing should be pending")
	}
}

func TestNeverApprovesWithoutAViewer(t *testing.T) {
	b := New(Options{})
	r := b.Ask(context.Background(), sample())
	if r.Outcome != NoViewer || r.Approved() {
		t.Fatalf("no viewer: %+v", r)
	}
	// Detaching the viewer later also ends what is pending.
	b, v, _ := newBroker(t)
	a := ask(b, context.Background(), sample())
	<-v.shown
	b.SetPresenter(nil)
	if r := a.wait(t); r.Outcome != NoViewer {
		t.Fatalf("viewer detached while waiting: %+v", r)
	}
	if r := b.Ask(context.Background(), sample()); r.Outcome != NoViewer {
		t.Fatalf("after detaching: %+v", r)
	}
}

func TestTimeoutRejects(t *testing.T) {
	b, v, mt := newBroker(t)
	a := ask(b, context.Background(), sample())
	shown := <-v.shown
	mt.fire(0)
	r := a.wait(t)
	if r.Outcome != Timeout || r.Approved() {
		t.Fatalf("%+v", r)
	}
	if mt.last != 120*time.Second {
		t.Errorf("timer set to %v, want the 120s default", mt.last)
	}
	if o, _ := v.outcome(shown.ID); o != Timeout {
		t.Errorf("the viewer is told about the timeout: %v", o)
	}
	// An answer arriving after the timeout changes nothing and is reported as unknown.
	if err := b.Resolve(shown.ID, true, ""); err == nil {
		t.Error("a late approval must be refused")
	}
}

func TestContextCancelRejects(t *testing.T) {
	b, v, _ := newBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	a := ask(b, ctx, sample())
	<-v.shown
	cancel()
	if r := a.wait(t); r.Outcome != Canceled || r.Approved() {
		t.Fatalf("%+v", r)
	}
}

func TestCancelAll(t *testing.T) {
	b, v, _ := newBroker(t)
	a1 := ask(b, context.Background(), sample())
	a2 := ask(b, context.Background(), sample())
	<-v.shown
	<-v.shown
	if len(b.Pending()) != 2 {
		t.Fatalf("pending = %d", len(b.Pending()))
	}
	b.CancelAll()
	for _, a := range []asked{a1, a2} {
		if r := a.wait(t); r.Outcome != Canceled {
			t.Errorf("%+v", r)
		}
	}
}

func TestFailureToPresentRejects(t *testing.T) {
	b, v, _ := newBroker(t)
	v.failWith = errors.New("websocket closed")
	r := b.Ask(context.Background(), sample())
	if r.Outcome != Failed || r.Approved() {
		t.Fatalf("%+v", r)
	}
	if len(b.Pending()) != 0 {
		t.Error("a request that could not be shown must not stay pending")
	}
}

func TestResolveCannotBeReplayedOrForged(t *testing.T) {
	b, v, _ := newBroker(t)
	a := ask(b, context.Background(), sample())
	shown := <-v.shown
	if err := b.Resolve("c_0000000000000000", true, ""); err == nil {
		t.Error("a guessed id must not resolve anything")
	}
	if err := b.Resolve("", true, ""); err == nil {
		t.Error("an empty id must not resolve anything")
	}
	if err := b.Resolve(shown.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.Resolve(shown.ID, true, "changed my mind"); err == nil {
		t.Error("the first answer wins; a second one must be refused")
	}
	if r := a.wait(t); r.Outcome != Rejected {
		t.Errorf("the rejection must stand: %+v", r)
	}
}

func TestSeveralRequestsAreIndependent(t *testing.T) {
	b, v, _ := newBroker(t)
	a1 := ask(b, context.Background(), Request{ActionID: "1", Tool: "exec"})
	r1 := <-v.shown
	a2 := ask(b, context.Background(), Request{ActionID: "2", Tool: "exec"})
	r2 := <-v.shown
	if r1.ID == r2.ID {
		t.Fatal("ids must be unique")
	}
	_ = b.Resolve(r2.ID, true, "")
	_ = b.Resolve(r1.ID, false, "")
	if got := a1.wait(t); got.Outcome != Rejected {
		t.Errorf("first: %+v", got)
	}
	if got := a2.wait(t); got.Outcome != Approved {
		t.Errorf("second: %+v", got)
	}
}

func TestRaceBetweenAnswerAndTimeout(t *testing.T) {
	// Whichever happens first wins and the other is harmless; the result is never an approval after a timeout.
	for i := 0; i < 200; i++ {
		b, v, mt := newBroker(t)
		a := ask(b, context.Background(), sample())
		shown := <-v.shown
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); mt.fire(0) }()
		var resolveErr error
		go func() { defer wg.Done(); resolveErr = b.Resolve(shown.ID, true, "") }()
		wg.Wait()
		r := a.wait(t)
		switch r.Outcome {
		case Approved:
			if resolveErr != nil {
				t.Fatal("approved although Resolve reported an error")
			}
		case Timeout:
			if resolveErr == nil {
				t.Fatal("timed out although Resolve reported success")
			}
		default:
			t.Fatalf("unexpected outcome %v", r.Outcome)
		}
	}
}

func TestNoteIsTrimmedAndLimited(t *testing.T) {
	long := strings.Repeat("x", 2*MaxNoteLength)
	if got := cleanNote(long); len([]rune(got)) != MaxNoteLength {
		t.Errorf("note length %d, want %d", len([]rune(got)), MaxNoteLength)
	}
	if got := cleanNote("a\x1b[31mred\x07\nline2\ttab"); got != "a[31mred\nline2\ttab" {
		t.Errorf("control characters other than newline and tab must go: %q", got)
	}
	if got := cleanNote("héllo ✓"); got != "héllo ✓" {
		t.Errorf("unicode is kept: %q", got)
	}
}

func TestDefaultsAndRealTimer(t *testing.T) {
	b := New(Options{})
	if b.opts.Timeout != 120*time.Second {
		t.Errorf("default timeout %v", b.opts.Timeout)
	}
	// The real timer path: a very short timeout really does reject.
	v := newViewer()
	short := New(Options{Timeout: 20 * time.Millisecond})
	short.SetPresenter(v)
	start := time.Now()
	r := short.Ask(context.Background(), sample())
	if r.Outcome != Timeout {
		t.Fatalf("%+v", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the timeout took far too long")
	}
}
