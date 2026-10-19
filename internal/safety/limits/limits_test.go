// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package limits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/safety"
)

func newLimits(t *testing.T, mutate func(*config.Config)) (*Limits, *FakeClock) {
	t.Helper()
	c := config.Defaults()
	if mutate != nil {
		mutate(&c)
	}
	clk := NewFakeClock()
	return New(safety.ProfileFor(c), clk), clk
}

func TestRateLimiterSpacesActions(t *testing.T) {
	l, clk := newLimits(t, nil) // N = 2 actions per second, burst 2
	ctx := context.Background()
	l.StartTask()
	start := clk.Now()
	for i := 0; i < 2; i++ {
		l.BeginResponse(3)
		if err := l.Admit(ctx); err != nil {
			t.Fatalf("action %d: %v", i, err)
		}
	}
	if clk.Slept() != 0 {
		t.Fatalf("the burst of two should not wait, slept %v", clk.Slept())
	}
	// The third and fourth are spaced 500 ms apart.
	l.BeginResponse(3)
	if err := l.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(start); got < 450*time.Millisecond || got > 550*time.Millisecond {
		t.Fatalf("third action waited %v, want about 500ms", got)
	}
	if err := l.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(start); got < 950*time.Millisecond || got > 1050*time.Millisecond {
		t.Fatalf("fourth action at %v, want about 1s", got)
	}
}

func TestRateLimiterRefusesLongWaits(t *testing.T) {
	// One action every 5 seconds: the second would wait 5 s, longer than the 2 s allowed.
	l, clk := newLimits(t, func(c *config.Config) { c.Safety.ActionsPerSec = 0.2 })
	ctx := context.Background()
	l.StartTask()
	l.BeginResponse(3)
	if err := l.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	slept := clk.Slept()
	err := l.Admit(ctx)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	if Code(err) != "rate_limited" {
		t.Errorf("Code = %q", Code(err))
	}
	if clk.Slept() != slept {
		t.Error("a refused action must not wait")
	}
	// The refusal consumed nothing: after the interval passes the next action goes through immediately.
	clk.Advance(5 * time.Second)
	if err := l.Admit(ctx); err != nil {
		t.Fatalf("after waiting out the interval: %v", err)
	}
	// ... and the refused one was not charged to the budgets.
	if a, _, _ := l.Remaining(); a != 60-2 {
		t.Errorf("actions left = %d, want 58 (two admitted, the refused one not charged)", a)
	}
}

func TestUnsafeProfileRunsFaster(t *testing.T) {
	l, clk := newLimits(t, func(c *config.Config) {
		c.Safety.Unsafe = true
		c.Safety.ActionsPerSec = 10
		c.Safety.ActionsPerResponse = 10
	})
	l.StartTask()
	l.BeginResponse(10)
	start := clk.Now()
	for i := 0; i < 20; i++ {
		l.BeginResponse(10)
		if err := l.Admit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := clk.Now().Sub(start); got > 2100*time.Millisecond {
		t.Errorf("20 actions at 10/s took %v", got)
	}
}

func TestPerResponseBudget(t *testing.T) {
	l, _ := newLimits(t, nil) // M = 3
	l.StartTask()
	run, dropped := l.BeginResponse(5)
	if run != 3 || dropped != 2 {
		t.Fatalf("5 requested: run %d dropped %d, want 3 and 2", run, dropped)
	}
	run, dropped = l.BeginResponse(2)
	if run != 2 || dropped != 0 {
		t.Fatalf("2 requested: run %d dropped %d", run, dropped)
	}
	// Even if a caller ignores BeginResponse, Admit enforces M.
	l.BeginResponse(3)
	for i := 0; i < 3; i++ {
		if err := l.Admit(context.Background()); err != nil {
			t.Fatalf("action %d: %v", i, err)
		}
	}
	err := l.Admit(context.Background())
	var be *BudgetError
	if !errors.As(err, &be) || be.Kind != "response" || be.Limit != 3 || Code(err) != "budget_exhausted" {
		t.Fatalf("fourth action in one response: %v", err)
	}
	// A new response starts counting again.
	l.BeginResponse(3)
	if err := l.Admit(context.Background()); err != nil {
		t.Fatalf("next response: %v", err)
	}
}

func TestPerTaskBudgetsAndCheckIn(t *testing.T) {
	l, _ := newLimits(t, func(c *config.Config) { c.Safety.ActionsPerTask = 7; c.Safety.MaxTurns = 3 })
	ctx := context.Background()
	l.StartTask()
	for turn := 0; turn < 3; turn++ {
		if err := l.BeginTurn(); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}
	err := l.BeginTurn()
	var be *BudgetError
	if !errors.As(err, &be) || be.Kind != "turns" || be.Limit != 3 {
		t.Fatalf("fourth turn: %v", err)
	}
	used := 0
	for {
		l.BeginResponse(3)
		var e error
		for i := 0; i < 3 && e == nil; i++ {
			if e = l.Admit(ctx); e == nil {
				used++
			}
		}
		if e != nil {
			if !errors.As(e, &be) || be.Kind != "actions" {
				t.Fatalf("expected the action budget, got %v", e)
			}
			break
		}
		if used > 20 {
			t.Fatal("budget never ran out")
		}
	}
	if used != 7 {
		t.Errorf("admitted %d actions, want exactly the budget of 7", used)
	}

	// The user checks in: another full window.
	l.CheckIn()
	if a, tn, _ := l.Remaining(); a != 7 || tn != 3 {
		t.Errorf("after check-in: %d actions, %d turns left", a, tn)
	}
	if err := l.BeginTurn(); err != nil {
		t.Errorf("turn after check-in: %v", err)
	}
	// A new task also starts fresh.
	l.StartTask()
	if a, tn, _ := l.Remaining(); a != 7 || tn != 3 {
		t.Errorf("new task: %d, %d", a, tn)
	}
}

func TestSessionAndIdleTimeouts(t *testing.T) {
	l, clk := newLimits(t, nil) // session 60 min, idle 15 min
	ctx := context.Background()
	l.StartTask()
	if err := l.Expired(); err != nil {
		t.Fatal(err)
	}
	clk.Advance(14 * time.Minute)
	if err := l.IdleExpired(); err != nil {
		t.Fatalf("14 minutes idle: %v", err)
	}
	clk.Advance(2 * time.Minute)
	if err := l.IdleExpired(); !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("16 minutes idle: %v", err)
	}
	l.Touch() // the user typed something
	if err := l.IdleExpired(); err != nil {
		t.Fatalf("after activity: %v", err)
	}
	if Code(ErrIdleTimeout) != "timeout" || Code(ErrSessionTimeout) != "timeout" {
		t.Error("timeouts map to the timeout code")
	}

	clk.Advance(45 * time.Minute)
	if err := l.Expired(); !errors.Is(err, ErrSessionTimeout) {
		t.Fatalf("after an hour: %v", err)
	}
	l.BeginResponse(1)
	if err := l.Admit(ctx); !errors.Is(err, ErrSessionTimeout) {
		t.Errorf("Admit after the session ended: %v", err)
	}
	if err := l.BeginTurn(); !errors.Is(err, ErrSessionTimeout) {
		t.Errorf("BeginTurn after the session ended: %v", err)
	}
	if _, _, left := l.Remaining(); left != 0 {
		t.Errorf("time left after expiry = %v", left)
	}
}

func TestSessionAllowanceIsTheProfiles(t *testing.T) {
	l, clk := newLimits(t, func(c *config.Config) { c.Safety.Unsafe = true; c.Safety.Timeout = config.Duration(8 * time.Hour) })
	clk.Advance(7 * time.Hour)
	if err := l.Expired(); err != nil {
		t.Errorf("unsafe sessions last 8 hours: %v", err)
	}
	clk.Advance(2 * time.Hour)
	if err := l.Expired(); !errors.Is(err, ErrSessionTimeout) {
		t.Errorf("after 9 hours: %v", err)
	}
}

func TestCancelingDuringTheWaitGivesTheCapacityBack(t *testing.T) {
	l, _ := newLimits(t, nil)
	l.StartTask()
	l.BeginResponse(3)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // use the burst up
		if err := l.Admit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before, _, _ := l.Remaining()
	err := l.Admit(canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if after, _, _ := l.Remaining(); after != before {
		t.Errorf("a canceled action was charged: %d -> %d", before, after)
	}
}

func TestConcurrentAdmitIsSafe(t *testing.T) {
	l, _ := newLimits(t, func(c *config.Config) {
		c.Safety.Unsafe = true
		c.Safety.ActionsPerSec = 10
		c.Safety.ActionsPerResponse = 10
		c.Safety.ActionsPerTask = 500
	})
	l.StartTask()
	l.BeginResponse(10)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted, refused := 0, 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := l.Admit(context.Background())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				admitted++
			} else {
				refused++
			}
		}()
	}
	wg.Wait()
	if admitted != 10 || refused != 30 {
		t.Errorf("admitted %d refused %d, want exactly the 10 of one response", admitted, refused)
	}
}

func TestNilClockMeansWallTime(t *testing.T) {
	l := New(safety.ProfileFor(config.Defaults()), nil)
	l.StartTask()
	l.BeginResponse(1)
	if err := l.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (RealClock{}).Sleep(context.Background(), 0); err != nil {
		t.Error(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (RealClock{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled sleep must return at once: %v", err)
	}
	if err := (RealClock{}).Sleep(context.Background(), time.Millisecond); err != nil {
		t.Error(err)
	}
}

func TestBreaker(t *testing.T) {
	b := NewBreaker(0) // default 5
	for i := 0; i < 4; i++ {
		if b.Record(false) {
			t.Fatalf("tripped after %d failures", i+1)
		}
	}
	if !b.Record(false) || !b.Tripped() {
		t.Fatal("five consecutive failures must trip it")
	}
	if !b.Record(true) {
		t.Error("a success does not clear a tripped breaker; only the user's resume does")
	}
	b.Reset()
	if b.Tripped() {
		t.Error("Reset clears it")
	}
	// A success in between resets the streak.
	for i := 0; i < 4; i++ {
		b.Record(false)
	}
	b.Record(true)
	for i := 0; i < 4; i++ {
		b.Record(false)
	}
	if b.Tripped() {
		t.Error("4+success+4 is not 5 in a row")
	}
	small := NewBreaker(2)
	small.Record(false)
	if !small.Record(false) {
		t.Error("custom threshold ignored")
	}
}

func TestLoopDetector(t *testing.T) {
	d := NewLoopDetector(0) // default 3
	key := ActionKey("click_element", map[string]any{"id": "a17", "button": "left"})
	first, second := d.Observe(key, "h1"), d.Observe(key, "h1") // stateful calls, so not a repeated expression
	if first || second {
		t.Fatal("two repeats are not a loop")
	}
	if !d.Observe(key, "h1") {
		t.Fatal("three identical actions on an unchanged screen are a loop")
	}
	if !d.Observe(key, "h1") {
		t.Fatal("it keeps reporting while the loop goes on")
	}
	// The screen changed: progress, not a loop.
	if d.Observe(key, "h2") {
		t.Fatal("a changed screen starts the count over")
	}
	// A different action starts over too.
	other := ActionKey("key", map[string]any{"keys": "Return"})
	d.Observe(other, "h2")
	d.Observe(other, "h2")
	if d.Observe(key, "h2") {
		t.Fatal("interleaved actions are not a loop")
	}
	d.Reset()
	first, second = d.Observe(key, "h2"), d.Observe(key, "h2")
	if first || second {
		t.Fatal("Reset forgets history")
	}
}

func TestActionKey(t *testing.T) {
	a := ActionKey("exec", map[string]any{"command": "ls", "timeout_s": 5})
	b := ActionKey("exec", map[string]any{"timeout_s": 5, "command": "ls"})
	if a != b {
		t.Error("the order the model wrote the arguments in must not matter")
	}
	if a == ActionKey("exec", map[string]any{"command": "ls -l", "timeout_s": 5}) {
		t.Error("different arguments must give a different key")
	}
	if a == ActionKey("type_text", map[string]any{"command": "ls", "timeout_s": 5}) {
		t.Error("a different tool must give a different key")
	}
	if len(a) != 64 {
		t.Errorf("key %q", a)
	}
	if ActionKey("x", make(chan int)) == "" {
		t.Error("unmarshalable arguments still get a key")
	}
}

func TestGate(t *testing.T) {
	var g Gate
	release, ok := g.TryAcquire()
	if !ok {
		t.Fatal("a free gate must be acquirable")
	}
	if _, ok2 := g.TryAcquire(); ok2 {
		t.Fatal("only one LLM request may be in flight")
	}
	release()
	release() // idempotent
	again, ok := g.TryAcquire()
	if !ok {
		t.Fatal("released gate must be free again")
	}
	again()
}

func TestBudgetErrorMessages(t *testing.T) {
	for _, tt := range []struct {
		e    *BudgetError
		want string
	}{
		{&BudgetError{Kind: "response", Limit: 3}, "at most 3 actions per model response"},
		{&BudgetError{Kind: "actions", Limit: 60}, "60 actions used"},
		{&BudgetError{Kind: "turns", Limit: 100}, "100 turns used"},
	} {
		if got := tt.e.Error(); !contains(got, tt.want) {
			t.Errorf("%q should mention %q", got, tt.want)
		}
	}
	if Code(errors.New("other")) != "" || Code(nil) != "" {
		t.Error("Code returns empty for foreign errors")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
