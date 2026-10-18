// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

func hashed(ok bool, hash string) Step {
	s := Step{ScreenHash: hash}
	s.Result.OK = ok
	return s
}

func TestTheSameActionOnAScreenThatDoesNotChangeIsALoop(t *testing.T) {
	g := NewGuards(0, 0)
	a := act(&actions.MouseArgs{Op: "click", X: 5, Y: 6})
	for i := 1; i <= 2; i++ {
		if why := g.Action(a, hashed(true, "h"), "seen"); why != "" {
			t.Fatalf("repeat %d: %q", i, why)
		}
	}
	why := g.Action(a, hashed(true, "h"), "seen")
	if !strings.Contains(why, "3 times in a row") || !strings.Contains(why, "mouse") || !strings.Contains(why, "nothing changed") {
		t.Errorf("the third: %q", why)
	}
}

func TestADifferentActionOrAScreenThatChangedIsNotALoop(t *testing.T) {
	g := NewGuards(3, 5)
	click := act(&actions.MouseArgs{Op: "click", X: 5, Y: 6})
	other := act(&actions.MouseArgs{Op: "click", X: 5, Y: 7})
	g.Action(click, hashed(true, "h"), "")
	g.Action(click, hashed(true, "h"), "")
	if why := g.Action(other, hashed(true, "h"), ""); why != "" {
		t.Errorf("a different action: %q", why)
	}
	g.Reset()
	g.Action(click, hashed(true, "h1"), "")
	g.Action(click, hashed(true, "h2"), "")
	if why := g.Action(click, hashed(true, "h3"), ""); why != "" {
		t.Errorf("a screen that moves on: %q", why)
	}
}

func TestWhatGivesNoScreenHashIsToldByWhatCameBackAndWhatTheModelSaw(t *testing.T) {
	ls := act(&actions.ExecArgs{Command: "ls", TimeoutS: 10})
	out := func(text string) Step {
		s := Step{}
		s.Result.OK = true
		s.Result.Parts = []prompt.Part{{Source: "exec.stdout", Text: text}}
		return s
	}
	g := NewGuards(3, 5)
	g.Action(ls, out("a"), "screen")
	g.Action(ls, out("a"), "screen")
	if why := g.Action(ls, out("a"), "screen"); why == "" {
		t.Error("the same command, the same output, the same screen: not a loop?")
	}
	g.Reset()
	g.Action(ls, out("a"), "screen")
	g.Action(ls, out("a"), "screen")
	if why := g.Action(ls, out("b"), "screen"); why != "" {
		t.Errorf("the output changed: %q", why)
	}
	g.Reset()
	g.Action(ls, out("a"), "screen 1")
	g.Action(ls, out("a"), "screen 2")
	if why := g.Action(ls, out("a"), "screen 3"); why != "" {
		t.Errorf("the screen the model saw changed: %q", why)
	}
	// A result that went differently is not the same state either.
	g.Reset()
	g.Action(ls, out("a"), "s")
	g.Action(ls, out("a"), "s")
	bad := out("a")
	bad.Result.OK, bad.Result.Code = false, actions.CodeTimeout
	if why := g.Action(ls, bad, "s"); strings.Contains(why, "nothing changed") {
		t.Errorf("a failure after two successes: %q", why)
	}
	// Another source of text is another state.
	g.Reset()
	g.Action(ls, out("a"), "s")
	g.Action(ls, out("a"), "s")
	err := Step{}
	err.Result.OK = true
	err.Result.Parts = []prompt.Part{{Source: "exec.stderr", Text: "a"}}
	if why := g.Action(ls, err, "s"); strings.Contains(why, "nothing changed") {
		t.Errorf("the same text from another stream: %q", why)
	}
}

func TestEnoughFailuresInARowTripTheBreakerAndASuccessClearsTheCount(t *testing.T) {
	g := NewGuards(3, 5)
	for i := 0; i < 4; i++ {
		a := act(&actions.KeyArgs{Keys: string(rune('a' + i))})
		if why := g.Action(a, hashed(false, ""), ""); why != "" {
			t.Fatalf("failure %d: %q", i+1, why)
		}
	}
	why := g.Action(act(&actions.KeyArgs{Keys: "e"}), hashed(false, ""), "")
	if why != "5 actions or answers in a row have failed." {
		t.Errorf("the fifth: %q", why)
	}
	g.Reset()
	for i := 0; i < 4; i++ {
		g.Action(act(&actions.KeyArgs{Keys: string(rune('a' + i))}), hashed(false, ""), "")
	}
	g.Action(act(&actions.KeyArgs{Keys: "x"}), hashed(true, "hx"), "")
	for i := 0; i < 4; i++ {
		if why := g.Action(act(&actions.KeyArgs{Keys: string(rune('p' + i))}), hashed(false, ""), ""); why != "" {
			t.Errorf("failure %d after a success: %q", i+1, why)
		}
	}
}

func TestUnusableAnswersCountAsFailuresAndUsableOnesDoNotClearThem(t *testing.T) {
	g := NewGuards(3, 5)
	for i := 0; i < 3; i++ {
		if why := g.Answer(false); why != "" {
			t.Fatalf("answer %d: %q", i+1, why)
		}
	}
	if why := g.Answer(true); why != "" {
		t.Errorf("a usable answer: %q", why)
	}
	g.Action(act(&actions.KeyArgs{Keys: "a"}), hashed(false, ""), "")
	if why := g.Answer(false); why != "5 actions or answers in a row have failed." {
		t.Errorf("the fifth failure: %q", why)
	}
	g.Reset()
	if why := g.Answer(false); why != "" {
		t.Errorf("after a reset: %q", why)
	}
}

func TestTheThresholdsCanBeSet(t *testing.T) {
	g := NewGuards(2, 2)
	a := act(&actions.KeyArgs{Keys: "a"})
	g.Action(a, hashed(true, "h"), "")
	if why := g.Action(a, hashed(true, "h"), ""); !strings.Contains(why, "2 times in a row") {
		t.Errorf("two repeats: %q", why)
	}
	g = NewGuards(2, 2)
	g.Action(act(&actions.KeyArgs{Keys: "a"}), hashed(false, ""), "")
	if why := g.Action(act(&actions.KeyArgs{Keys: "b"}), hashed(false, ""), ""); !strings.Contains(why, "2 actions or answers") {
		t.Errorf("two failures: %q", why)
	}
}

func TestTheLoopPausesTheTaskWhenTheModelRepeatsItselfAndGoesOnWhenThePersonSays(t *testing.T) {
	g := newLrig(t)
	g.loop.Guard = NewGuards(0, 0)
	g.act.fn = func(in Input) Step { s := okStep(in); s.ScreenHash = "h0"; return s }
	g.model.replies = []reply{calls(click("c1")), calls(click("c2")), calls(click("c3")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if n := len(g.model.requests()); n != 3 {
		t.Errorf("the model was asked %d times before the pause, want 3", n)
	}
	if txt := g.view.text(); !strings.Contains(txt, "3 times in a row") || !strings.Contains(txt, "Resume to let the task go on") {
		t.Errorf("the person was told: %s", txt)
	}
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}

	// A model that varies what it does is left alone.
	g = newLrig(t)
	g.loop.Guard = NewGuards(0, 0)
	g.act.fn = func(in Input) Step { s := okStep(in); s.ScreenHash = "h0"; return s }
	g.model.replies = []reply{calls(click("c1")), calls(keyA("c2")), calls(click("c3")), calls(keyA("c4")), calls(doneOK)}
	if out := g.run("t"); out.End != EndDone {
		t.Errorf("outcome %+v", out)
	}
}

func TestTheLoopPausesTheTaskAfterEnoughFailures(t *testing.T) {
	g := newLrig(t)
	g.loop.Guard = NewGuards(0, 0)
	g.act.fn = func(in Input) Step { return failStep(in, actions.CodeNotFound, "no such thing", StopBatch) }
	for i := 0; i < 5; i++ {
		g.model.replies = append(g.model.replies, calls(tc("c1", "key", `{"keys":"`+string(rune('a'+i))+`"}`)))
	}
	g.model.replies = append(g.model.replies, calls(doneOK))
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if n := len(g.act.ran()); n != 5 {
		t.Errorf("%d actions ran before the pause, want 5", n)
	}
	g.act.fn = nil
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
}

func TestTheGuardIsToldWhatTheModelSawEvenWithoutAPicture(t *testing.T) {
	g := newLrig(t)
	g.look.vision = false
	gd := &fakeGuard{}
	g.loop.Guard = gd
	g.model.replies = []reply{calls(keyA("c1")), calls(keyA("c2")), calls(doneOK)}
	g.run("t")
	if len(gd.screens) != 2 || gd.screens[0] == "" || len(gd.screens[0]) != 64 {
		t.Errorf("screens %q", gd.screens)
	}
	// With a picture it is the picture's hash.
	g = newLrig(t)
	gd = &fakeGuard{}
	g.loop.Guard = gd
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	g.run("t")
	if len(gd.screens) != 1 || gd.screens[0] != "h0" {
		t.Errorf("screens %q", gd.screens)
	}
}

func TestTheWatchdogEndsASessionWhoseTimeIsUp(t *testing.T) {
	prof := safety.ProfileFor(config.Defaults())
	m := NewMachine(nil)
	for _, s := range []State{Handshake, Ready, Running} {
		if err := m.To(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	clock := limits.NewFakeClock()
	lim := limits.New(prof, clock)
	tick := make(chan time.Time)
	var mu sync.Mutex
	var got []Expiry
	w := &Watchdog{Machine: m, Limits: lim, Tick: tick, OnExpire: func(e Expiry) { mu.Lock(); got = append(got, e); mu.Unlock() }}
	done := make(chan struct{})
	go func() { w.Run(context.Background()); close(done) }()
	// A send on the unbuffered channel completes when the watchdog has finished looking at the tick before.
	send := func() {
		select {
		case tick <- time.Now():
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the watchdog is not looking")
		}
	}
	send()
	clock.Advance(prof.IdleTimeout + time.Minute) // a task that is running is not idle, however long
	send()
	send()
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("a running task was called idle: %v", got)
	}
	clock.Advance(prof.SessionTimeout)
	send()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != ExpiredSession {
		t.Errorf("expired %v", got)
	}
}

func TestTheWatchdogEndsASessionNobodyIsActingIn(t *testing.T) {
	prof := safety.ProfileFor(config.Defaults())
	for _, st := range []State{Ready, Paused} {
		m := NewMachine(nil)
		for _, s := range []State{Handshake, Ready} {
			_ = m.To(s, "")
		}
		if st == Paused {
			_ = m.To(Running, "")
			_ = m.To(Paused, "")
		}
		clock := limits.NewFakeClock()
		lim := limits.New(prof, clock)
		tick := make(chan time.Time)
		got := make(chan Expiry, 2)
		w := &Watchdog{Machine: m, Limits: lim, Tick: tick, OnExpire: func(e Expiry) { got <- e }}
		go w.Run(context.Background())
		clock.Advance(prof.IdleTimeout - time.Second)
		lim.Touch() // the person did something
		clock.Advance(prof.IdleTimeout - time.Second)
		tick <- time.Now()
		select {
		case e := <-got:
			t.Fatalf("%s: expired too early: %v", st, e)
		case <-time.After(30 * time.Millisecond):
		}
		clock.Advance(2 * time.Second)
		tick <- time.Now()
		select {
		case e := <-got:
			if e != ExpiredIdle {
				t.Errorf("%s: expired %v", st, e)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: never expired", st)
		}
	}
}

func TestTheWatchdogStopsWhenTheSessionIsOverOrTheContextEnds(t *testing.T) {
	prof := safety.ProfileFor(config.Defaults())
	m := NewMachine(nil)
	_ = m.To(Ending, "")
	clock := limits.NewFakeClock()
	lim := limits.New(prof, clock)
	clock.Advance(2 * prof.SessionTimeout)
	w := &Watchdog{Machine: m, Limits: lim, OnExpire: func(Expiry) { t.Error("a session that is ending expired") }}
	done := make(chan struct{})
	go func() { w.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop for a session that is ending")
	}
	m2 := NewMachine(nil)
	ctx, cancel := context.WithCancel(context.Background())
	w2 := &Watchdog{Machine: m2, Limits: limits.New(prof, limits.NewFakeClock()), Tick: make(chan time.Time), OnExpire: func(Expiry) { t.Error("expired") }}
	done2 := make(chan struct{})
	go func() { w2.Run(ctx); close(done2) }()
	cancel()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop with its context")
	}
}

func TestTheWatchdogLooksAtOnceAndOnItsOwnClockByDefault(t *testing.T) {
	prof := safety.ProfileFor(config.Defaults())
	m := NewMachine(nil)
	clock := limits.NewFakeClock()
	lim := limits.New(prof, clock)
	clock.Advance(2 * prof.SessionTimeout)
	got := make(chan Expiry, 1)
	w := &Watchdog{Machine: m, Limits: lim, OnExpire: func(e Expiry) { got <- e }}
	go w.Run(context.Background())
	select {
	case e := <-got:
		if e != ExpiredSession {
			t.Errorf("expired %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a session that was over before it began was not noticed")
	}
}

// fakeTimers are the timers of the dead-man switch, fired by the test.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ft *fakeTimers) after(d time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.timers = append(ft.timers, t)
	return func() bool { ft.mu.Lock(); defer ft.mu.Unlock(); was := !t.stopped; t.stopped = true; return was }
}

// advance fires the timers that are due after d, in order.
func (ft *fakeTimers) fire(d time.Duration) {
	ft.mu.Lock()
	var due []*fakeTimer
	for _, t := range ft.timers {
		if !t.stopped && t.d <= d {
			t.stopped = true
			due = append(due, t)
		}
	}
	ft.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func TestTheDeadmanPausesTheTaskWhenTheViewerIsGoneAndOnlyThePersonResumesIt(t *testing.T) {
	g := newCrig(t)
	g.running()
	ft := &fakeTimers{}
	ended := make(chan struct{}, 1)
	dm, err := NewDeadman(g.c, func() { ended <- struct{}{} }, viewer.DeadmanOptions{AfterFunc: ft.after})
	if err != nil {
		t.Fatal(err)
	}
	dm.Connected()
	dm.Disconnected()
	ft.fire(2 * time.Second)
	if g.m.State() != Running {
		t.Fatalf("paused after 2 s: %s", g.m.State())
	}
	// A reload within the time pauses nothing.
	dm.Connected()
	ft.fire(time.Hour)
	if g.m.State() != Running {
		t.Fatalf("paused although the viewer came back: %s", g.m.State())
	}
	dm.Disconnected()
	ft.fire(3 * time.Second)
	if g.m.State() != Paused || g.m.Reason() != "the viewer went away" {
		t.Fatalf("after 3 s: %s %q", g.m.State(), g.m.Reason())
	}
	dm.Connected() // the viewer is back; the task is not resumed for it
	if g.m.State() != Paused {
		t.Errorf("the task resumed by itself: %s", g.m.State())
	}
	g.c.Control(viewer.OpResume)
	if g.m.State() != Running {
		t.Errorf("the person's Resume: %s", g.m.State())
	}
	select {
	case <-ended:
		t.Error("the session ended although the viewer came back")
	default:
	}
}

func TestTheDeadmanEndsTheSessionAfterAMinute(t *testing.T) {
	g := newCrig(t)
	g.running()
	ft := &fakeTimers{}
	ended := make(chan struct{}, 2)
	dm, err := NewDeadman(g.c, func() { ended <- struct{}{} }, viewer.DeadmanOptions{AfterFunc: ft.after})
	if err != nil {
		t.Fatal(err)
	}
	dm.Disconnected()
	ft.fire(59 * time.Second)
	if len(ended) != 0 {
		t.Fatal("ended before a minute")
	}
	ft.fire(60 * time.Second)
	if len(ended) != 1 {
		t.Errorf("ended %d times", len(ended))
	}
	dm.Stop()
}

func TestTheGuardSeesWhatFailedAsWellAsWhatWorked(t *testing.T) {
	g := newLrig(t)
	gd := &fakeGuard{}
	g.loop.Guard = gd
	g.act.fn = func(in Input) Step { return failStep(in, actions.CodeNotFound, "no", StopBatch) }
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2")), calls(doneOK)}
	g.run("t")
	if len(gd.actions) != 1 {
		t.Errorf("the guard saw %v: a failure ends the answer, but it was one action", gd.actions)
	}
	// A pause the guard asks for after a failure is made, and what failed is still reported to the model.
	g = newLrig(t)
	g.loop.Guard = &fakeGuard{onAction: func(int) string { return "Too many failures." }}
	g.act.fn = func(in Input) Step { return failStep(in, actions.CodeNotFound, "no", StopBatch) }
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if txt := g.view.text(); !strings.Contains(txt, "Too many failures. Resume to let the task go on") {
		t.Errorf("notices %s", txt)
	}
	g.act.fn = nil
	_ = g.m.To(Running, "resumed")
	wait(t, ch)
	if tools := toolTexts(g.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "error not_found") {
		t.Errorf("results %q", tools)
	}
}

func TestGuardsWithNoThresholdsUseTheDefaultsInTheirWordsToo(t *testing.T) {
	g := NewGuards(0, 0)
	for i := 0; i < 4; i++ {
		g.Action(act(&actions.KeyArgs{Keys: string(rune('a' + i))}), hashed(false, ""), "")
	}
	if why := g.Action(act(&actions.KeyArgs{Keys: "e"}), hashed(false, ""), ""); why != "5 actions or answers in a row have failed." {
		t.Errorf("the fifth failure: %q", why)
	}
}

func TestAResetStartsTheCountOfRepeatsOver(t *testing.T) {
	g := NewGuards(3, 5)
	a := act(&actions.KeyArgs{Keys: "a"})
	g.Action(a, hashed(true, "h"), "")
	g.Action(a, hashed(true, "h"), "")
	g.Reset()
	if why := g.Action(a, hashed(true, "h"), ""); why != "" {
		t.Errorf("the first repeat after a reset: %q", why)
	}
}

func TestTheSameOutputThatSucceededAndThatFailedAreNotTheSameState(t *testing.T) {
	ls := act(&actions.ExecArgs{Command: "ls", TimeoutS: 10})
	mk := func(ok bool, code string) Step {
		s := Step{}
		s.Result.OK, s.Result.Code = ok, code
		return s
	}
	// Only the success differs.
	g := NewGuards(3, 50)
	g.Action(ls, mk(false, actions.CodeTimeout), "s")
	g.Action(ls, mk(false, actions.CodeTimeout), "s")
	if why := g.Action(ls, mk(true, actions.CodeTimeout), "s"); why != "" {
		t.Errorf("success differs: %q", why)
	}
	// Only the code differs.
	g = NewGuards(3, 50)
	g.Action(ls, mk(false, actions.CodeTimeout), "s")
	g.Action(ls, mk(false, actions.CodeTimeout), "s")
	if why := g.Action(ls, mk(false, actions.CodeNotFound), "s"); why != "" {
		t.Errorf("code differs: %q", why)
	}
	// And the same is a loop.
	g = NewGuards(3, 50)
	g.Action(ls, mk(false, actions.CodeTimeout), "s")
	g.Action(ls, mk(false, actions.CodeTimeout), "s")
	if why := g.Action(ls, mk(false, actions.CodeTimeout), "s"); !strings.Contains(why, "nothing changed") {
		t.Errorf("the same failure thrice: %q", why)
	}
}

func TestWhatTheModelSawIsFingerprintedByThePictureOrByTheTextOfTheScreen(t *testing.T) {
	seenWith := func(tree string, ocr string) vision.Seen {
		var s vision.Seen
		if tree != "" {
			s.Raw.Tree = &protocol.A11yTreeRes{Generation: "g", Nodes: []protocol.A11yNode{{ID: "a1", Name: tree}}}
		}
		if ocr != "" {
			s.Raw.OCR = &protocol.OCRRes{Generation: "t", Lines: []protocol.OCRLine{{ID: "o1", Text: ocr}}}
		}
		return s
	}
	base := screenKey(seenWith("Open", "Hello"), "")
	if len(base) != 64 || base != screenKey(seenWith("Open", "Hello"), "") {
		t.Errorf("the same screen has the key %q", base)
	}
	if screenKey(seenWith("Close", "Hello"), "") == base {
		t.Error("a changed tree has the same key")
	}
	if screenKey(seenWith("Open", "World"), "") == base {
		t.Error("changed text has the same key")
	}
	if screenKey(seenWith("Open", "Hello"), "h7") != "h7" {
		t.Error("with a picture the key is the picture's hash")
	}
}
