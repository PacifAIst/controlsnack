// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/detect"
	"github.com/PacifAIst/controlsnack/internal/llm/fakellm"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack/internal/llm/vision"
	"github.com/PacifAIst/controlsnack/internal/safety/untrusted"
)

// The pieces of the LLM layer, put together as a session will put them: probe, plan, system prompt, conversation, backend,
// parser, results. It runs two turns against the fake model and looks at what the model was sent.
func TestTheLayerWorksTogetherForTwoTurns(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{
		fakellm.Act(fakellm.Tool("exec", map[string]any{"command": "uname -r"})),
		fakellm.Act(fakellm.Tool("done", map[string]any{"success": true, "summary": "the kernel is 6.12"})),
	}})
	ctx := context.Background()

	d := detect.New(detect.Options{Getenv: func(string) string { return "" }})
	c := config.Defaults()
	c.LLM.Backend, c.LLM.BaseURL = "lmstudio", s.URL()
	target, models, err := d.Resolve(ctx, c)
	if err != nil || target.Model != s.Model() || len(models) != 1 {
		t.Fatalf("target = %+v models = %v err = %v", target, models, err)
	}
	caps, err := d.Probe(ctx, target, false)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := vision.Plan(caps, target.Local, c.LLM)
	if err != nil {
		t.Fatal(err)
	}
	if !setup.Vision || setup.Mode != backend.ModeNative || setup.ImageFormat != imaging.WebP || !setup.Marks {
		t.Fatalf("setup = %+v", setup)
	}
	b, err := d.Backend(target)
	if err != nil {
		t.Fatal(err)
	}

	fencer := untrusted.WithNonce("3f9a1c")
	set, err := actions.NewSet(setup.Profile)
	if err != nil {
		t.Fatal(err)
	}
	sys, err := prompt.System(prompt.Params{Nonce: fencer.Nonce(), Width: 1280, Height: 800, MaxActions: 3, Vision: setup.Vision, Marks: setup.Marks, Mode: setup.Mode, Set: set})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := prompt.NewConversation(sys.Text, prompt.Options{Backend: "openai", Mode: setup.Mode, Set: set, Renderer: prompt.Renderer{Fencer: fencer}, ContextTokens: caps.ContextLength})
	if err != nil {
		t.Fatal(err)
	}
	picture, err := imaging.Encode(testImage(), setup.ImageFormat, 0)
	if err != nil {
		t.Fatal(err)
	}
	img := &backend.Image{MIME: setup.ImageFormat.MIME(), Data: picture}

	var final parse.Result
	for turn := 1; turn <= 2; turn++ {
		obs := prompt.Observation{
			Turn: turn, Screen: prompt.Size{W: 1280, H: 800}, Network: "disabled", Mode: "vision", Tools: setup.Profile,
			Task: "print the kernel version", Budget: prompt.Budget{ActionsLeft: 40, TurnsLeft: 80, TimeLeft: 30 * time.Minute},
			UI:    &prompt.UITree{Generation: "g1", Scope: "active_window", Nodes: []prompt.Node{{ID: "a1", Role: "frame", Name: "Terminal", Bounds: prompt.Rect{0, 0, 100, 100}}}},
			Image: &prompt.ImageNote{W: 1280, H: 800},
		}
		conv.Begin(obs, img)
		req, _, err := conv.Request(3)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := b.Complete(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		final = parse.Parse(set, resp.Reply, 3)
		if !final.Usable() {
			t.Fatalf("turn %d: %+v", turn, final)
		}
		if err := conv.Assistant(resp.Reply.Text, resp.Reply.ToolCalls); err != nil {
			t.Fatal(err)
		}
		var results []prompt.Result
		for _, item := range final.Actions {
			results = append(results, prompt.Result{
				CallID: item.CallID, Tool: item.Action.Tool, Action: prompt.Describe(item.Action), OK: true, Detail: "exit=0 12ms",
				Parts: []prompt.Part{{Source: "exec.stdout", Text: "6.12.x-amd64\n"}},
			})
		}
		if err := conv.Results(results, prompt.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	if final.Actions[0].Action.Tool != "done" {
		t.Fatalf("final = %+v", final)
	}

	reqs := s.Requests()
	if len(reqs) != 2 || s.Probes() == 0 || len(s.Errors()) != 0 {
		t.Fatalf("requests %d probes %d errors %q", len(reqs), s.Probes(), s.Errors())
	}
	first, second := reqs[0], reqs[1]
	if len(first.Messages) != 2 || first.Messages[0].Role != "system" || first.Pictures() != 1 || !first.HasTool("exec") || first.HasTool("screenshot") {
		t.Errorf("first request: %d messages, %d pictures, tools %v", len(first.Messages), first.Pictures(), first.Tools)
	}
	roles := ""
	for _, m := range second.Messages {
		roles += m.Role[:1]
	}
	if roles != "suatu" {
		t.Fatalf("roles of the second request = %s, want system, user, assistant, tool, user", roles)
	}
	if second.Pictures() != 1 {
		t.Errorf("only the latest picture stays in context: %d", second.Pictures())
	}
	{
		tool := second.Messages[3]
		if tool.Role != "tool" || tool.CallID == "" || !strings.HasPrefix(tool.Text, "1. exec ok exit=0 12ms") || !strings.Contains(tool.Text, "<<untrusted:3f9a1c exec.stdout>>6.12.x-amd64<<end:3f9a1c>>") {
			t.Errorf("the result of the first call: %+v", tool)
		}
		if call := second.Messages[2].Calls; len(call) != 1 || call[0].Name != "exec" || call[0].Args != `{"command":"uname -r"}` {
			t.Errorf("the model's own call as the server read it: %+v", call)
		}
		if strings.Contains(second.Messages[1].Text, "UI generation") {
			t.Errorf("the first observation is compact in the second request:\n%s", second.Messages[1].Text)
		}
		if !strings.Contains(second.Messages[4].Text, "TURN 2") || !strings.Contains(second.Messages[4].Text, "UI generation") {
			t.Errorf("the second observation is whole:\n%s", second.Messages[4].Text)
		}
	}
	if !strings.Contains(first.System(), "<<untrusted:3f9a1c source>>") {
		t.Error("the system prompt names the fence")
	}
}
