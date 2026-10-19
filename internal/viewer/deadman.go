// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PacifAIst/controlsnack/internal/safety/killswitch"
)

// The dead-man switch exists because the design assumes that someone is watching (D-20): an agent that acts for an hour with
// the window closed, the laptop shut or the viewer crashed is acting with nobody to stop it. When the viewer is gone, the
// agent is paused after a few seconds (long enough that a reload does not pause anything), and the session ends after a
// minute. It follows the connection: a window that is open answers the host's pings whether or not it is in front, so a
// window behind other windows does not end a session, and one that has gone (closed, crashed, or the machine asleep) is found
// by its connection closing or by the pings that are not answered.

// Default times of the dead-man switch (PLAN.md section 11 and the budgets table).
const (
	DefaultPauseAfter = 3 * time.Second
	DefaultEndAfter   = 60 * time.Second
)

// DeadmanOptions configure a Deadman.
type DeadmanOptions struct {
	// PauseAfter is how long the viewer may be gone before the agent is paused (default 3 s).
	PauseAfter time.Duration
	// EndAfter is how long it may be gone before the session ends (default 60 s).
	EndAfter time.Duration
	// Pause is called when the viewer has been gone for PauseAfter. The agent stays paused until the person says Resume: a
	// viewer that comes back does not resume it. It must not block.
	Pause func()
	// End is called when the viewer has been gone for EndAfter. It must not block.
	End func()
	// AfterFunc starts a timer (default time.AfterFunc); the tests use their own clock.
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
}

// Deadman watches whether the viewer is there.
type Deadman struct {
	o DeadmanOptions

	mu      sync.Mutex
	away    bool
	paused  bool
	ended   bool
	stopped bool
	seen    bool   // the viewer has connected at least once
	gen     uint64 // each absence has its own number, so that a timer of an earlier one does nothing
	cancel  []func() bool
}

// NewDeadman checks the options and makes a Deadman that is not watching yet.
func NewDeadman(o DeadmanOptions) (*Deadman, error) {
	if o.Pause == nil || o.End == nil {
		return nil, errors.New("viewer: the dead-man switch needs to know how to pause and how to end the session")
	}
	if o.PauseAfter <= 0 {
		o.PauseAfter = DefaultPauseAfter
	}
	if o.EndAfter <= 0 {
		o.EndAfter = DefaultEndAfter
	}
	if o.EndAfter < o.PauseAfter {
		return nil, fmt.Errorf("viewer: the session cannot end (%s) before it pauses (%s)", o.EndAfter, o.PauseAfter)
	}
	if o.AfterFunc == nil {
		o.AfterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return &Deadman{o: o}, nil
}

// Arm starts the clock for a viewer that has not come yet: a window that never opens ends the session as one that has gone
// does. It does nothing if the viewer has already been seen or the clock is running.
func (d *Deadman) Arm() {
	d.mu.Lock()
	seen := d.seen
	d.mu.Unlock()
	if !seen {
		d.Disconnected()
	}
}

// Connected is called when the viewer connects (HubOptions.OnConnect). It stops the clock. It does not resume what was paused.
func (d *Deadman) Connected() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.away, d.seen = false, true
	d.stopTimers()
}

// Disconnected is called when the viewer goes (HubOptions.OnDisconnect). It starts the clock.
func (d *Deadman) Disconnected() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.away {
		return
	}
	d.away, d.paused, d.ended = true, false, false
	d.gen++
	gen := d.gen
	d.cancel = append(d.cancel,
		d.o.AfterFunc(d.o.PauseAfter, func() { d.fire(gen, false) }),
		d.o.AfterFunc(d.o.EndAfter, func() { d.fire(gen, true) }),
	)
}

// Stop ends the watching: the session is over, and nothing is called after it.
func (d *Deadman) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	d.stopTimers()
}

func (d *Deadman) stopTimers() {
	for _, stop := range d.cancel {
		stop()
	}
	d.cancel = nil
}

// Away reports whether the viewer is gone, and Paused and Ended whether the switch has acted.
func (d *Deadman) Away() bool   { d.mu.Lock(); defer d.mu.Unlock(); return d.away }
func (d *Deadman) Paused() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.paused }
func (d *Deadman) Ended() bool  { d.mu.Lock(); defer d.mu.Unlock(); return d.ended }

func (d *Deadman) fire(gen uint64, end bool) {
	d.mu.Lock()
	if d.stopped || !d.away || d.gen != gen {
		d.mu.Unlock()
		return
	}
	if end {
		d.ended = true
	} else {
		d.paused = true
	}
	d.mu.Unlock()
	if end {
		d.o.End()
	} else {
		d.o.Pause()
	}
}

// KillOnExpiry returns an End function that fires the kill switch with the dead-man as its source.
func KillOnExpiry(k *killswitch.Killer, after time.Duration) func() {
	return func() {
		k.TriggerAsync(killswitch.SourceDeadman, fmt.Sprintf("the viewer has been gone for %s", after))
	}
}
