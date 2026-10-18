// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/confirm"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/viewer"
)

type fakeStep struct {
	mu    sync.Mutex
	calls []bool
}

func (f *fakeStep) SetStep(on bool) { f.mu.Lock(); f.calls = append(f.calls, on); f.mu.Unlock() }

// crig is a console over the real state machine, broker and budgets and a fake log and viewer.
type crig struct {
	t       *testing.T
	c       *Console
	m       *Machine
	broker  *confirm.Broker
	view    *events
	log     *log
	clock   *limits.FakeClock
	lim     *limits.Limits
	step    *fakeStep
	started []string
	fatal   []error
	mu      sync.Mutex
}

func newCrig(t *testing.T) *crig {
	t.Helper()
	g := &crig{t: t, m: NewMachine(nil), view: &events{}, log: &log{}, clock: limits.NewFakeClock(), step: &fakeStep{}}
	g.lim = limits.New(safety.ProfileFor(config.Defaults()), g.clock)
	g.broker = confirm.New(confirm.Options{Timeout: 5 * time.Second})
	g.c = &Console{Machine: g.m, Broker: g.broker, Limits: g.lim, Viewer: g.view, Audit: g.log, Step: g.step,
		StartTask: func(text string) { g.mu.Lock(); g.started = append(g.started, text); g.mu.Unlock() },
		Fatal:     func(err error) { g.mu.Lock(); g.fatal = append(g.fatal, err); g.mu.Unlock() }}
	return g
}

func (g *crig) ready() {
	g.t.Helper()
	for _, s := range []State{Handshake, Ready} {
		if err := g.m.To(s, ""); err != nil {
			g.t.Fatal(err)
		}
	}
}

func (g *crig) running() {
	g.t.Helper()
	g.ready()
	if err := g.m.To(Running, ""); err != nil {
		g.t.Fatal(err)
	}
}

func (g *crig) startedTasks() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...)
}

func (e *events) confirmRequests() []viewer.ConfirmRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.ConfirmRequest
	for _, m := range e.msgs {
		if c, ok := m.(viewer.ConfirmRequest); ok {
			out = append(out, c)
		}
	}
	return out
}

func (e *events) resolved() []viewer.ConfirmResolved {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.ConfirmResolved
	for _, m := range e.msgs {
		if c, ok := m.(viewer.ConfirmResolved); ok {
			out = append(out, c)
		}
	}
	return out
}

func (e *events) states() []viewer.State {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.State
	for _, m := range e.msgs {
		if c, ok := m.(viewer.State); ok {
			out = append(out, c)
		}
	}
	return out
}

func (e *events) notices() []viewer.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.Notice
	for _, m := range e.msgs {
		if c, ok := m.(viewer.Notice); ok {
			out = append(out, c)
		}
	}
	return out
}

func TestWhatThePersonWritesIsShownBackLoggedAndKeptForTheModel(t *testing.T) {
	g := newCrig(t)
	g.running()
	at := time.Date(2026, 3, 4, 7, 6, 7, 0, time.FixedZone("two hours east", 2*3600)) // 05:06:07 in UTC
	g.c.Now = func() time.Time { return at }
	g.c.Chat("please hurry")
	g.c.Chat("and be careful")

	chats := g.view.chats()
	if len(chats) != 2 || chats[0].Role != "user" || chats[0].Text != "please hurry" || chats[0].TS != "2026-03-04T05:06:07Z" || chats[0].ID == chats[1].ID || chats[0].ID == "" {
		t.Fatalf("chat lines %+v", chats)
	}
	all := g.log.all()
	if len(all) != 2 || all[0].Kind != "user" || all[0].Event != "chat" || all[0].Actor != "user" || string(all[0].Data) != `{"text":"please hurry"}` {
		t.Errorf("records %+v", all)
	}
	if got := g.c.Messages(); strings.Join(got, "|") != "please hurry|and be careful" {
		t.Errorf("messages %q", got)
	}
	if got := g.c.Messages(); len(got) != 0 {
		t.Errorf("the messages came twice: %q", got)
	}
	if len(g.startedTasks()) != 0 {
		t.Error("a task was started in the middle of a task")
	}
}

func TestAMessageToAnIdleSessionStartsATaskAndOneToABusyOneWaitsForTheNextTurn(t *testing.T) {
	g := newCrig(t)
	g.c.Chat("too early")
	if len(g.startedTasks()) != 0 || len(g.c.Messages()) != 1 {
		t.Error("a message before the session is ready should wait")
	}
	g.ready()
	g.c.Chat("find the kernel")
	if got := g.startedTasks(); len(got) != 1 || got[0] != "find the kernel" {
		t.Errorf("tasks %q", got)
	}
	if len(g.c.Messages()) != 0 {
		t.Error("the message that started a task is also kept for the model")
	}
	if len(g.view.chats()) != 2 {
		t.Errorf("the person's words are shown %d times", len(g.view.chats()))
	}
	for _, s := range []State{Running, Paused, AwaitingUser} {
		g2 := newCrig(t)
		g2.running()
		if s == Paused {
			g2.m.ToIf([]State{Running}, Paused, "")
		}
		if s == AwaitingUser {
			g2.m.ToIf([]State{Running}, AwaitingUser, "")
		}
		g2.c.Chat("hello")
		if len(g2.startedTasks()) != 0 || len(g2.c.Messages()) != 1 {
			t.Errorf("a message in state %s", s)
		}
	}
	// With nothing to start a task a message to a ready session waits for one.
	g3 := newCrig(t)
	g3.ready()
	g3.c.StartTask = nil
	g3.c.Chat("hello")
	if len(g3.c.Messages()) != 1 {
		t.Error("the message was lost")
	}
	// Nobody is listening once the session is over.
	g4 := newCrig(t)
	g4.running()
	if err := g4.m.To(Ending, ""); err != nil {
		t.Fatal(err)
	}
	g4.c.Chat("anyone?")
	if len(g4.c.Messages()) != 0 || len(g4.startedTasks()) != 0 {
		t.Error("a message to a session that is ending")
	}
}

func TestAMessageIsTheAnswerWhenTheModelHasAskedAQuestion(t *testing.T) {
	g := newCrig(t)
	g.running()
	got := make(chan string, 1)
	go func() {
		a, err := g.c.Ask(context.Background(), "Which?")
		if err != nil {
			a = "error: " + err.Error()
		}
		got <- a
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.c.mu.Lock()
		waiting := g.c.asking != nil
		g.c.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the question was never put")
		}
		time.Sleep(time.Millisecond)
	}
	g.c.Chat("the blue one")
	select {
	case a := <-got:
		if a != "the blue one" {
			t.Errorf("the answer is %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	if len(g.c.Messages()) != 0 {
		t.Error("the answer is also kept as a message")
	}
	// The next message is an ordinary one.
	g.c.Chat("another thing")
	if len(g.c.Messages()) != 1 {
		t.Error("a message after the answer should be kept for the model")
	}
}

func TestAQuestionStopsWaitingWhenItsContextEnds(t *testing.T) {
	g := newCrig(t)
	g.running()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := g.c.Ask(ctx, "Q?"); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end")
	}
	// A message after that is not swallowed by a question nobody waits on.
	g.c.Chat("late")
	if len(g.c.Messages()) != 1 {
		t.Error("the message was swallowed")
	}
}

func TestTheWordsOfThePersonAreCleanedCutAndMaskedInTheLog(t *testing.T) {
	g := newCrig(t)
	g.running()
	g.c.Redactor = secret.NewRedactor()
	g.c.Redactor.AddSecret("sk-live-1234567890")
	g.c.Chat("use \x1b[31mkey sk-live-1234567890\x07 please")
	g.c.Chat(strings.Repeat("long ", 1000))
	msgs := g.c.Messages()
	if len(msgs) != 2 || strings.ContainsAny(msgs[0], "\x1b\x07") || len([]rune(msgs[1])) > maxChatText+2 {
		t.Errorf("messages %q", msgs)
	}
	for _, r := range g.log.all() {
		if strings.Contains(string(r.Data), "sk-live-1234567890") {
			t.Errorf("the secret is in the log: %s", r.Data)
		}
	}
	if chats := g.view.chats(); len(chats) != 2 || strings.ContainsAny(chats[0].Text, "\x1b\x07") {
		t.Errorf("chat lines %+v", chats)
	}
}

func TestBlankMessagesAreIgnoredAndOnlyTheNewestAreKept(t *testing.T) {
	g := newCrig(t)
	g.running()
	g.c.Chat("")
	g.c.Chat("  \n\t ")
	if len(g.view.chats()) != 0 || len(g.log.all()) != 0 || len(g.c.Messages()) != 0 {
		t.Error("a blank message was taken")
	}
	for i := 0; i < maxQueued+5; i++ {
		g.c.Chat("m" + string(rune('a'+i%26)) + strings.Repeat("x", i))
	}
	msgs := g.c.Messages()
	if len(msgs) != maxQueued || !strings.HasPrefix(msgs[len(msgs)-1], "m") || len(msgs[len(msgs)-1]) != 2+maxQueued+4 {
		t.Errorf("%d messages kept; the last is %q", len(msgs), msgs[len(msgs)-1])
	}
	if len(msgs[0]) != 2+5 {
		t.Errorf("the oldest kept is %q, want the sixth sent", msgs[0])
	}
}

func TestEveryWordFromThePersonPostponesTheIdleTimeout(t *testing.T) {
	prof := safety.ProfileFor(config.Defaults())
	for name, act := range map[string]func(*crig){
		"chat":    func(g *crig) { g.c.Chat("hello") },
		"confirm": func(g *crig) { g.c.Confirm("nothing", true, "") },
		"control": func(g *crig) { g.c.Control(viewer.OpPause) },
	} {
		g := newCrig(t)
		g.running()
		g.clock.Advance(prof.IdleTimeout - time.Second)
		act(g)
		g.clock.Advance(2 * time.Second)
		if err := g.lim.IdleExpired(); err != nil {
			t.Errorf("%s did not count as the person's activity: %v", name, err)
		}
	}
}

func TestAConfirmationIsPutToThePageAndAnsweredByThePerson(t *testing.T) {
	g := newCrig(t)
	g.running()
	g.c.Connected()
	got := make(chan confirm.Result, 1)
	go func() {
		got <- g.broker.Ask(context.Background(), confirm.Request{ActionID: "t1a1", Tool: "exec", Args: json.RawMessage(`{"command":"rm -r x"}`),
			Rules: []confirm.RuleInfo{{ID: "CONFIRM-DELETE", Explanation: "deletes files"}}, Reason: "clean up"})
	}()
	var req viewer.ConfirmRequest
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rs := g.view.confirmRequests(); len(rs) == 1 {
			req = rs[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the page was never asked")
		}
		time.Sleep(time.Millisecond)
	}
	if req.ID == "" || req.ActionID != "t1a1" || req.Tool != "exec" || string(req.Args) != `{"command":"rm -r x"}` || req.Reason != "clean up" ||
		len(req.Rules) != 1 || req.Rules[0].ID != "CONFIRM-DELETE" || req.Rules[0].Explanation != "deletes files" {
		t.Errorf("request %+v", req)
	}
	if ts, err := time.Parse(timeFormatMS, req.ExpiresAt); err != nil || time.Until(ts) < 3*time.Second || !strings.HasSuffix(req.ExpiresAt, "Z") {
		t.Errorf("expires at %q (%v)", req.ExpiresAt, err)
	}
	g.c.Confirm("not-a-request", true, "")
	g.c.Confirm(req.ID, true, "go ahead\x1b[0m")
	select {
	case res := <-got:
		if !res.Approved() || !strings.HasPrefix(res.Note, "go ahead") || strings.ContainsRune(res.Note, 0x1b) {
			t.Errorf("result %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	if rs := g.view.resolved(); len(rs) != 1 || rs[0].ID != req.ID || rs[0].Outcome != viewer.OutcomeApproved {
		t.Errorf("resolved %+v", rs)
	}
	var kinds []string
	for _, r := range g.log.all() {
		kinds = append(kinds, r.Kind+":"+r.Event)
	}
	if strings.Join(kinds, ",") != "user:confirm" {
		t.Errorf("records %v (an id nobody asked about is not the person's answer)", kinds)
	}
	if d := string(g.log.all()[0].Data); !strings.Contains(d, req.ID) || !strings.Contains(d, `"approve":true`) || !strings.Contains(d, "go ahead") || strings.Contains(d, "u001b") {
		t.Errorf("record %s", d)
	}
}

func TestEachOutcomeIsToldToThePageInItsOwnWord(t *testing.T) {
	g := newCrig(t)
	for o, want := range map[confirm.Outcome]string{
		confirm.Approved: viewer.OutcomeApproved, confirm.Rejected: viewer.OutcomeRejected, confirm.Timeout: viewer.OutcomeExpired,
		confirm.NoViewer: viewer.OutcomeCanceled, confirm.Canceled: viewer.OutcomeCanceled, confirm.Failed: viewer.OutcomeCanceled,
	} {
		g.c.Resolved("id-"+string(o), o)
		rs := g.view.resolved()
		if last := rs[len(rs)-1]; last.ID != "id-"+string(o) || last.Outcome != want {
			t.Errorf("%s: %+v, want %s", o, last, want)
		}
	}
}

func TestArgumentsThatAreNotJSONYetAreShownAsJSON(t *testing.T) {
	g := newCrig(t)
	if err := g.c.Present(context.Background(), confirm.Request{ID: "r1", Args: map[string]any{"x": 1}}); err != nil {
		t.Fatal(err)
	}
	if rs := g.view.confirmRequests(); len(rs) != 1 || string(rs[0].Args) != `{"x":1}` {
		t.Errorf("requests %+v", rs)
	}
	if err := g.c.Present(context.Background(), confirm.Request{ID: "r2", Args: make(chan int)}); err == nil {
		t.Error("arguments that cannot be shown were accepted")
	}
}

func TestAPageThatGoesAwayTakesTheOpenQuestionsWithIt(t *testing.T) {
	g := newCrig(t)
	g.running()
	got := make(chan confirm.Result, 1)
	// Nobody is attached: refused at once.
	if res := g.broker.Ask(context.Background(), confirm.Request{Tool: "exec"}); res.Outcome != confirm.NoViewer {
		t.Errorf("with no page: %+v", res)
	}
	g.c.Connected()
	go func() {
		got <- g.broker.Ask(context.Background(), confirm.Request{Tool: "exec", Args: json.RawMessage(`{}`)})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(g.view.confirmRequests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never asked")
		}
		time.Sleep(time.Millisecond)
	}
	g.c.Disconnected()
	select {
	case res := <-got:
		if res.Outcome != confirm.NoViewer {
			t.Errorf("result %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the open question outlived the page")
	}
}

func TestPauseAndResumeMoveTheTaskOnlyWhereTheyMeanSomething(t *testing.T) {
	g := newCrig(t)
	g.ready()
	g.c.Control(viewer.OpPause)
	if g.m.State() != Ready {
		t.Errorf("a pause with no task: %s", g.m.State())
	}
	g.c.Control(viewer.OpResume)
	if g.m.State() != Ready {
		t.Errorf("a resume with no task: %s", g.m.State())
	}
	if err := g.m.To(Running, ""); err != nil {
		t.Fatal(err)
	}
	g.c.Control(viewer.OpResume)
	if g.m.State() != Running {
		t.Errorf("a resume that is not paused: %s", g.m.State())
	}
	g.c.Control(viewer.OpPause)
	if g.m.State() != Paused || g.m.Reason() != "paused by the person" {
		t.Errorf("pause: %s %q", g.m.State(), g.m.Reason())
	}
	g.c.Control(viewer.OpResume)
	if g.m.State() != Running || g.m.Reason() != "resumed by the person" {
		t.Errorf("resume: %s %q", g.m.State(), g.m.Reason())
	}
	// A question the model asked is answered, not paused over.
	g.m.ToIf([]State{Running}, AwaitingUser, "")
	g.c.Control(viewer.OpPause)
	if g.m.State() != AwaitingUser {
		t.Errorf("a pause over a question: %s", g.m.State())
	}
	g.c.Control(viewer.OpResume)
	if g.m.State() != AwaitingUser {
		t.Errorf("a resume over a question: %s", g.m.State())
	}
}

func TestStopEndsTheTaskAndTheQuestionsOpenForIt(t *testing.T) {
	g := newCrig(t)
	g.running()
	g.c.Connected()
	ctx, end := g.c.Begin(context.Background())
	defer end()
	got := make(chan confirm.Result, 1)
	go func() {
		got <- g.broker.Ask(context.Background(), confirm.Request{Tool: "exec", Args: json.RawMessage(`{}`)})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(g.view.confirmRequests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never asked")
		}
		time.Sleep(time.Millisecond)
	}
	g.c.Control(viewer.OpStop)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the task's context was not canceled")
	}
	select {
	case res := <-got:
		if res.Outcome != confirm.Canceled {
			t.Errorf("result %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the open question stayed open")
	}
	// Once the task has ended Stop has nothing to cancel, and the question it cancels still is canceled.
	end()
	g.c.Control(viewer.OpStop)
	if len(g.log.all()) != 2 || g.log.all()[0].Event != "control" || string(g.log.all()[0].Data) != `{"op":"stop"}` {
		t.Errorf("records %+v", g.log.all())
	}
}

func TestStepModeIsSwitchedWhereTheDispatcherLooksAndTheViewerSaysSo(t *testing.T) {
	g := newCrig(t)
	g.running()
	g.c.Control(viewer.OpStepOn)
	g.c.Control(viewer.OpStepOff)
	g.step.mu.Lock()
	calls := append([]bool(nil), g.step.calls...)
	g.step.mu.Unlock()
	if len(calls) != 2 || !calls[0] || calls[1] {
		t.Errorf("calls %v", calls)
	}
	ns := g.view.notices()
	if len(ns) != 2 || ns[0].Text != "Step mode is on: every action needs your approval." || ns[1].Text != "Step mode is off: actions the rules allow run without asking." || ns[0].Level != viewer.LevelInfo {
		t.Errorf("notices %+v", ns)
	}
	// Anything else the page sends is ignored and not logged as a control.
	n := len(g.log.all())
	g.c.Control("dance")
	g.c.Control(viewer.OpKill)
	if len(g.log.all()) != n {
		t.Error("an unknown control was logged")
	}
	// Without a dispatcher there is no step mode to switch.
	g.c.Step = nil
	g.c.Control(viewer.OpStepOn)
	if len(g.log.all()) != n {
		t.Error("a step control with nothing to switch was logged")
	}
}

func TestEveryControlIsInTheLog(t *testing.T) {
	g := newCrig(t)
	g.running()
	for _, op := range []string{viewer.OpPause, viewer.OpResume, viewer.OpStepOn, viewer.OpStepOff, viewer.OpStop} {
		g.c.Control(op)
	}
	var ops []string
	for _, r := range g.log.all() {
		var d struct{ Op string }
		if err := json.Unmarshal(r.Data, &d); err != nil || r.Kind != "user" || r.Event != "control" || r.Actor != "user" {
			t.Fatalf("record %+v", r)
		}
		ops = append(ops, d.Op)
	}
	if strings.Join(ops, ",") != "pause,resume,step_on,step_off,stop" {
		t.Errorf("ops %v", ops)
	}
}

func TestThePageHearsOfEveryChangeOfState(t *testing.T) {
	g := newCrig(t)
	g.c.Attach()
	g.ready()
	g.c.PauseFor("nobody is watching")
	if g.m.State() != Ready {
		t.Error("a session that is not running cannot be paused")
	}
	if err := g.m.To(Running, "task started"); err != nil {
		t.Fatal(err)
	}
	g.c.PauseFor("the viewer went away")
	st := g.view.states()
	var got []string
	for _, s := range st {
		got = append(got, s.State+":"+s.Reason)
	}
	if strings.Join(got, ",") != "handshake:,ready:,running:task started,paused:the viewer went away" {
		t.Errorf("states %v", got)
	}
}

func TestADeadLogEndsTheSessionWhateverThePersonDid(t *testing.T) {
	g := newCrig(t)
	g.running()
	g.log.failAt = 1
	g.c.Chat("hello")
	g.mu.Lock()
	n := len(g.fatal)
	g.mu.Unlock()
	if n != 1 || !strings.Contains(g.fatal[0].Error(), "the audit log can no longer be written") {
		t.Errorf("fatal %v", g.fatal)
	}
}

func TestAConsoleNeedsNeitherAViewerNorALogNorABroker(t *testing.T) {
	c := &Console{Machine: NewMachine(nil)}
	c.Chat("hello")
	c.Confirm("x", true, "")
	c.Control(viewer.OpStop)
	c.Control(viewer.OpStepOn)
	c.Connected()
	c.Disconnected()
	c.Resolved("x", confirm.Approved)
	c.Attach()
	if got := c.Messages(); len(got) != 1 {
		t.Errorf("messages %q", got)
	}
}

func TestTheContextOfATaskEndsWithTheTask(t *testing.T) {
	g := newCrig(t)
	g.running()
	ctx, end := g.c.Begin(context.Background())
	select {
	case <-ctx.Done():
		t.Fatal("the context ended before the task did")
	default:
	}
	end()
	select {
	case <-ctx.Done():
	default:
		t.Error("the context outlived the task")
	}
	// A second task has a context of its own, and Stop reaches it.
	ctx2, end2 := g.c.Begin(context.Background())
	defer end2()
	g.c.Control(viewer.OpStop)
	select {
	case <-ctx2.Done():
	case <-time.After(5 * time.Second):
		t.Error("Stop did not reach the second task")
	}
	// Ending the parent ends the task too.
	parent, cancel := context.WithCancel(context.Background())
	ctx3, end3 := g.c.Begin(parent)
	defer end3()
	cancel()
	select {
	case <-ctx3.Done():
	case <-time.After(5 * time.Second):
		t.Error("the parent's end did not reach the task")
	}
}
