// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

// A model that cannot see goes from the probe to a request, with no picture anywhere, and acts on what it read.
func TestATextOnlySessionFromTheProbeToTheRequest(t *testing.T) {
	caps := detect.Capabilities{Model: "llama3.2:3b", NativeTools: true, JSONPrompt: true}
	setup, err := Plan(caps, true, config.Defaults().LLM)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Vision || setup.Badge() != "TEXT-ONLY" || len(setup.Notices) != 1 {
		t.Fatalf("setup = %+v", setup)
	}

	g := newGuest(1280, 800, 1)
	o := observer(t, g, setup, nil)
	seen, err := o.Look(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	fencer := untrusted.WithNonce("3f9a1c")
	set, err := actions.NewSet(setup.Profile)
	if err != nil {
		t.Fatal(err)
	}
	sys, err := prompt.System(prompt.Params{Nonce: fencer.Nonce(), Width: 1280, Height: 800, MaxActions: 3, Vision: setup.Vision, Mode: setup.Mode, Set: set})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := prompt.NewConversation(sys.Text, prompt.Options{Backend: "ollama", Mode: setup.Mode, Set: set, Renderer: prompt.Renderer{Fencer: fencer}, ContextTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}
	obs := prompt.Observation{Turn: 1, Network: "disabled", Task: "print the kernel version", Budget: prompt.Budget{ActionsLeft: 40, TurnsLeft: 80, TimeLeft: 30 * time.Minute}}
	seen.Fill(&obs)
	conv.Begin(obs, nil)
	req, rep, err := conv.Request(3)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ImageTokens != 0 {
		t.Errorf("a picture is counted: %+v", rep)
	}
	for _, m := range req.Messages {
		if len(m.Images) != 0 {
			t.Fatalf("a picture in a text-only request: %+v", m)
		}
	}
	system, observation := req.Messages[0].Text, req.Messages[1].Text
	for _, want := range []string{"cannot see the screen", "ids o1, o2", "Answer by calling the tools"} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %q", want)
		}
	}
	for _, want := range []string{"mode: text-only", "tools: compact", "screen 1280x800", "UI generation g7", "OCR generation g3", "o1 \"cs@guest:~$\" [10,30,200,20]", "a2 push_button \"OK\""} {
		if !strings.Contains(observation, want) {
			t.Errorf("the observation lacks %q:\n%s", want, observation)
		}
	}
	if strings.Contains(observation, "[image attached") {
		t.Errorf("the observation mentions a picture:\n%s", observation)
	}

	// The model reads a line of text at (10,30) and clicks there: those are screen pixels, and the mapper says so.
	click := parseAction(t, "mouse", `{"op":"click","x":110,"y":40}`)
	g2, err := seen.Mapper.Guest(click)
	if err != nil || g2.Args.(*actions.MouseArgs).X != 110 || g2.Args.(*actions.MouseArgs).Y != 40 {
		t.Errorf("the click in text-only mode: %+v %v", g2, err)
	}
}
