// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety/confirm"
)

func TestThePictureTheModelGetsIsTheScreenCutToTheLongestSideAllowed(t *testing.T) {
	for _, tc := range []struct {
		guest prompt.Size
		max   int
		want  prompt.Size
	}{
		{prompt.Size{W: 1920, H: 1080}, 0, prompt.Size{W: 1920, H: 1080}},
		{prompt.Size{W: 1920, H: 1080}, 2000, prompt.Size{W: 1920, H: 1080}},
		{prompt.Size{W: 1920, H: 1080}, 1920, prompt.Size{W: 1920, H: 1080}},
		{prompt.Size{W: 1920, H: 1080}, 1280, prompt.Size{W: 1280, H: 720}},
		{prompt.Size{W: 1280, H: 800}, 1000, prompt.Size{W: 1000, H: 625}},
		{prompt.Size{W: 1000, H: 333}, 500, prompt.Size{W: 500, H: 167}},
		{prompt.Size{W: 400, H: 300}, 200, prompt.Size{W: 200, H: 150}},
		{prompt.Size{W: 333, H: 1000}, 500, prompt.Size{W: 167, H: 500}},
		{prompt.Size{W: 800, H: 600}, 1, prompt.Size{W: 1, H: 1}},
	} {
		if got := pictureSize(tc.guest, tc.max); got != tc.want {
			t.Errorf("%v cut to %d: %v, want %v", tc.guest, tc.max, got, tc.want)
		}
	}
}

// blockingCaller waits for the context of a call as a real connection does.
type blockingCaller struct{ a *guestAgent }

func (b blockingCaller) Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error) {
	done := make(chan error, 1)
	go func() { _, err := b.a.Call(ctx, typ, req, blob, res); done <- err }()
	select {
	case err := <-done:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAnAgentThatDoesNotAnswerTheReadOfAScriptDoesNotHoldUpThePolicy(t *testing.T) {
	a := newAssembly(t)
	a.agent.on(protocol.TypeFilePeek, func(any, any) error { time.Sleep(500 * time.Millisecond); return nil })
	r := agentScripts{link: blockingCaller{a.agent}, timeout: 30 * time.Millisecond}
	began := time.Now()
	if _, _, _, err := r.Peek(context.Background(), "/tmp/x.sh", 100); err == nil || time.Since(began) > 400*time.Millisecond {
		t.Errorf("the read took %s and said %v", time.Since(began), err)
	}
}

func TestMarksAndTheTreeInThePrompt(t *testing.T) {
	with := newAssembly(t, func(p *Parts) { p.Model.Setup.Marks = true })
	with.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	with.run("t")
	sys := with.model.requests()[0].Messages[0].Text
	if !strings.Contains(sys, "Numbered boxes") || strings.Contains(sys, "no accessibility tree") {
		t.Errorf("with a tree and marks:\n%s", sys)
	}
	without := newAssembly(t, func(p *Parts) {
		p.Model.Setup.Marks = true
		p.Capabilities = []string{protocol.CapScreenshot, protocol.CapInput, protocol.CapExec}
	})
	without.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	without.run("t")
	sys = without.model.requests()[0].Messages[0].Text
	if strings.Contains(sys, "Numbered boxes") || !strings.Contains(sys, "no accessibility tree") {
		t.Errorf("with no tree:\n%s", sys)
	}
}

func TestTheObservationFormatIsTheConfigurations(t *testing.T) {
	a := newAssembly(t, func(p *Parts) { p.Cfg.LLM.ObsFormat = "json" })
	a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	a.run("t")
	if txt := lastText(a.model.requests()[0]); !strings.HasPrefix(strings.TrimSpace(txt), "{") {
		t.Errorf("the observation is not JSON:\n%s", txt)
	}
	b := newAssembly(t)
	b.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	b.run("t")
	if txt := lastText(b.model.requests()[0]); !strings.HasPrefix(txt, "TURN 1") {
		t.Errorf("the observation is not text:\n%s", txt)
	}
}

func TestTheModelsContextIsWhatItSaysOrWhatTheUserGave(t *testing.T) {
	// A window too small for even the system prompt ends the task, whichever way the size is known.
	a := newAssembly(t, func(p *Parts) { p.Model.Caps.ContextLength = 0; p.Cfg.LLM.NumCtx = 200 })
	a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if out := a.run("t"); out.End != EndFailed {
		t.Errorf("a window of 200 tokens: %+v", out)
	}
	b := newAssembly(t, func(p *Parts) { p.Model.Caps.ContextLength = 200; p.Cfg.LLM.NumCtx = 0 })
	b.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if out := b.run("t"); out.End != EndFailed {
		t.Errorf("a model that says its window is 200 tokens: %+v", out)
	}
	c := newAssembly(t, func(p *Parts) { p.Model.Caps.ContextLength = 0; p.Cfg.LLM.NumCtx = 0 })
	c.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if out := c.run("t"); out.End != EndDone {
		t.Errorf("no window known: %+v", out)
	}
}

func TestTheReadsOfTheTextAreGivenTheTimeTheGuestNeeds(t *testing.T) {
	for _, slow := range []int{0, 1, 4} {
		a := newAssembly(t, func(p *Parts) {
			p.SlowBy = slow
			p.Model = testModel(p.Model.Backend, false)
			p.Model.Setup.Profile = "compact"
		})
		a.agent.on(protocol.TypeOCR, func(_ any, out any) error {
			*out.(*protocol.OCRRes) = protocol.OCRRes{Generation: "t1"}
			return nil
		})
		a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
		a.run("t")
		want := ocrBudgetBase * time.Duration(max(1, slow))
		var got, tree int
		for _, c := range a.agent.calls {
			switch c.typ {
			case protocol.TypeOCR:
				got = c.req.(protocol.OCRReq).BudgetMS
			case protocol.TypeA11yTree:
				tree = c.req.(protocol.A11yTreeReq).BudgetMS
			}
		}
		if got != int(want.Milliseconds()) || tree != int((treeBudgetBase*time.Duration(max(1, slow))).Milliseconds()) {
			t.Errorf("slow by %d: the text's budget is %d ms and the tree's %d ms", slow, got, tree)
		}
	}
}

type neverAnswers struct{}

func (neverAnswers) Present(context.Context, confirm.Request) error { return nil }
func (neverAnswers) Resolved(string, confirm.Outcome)               {}

func TestStepModeAndTheConfirmationTimeoutComeFromTheProfileAndTheConsole(t *testing.T) {
	a := newAssembly(t, func(p *Parts) {
		p.Profile.ConfirmTimeout = 60 * time.Millisecond
		p.Profile.Step = true
	})
	// A page is attached and never answers: with the broker's own time this would wait two minutes.
	a.s.Broker.SetPresenter(neverAnswers{})
	a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"ls","timeout_s":10}`)), calls(tc("c2", "done", `{"success":false,"summary":"x"}`))}
	a.run("t")
	if tools := toolTexts(a.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "confirm_timeout") {
		t.Errorf("results %q", tools)
	}
	// The person's word on step mode reaches the dispatcher.
	b := newAssembly(t)
	b.s.Console.Control("step_on")
	b.model.replies = []reply{calls(tc("c1", "exec", `{"command":"ls","timeout_s":10}`)), calls(tc("c2", "done", `{"success":false,"summary":"x"}`))}
	b.run("t")
	if tools := toolTexts(b.model.requests()[1]); len(tools) != 1 || !strings.Contains(tools[0], "user_rejected") {
		t.Errorf("step mode did not ask: %q", tools)
	}
}

func TestTheGuestsHomeAndTheNetworkChangeWhatTheRulesSay(t *testing.T) {
	// Writing to the shell's start-up file of the guest account is a thing to ask about, wherever the home is.
	for _, user := range []string{"cs", "alice"} {
		a := newAssembly(t, func(p *Parts) { p.User = user })
		a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"echo x >> /home/`+user+`/.bashrc","timeout_s":10}`)), calls(tc("c2", "done", `{"success":false,"summary":"x"}`))}
		a.run("t")
		tools := toolTexts(a.model.requests()[1])
		if len(tools) != 1 || strings.Contains(tools[0], "exec ok") {
			t.Errorf("%s: a write to the account's start-up file ran: %q", user, tools)
		}
	}
	// A command that can send data out is only a question when the guest has a way out.
	for _, net := range []bool{false, true} {
		a := newAssembly(t, func(p *Parts) { p.Cfg.Net.Allow = net })
		a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"curl -d x http://example.org/","timeout_s":10}`)), calls(tc("c2", "done", `{"success":false,"summary":"x"}`))}
		a.run("t")
		tools := toolTexts(a.model.requests()[1])
		asked := len(tools) == 1 && strings.Contains(tools[0], "user_rejected")
		if asked != net {
			t.Errorf("net=%v: %q", net, tools)
		}
	}
}

func TestTheModelsKeyIsMaskedWhateverItLooksLike(t *testing.T) {
	const key = "plainsecretvalue-1234567890"
	a := newAssembly(t, func(p *Parts) { p.Model.Target.APIKey = key })
	a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"echo `+key+`","timeout_s":30}`)), calls(tc("c2", "done", `{"success":true,"summary":"ok"}`))}
	a.run("t")
	for _, r := range a.log.all() {
		if strings.Contains(string(r.Args)+string(r.Result)+string(r.Data), key) {
			t.Errorf("the key is in the log: %+v", r)
		}
	}
}
