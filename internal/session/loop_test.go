// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/limits"
	"github.com/PacifAIst/controlsnack-local/internal/safety/secret"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

// reply is one answer of the scripted model: what it says, or how it fails.
type reply struct {
	text  string
	calls []parse.ToolCall
	err   error
	trunc bool
	// hook runs when the model is asked, before it answers.
	hook func(ctx context.Context)
}

// scriptedModel answers from a list, and keeps what it was asked.
type scriptedModel struct {
	mu      sync.Mutex
	replies []reply
	reqs    []backend.Request
}

func (s *scriptedModel) Name() string { return "openai" }

func (s *scriptedModel) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	i := len(s.reqs) - 1
	s.mu.Unlock()
	if i >= len(s.replies) {
		return backend.Response{}, &backend.Error{Kind: backend.KindBadRequest, Backend: "script", Message: fmt.Sprintf("the script has no reply %d", i+1)}
	}
	r := s.replies[i]
	if r.hook != nil {
		r.hook(ctx)
	}
	if r.err != nil {
		return backend.Response{}, r.err
	}
	return backend.Response{Reply: parse.Reply{Text: r.text, ToolCalls: r.calls}, Model: "scripted", Usage: backend.Usage{PromptTokens: 100, CompletionTokens: 10}, Truncated: r.trunc, Latency: time.Millisecond}, nil
}

func (s *scriptedModel) requests() []backend.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]backend.Request(nil), s.reqs...)
}

// fakeLook shows the same screen each turn, with a hash that can change.
type fakeLook struct {
	mu     sync.Mutex
	n      int
	errs   map[int]error // the look (from 1) that fails
	vision bool
	onLook func()       // runs at each look, before it answers
	reused map[int]bool // the look (from 1) whose picture is the last one again
}

func (f *fakeLook) Look(ctx context.Context) (vision.Seen, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	err, hook := f.errs[n], f.onLook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return vision.Seen{}, err
	}
	m, _ := vision.Identity(prompt.Size{W: 1280, H: 720})
	seen := vision.Seen{Mapper: m, Cursor: prompt.Point{X: 5, Y: 6}}
	if f.vision {
		seen.Shot = &vision.Shot{Image: backend.Image{MIME: backend.MIMEPNG, Data: []byte(fmt.Sprintf("png-%d", n))}, Note: prompt.ImageNote{W: 1280, H: 720}, Mapper: m, Hash: "h0", Reused: f.reused[n]}
	}
	return seen, nil
}

func (f *fakeLook) looks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// fakeActor stands in for the dispatcher.
type fakeActor struct {
	mu     sync.Mutex
	inputs []Input
	fn     func(Input) Step
}

func (a *fakeActor) Run(_ context.Context, in Input) Step {
	a.mu.Lock()
	a.inputs = append(a.inputs, in)
	fn := a.fn
	a.mu.Unlock()
	if fn != nil {
		return fn(in)
	}
	return okStep(in)
}

func (a *fakeActor) ran() []Input {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Input(nil), a.inputs...)
}

func okStep(in Input) Step {
	return Step{Result: prompt.Result{CallID: in.CallID, Tool: in.Action.Tool, Action: prompt.Describe(in.Action), OK: true, Detail: "ok"}}
}

func failStep(in Input, code, detail string, stop Stop) Step {
	return Step{Result: prompt.Result{CallID: in.CallID, Tool: in.Action.Tool, Action: prompt.Describe(in.Action), Code: code, Detail: detail}, Stop: stop}
}

// fakePerson is the one watching.
type fakePerson struct {
	mu       sync.Mutex
	messages []string
	asked    []string
	answer   string
	err      error
	during   func()
}

func (p *fakePerson) Messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.messages
	p.messages = nil
	return m
}

func (p *fakePerson) Ask(ctx context.Context, q string) (string, error) {
	p.mu.Lock()
	p.asked = append(p.asked, q)
	during, err, ans := p.during, p.err, p.answer
	p.mu.Unlock()
	if during != nil {
		during()
	}
	if err != nil {
		return "", err
	}
	return ans, nil
}

// fakeGuard says when to pause.
type fakeGuard struct {
	mu       sync.Mutex
	actions  []string
	screens  []string
	answers  []bool
	resets   int
	onAction func(n int) string
	onAnswer func(n int, usable bool) string
}

func (g *fakeGuard) Action(a actions.Action, _ Step, screen string) string {
	g.mu.Lock()
	g.screens = append(g.screens, screen)
	g.mu.Unlock()
	g.mu.Lock()
	g.actions = append(g.actions, a.Tool)
	n, f := len(g.actions), g.onAction
	g.mu.Unlock()
	if f != nil {
		return f(n)
	}
	return ""
}

func (g *fakeGuard) Answer(usable bool) string {
	g.mu.Lock()
	g.answers = append(g.answers, usable)
	n, f := len(g.answers), g.onAnswer
	g.mu.Unlock()
	if f != nil {
		return f(n, usable)
	}
	return ""
}

func (g *fakeGuard) Reset() { g.mu.Lock(); g.resets++; g.mu.Unlock() }

// lrig is a turn loop with everything around it faked except the state machine, the budgets and the conversation.
type lrig struct {
	t      *testing.T
	m      *Machine
	clock  *limits.FakeClock
	lim    *limits.Limits
	conv   *prompt.Conversation
	model  *scriptedModel
	look   *fakeLook
	act    *fakeActor
	person *fakePerson
	log    *log
	view   *events
	loop   *Loop
	killed bool
	seen   []vision.RawSeen
}

func newLrig(t *testing.T, mod ...func(*config.Config)) *lrig {
	t.Helper()
	cfg := config.Defaults()
	for _, f := range mod {
		f(&cfg)
	}
	prof := safety.ProfileFor(cfg)
	set, err := actions.NewSet("full")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := prompt.NewConversation("You are a test.", prompt.Options{Backend: "openai", Mode: backend.ModeNative, Set: set, Renderer: prompt.Renderer{Fencer: untrusted.WithNonce("3f9a1c")}})
	if err != nil {
		t.Fatal(err)
	}
	g := &lrig{t: t, m: NewMachine(nil), clock: limits.NewFakeClock(), model: &scriptedModel{}, look: &fakeLook{vision: true}, act: &fakeActor{}, person: &fakePerson{}, log: &log{}, view: &events{}, conv: conv}
	g.lim = limits.New(prof, g.clock)
	for _, s := range []State{Handshake, Ready} {
		if err := g.m.To(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	g.loop = &Loop{Machine: g.m, Limits: g.lim, Look: g.look, Conv: conv, Model: g.model, Set: set, Act: g.act, View: func(r vision.RawSeen) { g.seen = append(g.seen, r) },
		Viewer: g.view, Audit: g.log, Person: g.person, Killed: func() bool { return g.killed }, Network: "disabled", MaxActions: prof.ActionsPerResponse}
	return g
}

func tc(id, name, args string) parse.ToolCall {
	return parse.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func calls(cs ...parse.ToolCall) reply { return reply{calls: cs} }

var (
	doneOK = tc("cd", "done", `{"success":true,"summary":"all set"}`)
	keyA   = func(id string) parse.ToolCall { return tc(id, "key", `{"keys":"a"}`) }
	click  = func(id string) parse.ToolCall { return tc(id, "mouse", `{"op":"click","x":100,"y":100}`) }
)

func (g *lrig) run(task string) Outcome {
	g.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	select {
	case o := <-g.runAsync(ctx, task):
		return o
	case <-time.After(5 * time.Second):
		cancel()
		g.t.Fatalf("the task did not end (the session is %s)", g.m.State())
		return Outcome{}
	}
}

// runAsync runs a task in the background; the result comes on the channel.
func (g *lrig) runAsync(ctx context.Context, task string) <-chan Outcome {
	ch := make(chan Outcome, 1)
	go func() { ch <- g.loop.Run(ctx, task) }()
	return ch
}

func (g *lrig) waitState(want State) {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := g.m.Wait(ctx, want); err != nil {
		g.t.Fatalf("the session never became %s (it is %s): %v", want, g.m.State(), err)
	}
}

func wait(t *testing.T, ch <-chan Outcome) Outcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(10 * time.Second):
		t.Fatal("the task did not end")
		return Outcome{}
	}
}

// toolTexts are the results the model was sent for its last answer, in the request that follows it.
func toolTexts(req backend.Request) []string {
	msgs := req.Messages
	end := len(msgs) - 1 // the last message is the new observation
	var out []string
	for i := end - 1; i >= 0 && msgs[i].Role == backend.RoleTool; i-- {
		out = append([]string{msgs[i].Text}, out...)
	}
	return out
}

func lastText(req backend.Request) string { return req.Messages[len(req.Messages)-1].Text }

func TestATaskThatIsDoneInOneTurn(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{text: "I will finish.", calls: []parse.ToolCall{doneOK}}}
	out := g.run("find the kernel")
	if out.End != EndDone || !out.Success || out.Summary != "all set" || out.Turns != 1 || out.Err != nil {
		t.Fatalf("outcome %+v", out)
	}
	if g.m.State() != Ready {
		t.Errorf("the session is %s after the task", g.m.State())
	}
	// The model was told the task, the network, the budget and the screen.
	reqs := g.model.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	txt := lastText(reqs[0])
	for _, want := range []string{"find the kernel", "disabled"} {
		if !strings.Contains(txt, want) {
			t.Errorf("the observation lacks %q:\n%s", want, txt)
		}
	}
	if last := reqs[0].Messages[len(reqs[0].Messages)-1]; len(last.Images) != 1 {
		t.Errorf("the observation has %d pictures", len(last.Images))
	}
	if reqs[0].MaxActions != g.loop.MaxActions {
		t.Errorf("the limit on actions is %d", reqs[0].MaxActions)
	}
	// The log has the model's call and the end; the viewer the words.
	all := g.log.all()
	if len(all) != 2 || all[0].Kind != "llm" || all[0].Turn != 1 || all[1].Kind != "action" || all[1].Tool != "done" || all[1].Verdict != "allowed" || all[1].Event != "result" {
		t.Fatalf("records %+v", all)
	}
	if !strings.Contains(string(all[0].LLM), `"prompt_tokens":100`) || !strings.Contains(string(all[0].LLM), `"response_sha256"`) || strings.Contains(string(all[0].LLM), "I will finish") {
		t.Errorf("llm record %s", all[0].LLM)
	}
	if len(all[1].ScreenshotSHA256) != 64 {
		t.Errorf("the screenshot hash is %q", all[1].ScreenshotSHA256)
	}
	var says []string
	g.view.mu.Lock()
	for _, m := range g.view.msgs {
		if c, ok := m.(interface{ MessageType() string }); ok && c.MessageType() == "chat" {
			says = append(says, fmt.Sprintf("%+v", m))
		}
	}
	g.view.mu.Unlock()
	if len(says) != 2 || !strings.Contains(says[0], "I will finish") || !strings.Contains(says[1], "all set") {
		t.Errorf("chat lines %v", says)
	}
	if tr := g.view.traces(); len(tr) != 1 || tr[0].Tool != "done" || tr[0].Status != "done" || tr[0].Verdict != "allow" {
		t.Errorf("traces %+v", tr)
	}
}

func TestEachActionIsHandedOverWithItsPlaceItsIdAndTheScreenItWasPlannedOn(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(keyA("c1"), tc("c2", "type_text", `{"text":"hi"}`)), calls(doneOK)}
	out := g.run("type")
	if out.End != EndDone || out.Turns != 2 {
		t.Fatalf("outcome %+v", out)
	}
	ran := g.act.ran()
	if len(ran) != 2 {
		t.Fatalf("%d actions ran", len(ran))
	}
	for i, want := range []struct{ id, call, tool string }{{"t1a1", "c1", "key"}, {"t1a2", "c2", "type_text"}} {
		in := ran[i]
		if in.ID != want.id || in.CallID != want.call || in.Action.Tool != want.tool || in.Turn != 1 || len(in.ScreenSHA) != 64 || in.Mapper.GuestSize().W != 1280 {
			t.Errorf("action %d: %+v", i+1, in)
		}
	}
	// Every call is answered, in order, in the next request.
	reqs := g.model.requests()
	tools := toolTexts(reqs[1])
	if len(tools) != 2 || !strings.Contains(tools[0], "key ok") || !strings.Contains(tools[1], "type_text ok") {
		t.Errorf("results %q", tools)
	}
	var ids []string
	for _, m := range reqs[1].Messages {
		if m.Role == backend.RoleTool {
			ids = append(ids, m.CallID)
		}
	}
	if strings.Join(ids, ",") != "c1,c2" {
		t.Errorf("the results answer calls %v", ids)
	}
	if got := reqs[1].Messages[len(reqs[1].Messages)-1].Text; !strings.Contains(got, "2") {
		t.Errorf("the second observation does not say it is turn 2:\n%s", got)
	}
	if a, turns, _ := g.lim.Remaining(); a != safety.ProfileFor(config.Defaults()).ActionsPerTask || turns != safety.ProfileFor(config.Defaults()).MaxTurns-2 {
		t.Errorf("budget left: %d actions (the fake actor charges none), %d turns", a, turns)
	}
	if len(g.seen) != 2 {
		t.Errorf("the executor was told what the model saw %d times", len(g.seen))
	}
}

func TestWhatFollowsAFailedActionIsNotRun(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step {
		if in.Action.Tool == "key" {
			return failStep(in, actions.CodeNotFound, "no such thing", StopBatch)
		}
		return okStep(in)
	}
	g.model.replies = []reply{calls(tc("c1", "type_text", `{"text":"x"}`), keyA("c2"), keyA("c3")), calls(doneOK)}
	g.run("t")
	if ran := g.act.ran(); len(ran) != 2 {
		t.Fatalf("%d actions ran, want 2", len(ran))
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 3 || !strings.Contains(tools[0], "ok") || !strings.Contains(tools[1], "error not_found") ||
		tools[2] != "3. key not run not run: an earlier action of the answer did not work. Answer again if you still want it." {
		t.Errorf("results %q", tools)
	}
}

func TestDoneEndsTheAnswerAndEverythingAfterItIsNotRun(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(keyA("c1"), doneOK, keyA("c3"))}
	out := g.run("t")
	if out.End != EndDone || len(g.act.ran()) != 1 {
		t.Fatalf("outcome %+v, %d ran", out, len(g.act.ran()))
	}
	// The conversation stays well formed for the next task: all three calls were answered.
	if err := g.conv.Results(nil, prompt.Limits{}); err == nil {
		t.Log("(a further Results call is refused only when counts differ)")
	}
	g.model.replies = append(g.model.replies, calls(doneOK))
	g.look.errs = nil
	if out := g.run("again"); out.End != EndDone {
		t.Fatalf("the second task: %+v", out)
	}
	tools := 0
	for _, m := range g.model.requests()[1].Messages {
		if m.Role == backend.RoleTool {
			tools++
		}
	}
	if tools != 3 {
		t.Errorf("the second task's request holds %d tool results, want the 3 of the first answer", tools)
	}
}

func TestAScreenshotEndsTheAnswerSoThatTheNextObservationShowsTheScreen(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(tc("c1", "screenshot", `{}`), keyA("c2")), calls(doneOK)}
	g.run("t")
	if ran := g.act.ran(); len(ran) != 1 || ran[0].Action.Tool != "screenshot" {
		t.Fatalf("ran %+v", ran)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[1], "not run") || !strings.Contains(tools[1], "screenshot") {
		t.Errorf("results %q", tools)
	}
}

func TestWhenTheScreenChangedAnActionThatSaysWhereToClickIsNotRun(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step {
		s := okStep(in)
		s.ScreenHash = "h1" // the screen was "h0" when the model looked
		return s
	}
	g.model.replies = []reply{calls(keyA("c1"), tc("c2", "click_element", `{"id":"a1"}`), click("c3")), calls(doneOK)}
	g.run("t")
	ran := g.act.ran()
	if len(ran) != 2 || ran[0].Action.Tool != "key" || ran[1].Action.Tool != "click_element" {
		t.Fatalf("ran %+v", ran)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 3 || !strings.Contains(tools[2], "not run") || !strings.Contains(tools[2], "screen changed") {
		t.Errorf("results %q", tools)
	}

	// A screen that did not change lets the click go.
	g = newLrig(t)
	g.act.fn = func(in Input) Step { s := okStep(in); s.ScreenHash = "h0"; return s }
	g.model.replies = []reply{calls(keyA("c1"), click("c2")), calls(doneOK)}
	g.run("t")
	if len(g.act.ran()) != 2 {
		t.Errorf("%d actions ran with the screen unchanged", len(g.act.ran()))
	}

	// With no picture to compare to the first change cannot be told, but the second can.
	g = newLrig(t)
	g.look.vision = false
	n := 0
	g.act.fn = func(in Input) Step {
		n++
		s := okStep(in)
		s.ScreenHash = fmt.Sprintf("x%d", n)
		return s
	}
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"), click("c3")), calls(doneOK)}
	g.run("t")
	if len(g.act.ran()) != 2 {
		t.Errorf("%d actions ran", len(g.act.ran()))
	}
}

func TestTheModelCanAskAQuestionAndTheAnswerGoesBack(t *testing.T) {
	g := newLrig(t)
	g.person.answer = "the blue one"
	var during State
	g.person.during = func() { during = g.m.State() }
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Which color?"}`), keyA("c2")), calls(doneOK)}
	out := g.run("t")
	if out.End != EndDone || len(g.act.ran()) != 0 {
		t.Fatalf("outcome %+v, ran %d", out, len(g.act.ran()))
	}
	if during != AwaitingUser {
		t.Errorf("the session was %s while the person was asked", during)
	}
	if len(g.person.asked) != 1 || g.person.asked[0] != "Which color?" {
		t.Errorf("asked %q", g.person.asked)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[0], "The user answered: the blue one") || !strings.Contains(tools[1], "not run") {
		t.Errorf("results %q", tools)
	}
	all := g.log.all()
	found := false
	for _, r := range all {
		found = found || (r.Tool == "ask_user" && r.Event == "result" && r.Verdict == "allowed")
	}
	if !found {
		t.Errorf("no record of the question: %+v", all)
	}
	// A long answer is not cut: what does not fit in a result is in the next observation.
	g = newLrig(t)
	g.person.answer = strings.Repeat("long ", 100)
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`)), calls(doneOK)}
	g.run("t")
	if !strings.Contains(lastText(g.model.requests()[1]), strings.TrimSpace(g.person.answer)) {
		t.Error("the whole answer is not in the next observation")
	}
}

func TestAQuestionNobodyAnswersEndsTheTask(t *testing.T) {
	g := newLrig(t)
	g.person.err = context.Canceled
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`))}
	ctx, cancel := context.WithCancel(context.Background())
	g.person.during = cancel
	out := g.loop.Run(ctx, "t")
	if out.End != EndStopped || g.m.State() != Ready {
		t.Errorf("outcome %+v, state %s", out, g.m.State())
	}
	// Without a person there is nobody to ask, and the model is told.
	g = newLrig(t)
	g.loop.Person = nil
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`)), calls(doneOK)}
	g.run("t")
	if tools := toolTexts(g.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "nobody to ask") {
		t.Errorf("results %q", tools)
	}
}

func TestWhatThePersonWroteIsInTheNextObservation(t *testing.T) {
	g := newLrig(t)
	g.person.messages = []string{"please hurry"}
	g.model.replies = []reply{calls(doneOK)}
	g.run("t")
	if txt := lastText(g.model.requests()[0]); !strings.Contains(txt, "please hurry") {
		t.Errorf("the observation lacks the person's words:\n%s", txt)
	}
}

func TestAnAnswerWithNothingToRunIsMetWithARepairRequestAndThenAPause(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{text: "I think I should look."}, {text: "Hmm."}, {text: "Still thinking."}, calls(doneOK)}
	ctx := context.Background()
	ch := g.runAsync(ctx, "t")
	g.waitState(Paused)
	reqs := g.model.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests before the pause, want 3 (one answer and two repairs)", len(reqs))
	}
	for i := 1; i < 3; i++ {
		if txt := lastText(reqs[i]); !strings.Contains(txt, "could not be used") {
			t.Errorf("request %d lacks the repair message:\n%s", i+1, txt)
		}
	}
	g.view.mu.Lock()
	notices := fmt.Sprintf("%+v", g.view.msgs)
	g.view.mu.Unlock()
	if !strings.Contains(notices, "could not be used 3 times in a row") {
		t.Errorf("the person was not told: %s", notices)
	}
	if err := g.m.To(Running, "resume"); err != nil {
		t.Fatal(err)
	}
	out := wait(t, ch)
	if out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	// The resumed turn is a fresh one, with a note about what went wrong.
	if len(g.model.requests()) != 4 || g.look.looks() != 2 {
		t.Errorf("%d requests, %d looks", len(g.model.requests()), g.look.looks())
	}
}

func TestAnInvalidCallIsAnsweredAndTheModelIsAskedAgain(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(tc("c1", "mouse", `{"op":"click"}`), keyA("c2")), calls(doneOK)}
	out := g.run("t")
	if out.End != EndDone || len(g.act.ran()) != 0 {
		t.Fatalf("outcome %+v, ran %d", out, len(g.act.ran()))
	}
	reqs := g.model.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	// Both calls are answered before the model is asked to answer again.
	var ids []string
	for _, m := range reqs[1].Messages {
		if m.Role == backend.RoleTool {
			ids = append(ids, m.CallID+":"+m.Text)
		}
	}
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "c1:") || !strings.Contains(ids[0], "x") || !strings.HasPrefix(ids[1], "c2:") || !strings.Contains(ids[1], "not run") {
		t.Errorf("answers %q", ids)
	}
	if !strings.Contains(lastText(reqs[1]), "Your last answer could not be used") {
		t.Errorf("no repair message:\n%s", lastText(reqs[1]))
	}
}

func TestAnAnswerCutOffByTheLengthLimitIsSaidSo(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{text: "I will", trunc: true}, calls(doneOK)}
	g.run("t")
	if txt := lastText(g.model.requests()[1]); !strings.Contains(txt, "cut off") {
		t.Errorf("no word about the cut:\n%s", txt)
	}
}

func TestOnlyTheValidActionsBeforeTheFirstMistakeAreRun(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(keyA("c1"), tc("c2", "mouse", `{"op":"click"}`), keyA("c3")), calls(doneOK)}
	g.run("t")
	if ran := g.act.ran(); len(ran) != 1 || ran[0].CallID != "c1" {
		t.Fatalf("ran %+v", ran)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 3 || !strings.Contains(tools[0], "ok") || !strings.Contains(tools[1], "error invalid_args") || !strings.Contains(tools[2], "not run") {
		t.Errorf("results %q", tools)
	}
}

func TestActionsBeyondTheResponseLimitAreDropped(t *testing.T) {
	g := newLrig(t, func(c *config.Config) { c.Safety.ActionsPerResponse = 2 })
	g.loop.MaxActions = 4 // the loop would let four through; the profile's limit says two
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"), keyA("c3"), keyA("c4")), calls(doneOK)}
	g.run("t")
	if len(g.act.ran()) != 2 {
		t.Fatalf("%d actions ran, want 2", len(g.act.ran()))
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 4 || tools[2] != "3. key dropped dropped: more actions than one answer may run" || tools[3] != "4. key dropped dropped: more actions than one answer may run" {
		t.Errorf("results %q", tools)
	}
}

func TestAnAnswerInJSONHasItsResultsInTheNextObservation(t *testing.T) {
	g := newLrig(t)
	conv, err := prompt.NewConversation("You are a test.", prompt.Options{Backend: "ollama", Mode: backend.ModeJSON, Set: g.loop.Set, Renderer: prompt.Renderer{Fencer: untrusted.WithNonce("3f9a1c")}})
	if err != nil {
		t.Fatal(err)
	}
	g.loop.Conv = conv
	g.model.replies = []reply{
		{text: `{"thought":"go","actions":[{"tool":"key","args":{"keys":"a"}},{"tool":"mouse","args":{"op":"click"}}]}`},
		{text: `{"thought":"done","actions":[{"tool":"done","args":{"success":true,"summary":"ok"}}]}`},
	}
	out := g.run("t")
	if out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	txt := lastText(g.model.requests()[1])
	if !strings.Contains(txt, "key") || !strings.Contains(txt, "ok") || !strings.Contains(txt, "Not run, because it was not valid") {
		t.Errorf("the next observation lacks the results and the note:\n%s", txt)
	}
}

func TestStopEndsTheTaskAndLeavesTheSessionReady(t *testing.T) {
	g := newLrig(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.model.replies = []reply{{hook: func(context.Context) { cancel() }, err: context.Canceled}}
	out := g.loop.Run(ctx, "t")
	if out.End != EndStopped || g.m.State() != Ready || out.Err != nil {
		t.Errorf("outcome %+v, state %s", out, g.m.State())
	}
	// The person can start another task.
	g.model.replies = append(g.model.replies, calls(doneOK))
	if out := g.run("again"); out.End != EndDone {
		t.Errorf("the next task: %+v", out)
	}
}

func TestAKillEndsTheTaskAndLeavesTheStateToTheKiller(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"))}
	g.act.fn = func(in Input) Step {
		g.killed = true
		return failStep(in, actions.CodeInternal, "The session is being killed.", StopKilled)
	}
	out := g.run("t")
	if out.End != EndKilled || g.m.State() != Running {
		t.Errorf("outcome %+v, state %s", out, g.m.State())
	}
	if len(g.act.ran()) != 1 {
		t.Errorf("%d actions ran after the kill", len(g.act.ran()))
	}
	// A kill before the first turn does nothing at all.
	g = newLrig(t)
	g.killed = true
	if out := g.run("t"); out.End != EndKilled || g.look.looks() != 0 {
		t.Errorf("outcome %+v, %d looks", out, g.look.looks())
	}
}

func TestAPauseDuringTheAnswerHoldsTheActionsUntilTheResumeAndThenDropsThem(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{calls: []parse.ToolCall{keyA("c1"), keyA("c2")}, hook: func(context.Context) { g.m.ToIf([]State{Running}, Paused, "the person paused") }}, calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	time.Sleep(50 * time.Millisecond)
	if n := len(g.act.ran()); n != 0 {
		t.Fatalf("%d actions ran while paused", n)
	}
	_ = g.m.To(Running, "resumed")
	out := wait(t, ch)
	if out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	if n := len(g.act.ran()); n != 0 {
		t.Errorf("%d actions ran: the screen may have changed during the pause, so the answer is dropped", n)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[0], "paused") {
		t.Errorf("results %q", tools)
	}
}

func TestTheBudgetOfTurnsPausesForACheckInAndAResumeGivesAnother(t *testing.T) {
	g := newLrig(t, func(c *config.Config) { c.Safety.MaxTurns = 1 })
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if r := g.m.Reason(); !strings.HasPrefix(r, "check-in") {
		t.Errorf("the pause says %q", r)
	}
	if len(g.model.requests()) != 1 {
		t.Errorf("%d requests: the model was asked past the budget", len(g.model.requests()))
	}
	_ = g.m.To(Running, "resumed")
	out := wait(t, ch)
	if out.End != EndDone || out.Turns != 2 {
		t.Errorf("outcome %+v", out)
	}
}

func TestTheBudgetOfActionsPausesForACheckInAfterTheAnswer(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step {
		return failStep(in, actions.CodeBudgetExhausted, "budget exhausted", StopBudget)
	}
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if r := g.m.Reason(); !strings.HasPrefix(r, "check-in") {
		t.Errorf("the pause says %q", r)
	}
	if len(g.act.ran()) != 1 {
		t.Errorf("%d actions ran", len(g.act.ran()))
	}
	g.act.fn = nil
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone {
		t.Errorf("outcome %+v", out)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[1], "budget") {
		t.Errorf("results %q", tools)
	}
}

func TestTheSessionsTimeEndsTheTaskButNotTheSession(t *testing.T) {
	g := newLrig(t)
	g.clock.Advance(safety.ProfileFor(config.Defaults()).SessionTimeout + time.Second)
	out := g.run("t")
	if out.End != EndTimeout || !errors.Is(out.Err, limits.ErrSessionTimeout) || g.look.looks() != 0 {
		t.Errorf("outcome %+v", out)
	}
	if g.m.State() != Running {
		t.Errorf("the session is %s: ending it is the runner's", g.m.State())
	}
}

func TestAModelThatCannotBeReachedPausesAndAModelThatRefusesEndsTheTask(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{err: &backend.Error{Kind: backend.KindUnavailable, Backend: "openai", Message: "connection refused"}}, calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	g.view.mu.Lock()
	notices := fmt.Sprintf("%+v", g.view.msgs)
	g.view.mu.Unlock()
	if !strings.Contains(notices, "did not answer") || !strings.Contains(notices, "connection refused") {
		t.Errorf("notices %s", notices)
	}
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone {
		t.Errorf("outcome %+v", out)
	}

	for _, k := range []backend.Kind{backend.KindAuth, backend.KindBadRequest, backend.KindNotFound} {
		g := newLrig(t)
		g.model.replies = []reply{{err: &backend.Error{Kind: k, Backend: "openai", Message: "no"}}}
		out := g.run("t")
		if out.End != EndFailed || out.Err == nil || g.m.State() != Ready {
			t.Errorf("%v: outcome %+v, state %s", k, out, g.m.State())
		}
	}
}

func TestAScreenThatCannotBeReadPausesTheTask(t *testing.T) {
	g := newLrig(t)
	g.look.errs = map[int]error{1: errors.New("the agent did not answer")}
	g.model.replies = []reply{calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if len(g.model.requests()) != 0 {
		t.Error("the model was asked without a screen")
	}
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone || g.look.looks() != 2 {
		t.Errorf("outcome %+v, %d looks", out, g.look.looks())
	}
}

func TestADeadAuditLogEndsTheTask(t *testing.T) {
	g := newLrig(t)
	g.log.failAt = 1
	g.model.replies = []reply{calls(keyA("c1"))}
	if out := g.run("t"); out.End != EndAudit || out.Err == nil || len(g.act.ran()) != 0 {
		t.Errorf("the model's answer could not be recorded: %+v", out)
	}
	g = newLrig(t)
	g.log.failAt = 2
	g.model.replies = []reply{calls(doneOK)}
	if out := g.run("t"); out.End != EndAudit || out.Err == nil {
		t.Errorf("the end of the task could not be recorded: %+v", out)
	}
	g = newLrig(t)
	g.act.fn = func(in Input) Step { s := okStep(in); s.Stop, s.Err = StopAudit, errors.New("disk full"); return s }
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"))}
	if out := g.run("t"); out.End != EndAudit || len(g.act.ran()) != 1 {
		t.Errorf("the dispatcher's log failed: %+v", out)
	}
}

func TestAnActionTimeoutOrSessionOverRunsEndTheTask(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step { return failStep(in, actions.CodeBudgetExhausted, "time", StopTimeout) }
	g.model.replies = []reply{calls(keyA("c1"))}
	if out := g.run("t"); out.End != EndTimeout || !errors.Is(out.Err, limits.ErrSessionTimeout) {
		t.Errorf("outcome %+v", out)
	}
	g = newLrig(t)
	g.act.fn = func(in Input) Step { _ = g.m.To(Ending, "the session is ending"); return okStep(in) }
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"))}
	if out := g.run("t"); out.End != EndSession || len(g.act.ran()) != 1 {
		t.Errorf("outcome %+v", out)
	}
}

func TestAGuardCanPauseTheTaskAfterAnActionOrAnAnswer(t *testing.T) {
	g := newLrig(t)
	gd := &fakeGuard{onAction: func(n int) string {
		if n == 2 {
			return "The same action three times."
		}
		return ""
	}}
	g.loop.Guard = gd
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"), keyA("c3")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if len(g.act.ran()) != 2 {
		t.Errorf("%d actions ran, want 2", len(g.act.ran()))
	}
	if gd.resets != 1 {
		t.Errorf("the guard was reset %d times before the resume", gd.resets)
	}
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	if gd.resets != 2 {
		t.Errorf("the guard was reset %d times after the resume", gd.resets)
	}
	if got := strings.Join(gd.actions, ","); got != "key,key" {
		t.Errorf("the guard saw %s (done and ask_user are the loop's own)", got)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 3 || !strings.Contains(tools[2], "not run") {
		t.Errorf("results %q", tools)
	}

	// An answer the guard has had enough of.
	g = newLrig(t)
	g.loop.Guard = &fakeGuard{onAnswer: func(n int, usable bool) string {
		if !usable && n == 2 {
			return "Five unusable answers in a row."
		}
		return ""
	}}
	g.model.replies = []reply{{text: "x"}, {text: "y"}, calls(doneOK)}
	ch = g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if len(g.model.requests()) != 2 {
		t.Errorf("%d requests: the guard's pause should come at once", len(g.model.requests()))
	}
	_ = g.m.To(Running, "resumed")
	if out := wait(t, ch); out.End != EndDone {
		t.Errorf("outcome %+v", out)
	}
}

func TestATaskCannotStartUnlessTheSessionIsReady(t *testing.T) {
	g := newLrig(t)
	g.m = NewMachine(nil)
	g.loop.Machine = g.m
	out := g.run("t")
	var te *TransitionError
	if out.End != EndFailed || !errors.As(out.Err, &te) {
		t.Errorf("outcome %+v", out)
	}
}

func TestAConversationThatCannotFitEndsTheTaskWithAnExplanation(t *testing.T) {
	g := newLrig(t)
	conv, err := prompt.NewConversation(strings.Repeat("system ", 400), prompt.Options{Backend: "openai", Mode: backend.ModeNative, Set: g.loop.Set, Renderer: prompt.Renderer{Fencer: untrusted.WithNonce("3f9a1c")}, ContextTokens: 600})
	if err != nil {
		t.Fatal(err)
	}
	g.loop.Conv = conv
	out := g.run("t")
	var ce *prompt.ContextError
	if out.End != EndFailed || !errors.As(out.Err, &ce) || len(g.model.requests()) != 0 {
		t.Errorf("outcome %+v", out)
	}
	if g.m.State() != Ready {
		t.Errorf("the session is %s", g.m.State())
	}
}

func TestWhatTheModelWritesInADoneSummaryIsMaskedWherePeopleLook(t *testing.T) {
	g := newLrig(t)
	g.loop.Redactor = secret.NewRedactor()
	g.loop.Redactor.AddSecret("sk-live-1234567890")
	g.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"used sk-live-1234567890"}`))}
	g.run("t")
	for _, r := range g.log.all() {
		if strings.Contains(string(r.Args), "sk-live-1234567890") {
			t.Errorf("the secret is in the log: %s", r.Args)
		}
	}
	for _, tr := range g.view.traces() {
		if strings.Contains(string(tr.Args), "sk-live-1234567890") {
			t.Errorf("the secret is in the viewer: %s", tr.Args)
		}
	}
}
