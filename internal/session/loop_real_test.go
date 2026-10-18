// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/safety/audit"
)

// realLoop is the loop with the real dispatcher, rules, broker and executor (and a fake guest) under it.
func realLoop(t *testing.T) (*lrig, *dgrid) {
	t.Helper()
	g := newLrig(t)
	d := newDgrid(t)
	d.d.Machine, d.d.Limits, d.d.Audit, d.d.Viewer = g.m, g.lim, g.log, g.view
	d.asker.answer, d.asker.approve = true, true
	g.loop.Act, g.loop.View = d.d, d.d.Exec.SetView
	return g, d
}

func TestTheLoopAndTheDispatcherTogether(t *testing.T) {
	g, d := realLoop(t)
	g.model.replies = []reply{
		calls(tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)),
		calls(tc("c2", "exec", `{"command":"rm -rf /","timeout_s":30}`), keyA("c3")),
		calls(tc("c4", "exec", `{"command":"rm -r ~/work/old","timeout_s":30}`)),
		calls(doneOK),
	}
	out := g.run("look at the kernel")
	if out.End != EndDone || out.Turns != 4 {
		t.Fatalf("outcome %+v", out)
	}
	reqs := g.model.requests()
	if tools := toolTexts(reqs[1]); len(tools) != 1 || !strings.Contains(tools[0], "exec ok exit=0") || !strings.Contains(tools[0], "exec.stdout>>ok<<end:3f9a1c>>") {
		t.Errorf("the first result: %q", tools)
	}
	tools := toolTexts(reqs[2])
	if len(tools) != 2 || !strings.Contains(tools[0], "error policy_denied") || !strings.Contains(tools[0], "Refused by the safety rules") ||
		!strings.Contains(tools[0], "do not try to get around them") || !strings.Contains(tools[1], "not run") {
		t.Errorf("the refusal: %q", tools)
	}
	if tools := toolTexts(reqs[3]); len(tools) != 1 || !strings.Contains(tools[0], "exec ok") {
		t.Errorf("the confirmed command: %q", tools)
	}
	if len(d.asker.requests()) != 1 {
		t.Errorf("the person was asked %d times", len(d.asker.requests()))
	}
	// The log tells the whole story, in order, and verifies as a chain of records.
	var story []string
	for _, r := range g.log.all() {
		switch r.Kind {
		case audit.KindLLM:
			story = append(story, "llm")
		case audit.KindAction:
			story = append(story, r.Tool+":"+r.Event+":"+r.Verdict)
		}
	}
	want := "llm,exec:decision:allowed,exec:result:allowed,llm,exec:decision:denied,llm,exec:decision:confirmed,exec:result:confirmed,llm,done:result:allowed"
	if got := strings.Join(story, ","); got != want {
		t.Errorf("the log reads\n%s\nwant\n%s", got, want)
	}
}
