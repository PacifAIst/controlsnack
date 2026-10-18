// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

// Guards is the Guard of a session: a model that does the same thing again and again while the screen stays as it was is not
// getting anywhere, and nor is one whose actions and answers keep failing. Neither is stopped for good: the task is paused and
// the person is told, and they decide (Resume forgets what was seen).
type Guards struct {
	loop    *limits.LoopDetector
	breaker *limits.Breaker
	// repeats and failures are the thresholds, for the words.
	repeats, failures int
}

// NewGuards makes the guards; a threshold below its minimum selects the default (3 identical actions, 5 failures in a row).
func NewGuards(repeats, failures int) *Guards {
	if repeats < 2 {
		repeats = limits.DefaultLoopThreshold
	}
	if failures < 1 {
		failures = limits.DefaultBreakerThreshold
	}
	return &Guards{loop: limits.NewLoopDetector(repeats), breaker: limits.NewBreaker(failures), repeats: repeats, failures: failures}
}

// Action is told what an action came to. The action is a repeat when it has the same tool and arguments as the one before and
// the screen is as it was: the hash the agent gave after an input, or, for what gives none, a fingerprint of what the model saw
// and of what came back. Waiting is not a repeat: time is what a wait is for, and it can be what the screen needs (an editor
// that registers with the accessibility service a while after its window is up); waits are bounded by the budgets, and a wait
// forgets the repeats before it, because the world has had time to change.
func (g *Guards) Action(a actions.Action, s Step, screen string) string {
	looping := false
	if a.Tool == actions.ToolWaitFor {
		g.loop.Reset()
	} else {
		looping = g.loop.Observe(limits.ActionKey(a.Tool, a.Args), stateOf(s, screen))
	}
	tripped := g.breaker.Record(s.Result.OK)
	switch {
	case looping:
		return fmt.Sprintf("The model has done the same thing %d times in a row (%s) and nothing changed.", g.repeats, textclean.Line(prompt.Describe(a), 100))
	case tripped:
		return fmt.Sprintf("%d actions or answers in a row have failed.", g.failures)
	}
	return ""
}

// Answer is told whether an answer of the model held an action that could be run. An answer that did is not a success: its
// action may fail, and the streak of failures goes on.
func (g *Guards) Answer(usable bool) string {
	if !usable && g.breaker.Record(false) {
		return fmt.Sprintf("%d actions or answers in a row have failed.", g.failures)
	}
	return ""
}

// Reset forgets what was seen: the person resumed.
func (g *Guards) Reset() {
	g.loop.Reset()
	g.breaker.Reset()
}

// stateOf is what the screen and the result were after an action, for telling a repeat that changed nothing from one that did.
func stateOf(s Step, screen string) string {
	if s.ScreenHash != "" {
		return s.ScreenHash
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%v|%s|", screen, s.Result.OK, s.Result.Code)
	for _, p := range s.Result.Parts {
		fmt.Fprintf(h, "%s|%s|", p.Source, p.Text)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Expiry says which clock ran out.
type Expiry string

// The clocks of a session.
const (
	// ExpiredSession: the whole allowance of time for the session is used up.
	ExpiredSession Expiry = "session_timeout"
	// ExpiredIdle: nobody has done anything for the idle timeout while the session was ready or paused.
	ExpiredIdle Expiry = "idle_timeout"
)

// Watchdog watches the two clocks that no task is there to watch: the session's time (a task waiting for the person for
// hours does not notice it running out) and the idle timeout (while the session is ready or paused, nobody is acting).
type Watchdog struct {
	Machine *Machine
	Limits  *limits.Limits
	// OnExpire is called once, with the clock that ran out; it must not block. The session ends.
	OnExpire func(Expiry)
	// Tick is when to look (every second when nil).
	Tick <-chan time.Time
}

// Run looks at the clocks until one has run out, the session is over or the context ends.
func (w *Watchdog) Run(ctx context.Context) {
	tick := w.Tick
	if tick == nil {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		tick = t.C
	}
	for {
		if e, over := w.check(); over {
			if e != "" {
				w.OnExpire(e)
			}
			return
		}
		select {
		case <-tick:
		case <-ctx.Done():
			return
		}
	}
}

// check says which clock has run out, if one has, and whether watching is over.
func (w *Watchdog) check() (Expiry, bool) {
	st := w.Machine.State()
	if st.Final() {
		return "", true
	}
	if w.Limits.Expired() != nil {
		return ExpiredSession, true
	}
	if (st == Ready || st == Paused) && w.Limits.IdleExpired() != nil {
		return ExpiredIdle, true
	}
	return "", false
}

// NewDeadman makes the dead-man switch of a session (D-20): the task is paused when the viewer has been gone for the first
// time given, and does not go on by itself when it comes back (the person says Resume); the session ends after the second.
func NewDeadman(c *Console, end func(), o viewer.DeadmanOptions) (*viewer.Deadman, error) {
	o.Pause = func() { c.PauseFor("the viewer went away") }
	o.End = end
	return viewer.NewDeadman(o)
}
