// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package viewer_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/safety/killswitch"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// timers is a clock that moves when it is told to, and fires what is due.
type timers struct {
	mu   sync.Mutex
	now  time.Duration
	list []*timer
}

type timer struct {
	at      time.Duration
	f       func()
	stopped bool
	fired   bool
}

func (c *timers) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{at: c.now + d, f: f}
	c.list = append(c.list, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// advance moves the clock and fires, in order, the timers that have come due and were not stopped.
func (c *timers) advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var due *timer
		for _, t := range c.list {
			if !t.stopped && !t.fired && t.at <= c.now && (due == nil || t.at < due.at) {
				due = t
			}
		}
		if due != nil {
			due.fired = true
		}
		c.mu.Unlock()
		if due == nil {
			return
		}
		due.f()
	}
}

// fireStopped runs the function of every timer that was stopped, as a timer that had already begun to fire when it was stopped.
func (c *timers) fireStopped() {
	c.mu.Lock()
	var fs []func()
	for _, t := range c.list {
		if t.stopped && !t.fired {
			fs = append(fs, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

type deadmanRig struct {
	clock        *timers
	d            *viewer.Deadman
	pauses, ends atomic.Int64
}

func newDeadman(t *testing.T) *deadmanRig {
	t.Helper()
	r := &deadmanRig{clock: &timers{}}
	d, err := viewer.NewDeadman(viewer.DeadmanOptions{
		Pause: func() { r.pauses.Add(1) }, End: func() { r.ends.Add(1) }, AfterFunc: r.clock.AfterFunc,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	return r
}

func (r *deadmanRig) counts() (int64, int64) { return r.pauses.Load(), r.ends.Load() }

func TestTheViewerGoingAwayPausesAfterThreeSecondsAndEndsAfterAMinute(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.clock.advance(2999 * time.Millisecond)
	if p, e := r.counts(); p != 0 || e != 0 {
		t.Fatalf("acted too soon: %d pauses, %d ends", p, e)
	}
	r.clock.advance(time.Millisecond)
	if p, e := r.counts(); p != 1 || e != 0 || !r.d.Paused() || r.d.Ended() {
		t.Fatalf("at 3 s: %d pauses, %d ends", p, e)
	}
	r.clock.advance(56 * time.Second) // 59 s
	if _, e := r.counts(); e != 0 {
		t.Fatal("ended too soon")
	}
	r.clock.advance(time.Second)
	if p, e := r.counts(); p != 1 || e != 1 || !r.d.Ended() {
		t.Fatalf("at 60 s: %d pauses, %d ends", p, e)
	}
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 1 || e != 1 {
		t.Errorf("it went on acting: %d pauses, %d ends", p, e)
	}
}

func TestAReloadOrABlinkOfTheConnectionDoesNothing(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.clock.advance(2 * time.Second)
	r.d.Connected()
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 0 || e != 0 || r.d.Away() || r.d.Paused() {
		t.Errorf("a window that came back in time was punished: %d pauses, %d ends", p, e)
	}
}

func TestATimerThatFiresAfterTheViewerIsBackDoesNothing(t *testing.T) {
	// A timer that had begun to fire when it was stopped runs its function anyway, and finds the viewer there.
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.d.Connected()
	r.clock.fireStopped()
	if p, e := r.counts(); p != 0 || e != 0 {
		t.Errorf("a late timer acted on a viewer that was there: %d pauses, %d ends", p, e)
	}
}

func TestAViewerThatComesBackAfterThePauseDoesNotResumeAndStopsTheEnd(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.clock.advance(10 * time.Second)
	if p, _ := r.counts(); p != 1 {
		t.Fatalf("%d pauses", p)
	}
	r.d.Connected()
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 1 || e != 0 {
		t.Errorf("%d pauses, %d ends after the viewer came back", p, e)
	}
	if !r.d.Paused() {
		t.Error("coming back resumed the agent; only the person's Resume may")
	}
	// A new absence starts the clock again from nothing.
	r.d.Disconnected()
	if r.d.Paused() {
		t.Error("the new absence still counts as paused")
	}
	r.clock.advance(3 * time.Second)
	if p, _ := r.counts(); p != 2 {
		t.Errorf("%d pauses for two absences", p)
	}
	r.clock.advance(57 * time.Second)
	if _, e := r.counts(); e != 1 {
		t.Errorf("%d ends", e)
	}
}

func TestAWindowThatNeverOpensEndsTheSessionToo(t *testing.T) {
	r := newDeadman(t)
	r.d.Arm()
	r.d.Arm() // a second arming is the same clock
	r.clock.advance(60 * time.Second)
	if p, e := r.counts(); p != 1 || e != 1 {
		t.Errorf("%d pauses, %d ends for a viewer that never came", p, e)
	}
	// Arming after the viewer has been seen does nothing.
	r = newDeadman(t)
	r.d.Connected()
	r.d.Arm()
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 0 || e != 0 || r.d.Away() {
		t.Errorf("arming a viewer that is there acted: %d pauses, %d ends", p, e)
	}
}

func TestStoppingTheDeadmanEndsItsWatch(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.d.Stop()
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 0 || e != 0 {
		t.Errorf("acted after Stop: %d pauses, %d ends", p, e)
	}
	r.d.Disconnected()
	r.d.Arm()
	r.d.Connected()
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 0 || e != 0 {
		t.Errorf("acted for an event after Stop: %d pauses, %d ends", p, e)
	}
}

func TestADisconnectEventTwiceIsOneAbsence(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.d.Disconnected()
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 1 || e != 1 {
		t.Errorf("%d pauses, %d ends", p, e)
	}
}

func TestATimerOfAnEarlierAbsenceDoesNothingInALaterOne(t *testing.T) {
	// A timer that had begun to fire when it was stopped runs its function anyway; it must find that its absence is over.
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected() // absence 1
	r.clock.advance(time.Second)
	r.d.Connected()    // stops the timers of absence 1
	r.d.Disconnected() // absence 2 begins at 1 s
	r.clock.fireStopped()
	if p, e := r.counts(); p != 0 || e != 0 {
		t.Errorf("a timer of the first absence acted in the second: %d pauses, %d ends", p, e)
	}
	r.clock.advance(3 * time.Second)
	if p, _ := r.counts(); p != 1 {
		t.Errorf("the second absence's own timer did not act: %d", p)
	}
}

func TestTheDeadmanWantsToKnowHowToActAndAnOrder(t *testing.T) {
	nop := func() {}
	for name, o := range map[string]viewer.DeadmanOptions{
		"no pause":     {End: nop},
		"no end":       {Pause: nop},
		"ending first": {Pause: nop, End: nop, PauseAfter: time.Minute, EndAfter: time.Second},
	} {
		if _, err := viewer.NewDeadman(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := viewer.NewDeadman(viewer.DeadmanOptions{Pause: nop, End: nop}); err != nil {
		t.Errorf("the defaults: %v", err)
	}
}

func TestTheDeadmanKillsWithItsOwnSource(t *testing.T) {
	k := killswitch.New(killswitch.Options{})
	viewer.KillOnExpiry(k, time.Minute)()
	select {
	case <-k.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the kill switch did not finish")
	}
	res, _ := k.Result()
	if res.Source != killswitch.SourceDeadman || res.Reason != "the viewer has been gone for 1m0s" {
		t.Errorf("%+v", res)
	}
}

func TestTheDeadmanFollowsTheRealConnection(t *testing.T) {
	// Wired to the hub as the session will wire it, with short real times.
	var pauses, ends atomic.Int64
	var d *viewer.Deadman
	h := startHub(t, func(o *viewer.HubOptions) {
		o.OnConnect = func() { d.Connected() }
		o.OnDisconnect = func() { d.Disconnected() }
	})
	d, _ = viewer.NewDeadman(viewer.DeadmanOptions{
		PauseAfter: 150 * time.Millisecond, EndAfter: 500 * time.Millisecond,
		Pause: func() { pauses.Add(1) }, End: func() { ends.Add(1) },
	})
	d.Arm()
	c := h.dial(t, h.login())
	readType(t, c, "hello")
	time.Sleep(700 * time.Millisecond) // longer than the end: a viewer that is there is never punished
	if pauses.Load() != 0 || ends.Load() != 0 {
		t.Fatalf("a connected viewer was punished: %d pauses, %d ends", pauses.Load(), ends.Load())
	}
	_ = c.Close(1000, "bye")
	until(t, "the pause", func() bool { return pauses.Load() == 1 })
	if ends.Load() != 0 {
		t.Error("ended with the pause")
	}
	until(t, "the end", func() bool { return ends.Load() == 1 })
	d.Stop()
}

func (c *timers) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.list {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

func TestARepeatedDisconnectDoesNotRestartTheClock(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	r.clock.advance(2 * time.Second)
	r.d.Disconnected() // the same absence, reported again
	r.clock.advance(time.Second)
	if p, _ := r.counts(); p != 1 {
		t.Errorf("%d pauses at 3 s of one absence: the clock was restarted", p)
	}
	r.clock.advance(57 * time.Second)
	if _, e := r.counts(); e != 1 {
		t.Errorf("%d ends at 60 s of one absence", e)
	}
}

func TestComingBackLeavesNoTimerRunning(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Disconnected()
	if got := r.clock.pending(); got != 2 {
		t.Fatalf("%d timers for an absence, want 2", got)
	}
	r.d.Connected()
	if got := r.clock.pending(); got != 0 {
		t.Errorf("%d timers are still running for a viewer that is back", got)
	}
	r.d.Disconnected()
	r.d.Stop()
	if got := r.clock.pending(); got != 0 {
		t.Errorf("%d timers are still running after Stop", got)
	}
}

func TestNothingStartsAfterStop(t *testing.T) {
	r := newDeadman(t)
	r.d.Connected()
	r.d.Stop()
	r.d.Disconnected() // an absence that begins after the session is over
	r.d.Connected()
	r.d.Disconnected()
	r.d.Arm()
	if got := r.clock.pending(); got != 0 {
		t.Errorf("%d timers were started after Stop", got)
	}
	r.clock.advance(time.Hour)
	if p, e := r.counts(); p != 0 || e != 0 {
		t.Errorf("acted after Stop: %d pauses, %d ends", p, e)
	}
}
