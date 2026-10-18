// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

func intp(n int) *int { return &n }

// A browser the guest has shown: w1 is a button named "Delete", w2 a password box.
func (g *dgrid) browserNodes() {
	g.t.Helper()
	g.agent.on(protocol.TypeBrowser, func(req any, out any) error {
		r := req.(protocol.BrowserReq)
		if r.Op == "ax_tree" {
			*out.(*protocol.BrowserRes) = protocol.BrowserRes{Generation: "w1", Nodes: []protocol.A11yNode{
				{ID: "w1", Role: "push button", Name: "Delete", Bounds: protocol.Rect{10, 10, 50, 20}},
				{ID: "w2", Role: "password text", Name: "Secret", Bounds: protocol.Rect{10, 40, 50, 20}},
			}}
		}
		return nil
	})
	if d := g.d.Exec.Do(context.Background(), act(&actions.BrowserArgs{Op: "ax_tree"})); !d.Result.OK {
		g.t.Fatal(d.Result)
	}
}

func TestEveryKindOfGUIActionIsJudgedByItsOwnRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		args actions.Args
		// asked is how many times the person is asked, and denied whether it is refused outright.
		asked  int
		denied bool
		hidden string // text that must not be in the log
	}{
		{"click_element on a button named Delete", &actions.ClickElementArgs{ID: "a2"}, 1, false, ""},
		{"click_element on a harmless button", &actions.ClickElementArgs{ID: "a1"}, 0, false, ""},
		{"set_text of a password field", &actions.SetTextArgs{ID: "a3", Text: "pw-one"}, 1, false, "pw-one"},
		{"set_text of a harmless field", &actions.SetTextArgs{ID: "a1", Text: "hello"}, 0, false, ""},
		{"a drag from the Delete button", &actions.MouseArgs{Op: "drag", X: 640, Y: 360, ToX: intp(100), ToY: intp(100)}, 1, false, ""},
		{"a drag onto the Delete button", &actions.MouseArgs{Op: "drag", X: 100, Y: 100, ToX: intp(640), ToY: intp(360)}, 1, false, ""},
		{"a drag from nothing to nothing", &actions.MouseArgs{Op: "drag", X: 100, Y: 100, ToX: intp(110), ToY: intp(110)}, 0, false, ""},
		{"a double click on the Delete button", &actions.MouseArgs{Op: "double_click", X: 640, Y: 360}, 1, false, ""},
		{"a move over the Delete button", &actions.MouseArgs{Op: "move", X: 640, Y: 360}, 0, false, ""},
		{"the key that deletes for good", &actions.KeyArgs{Keys: "shift+Delete"}, 1, false, ""},
		{"an ordinary key", &actions.KeyArgs{Keys: "Delete"}, 0, false, ""},
		{"typing into the focused password field", &actions.TypeTextArgs{Text: "pw-two"}, 1, false, "pw-two"},
		{"browser: an address that is code", &actions.BrowserArgs{Op: "open", URL: "javascript:alert(1)"}, 0, true, ""},
		{"browser: an ordinary address", &actions.BrowserArgs{Op: "open", URL: "https://example.org/"}, 0, false, ""},
		{"browser: typing into a password box by selector", &actions.BrowserArgs{Op: "type", Target: "input[type=password]", Text: "pw-three"}, 1, false, "pw-three"},
		{"browser: typing into a password box by id", &actions.BrowserArgs{Op: "type", Target: "w2", Text: "pw-four"}, 1, false, "pw-four"},
		{"browser: clicking the Delete button by id", &actions.BrowserArgs{Op: "click", Target: "w1"}, 1, false, ""},
		{"browser: clicking something unnamed by selector", &actions.BrowserArgs{Op: "click", Target: "#plain"}, 0, false, ""},
	} {
		g := newDgrid(t)
		g.browserNodes()
		g.asker.answer, g.asker.approve = true, true
		before := g.agent.count()
		step := g.run(tc.args, "")
		if got := len(g.asker.requests()); got != tc.asked {
			t.Errorf("%s: the person was asked %d times, want %d (%+v)", tc.name, got, tc.asked, step.Result)
		}
		if tc.denied {
			if step.Result.OK || step.Result.Code != actions.CodePolicyDenied || g.agent.count() != before {
				t.Errorf("%s: %+v", tc.name, step)
			}
		} else if !step.Result.OK {
			t.Errorf("%s: %+v", tc.name, step.Result)
		}
		if tc.hidden != "" {
			for _, rc := range g.log.all() {
				if strings.Contains(string(rc.Args), tc.hidden) {
					t.Errorf("%s: the text is in the log: %s", tc.name, rc.Args)
				}
			}
			for _, tr := range g.view.traces() {
				if strings.Contains(string(tr.Args), tc.hidden) {
					t.Errorf("%s: the text is in the viewer: %s", tc.name, tr.Args)
				}
			}
		}
	}
}

func TestTheProfileDecidesForGUIActionsToo(t *testing.T) {
	// Unsafe: a click that would ask is not asked.
	g := newDgrid(t, func(c *config.Config) { c.Safety.Unsafe = true })
	if step := g.run(&actions.ClickElementArgs{ID: "a2"}, ""); !step.Result.OK || len(g.asker.requests()) != 0 {
		t.Errorf("unsafe: %+v, asked %d", step.Result, len(g.asker.requests()))
	}
	// Step: even a harmless click asks.
	g = newDgrid(t, func(c *config.Config) { c.Safety.Step = true })
	g.asker.answer, g.asker.approve = true, true
	if step := g.run(&actions.ClickElementArgs{ID: "a1"}, ""); !step.Result.OK || len(g.asker.requests()) != 1 {
		t.Errorf("step: %+v, asked %d", step.Result, len(g.asker.requests()))
	}
}

func TestWhatTheRulesHaveNothingToSayAboutStillAsksInStepMode(t *testing.T) {
	g := newDgrid(t, func(c *config.Config) { c.Safety.Step = true })
	g.asker.answer, g.asker.approve = true, true
	g.agent.on(protocol.TypeA11yTree, func(any, any) error { return nil })
	if step := g.run(&actions.ScreenshotArgs{}, ""); !step.Result.OK || len(g.asker.requests()) != 1 {
		t.Errorf("a screenshot in step mode: %+v, asked %d", step.Result, len(g.asker.requests()))
	}
	g = newDgrid(t)
	g.run(&actions.ScreenshotArgs{}, "")
	if len(g.asker.requests()) != 0 {
		t.Error("a screenshot asked outside step mode")
	}
}

func TestTheDurationIsTheDispatchersOwnClockAndTheScreenHashIsPassedOn(t *testing.T) {
	g := newDgrid(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	g.d.Now = func() time.Time { at = at.Add(250 * time.Millisecond); return at }
	step := g.run(&actions.KeyArgs{Keys: "a"}, "")
	if step.ScreenHash != "after" {
		t.Errorf("the screen hash is %q", step.ScreenHash)
	}
	tr := g.view.traces()
	if len(tr) != 2 || tr[1].DurationMS != 250 {
		t.Errorf("traces %+v", tr)
	}
	if res := rec(t, g.log, 1).Result; !strings.Contains(string(res), `"duration_ms":250`) {
		t.Errorf("result record %s", res)
	}
}

func TestTheRulesAreNamedOnceEachToThePersonAndToTheModel(t *testing.T) {
	// A command that trips the same rule twice.
	g := newDgrid(t)
	step := g.run(&actions.ExecArgs{Command: "rm -rf / ; rm -rf /home", TimeoutS: 60}, "")
	if step.Result.Code != actions.CodePolicyDenied {
		t.Fatalf("%+v", step.Result)
	}
	seen := map[string]int{}
	for _, id := range g.log.all()[0].Rules {
		seen[id]++
	}
	if len(seen) == 0 {
		t.Fatal("no rules named")
	}
	for id, n := range seen {
		if n > 1 {
			t.Logf("the rule %s was hit %d times", id, n)
		}
		if c := strings.Count(step.Result.Detail, id+" ("); c != 1 {
			t.Errorf("the refusal names %s %d times: %s", id, c, step.Result.Detail)
		}
	}
	g = newDgrid(t, func(c *config.Config) { c.Safety.Unsafe = true })
	g.asker.answer, g.asker.approve = true, true
	g.run(&actions.ExecArgs{Command: "rm -rf / ; rm -rf /home", TimeoutS: 60}, "")
	reqs := g.asker.requests()
	if len(reqs) != 1 {
		t.Fatalf("asked %d times", len(reqs))
	}
	ids := map[string]bool{}
	for _, ri := range reqs[0].Rules {
		if ids[ri.ID] {
			t.Errorf("the person is shown %s twice", ri.ID)
		}
		ids[ri.ID] = true
	}
}

func TestAnErrorFromTheRulesOfNoToolIsNotInventedWhenNothingIsKnown(t *testing.T) {
	// A refusal with no rule to name still says it is refused (the profile can refuse on its own account).
	if got := refusedWords(nil); got != "Refused by the safety rules" {
		t.Errorf("%q", got)
	}
}
