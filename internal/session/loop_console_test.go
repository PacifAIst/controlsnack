// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
	"github.com/PacifAIst/controlsnack/internal/safety/confirm"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

// consoleLoop is the loop with a console as its person, sharing the loop's state machine, budgets, log and viewer.
func consoleLoop(t *testing.T) (*lrig, *Console) {
	t.Helper()
	g := newLrig(t)
	br := confirm.New(confirm.Options{Timeout: 5 * time.Second})
	c := &Console{Machine: g.m, Broker: br, Limits: g.lim, Viewer: g.view, Audit: g.log}
	g.loop.Person = c
	return g, c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTheModelAsksAndThePersonAnswersInTheChat(t *testing.T) {
	g, c := consoleLoop(t)
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Which color?"}`)), calls(doneOK)}
	ctx, end := c.Begin(context.Background())
	defer end()
	ch := g.runAsync(ctx, "paint it")
	waitFor(t, "the question", func() bool { return g.m.State() == AwaitingUser })
	c.Chat("the blue one")
	out := wait(t, ch)
	if out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	tools := toolTexts(g.model.requests()[1])
	if len(tools) != 1 || !strings.Contains(tools[0], "The user answered: the blue one") {
		t.Errorf("results %q", tools)
	}
	// What was said in the middle of a task reaches the next turn.
	g2, c2 := consoleLoop(t)
	g2.model.replies = []reply{{calls: []parse.ToolCall{keyA("c1")}, hook: func(context.Context) { c2.Chat("and use the dark theme") }}, calls(doneOK)}
	ctx2, end2 := c2.Begin(context.Background())
	defer end2()
	if out := wait(t, g2.runAsync(ctx2, "paint it")); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	if txt := lastText(g2.model.requests()[1]); !strings.Contains(txt, "USER: and use the dark theme") {
		t.Errorf("the next observation:\n%s", txt)
	}
}

func TestAMessageToAReadySessionStartsTheNextTask(t *testing.T) {
	g, c := consoleLoop(t)
	tasks := make(chan string, 1)
	c.StartTask = func(text string) { tasks <- text }
	g.model.replies = []reply{calls(doneOK)}
	ctx, end := c.Begin(context.Background())
	if out := g.loop.Run(ctx, "first"); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	end()
	c.Chat("now the second thing")
	select {
	case text := <-tasks:
		if text != "now the second thing" {
			t.Errorf("task %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no task started")
	}
}

func TestPauseHoldsTheActionsAndStopEndsTheTaskFromThePage(t *testing.T) {
	g, c := consoleLoop(t)
	g.model.replies = []reply{{calls: []parse.ToolCall{keyA("c1"), keyA("c2")}, hook: func(context.Context) { c.Control(viewer.OpPause) }}}
	ctx, end := c.Begin(context.Background())
	defer end()
	ch := g.runAsync(ctx, "t")
	waitFor(t, "the pause", func() bool { return g.m.State() == Paused })
	time.Sleep(30 * time.Millisecond)
	if len(g.act.ran()) != 0 {
		t.Fatal("an action ran while paused")
	}
	c.Control(viewer.OpStop)
	out := wait(t, ch)
	if out.End != EndStopped || g.m.State() != Ready {
		t.Errorf("outcome %+v, state %s", out, g.m.State())
	}
	if len(g.act.ran()) != 0 {
		t.Error("an action ran after the stop")
	}
}

func TestStopInTheMiddleOfAQuestionEndsThatQuestion(t *testing.T) {
	g, c := consoleLoop(t)
	g.model.replies = []reply{calls(tc("c1", "ask_user", `{"question":"Q?"}`), keyA("c2"))}
	ctx, end := c.Begin(context.Background())
	defer end()
	ch := g.runAsync(ctx, "t")
	waitFor(t, "the question", func() bool { return g.m.State() == AwaitingUser })
	c.Control(viewer.OpStop)
	out := wait(t, ch)
	if out.End != EndStopped || g.m.State() != Ready {
		t.Errorf("outcome %+v, state %s", out, g.m.State())
	}
}

func TestStepModeAsksForEveryActionUntilThePersonSwitchesItOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile bool // --step at the start
		word    string
		asked   int
	}{
		{"off by default", false, "", 0},
		{"on by the profile", true, "", 1},
		{"switched on by the person", false, viewer.OpStepOn, 1},
		{"switched off by the person", true, viewer.OpStepOff, 0},
	} {
		d := newDgrid(t, func(c *config.Config) { c.Safety.Step = tc.profile })
		d.asker.answer, d.asker.approve = true, true
		con := &Console{Machine: d.machine, Broker: d.d.Broker, Step: d.d}
		if tc.word != "" {
			con.Control(tc.word)
		}
		if step := d.run(&actions.KeyArgs{Keys: "a"}, ""); !step.Result.OK || len(d.asker.requests()) != tc.asked {
			t.Errorf("%s: %+v, asked %d, want %d", tc.name, step.Result, len(d.asker.requests()), tc.asked)
		}
	}
}
