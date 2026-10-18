// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is the state of a session (PLAN.md section 3.4). The values are the names the viewer shows.
type State string

// The states.
const (
	Booting         State = "booting"          // accelerator selection, QEMU, the guest's boot
	Handshake       State = "handshake"        // the agent is being installed and has not yet answered
	Ready           State = "ready"            // the VM is up and no task is running
	Running         State = "running"          // a task is running: the turn loop is going round
	AwaitingConfirm State = "awaiting_confirm" // an action waits for the person's decision
	AwaitingUser    State = "awaiting_user"    // the model asked the person a question
	Paused          State = "paused"           // the person paused, or the viewer went away
	Ending          State = "ending"           // going down in order: the last records are written, the VM is stopped
	Ended           State = "ended"            // over (terminal)
	Killed          State = "killed"           // the kill switch fired (terminal)
)

// AllStates lists every state in the order of the life of a session.
var AllStates = []State{Booting, Handshake, Ready, Running, AwaitingConfirm, AwaitingUser, Paused, Ending, Ended, Killed}

// Terminal reports whether nothing more can happen in the state.
func (s State) Terminal() bool { return s == Ended || s == Killed }

// Final reports whether the session is over or on its way to being over.
func (s State) Final() bool { return s == Ending || s == Ended || s == Killed }

// Active reports whether a task is in progress, whether or not it is moving at this moment.
func (s State) Active() bool {
	return s == Running || s == AwaitingConfirm || s == AwaitingUser || s == Paused
}

// transitions is every move a session can make. Anything else is a bug of the session, and the machine says so.
//
//	Booting → Handshake → Ready ⇄ Running ⇄ { AwaitingConfirm, AwaitingUser, Paused }
//	Running → Ready (done, Stop, task budget)    any → Ending → Ended    any → Killed
var transitions = map[State][]State{
	Booting:         {Handshake, Ending, Killed},
	Handshake:       {Ready, Ending, Killed},
	Ready:           {Running, Ending, Killed},
	Running:         {Ready, AwaitingConfirm, AwaitingUser, Paused, Ending, Killed},
	AwaitingConfirm: {Running, Ready, Ending, Killed},
	AwaitingUser:    {Running, Ready, Ending, Killed},
	Paused:          {Running, Ready, Ending, Killed},
	Ending:          {Ended, Killed},
	Ended:           nil,
	Killed:          nil,
}

// CanMove reports whether a session may go from one state to another.
func CanMove(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// TransitionError is returned for a move that the diagram does not allow.
type TransitionError struct{ From, To State }

func (e *TransitionError) Error() string {
	if e.From.Terminal() {
		return fmt.Sprintf("the session is %s and cannot become %s", e.From, e.To)
	}
	return fmt.Sprintf("a session cannot go from %s to %s", e.From, e.To)
}

// ErrUnknownState is returned for a state that is not one of the ten.
var ErrUnknownState = errors.New("session: unknown state")

// Change is one move, as the observers see it.
type Change struct {
	From, To State
	Reason   string
	At       time.Time
}

// Machine holds the state of a session and lets whatever else the session has (the viewer, the audit log, the turn loop)
// follow it. It is safe for concurrent use; the changes are told to the observers one at a time, in the order they were made.
type Machine struct {
	now func() time.Time

	mu      sync.Mutex
	state   State
	reason  string
	changed chan struct{} // closed, and replaced, at each change: what a waiter waits on

	obsMu sync.Mutex // held while the observers are told, so that the order of the changes is the order they are heard in
	obs   []func(Change)
}

// NewMachine starts a session in Booting.
func NewMachine(now func() time.Time) *Machine {
	if now == nil {
		now = time.Now
	}
	return &Machine{now: now, state: Booting, reason: "starting", changed: make(chan struct{})}
}

// State is the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Reason is why the machine is in the current state.
func (m *Machine) Reason() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
}

// OnChange adds an observer. It is told of every move from now on, in order, and must not call To.
func (m *Machine) OnChange(f func(Change)) {
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	m.obs = append(m.obs, f)
}

// To moves to another state. A move the diagram does not allow is an error and changes nothing.
func (m *Machine) To(next State, reason string) error {
	_, err := m.move(nil, next, reason)
	return err
}

// ToIf moves only if the machine is in one of the given states, and says whether it moved: the way to answer two events that
// race (the person pauses at the moment the task ends) without one undoing the other.
func (m *Machine) ToIf(from []State, next State, reason string) bool {
	moved, _ := m.move(from, next, reason)
	return moved
}

// move makes one move. only, when it is not empty, limits the states the move may start from; a move that is not made for that
// reason is not an error.
func (m *Machine) move(only []State, next State, reason string) (bool, error) {
	if !known(next) {
		return false, fmt.Errorf("%w %q", ErrUnknownState, string(next))
	}
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	m.mu.Lock()
	if len(only) > 0 {
		in := false
		for _, s := range only {
			in = in || s == m.state
		}
		if !in {
			m.mu.Unlock()
			return false, nil
		}
	}
	if !CanMove(m.state, next) {
		err := &TransitionError{From: m.state, To: next}
		m.mu.Unlock()
		if len(only) > 0 {
			return false, nil
		}
		return false, err
	}
	c := Change{From: m.state, To: next, Reason: reason, At: m.now()}
	m.state, m.reason = next, reason
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
	for _, f := range m.obs {
		f(c)
	}
	return true, nil
}

func known(s State) bool {
	for _, k := range AllStates {
		if k == s {
			return true
		}
	}
	return false
}

// WaitRunning blocks until the machine is Running, and returns Running. It returns the state it found instead if that is one
// from which the task will not go on by itself (Ready: the task ended; a final state), and the context's error if the
// context ends first. While the state is Paused, AwaitingUser or AwaitingConfirm it keeps waiting: that is the gate of the
// turn loop.
func (m *Machine) WaitRunning(ctx context.Context) (State, error) {
	for {
		m.mu.Lock()
		s, ch := m.state, m.changed
		m.mu.Unlock()
		switch s {
		case Running:
			return Running, nil
		case Paused, AwaitingUser, AwaitingConfirm, Booting, Handshake:
			// keep waiting
		default:
			return s, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return s, ctx.Err()
		}
	}
}

// Wait blocks until the machine is in one of the states, and returns the one it found; it returns the context's error if the
// context ends first, and the state it found with ErrFinal if the machine became final without reaching one of them.
func (m *Machine) Wait(ctx context.Context, states ...State) (State, error) {
	for {
		m.mu.Lock()
		s, ch := m.state, m.changed
		m.mu.Unlock()
		for _, w := range states {
			if s == w {
				return s, nil
			}
		}
		if s.Terminal() {
			return s, ErrFinal
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return s, ctx.Err()
		}
	}
}

// ErrFinal is returned by Wait when the session ended before reaching the state that was waited for.
var ErrFinal = errors.New("session: the session is over")
