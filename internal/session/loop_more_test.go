// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/safety"
	"github.com/PacifAIst/controlsnack/internal/safety/untrusted"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

func TestTheObservationCarriesTheTurnTheTaskTheNetworkAndWhatIsLeft(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	g.run("find the kernel")
	prof := safety.ProfileFor(config.Defaults())
	reqs := g.model.requests()
	for i, want := range [][]string{
		{"TURN 1 · screen 1280x720 · cursor 5,6 · network: disabled", "TASK: find the kernel", fmt.Sprintf("BUDGET actions_left=%d turns_left=%d", prof.ActionsPerTask, prof.MaxTurns-1)},
		{"TURN 2 ", "TASK: find the kernel", fmt.Sprintf("turns_left=%d", prof.MaxTurns-2)},
	} {
		txt := lastText(reqs[i])
		for _, w := range want {
			if !strings.Contains(txt, w) {
				t.Errorf("observation %d lacks %q:\n%s", i+1, w, txt)
			}
		}
	}
	g = newLrig(t)
	g.loop.Network = "enabled"
	g.model.replies = []reply{calls(doneOK)}
	g.run("t")
	if !strings.Contains(lastText(g.model.requests()[0]), "network: enabled") {
		t.Error("the network is not as the session said")
	}
}

func TestTheLogNamesThePictureTheModelSaw(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(doneOK)}
	g.run("t")
	sum := sha256.Sum256([]byte("png-1"))
	want := hex.EncodeToString(sum[:])
	for _, r := range g.log.all() {
		if r.Kind == "action" && r.ScreenshotSHA256 != want {
			t.Errorf("the record names %q, want %q", r.ScreenshotSHA256, want)
		}
	}
	// Without a picture (text-only) it names none.
	g = newLrig(t)
	g.look.vision = false
	g.model.replies = []reply{calls(doneOK)}
	g.run("t")
	for _, r := range g.log.all() {
		if r.ScreenshotSHA256 != "" {
			t.Errorf("a picture is named where there was none: %q", r.ScreenshotSHA256)
		}
	}
}

func TestEachTaskHasItsOwnBudget(t *testing.T) {
	g := newLrig(t, func(c *config.Config) { c.Safety.MaxTurns = 2 })
	for i := 0; i < 2; i++ {
		g.model.replies = append(g.model.replies, calls(keyA("c1")), calls(doneOK))
	}
	for i := 1; i <= 2; i++ {
		if out := g.run("task"); out.End != EndDone || out.Turns != 2 {
			t.Fatalf("task %d: %+v", i, out)
		}
	}
}

func TestATaskWhoseContextHasEndedDoesNothing(t *testing.T) {
	g := newLrig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := g.loop.Run(ctx, "t")
	if out.End != EndStopped || g.look.looks() != 0 || g.m.State() != Ready {
		t.Errorf("outcome %+v, %d looks, state %s", out, g.look.looks(), g.m.State())
	}
}

func TestTheEndOfTheSessionUnderTheTaskEndsIt(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step { _ = g.m.To(Killed, "killed"); return okStep(in) }
	g.model.replies = []reply{calls(keyA("c1"))}
	if out := g.run("t"); out.End != EndKilled || g.look.looks() != 1 {
		t.Errorf("a killed session: %+v", out)
	}
	g = newLrig(t)
	g.act.fn = func(in Input) Step { _ = g.m.To(Ending, "the viewer went away"); return okStep(in) }
	g.model.replies = []reply{calls(keyA("c1"))}
	if out := g.run("t"); out.End != EndSession || g.look.looks() != 1 {
		t.Errorf("an ending session: %+v", out)
	}
	// Stop, from outside, while the model is thinking: the session is Ready again, and the task is over.
	g = newLrig(t)
	g.model.replies = []reply{{calls: []parse.ToolCall{keyA("c1")}, hook: func(context.Context) { g.m.ToIf([]State{Running}, Ready, "stopped") }}}
	if out := g.run("t"); out.End != EndStopped || len(g.act.ran()) != 0 {
		t.Errorf("a stopped session: %+v, %d ran", out, len(g.act.ran()))
	}
}

func TestTheNumberOfTriesBeforeAPauseIsTheLoopsToSet(t *testing.T) {
	for _, tc := range []struct{ repairs, asked int }{{0, 3}, {1, 2}, {2, 3}, {4, 5}} {
		g := newLrig(t)
		g.loop.Repairs = tc.repairs
		for i := 0; i < 6; i++ {
			g.model.replies = append(g.model.replies, reply{text: "words"})
		}
		ch := g.runAsync(context.Background(), "t")
		g.waitState(Paused)
		if n := len(g.model.requests()); n != tc.asked {
			t.Errorf("repairs=%d: the model was asked %d times, want %d", tc.repairs, n, tc.asked)
		}
		_ = g.m.To(Ready, "stop")
		wait(t, ch)
	}
}

func TestTheRepairRequestAsksForTheKindOfAnswerTheModelGives(t *testing.T) {
	g := newLrig(t)
	conv, err := prompt.NewConversation("You are a test.", prompt.Options{Backend: "ollama", Mode: backend.ModeJSON, Set: g.loop.Set, Renderer: prompt.Renderer{Fencer: untrusted.WithNonce("3f9a1c")}})
	if err != nil {
		t.Fatal(err)
	}
	g.loop.Conv = conv
	g.model.replies = []reply{{text: "I will look."}, {text: `{"actions":[{"tool":"done","args":{"success":true,"summary":"ok"}}]}`}}
	g.run("t")
	if txt := lastText(g.model.requests()[1]); !strings.Contains(txt, "one JSON object") {
		t.Errorf("the repair request for a JSON model:\n%s", txt)
	}
	g = newLrig(t)
	g.model.replies = []reply{{text: "I will look."}, calls(doneOK)}
	g.run("t")
	if txt := lastText(g.model.requests()[1]); !strings.Contains(txt, "tool calls") || strings.Contains(txt, "one JSON object") {
		t.Errorf("the repair request for a native model:\n%s", txt)
	}
}

func TestWhatWentWrongIsToldToTheTurnThatFollowsAPause(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{text: "a"}, {text: "b"}, {text: "c"}, calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	_ = g.m.To(Running, "resumed")
	wait(t, ch)
	if txt := lastText(g.model.requests()[3]); !strings.Contains(txt, "NOTE: Your last answer could not be used") {
		t.Errorf("the resumed turn:\n%s", txt)
	}
}

func TestACallBeyondTheLimitOfAnUnusableAnswerIsAnsweredToo(t *testing.T) {
	g := newLrig(t)
	g.loop.MaxActions = 2
	g.model.replies = []reply{calls(tc("c1", "mouse", `{"op":"click"}`), keyA("c2"), keyA("c3")), calls(doneOK)}
	g.run("t")
	var tools []string
	for _, m := range g.model.requests()[1].Messages {
		if m.Role == backend.RoleTool {
			tools = append(tools, m.Text)
		}
	}
	if len(tools) != 3 || !strings.Contains(tools[0], "invalid_args") || !strings.Contains(tools[1], "key not run") || !strings.Contains(tools[2], "key dropped") {
		t.Errorf("results %q", tools)
	}
}

func TestAKillBetweenActionsStopsTheRest(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step { g.killed = true; return okStep(in) }
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"))}
	if out := g.run("t"); out.End != EndKilled || len(g.act.ran()) != 1 {
		t.Errorf("outcome %+v, %d ran", out, len(g.act.ran()))
	}
}

func TestAStopTheDispatcherReportsEndsTheTask(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step {
		return failStep(in, actions.CodeInternal, "The action was stopped.", StopCanceled)
	}
	g.model.replies = []reply{calls(keyA("c1"), keyA("c2"))}
	out := g.run("t")
	if out.End != EndStopped || len(g.act.ran()) != 1 || g.m.State() != Ready {
		t.Errorf("outcome %+v, %d ran, state %s", out, len(g.act.ran()), g.m.State())
	}
	// The conversation stays well formed: the next task's request answers both calls.
	g.model.replies = append(g.model.replies, calls(doneOK))
	g.act.fn = nil
	g.run("again")
	n := 0
	for _, m := range g.model.requests()[1].Messages {
		if m.Role == backend.RoleTool {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d results in the request after the stop, want 2", n)
	}
}

func TestADoneThatSaysItFailedIsNotASuccess(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{calls(tc("c1", "done", `{"success":false,"summary":"could not find it"}`))}
	out := g.run("t")
	if out.End != EndDone || out.Success || out.Summary != "could not find it" {
		t.Errorf("outcome %+v", out)
	}
}

func TestAnAnswerThatCannotBeRecordedEndsTheTask(t *testing.T) {
	g := newLrig(t)
	g.person.answer = "yes"
	g.log.failAt = 2
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`), keyA("c2"))}
	out := g.run("t")
	if out.End != EndAudit || out.Err == nil || len(g.act.ran()) != 0 {
		t.Errorf("outcome %+v", out)
	}
}

func TestOnlyMouseAndScrollSayWhereTheyAct(t *testing.T) {
	for _, tc := range []struct {
		args actions.Args
		want bool
	}{
		{&actions.MouseArgs{Op: "move", X: 1, Y: 1}, true},
		{&actions.ScrollArgs{X: 1, Y: 1, DY: 1}, true},
		{&actions.KeyArgs{Keys: "a"}, false},
		{&actions.TypeTextArgs{Text: "a"}, false},
		{&actions.ClickElementArgs{ID: "a1"}, false},
		{&actions.ExecArgs{Command: "ls"}, false},
		{&actions.BrowserArgs{Op: "back"}, false},
	} {
		if got := usesCoordinates(act(tc.args)); got != tc.want {
			t.Errorf("%s: %v", tc.args.Tool(), got)
		}
	}
}

func TestAScrollAfterTheScreenChangedIsNotRunEither(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step { s := okStep(in); s.ScreenHash = "h1"; return s }
	g.model.replies = []reply{calls(keyA("c1"), tc("c2", "scroll", `{"x":10,"y":10,"dy":3}`)), calls(doneOK)}
	g.run("t")
	if len(g.act.ran()) != 1 {
		t.Errorf("%d ran", len(g.act.ran()))
	}
}

func TestAKillWhileTheModelIsThinkingEndsTheTaskAsAKill(t *testing.T) {
	g := newLrig(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.model.replies = []reply{{hook: func(context.Context) { g.killed = true; cancel() }, err: context.Canceled}}
	if out := g.loop.Run(ctx, "t"); out.End != EndKilled || g.m.State() != Running {
		t.Errorf("outcome %+v, state %s", out, g.m.State())
	}
	// And a kill that lands while the screen is being read.
	g = newLrig(t)
	ctx, cancel = context.WithCancel(context.Background())
	g.look.errs = map[int]error{1: context.Canceled}
	g.look.onLook = func() { g.killed = true; cancel() }
	if out := g.loop.Run(ctx, "t"); out.End != EndKilled {
		t.Errorf("outcome %+v", out)
	}
}

func TestTheGuardForgetsWhenTheBudgetIsRenewed(t *testing.T) {
	g := newLrig(t, func(c *config.Config) { c.Safety.MaxTurns = 1 })
	gd := &fakeGuard{}
	g.loop.Guard = gd
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if gd.resets != 1 {
		t.Fatalf("%d resets before the resume", gd.resets)
	}
	_ = g.m.To(Running, "resumed")
	wait(t, ch)
	if gd.resets != 2 {
		t.Errorf("%d resets after the resume", gd.resets)
	}
}

// text is everything the viewer was sent, as one string.
func (e *events) text() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("%+v", e.msgs)
}

func TestWhatTheViewerIsToldWhenTheScreenCannotBeReadOrTheConversationCannotFit(t *testing.T) {
	g := newLrig(t)
	g.look.errs = map[int]error{1: errors.New("the agent did not answer")}
	g.model.replies = []reply{calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if txt := g.view.text(); !strings.Contains(txt, "The screen could not be read: the agent did not answer. Resume to try again") {
		t.Errorf("notices %s", txt)
	}
	_ = g.m.To(Running, "resumed")
	wait(t, ch)

	// A look that fails because the task was stopped is not a problem to report.
	g = newLrig(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.look.errs = map[int]error{1: context.Canceled}
	g.look.onLook = cancel
	if out := g.loop.Run(ctx, "t"); out.End != EndStopped {
		t.Errorf("outcome %+v", out)
	}
	if txt := g.view.text(); strings.Contains(txt, "could not be read") || g.m.State() != Ready {
		t.Errorf("a stop was reported as a failure: %s (state %s)", txt, g.m.State())
	}

	g = newLrig(t)
	conv, err := prompt.NewConversation(strings.Repeat("system ", 400), prompt.Options{Backend: "openai", Mode: backend.ModeNative, Set: g.loop.Set, Renderer: prompt.Renderer{Fencer: untrusted.WithNonce("3f9a1c")}, ContextTokens: 600})
	if err != nil {
		t.Fatal(err)
	}
	g.loop.Conv = conv
	g.run("t")
	if txt := g.view.text(); !strings.Contains(txt, "Level:error") || !strings.Contains(txt, "context") {
		t.Errorf("the person was not told why: %s", txt)
	}
}

func TestWithoutALimitOfItsOwnTheLoopAsksForTheDefaultNumberOfActions(t *testing.T) {
	g := newLrig(t)
	g.loop.MaxActions = 0
	g.model.replies = []reply{calls(doneOK)}
	g.run("t")
	if got := g.model.requests()[0].MaxActions; got != actions.DefaultMaxActions {
		t.Errorf("the request allows %d actions, want %d", got, actions.DefaultMaxActions)
	}
}

func (e *events) chats() []viewer.Chat {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []viewer.Chat
	for _, m := range e.msgs {
		if c, ok := m.(viewer.Chat); ok {
			out = append(out, c)
		}
	}
	return out
}

func TestWhatTheModelSaysIsShownOnceEachWithItsOwnIdAndTime(t *testing.T) {
	g := newLrig(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	g.loop.Now = func() time.Time { at = at.Add(250 * time.Millisecond); return at }
	g.model.replies = []reply{calls(keyA("c1")), {text: "Almost there.", calls: []parse.ToolCall{doneOK}}}
	g.run("t")
	chats := g.view.chats()
	// The first answer said nothing; the second said a sentence and then gave its summary.
	if len(chats) != 2 || chats[0].Text != "Almost there." || chats[1].Text != "all set" || chats[0].Role != "assistant" {
		t.Fatalf("chat lines %+v", chats)
	}
	if chats[0].ID == chats[1].ID || chats[0].ID == "" {
		t.Errorf("ids %q and %q", chats[0].ID, chats[1].ID)
	}
	for _, c := range chats {
		if !strings.HasPrefix(c.TS, "2026-03-04T05:06:") || !strings.HasSuffix(c.TS, "Z") {
			t.Errorf("the time of %q is %q", c.Text, c.TS)
		}
	}
}

func TestTheLogSaysWhoAnsweredHowFastAndWhetherItWasCutOff(t *testing.T) {
	g := newLrig(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	g.loop.Now = func() time.Time { at = at.Add(250 * time.Millisecond); return at }
	g.model.replies = []reply{{text: "I will", trunc: true}, {text: "Done now.", calls: []parse.ToolCall{doneOK}}}
	g.run("t")
	var llms []map[string]any
	for _, r := range g.log.all() {
		if r.Kind == "llm" {
			var m map[string]any
			if err := json.Unmarshal(r.LLM, &m); err != nil {
				t.Fatal(err)
			}
			llms = append(llms, m)
		}
	}
	if len(llms) != 2 {
		t.Fatalf("%d llm records", len(llms))
	}
	sum := sha256.Sum256([]byte("Done now."))
	if llms[0]["truncated"] != true || llms[1]["truncated"] != false || llms[0]["latency_ms"] != 250.0 || llms[1]["model"] != "scripted" ||
		llms[1]["response_sha256"] != hex.EncodeToString(sum[:]) || llms[1]["tool_calls"] != 1.0 || llms[1]["completion_tokens"] != 10.0 {
		t.Errorf("records %+v", llms)
	}
}

func TestWhyTheModelCannotBeUsedIsSaid(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{err: &backend.Error{Kind: backend.KindAuth, Backend: "openai", Status: 401, Message: "bad key"}}}
	out := g.run("t")
	if txt := g.view.text(); !strings.Contains(txt, "The model cannot be used: openai") || !strings.Contains(txt, "bad key") || !strings.Contains(txt, "Level:error") || out.End != EndFailed {
		t.Errorf("outcome %+v, notices %s", out, txt)
	}
}

func TestADeadLogSaysItIsTheEndOfTheSession(t *testing.T) {
	g := newLrig(t)
	g.log.failAt = 1
	g.model.replies = []reply{calls(keyA("c1"))}
	out := g.run("t")
	if out.Err == nil || !strings.Contains(out.Err.Error(), "the audit log can no longer be written, so the session stops") {
		t.Errorf("outcome %+v", out)
	}
}

func TestWithNoPictureToCompareTheFirstChangeIsNotKnownToBeOne(t *testing.T) {
	g := newLrig(t)
	g.look.vision = false
	g.act.fn = func(in Input) Step { s := okStep(in); s.ScreenHash = "x1"; return s }
	g.model.replies = []reply{calls(keyA("c1"), click("c2")), calls(doneOK)}
	g.run("t")
	if len(g.act.ran()) != 2 {
		t.Errorf("%d ran: with nothing to compare to the first hash is the baseline", len(g.act.ran()))
	}
}

func TestTheGuardIsNotToldOfWhatTheLoopDoesItself(t *testing.T) {
	g := newLrig(t)
	gd := &fakeGuard{}
	g.loop.Guard = gd
	g.person.answer = "yes"
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`)), calls(doneOK)}
	g.run("t")
	if len(gd.actions) != 0 {
		t.Errorf("the guard saw %v", gd.actions)
	}
}

func TestWhatThePersonIsToldWhenAGuardPausesTheTask(t *testing.T) {
	g := newLrig(t)
	g.loop.Guard = &fakeGuard{onAction: func(int) string { return "The same action three times." }}
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if txt := g.view.text(); !strings.Contains(txt, "The same action three times. Resume to let the task go on, or Stop to end it.") {
		t.Errorf("notices %s", txt)
	}
	_ = g.m.To(Running, "resumed")
	wait(t, ch)
}

func TestAnActorThatAnswersTheWrongCallEndsTheTaskBeforeTheConversationIsSpoiled(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step { s := okStep(in); s.Result.Tool = "something_else"; return s }
	g.model.replies = []reply{calls(keyA("c1"))}
	out := g.run("t")
	if out.End != EndFailed || out.Err == nil {
		t.Errorf("outcome %+v", out)
	}
}

func TestAStopFromOutsideLeavesTheUnrunActionsSayingSo(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{calls: []parse.ToolCall{keyA("c1"), keyA("c2")}, hook: func(context.Context) { g.m.ToIf([]State{Running}, Ready, "stopped") }}, calls(doneOK)}
	if out := g.run("t"); out.End != EndStopped || len(g.act.ran()) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	g.run("again")
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[0], "not run: the task was stopped") || !strings.Contains(tools[1], "not run: the task was stopped") {
		t.Errorf("results %q", tools)
	}
}

func TestTheModelIsNotAskedAgainOnceTheConversationIsSpoiled(t *testing.T) {
	g := newLrig(t)
	g.act.fn = func(in Input) Step { s := okStep(in); s.Result.Tool = "something_else"; return s }
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK)}
	out := g.run("t")
	if out.End != EndFailed || out.Err == nil || !strings.Contains(out.Err.Error(), "something_else") || len(g.model.requests()) != 1 {
		t.Errorf("outcome %+v, %d requests", out, len(g.model.requests()))
	}
}

func TestWhatTheLoopDoesItselfIsRecordedWithItsReasonAndShownWithItsTurn(t *testing.T) {
	g := newLrig(t)
	g.model.replies = []reply{{text: "Because it is over.", calls: []parse.ToolCall{doneOK}}}
	g.run("t")
	all := g.log.all()
	if len(all) != 2 || all[1].Reason != "Because it is over." || all[1].ActionID != "t1a1" {
		t.Errorf("records %+v", all)
	}
	tr := g.view.traces()
	if len(tr) != 1 || tr[0].Reason != "Because it is over." || tr[0].Turn != 1 || tr[0].ActionID != "t1a1" || tr[0].Summary != "the task is finished" {
		t.Errorf("traces %+v", tr)
	}
	g = newLrig(t)
	g.model.replies = []reply{calls(keyA("c1")), calls(doneOK), calls(doneOK)}
	g.run("t")
	if tr := g.view.traces(); len(tr) != 1 || tr[0].Turn != 2 {
		t.Errorf("traces %+v", tr)
	}
	g.run("again")
	if txt := toolTexts(g.model.requests()[2]); len(txt) != 1 || txt[0] != "1. done ok the task is finished" {
		t.Errorf("the result of done: %q", txt)
	}
}

func TestAQuestionIsShownWithHowLongItWaitedAndANotableAnswerIsCutShort(t *testing.T) {
	g := newLrig(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	g.loop.Now = func() time.Time { at = at.Add(250 * time.Millisecond); return at }
	g.person.answer = strings.Repeat("long ", 100)
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Which one?"}`)), calls(doneOK)}
	g.run("t")
	tr := g.view.traces()
	if len(tr) != 2 || tr[0].Tool != "ask_user" || tr[0].DurationMS != 250 || tr[0].Status != "done" || tr[0].Turn != 1 {
		t.Errorf("traces %+v", tr)
	}
	var said []string
	for _, c := range g.view.chats() {
		said = append(said, c.Text)
	}
	if len(said) < 1 || said[0] != "Which one?" {
		t.Errorf("the question is not shown: %q", said)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 1 || len(tools[0]) > 400 || !strings.HasPrefix(tools[0], "1. ask_user ok The user answered: long long") {
		t.Errorf("the answer in the result is not cut: %d bytes: %q", len(tools[0]), tools[0])
	}
}

func TestAQuestionThatCannotBeAskedIsToldToTheModelUnlessTheTaskWasStopped(t *testing.T) {
	g := newLrig(t)
	g.person.err = errors.New("the viewer is gone")
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`), keyA("c2")), calls(doneOK)}
	out := g.run("t")
	if out.End != EndDone || len(g.act.ran()) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[0], "error internal the question was not answered") || !strings.Contains(tools[1], "not run") {
		t.Errorf("results %q", tools)
	}
}

func TestAnActionWhoseArgumentsAreNotItsToolsIsAFailureNotACrash(t *testing.T) {
	g := newLrig(t)
	bad := parse.Item{Index: 1, CallID: "c1", Action: actions.Action{Tool: "done", Args: &actions.KeyArgs{Keys: "a"}}}
	if step, out := g.loop.finish(1, "t1a1", bad, ""); step.Stop != StopBatch || step.Result.OK || out.End != "" {
		t.Errorf("finish: %+v %+v", step, out)
	}
	bad.Action.Tool = "ask_user"
	if step := g.loop.ask(context.Background(), 1, "t1a1", bad, ""); step.Stop != StopBatch || step.Result.OK || step.Result.Code != actions.CodeInternal {
		t.Errorf("ask: %+v", step)
	}
}

func TestAQuestionIsCleanedBeforeItIsShownOrAsked(t *testing.T) {
	g := newLrig(t)
	g.person.answer = "ok"
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Which\u001b[31m one?\u0007"}`)), calls(doneOK)}
	g.run("t")
	if len(g.person.asked) != 1 || strings.ContainsAny(g.person.asked[0], "\x1b\x07") || !strings.Contains(g.person.asked[0], "Which") {
		t.Errorf("the person was asked %q", g.person.asked)
	}
	if cs := g.view.chats(); len(cs) == 0 || strings.ContainsAny(cs[0].Text, "\x1b\x07") {
		t.Errorf("the chat shows %+v", cs)
	}
}

func TestAStopDuringAQuestionSaysSoToTheActionsAfterIt(t *testing.T) {
	g := newLrig(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.person.err = context.Canceled
	g.person.during = cancel
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`), keyA("c2")), calls(doneOK)}
	if out := g.loop.Run(ctx, "t"); out.End != EndStopped {
		t.Fatalf("outcome %+v", out)
	}
	g.person.err, g.person.during = nil, nil
	g.run("again")
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 2 || !strings.Contains(tools[0], "the question was not answered") || !strings.Contains(tools[1], "not run: the task was stopped") {
		t.Errorf("results %q", tools)
	}
}

func TestEachWayTheModelCanFailComesWithWhatThePersonCanDoAboutIt(t *testing.T) {
	for _, tc := range []struct {
		kind   backend.Kind
		fatal  bool
		advice string
	}{
		{backend.KindUnavailable, false, "Is the model's server running"},
		{backend.KindTimeout, false, "llm.timeout"},
		{backend.KindRateLimited, false, "limiting requests"},
		{backend.KindServer, false, "error of its own"},
		{backend.KindProtocol, false, "does not understand"},
		{backend.KindAuth, true, "refused the key"},
		{backend.KindBadRequest, true, "context window"},
		{backend.KindNotFound, true, "base URL"},
	} {
		g := newLrig(t)
		g.model.replies = []reply{{err: &backend.Error{Kind: tc.kind, Backend: "openai", Message: "boom"}}, calls(doneOK)}
		if tc.fatal {
			if out := g.run("t"); out.End != EndFailed {
				t.Errorf("%v: outcome %+v", tc.kind, out)
			}
		} else {
			ch := g.runAsync(context.Background(), "t")
			g.waitState(Paused)
			_ = g.m.To(Running, "resumed")
			wait(t, ch)
		}
		txt := g.view.text()
		if !strings.Contains(txt, tc.advice) || !strings.Contains(txt, "boom") {
			t.Errorf("%v: notices %s", tc.kind, txt)
		}
	}
	// An error that is not the backend's has no advice and still is reported.
	g := newLrig(t)
	g.model.replies = []reply{{err: errors.New("something odd")}, calls(doneOK)}
	ch := g.runAsync(context.Background(), "t")
	g.waitState(Paused)
	if txt := g.view.text(); !strings.Contains(txt, "The model did not answer: something odd. Resume to try again") {
		t.Errorf("notices %s", txt)
	}
	_ = g.m.To(Running, "resumed")
	wait(t, ch)
	if modelAdvice(backend.Kind(99)) != "" {
		t.Error("advice for a kind of failure nobody has heard of")
	}
}
