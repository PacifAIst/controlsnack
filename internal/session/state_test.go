// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// allowed is the diagram of PLAN.md section 3.4, written out once more, edge by edge, so that a change to the table in state.go
// has to be a change to this too.
var allowed = map[State][]State{
	Booting:         {Handshake, Ending, Killed},
	Handshake:       {Ready, Ending, Killed},
	Ready:           {Running, Ending, Killed},
	Running:         {Ready, AwaitingConfirm, AwaitingUser, Paused, Ending, Killed},
	AwaitingConfirm: {Running, Ready, Ending, Killed},
	AwaitingUser:    {Running, Ready, Ending, Killed},
	Paused:          {Running, Ready, Ending, Killed},
	Ending:          {Ended, Killed},
	Ended:           {},
	Killed:          {},
}

func TestEveryMoveOfTheDiagramIsAllowedAndNoOtherIs(t *testing.T) {
	for _, from := range AllStates {
		want := map[State]bool{}
		for _, to := range allowed[from] {
			want[to] = true
		}
		for _, to := range AllStates {
			if got := CanMove(from, to); got != want[to] {
				t.Errorf("%s -> %s: allowed %v, the diagram says %v", from, to, got, want[to])
			}
		}
	}
}

func TestTheDiagramHasTheShapeThePlanDescribes(t *testing.T) {
	// Any state but the two terminal ones can be killed and can end; nothing leaves a terminal state; Ready is reached from
	// Handshake and from the task states; a task starts only from Ready.
	for _, s := range AllStates {
		if s.Terminal() {
			if len(transitions[s]) != 0 {
				t.Errorf("%s is terminal and has moves", s)
			}
			continue
		}
		if !CanMove(s, Killed) {
			t.Errorf("%s cannot be killed", s)
		}
		if s != Ending && !CanMove(s, Ending) {
			t.Errorf("%s cannot end", s)
		}
	}
	for _, s := range AllStates {
		if CanMove(s, Running) && s != Ready && !s.Active() {
			t.Errorf("%s can become Running", s)
		}
	}
	if CanMove(Booting, Ready) || CanMove(Booting, Running) || CanMove(Handshake, Running) {
		t.Error("the VM can be used before the agent has answered")
	}
	if CanMove(Ready, Paused) || CanMove(Ready, AwaitingUser) || CanMove(Ready, AwaitingConfirm) {
		t.Error("something is awaited or paused with no task")
	}
	if CanMove(Ending, Ready) || CanMove(Ended, Ending) || CanMove(Killed, Ended) {
		t.Error("a session that is going down comes back")
	}
}

func TestTheStatesAreThoseTheViewerKnows(t *testing.T) {
	// The page and the audit log use these names: the two packages must agree.
	for s, v := range map[State]string{
		Booting: viewer.StateBooting, Handshake: viewer.StateHandshake, Ready: viewer.StateReady, Running: viewer.StateRunning,
		AwaitingConfirm: viewer.StateAwaitingConfirm, AwaitingUser: viewer.StateAwaitingUser, Paused: viewer.StatePaused,
		Ending: viewer.StateEnding, Ended: viewer.StateEnded, Killed: viewer.StateKilled,
	} {
		if string(s) != v {
			t.Errorf("%s is %q in the viewer", s, v)
		}
	}
	if len(AllStates) != 10 {
		t.Errorf("%d states", len(AllStates))
	}
}

func TestPredicatesOfTheStates(t *testing.T) {
	for _, tc := range []struct {
		s                       State
		terminal, final, active bool
	}{
		{Booting, false, false, false}, {Handshake, false, false, false}, {Ready, false, false, false},
		{Running, false, false, true}, {AwaitingConfirm, false, false, true}, {AwaitingUser, false, false, true}, {Paused, false, false, true},
		{Ending, false, true, false}, {Ended, true, true, false}, {Killed, true, true, false},
	} {
		if tc.s.Terminal() != tc.terminal || tc.s.Final() != tc.final || tc.s.Active() != tc.active {
			t.Errorf("%s: terminal %v final %v active %v", tc.s, tc.s.Terminal(), tc.s.Final(), tc.s.Active())
		}
	}
}

func TestAMoveThatIsNotAllowedChangesNothingAndSaysWhy(t *testing.T) {
	m := NewMachine(nil)
	if m.State() != Booting || m.Reason() != "starting" {
		t.Fatalf("a new session is %s (%s)", m.State(), m.Reason())
	}
	var heard []Change
	m.OnChange(func(c Change) { heard = append(heard, c) })
	err := m.To(Running, "too early")
	var te *TransitionError
	if !errors.As(err, &te) || te.From != Booting || te.To != Running || m.State() != Booting || len(heard) != 0 {
		t.Fatalf("%v, state %s, heard %v", err, m.State(), heard)
	}
	if err.Error() != "a session cannot go from booting to running" {
		t.Errorf("message %q", err)
	}
	if err := m.To("dreaming", "x"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("an unknown state: %v", err)
	}
	if err := m.To(Booting, "again"); err == nil {
		t.Error("a move to the same state")
	}
	// From a terminal state the message says so.
	if err := m.To(Killed, "kill"); err != nil {
		t.Fatal(err)
	}
	if err := m.To(Ending, "late"); err == nil || err.Error() != "the session is killed and cannot become ending" {
		t.Errorf("after the kill: %v", err)
	}
}

func TestObserversHearEveryMoveInOrderWithItsReason(t *testing.T) {
	clock := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	m := NewMachine(func() time.Time { clock = clock.Add(time.Second); return clock })
	var a, b []string
	m.OnChange(func(c Change) {
		a = append(a, fmt.Sprintf("%s>%s:%s@%s", c.From, c.To, c.Reason, c.At.Format("15:04:05")))
	})
	m.OnChange(func(c Change) { b = append(b, string(c.To)) })
	for _, step := range []struct {
		to     State
		reason string
	}{{Handshake, "booted"}, {Ready, "agent up"}, {Running, "task"}, {AwaitingConfirm, "needs approval"}, {Running, "approved"}, {Paused, "viewer gone"}, {Running, "resumed"}, {Ready, "done"}, {Ending, "bye"}, {Ended, "over"}} {
		if err := m.To(step.to, step.reason); err != nil {
			t.Fatal(err)
		}
	}
	if len(a) != 10 || a[0] != "booting>handshake:booted@12:00:01" || a[9] != "ending>ended:over@12:00:10" {
		t.Errorf("the first observer heard %q", a)
	}
	if fmt.Sprint(b) != "[handshake ready running awaiting_confirm running paused running ready ending ended]" {
		t.Errorf("the second heard %v", b)
	}
	if m.State() != Ended || m.Reason() != "over" {
		t.Errorf("%s (%s)", m.State(), m.Reason())
	}
}

func TestConcurrentMovesAreOrderedAndEachObserverHearsThemInTheOrderTheyHappened(t *testing.T) {
	m := NewMachine(nil)
	if err := m.To(Handshake, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.To(Ready, ""); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var heard []Change
	m.OnChange(func(c Change) { mu.Lock(); heard = append(heard, c); mu.Unlock() })
	// Four goroutines each try to start and to end a task, over and over, and to pause it: whatever interleaving, the changes
	// heard must chain (each begins where the one before ended).
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				m.ToIf([]State{Ready}, Running, fmt.Sprint("start ", i))
				m.ToIf([]State{Running}, Paused, "pause")
				m.ToIf([]State{Paused}, Running, "resume")
				m.ToIf([]State{Running, Paused}, Ready, "end")
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	prev := Ready
	for i, c := range heard {
		if c.From != prev || !CanMove(c.From, c.To) {
			t.Fatalf("change %d is %s>%s after %s", i, c.From, c.To, prev)
		}
		prev = c.To
	}
	if prev != m.State() || len(heard) == 0 {
		t.Errorf("the last change led to %s, the machine is %s (%d changes)", prev, m.State(), len(heard))
	}
}

func TestToIfMovesOnlyFromTheStatesAskedFor(t *testing.T) {
	m := NewMachine(nil)
	_ = m.To(Handshake, "")
	_ = m.To(Ready, "")
	_ = m.To(Running, "")
	var n int
	m.OnChange(func(Change) { n++ })
	if m.ToIf([]State{Ready}, Paused, "x") || m.State() != Running || n != 0 {
		t.Error("moved from a state that was not asked for")
	}
	if m.ToIf([]State{Running}, Booting, "x") || m.State() != Running || n != 0 {
		t.Error("made a move the diagram does not allow")
	}
	if !m.ToIf([]State{Ready, Running}, Paused, "pause") || m.State() != Paused || n != 1 {
		t.Errorf("did not move: %s, %d changes", m.State(), n)
	}
	if m.ToIf([]State{Running}, Paused, "again") || n != 1 {
		t.Error("the same move twice")
	}
	if m.ToIf(nil, Running, "no states given") == false || m.State() != Running {
		t.Error("with no states given any state will do")
	}
}

func TestWaitRunningIsTheGateOfTheTurnLoop(t *testing.T) {
	m := NewMachine(nil)
	_ = m.To(Handshake, "")
	_ = m.To(Ready, "")
	ctx := context.Background()
	// Ready means the task is over: the loop must not wait for it.
	if s, err := m.WaitRunning(ctx); s != Ready || err != nil {
		t.Errorf("in Ready: %s, %v", s, err)
	}
	_ = m.To(Running, "")
	if s, err := m.WaitRunning(ctx); s != Running || err != nil {
		t.Errorf("in Running: %s, %v", s, err)
	}
	// Paused, awaiting the person: it waits, and goes on when the state is Running again.
	for _, wait := range []State{Paused, AwaitingUser, AwaitingConfirm} {
		_ = m.To(wait, "")
		got := make(chan State, 1)
		go func() { s, _ := m.WaitRunning(ctx); got <- s }()
		select {
		case s := <-got:
			t.Fatalf("returned %s while %s", s, wait)
		case <-time.After(50 * time.Millisecond):
		}
		_ = m.To(Running, "")
		if s := <-got; s != Running {
			t.Errorf("after %s: %s", wait, s)
		}
	}
	// It returns what it finds when the task ends or the session does.
	for _, final := range []State{Ready, Ending, Killed} {
		m2 := NewMachine(nil)
		_ = m2.To(Handshake, "")
		_ = m2.To(Ready, "")
		_ = m2.To(Running, "")
		_ = m2.To(Paused, "")
		got := make(chan State, 1)
		go func() { s, _ := m2.WaitRunning(ctx); got <- s }()
		time.Sleep(20 * time.Millisecond)
		_ = m2.To(final, "")
		if s := <-got; s != final {
			t.Errorf("a paused task that became %s: %s", final, s)
		}
	}
	// A context that ends stops the wait.
	_ = m.To(Paused, "")
	cctx, cancel := context.WithCancel(ctx)
	got := make(chan error, 1)
	go func() { _, err := m.WaitRunning(cctx); got <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled wait: %v", err)
	}
	// Before the VM is ready the loop does not run either.
	m3 := NewMachine(nil)
	done := make(chan struct{})
	go func() { _, _ = m3.WaitRunning(ctx); close(done) }()
	select {
	case <-done:
		t.Error("returned while Booting")
	case <-time.After(30 * time.Millisecond):
	}
	_ = m3.To(Killed, "")
	<-done
}

func TestWaitSeesTheStateAskedForOrTheEndOfTheSession(t *testing.T) {
	m := NewMachine(nil)
	ctx := context.Background()
	got := make(chan State, 1)
	go func() { s, _ := m.Wait(ctx, Ready); got <- s }()
	time.Sleep(20 * time.Millisecond)
	_ = m.To(Handshake, "")
	_ = m.To(Ready, "")
	if s := <-got; s != Ready {
		t.Errorf("waited for Ready and got %s", s)
	}
	if s, err := m.Wait(ctx, Ready, Running); s != Ready || err != nil {
		t.Errorf("already there: %s, %v", s, err)
	}
	_ = m.To(Killed, "")
	if s, err := m.Wait(ctx, Running); s != Killed || !errors.Is(err, ErrFinal) {
		t.Errorf("a killed session: %s, %v", s, err)
	}
	if s, err := m.Wait(ctx, Killed); s != Killed || err != nil {
		t.Errorf("waiting for Killed in Killed: %s, %v", s, err)
	}
	m2 := NewMachine(nil)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := m2.Wait(cctx, Ready); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a wait that times out: %v", err)
	}
}
