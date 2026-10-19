// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package limits

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/PacifAIst/controlsnack/internal/safety"
)

// MaxRateWait is how long an action may be held back by the rate limiter before it is refused instead
// (PLAN.md section 6.6).
const MaxRateWait = 2 * time.Second

// Errors the orchestrator turns into tool results and state changes.
var (
	// ErrRateLimited: the action would have to wait longer than MaxRateWait. Tool error code rate_limited.
	ErrRateLimited = errors.New("rate limited: too many actions per second")
	// ErrSessionTimeout: the session has run for its whole time allowance.
	ErrSessionTimeout = errors.New("session timeout reached")
	// ErrIdleTimeout: nobody has used the session for the idle allowance.
	ErrIdleTimeout = errors.New("idle timeout reached")
)

// BudgetError reports an exhausted budget. Tool error code budget_exhausted; the orchestrator pauses for a
// check-in rather than ending the session.
type BudgetError struct {
	Kind  string // "actions" (per task), "turns" (per task) or "response" (per model response)
	Limit int
}

func (e *BudgetError) Error() string {
	switch e.Kind {
	case "response":
		return fmt.Sprintf("budget exhausted: at most %d actions per model response", e.Limit)
	default:
		return fmt.Sprintf("budget exhausted: %d %s used for this task", e.Limit, e.Kind)
	}
}

// Code maps an error from this package to the tool error code of PLAN.md section 5.3 ("" if it is none of ours).
func Code(err error) string {
	var be *BudgetError
	switch {
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.As(err, &be):
		return "budget_exhausted"
	case errors.Is(err, ErrSessionTimeout), errors.Is(err, ErrIdleTimeout):
		return "timeout"
	}
	return ""
}

// Clock is the time source, injectable so tests need no real waiting.
type Clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx is done, returning ctx's error in that case.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock uses the wall clock.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits d or until ctx ends.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Limits enforces the budgets of one session. It is safe for concurrent use.
//
// The counters and the limiter are driven by the safety.Profile, so --unsafe widens them and nothing else does.
// Per-response and per-task counts are charged when an action is admitted, not when it succeeds: a failing
// action still cost the user attention and, if it reached the guest, time.
type Limits struct {
	mu   sync.Mutex
	clk  Clock
	prof safety.Profile
	lim  *rate.Limiter

	start    time.Time
	activity time.Time

	taskActions int
	taskTurns   int
	respActions int
}

// New creates the limits for a session that starts now.
func New(p safety.Profile, clk Clock) *Limits {
	if clk == nil {
		clk = RealClock{}
	}
	burst := int(math.Ceil(p.ActionsPerSec))
	if burst < 1 {
		burst = 1
	}
	now := clk.Now()
	return &Limits{clk: clk, prof: p, lim: rate.NewLimiter(rate.Limit(p.ActionsPerSec), burst), start: now, activity: now}
}

// StartTask begins a new user task: the per-task counters start from zero.
func (l *Limits) StartTask() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.taskActions, l.taskTurns, l.respActions = 0, 0, 0
}

// CheckIn records that the user allowed the model to go on after a budget pause: it gets another full window.
func (l *Limits) CheckIn() { l.StartTask() }

// Touch records user activity, which postpones the idle timeout.
func (l *Limits) Touch() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.activity = l.clk.Now()
}

// Expired reports whether the whole-session allowance has run out.
func (l *Limits) Expired() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expiredLocked()
}

func (l *Limits) expiredLocked() error {
	if l.clk.Now().Sub(l.start) >= l.prof.SessionTimeout {
		return ErrSessionTimeout
	}
	return nil
}

// IdleExpired reports whether the idle allowance has run out. The orchestrator asks while the session is Ready
// or Paused (a running task is not idle).
func (l *Limits) IdleExpired() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clk.Now().Sub(l.activity) >= l.prof.IdleTimeout {
		return ErrIdleTimeout
	}
	return nil
}

// BeginTurn charges one model turn to the task. It fails with a *BudgetError once the turn budget is used up,
// and with ErrSessionTimeout when the session has run out of time.
func (l *Limits) BeginTurn() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.expiredLocked(); err != nil {
		return err
	}
	if l.taskTurns >= l.prof.MaxTurns {
		return &BudgetError{Kind: "turns", Limit: l.prof.MaxTurns}
	}
	l.taskTurns++
	return nil
}

// BeginResponse starts the accounting of one model response that asked for n actions. It returns how many may
// run (at most ActionsPerResponse, M) and how many are dropped; the model is told about the dropped ones.
func (l *Limits) BeginResponse(n int) (run, dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.respActions = 0
	run = min(n, l.prof.ActionsPerResponse)
	return run, n - run
}

// Admit is called before each action runs. It checks the session time and the per-response and per-task
// budgets, then waits for the rate limiter, but never longer than MaxRateWait: if the wait would be longer the
// action is refused with ErrRateLimited and no capacity is consumed. A canceled ctx ends the wait.
func (l *Limits) Admit(ctx context.Context) error {
	l.mu.Lock()
	if err := l.expiredLocked(); err != nil {
		l.mu.Unlock()
		return err
	}
	if l.respActions >= l.prof.ActionsPerResponse {
		l.mu.Unlock()
		return &BudgetError{Kind: "response", Limit: l.prof.ActionsPerResponse}
	}
	if l.taskActions >= l.prof.ActionsPerTask {
		l.mu.Unlock()
		return &BudgetError{Kind: "actions", Limit: l.prof.ActionsPerTask}
	}
	now := l.clk.Now()
	res := l.lim.ReserveN(now, 1)
	if !res.OK() {
		l.mu.Unlock()
		return ErrRateLimited
	}
	delay := res.DelayFrom(now)
	if delay > MaxRateWait {
		res.CancelAt(now)
		l.mu.Unlock()
		return ErrRateLimited
	}
	l.respActions++
	l.taskActions++
	l.mu.Unlock()

	if err := l.clk.Sleep(ctx, delay); err != nil {
		// The action will not run, so give its capacity back.
		l.mu.Lock()
		res.CancelAt(l.clk.Now())
		l.respActions--
		l.taskActions--
		l.mu.Unlock()
		return err
	}
	return nil
}

// Remaining reports what is left of the task and session budgets, for the observation and the viewer header.
func (l *Limits) Remaining() (actions, turns int, session time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	session = l.prof.SessionTimeout - l.clk.Now().Sub(l.start)
	if session < 0 {
		session = 0
	}
	return l.prof.ActionsPerTask - l.taskActions, l.prof.MaxTurns - l.taskTurns, session
}
