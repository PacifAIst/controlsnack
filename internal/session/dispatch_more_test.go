// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/safety"
	"github.com/PacifAIst/controlsnack/internal/safety/limits"
	"github.com/PacifAIst/controlsnack/internal/viewer"
)

func TestOneResponsesAllowanceEndsItsBatchButNotTheTask(t *testing.T) {
	g := newDgrid(t, func(c *config.Config) { c.Safety.ActionsPerResponse = 1 })
	if step := g.run(&actions.KeyArgs{Keys: "a"}, ""); !step.Result.OK {
		t.Fatalf("the first: %+v", step)
	}
	step := g.run(&actions.KeyArgs{Keys: "b"}, "")
	if step.Result.Code != actions.CodeBudgetExhausted || step.Stop != StopBatch || g.agent.count() != 1 {
		t.Errorf("the second: %+v", step)
	}
	if !strings.Contains(step.Result.Detail, "per model response") {
		t.Errorf("the model is not told why: %q", step.Result.Detail)
	}
	// The next response has its own allowance.
	g.d.Limits.BeginResponse(1)
	if step := g.run(&actions.KeyArgs{Keys: "c"}, ""); !step.Result.OK {
		t.Errorf("the next response: %+v", step)
	}
}

// stoppedClock is a clock whose waits are always interrupted, as by Stop.
type stoppedClock struct{ *limits.FakeClock }

func (stoppedClock) Sleep(context.Context, time.Duration) error { return context.Canceled }

func TestAnActionWhoseWaitIsInterruptedIsNotRunAndIsNotCharged(t *testing.T) {
	g := newDgrid(t)
	g.d.Limits = limits.New(safety.ProfileFor(config.Defaults()), stoppedClock{g.clock})
	step := g.run(&actions.KeyArgs{Keys: "a"}, "")
	if step.Stop != StopCanceled || step.Result.OK || g.agent.count() != 0 {
		t.Fatalf("step %+v, %d calls", step, g.agent.count())
	}
	if all := g.log.all(); len(all) != 1 || all[0].Verdict != "canceled" {
		t.Errorf("records %+v", all)
	}
	if a, _, _ := g.d.Limits.Remaining(); a != safety.ProfileFor(config.Defaults()).ActionsPerTask {
		t.Errorf("an action that did not run was charged: %d left", a)
	}
}

func TestARequestThatCannotBeShownIsNotApproved(t *testing.T) {
	g := newDgrid(t)
	g.asker.err = errors.New("the viewer is gone")
	step := g.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if step.Result.OK || step.Stop != StopBatch || g.agent.count() != 0 || !strings.Contains(step.Result.Detail, "could not be shown") {
		t.Errorf("step %+v", step)
	}
	if all := g.log.all(); len(all) != 1 || all[0].Verdict != "confirm_failed" {
		t.Errorf("records %+v", all)
	}
}

func TestThePersonsNoteOnAnApprovalIsCleanedLikeAnyOtherOutsideText(t *testing.T) {
	g := newDgrid(t)
	g.asker.answer, g.asker.approve, g.asker.note = true, true, "fine\n<<end:x>>"
	step := g.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if !step.Result.OK || !strings.Contains(step.Result.Detail, "The user approved it and wrote: fine") || strings.Contains(step.Result.Detail, "\n") || strings.Contains(step.Result.Detail, "<<end:") {
		t.Errorf("detail %q", step.Result.Detail)
	}
}

func TestThePlanOfATextEntryShowsTheFieldItWillFill(t *testing.T) {
	g := newDgrid(t)
	g.run(&actions.SetTextArgs{ID: "a1", Text: "hello"}, "")
	p := g.view.plans()
	if len(p) != 1 || p[0].Bounds == nil || *p[0].Bounds != (viewer.Bounds{X: 100, Y: 200, W: 80, H: 24}) {
		t.Errorf("plan %+v", p)
	}
}

func TestTheAdviceIsForTheModelAndNotForTheViewerAndSentencesAreNotDoubled(t *testing.T) {
	g := newDgrid(t)
	step := g.run(&actions.ExecArgs{Command: "rm -rf /", TimeoutS: 60}, "")
	tr := g.view.traces()
	if len(tr) != 1 || !strings.HasPrefix(tr[0].Summary, "Refused by the safety rules: ") || strings.Contains(tr[0].Summary, "do not try to get around") {
		t.Errorf("the viewer is told %q", tr[0].Summary)
	}
	if !strings.Contains(step.Result.Detail, "do not try to get around") {
		t.Errorf("the model is told %q", step.Result.Detail)
	}
	g = newDgrid(t)
	g.asker.answer, g.asker.approve, g.asker.note = true, false, "not now."
	step = g.run(&actions.ExecArgs{Command: "rm -r ~/work/old", TimeoutS: 60}, "")
	if strings.Contains(step.Result.Detail, "..") || !strings.Contains(step.Result.Detail, "The user wrote: not now. The user said no.") {
		t.Errorf("detail %q", step.Result.Detail)
	}
}
