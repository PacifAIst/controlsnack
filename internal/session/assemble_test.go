// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/safety"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

// guestAgent is a fake agent that behaves like a small desktop: a screen, a tree with one button, commands and files.
type guestAgent struct {
	*fakeAgent
	mu sync.Mutex
	up bool
	// blob is the last picture made; peeked the head of a file.
	blob, peeked []byte
}

func (g *guestAgent) Up() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.up }

func (g *guestAgent) setUp(up bool) { g.mu.Lock(); g.up = up; g.mu.Unlock() }

func newGuestAgent(w, h int) *guestAgent {
	a := &guestAgent{fakeAgent: newAgent(), up: true}
	a.on(protocol.TypeScreenInfo, func(_ any, out any) error {
		*out.(*protocol.ScreenInfoRes) = protocol.ScreenInfoRes{Monitors: []protocol.Monitor{{Bounds: protocol.Rect{0, 0, w, h}, Primary: true}}, Cursor: protocol.Point{X: 7, Y: 8}}
		return nil
	})
	a.on(protocol.TypeA11yTree, func(_ any, out any) error {
		*out.(*protocol.A11yTreeRes) = protocol.A11yTreeRes{Generation: "g1", Nodes: []protocol.A11yNode{
			{ID: "a1", Role: "push button", Name: "OK", Bounds: protocol.Rect{10, 10, 60, 20}, Actions: []string{"click"}}}}
		return nil
	})
	a.on(protocol.TypeInput, func(_ any, out any) error {
		*out.(*protocol.InputRes) = protocol.InputRes{ScreenHashAfter: "ab"}
		return nil
	})
	a.on(protocol.TypeExec, func(_ any, out any) error {
		*out.(*protocol.ExecRes) = protocol.ExecRes{ExitCode: 0, DurationMS: 3, Stdout: "6.1.0-cs\n"}
		return nil
	})
	a.on(protocol.TypeScreenshot, func(req any, out any) error {
		r := req.(protocol.ScreenshotReq)
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		scaled, sc := imaging.Scale(img, r.MaxLongEdge)
		blob, err := imaging.Encode(scaled, imaging.Format(r.Format), r.Quality)
		if err != nil {
			return err
		}
		b := scaled.Bounds()
		*out.(*protocol.ScreenshotRes) = protocol.ScreenshotRes{W: b.Dx(), H: b.Dy(), Format: r.Format, Hash: "0123456789abcdef", Cursor: protocol.Point{X: 7, Y: 8}, Scale: sc}
		a.blob = blob
		return nil
	})
	return a
}

func (g *guestAgent) Call(ctx context.Context, typ string, req any, blob []byte, res any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, err := g.fakeAgent.Call(ctx, typ, req, blob, res)
	if err != nil {
		return nil, err
	}
	if typ == protocol.TypeScreenshot {
		return g.blob, nil
	}
	if typ == protocol.TypeFilePeek {
		return g.peeked, nil
	}
	return nil, nil
}

// testModel is a model the session can use: native tool calls, pictures in PNG.
func testModel(b backend.Backend, vis bool) Model {
	return Model{
		Target:  detect.Target{Backend: "lmstudio", Protocol: detect.ProtocolOpenAI, BaseURL: "http://127.0.0.1:1234/v1", Model: "fake", Local: true},
		Caps:    detect.Capabilities{ContextLength: 16384, Vision: vis, NativeTools: true},
		Setup:   vision.Setup{Vision: vis, Mode: backend.ModeNative, Profile: "full", ImageFormat: imaging.PNG, ImageMax: 200, Coords: detect.CoordsPixels},
		Backend: b,
	}
}

type assembly struct {
	t     *testing.T
	s     *Session
	agent *guestAgent
	model *scriptedModel
	log   *log
	view  *events
	parts Parts
}

func newAssembly(t *testing.T, mod ...func(*Parts)) *assembly {
	t.Helper()
	cfg := config.Defaults()
	a := &assembly{t: t, agent: newGuestAgent(400, 300), model: &scriptedModel{}, log: &log{}, view: &events{}}
	a.parts = Parts{Cfg: cfg, Profile: safety.ProfileFor(cfg), Model: testModel(a.model, true), Agent: a.agent, Guest: prompt.Size{W: 400, H: 300}, User: "cs",
		Capabilities: []string{protocol.CapScreenshot, protocol.CapInput, protocol.CapExec, protocol.CapA11y, protocol.CapFilePeek}, SlowBy: 1, Dir: t.TempDir(),
		Machine: NewMachine(nil), Viewer: a.view, Audit: a.log, Fencer: untrusted.WithNonce("3f9a1c")}
	for _, m := range mod {
		m(&a.parts)
	}
	var err error
	a.s, err = Assemble(a.parts)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []State{Handshake, Ready} {
		if err := a.parts.Machine.To(st, ""); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func (a *assembly) run(task string) Outcome {
	a.t.Helper()
	ctx, end := a.s.Console.Begin(context.Background())
	defer end()
	ch := make(chan Outcome, 1)
	go func() { ch <- a.s.Loop.Run(ctx, task) }()
	select {
	case o := <-ch:
		return o
	case <-time.After(10 * time.Second):
		a.t.Fatal("the task did not end")
		return Outcome{}
	}
}

func TestAWholeTaskThroughTheAssembledSession(t *testing.T) {
	a := newAssembly(t)
	a.model.replies = []reply{
		{text: "I will read the kernel version.", calls: []parse.ToolCall{tc("c1", "exec", `{"command":"uname -r","timeout_s":30}`)}},
		calls(tc("c2", "click_element", `{"id":"a1"}`)),
		calls(tc("c3", "done", `{"success":true,"summary":"the kernel is 6.1.0-cs"}`)),
	}
	a.agent.on(protocol.TypeA11yAction, func(_ any, out any) error {
		*out.(*protocol.A11yActionRes) = protocol.A11yActionRes{Method: "action:click"}
		return nil
	})
	out := a.run("Print the kernel version")
	if out.End != EndDone || !out.Success || out.Turns != 3 || a.parts.Machine.State() != Ready {
		t.Fatalf("outcome %+v, state %s", out, a.parts.Machine.State())
	}
	reqs := a.model.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	// The model is told the screen, the tree and the task, and the first result carries what the command printed, fenced.
	first := lastText(reqs[0])
	for _, want := range []string{"TASK: Print the kernel version", "network: disabled", "OK", "a1"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first observation lacks %q:\n%s", want, first)
		}
	}
	if tools := toolTexts(reqs[1]); len(tools) != 1 || !strings.Contains(tools[0], "<<untrusted:3f9a1c exec.stdout>>6.1.0-cs<<end:3f9a1c>>") {
		t.Errorf("the command's result: %q", tools)
	}
	// The prompt names this session's fence and the picture's coordinate space (400x300 cut to a longest side of 200).
	sys := reqs[0].Messages[0].Text
	if !strings.Contains(sys, "3f9a1c") || !strings.Contains(sys, "200") || !strings.Contains(sys, "150") {
		t.Errorf("the system prompt:\n%s", sys)
	}
	if len(a.s.System.TextSHA256) != 64 || a.s.System.TemplateSHA256 != prompt.TemplateSHA256() {
		t.Errorf("hashes %+v", a.s.System)
	}
	// The log tells it all: three answers, the command and the click with decision and result, and the end.
	var story []string
	for _, r := range a.log.all() {
		switch r.Kind {
		case "llm":
			story = append(story, "llm")
		case "action":
			story = append(story, r.Tool+":"+r.Event+":"+r.Verdict)
		}
	}
	want := "llm,exec:decision:allowed,exec:result:allowed,llm,click_element:decision:allowed,click_element:result:allowed,llm,done:result:allowed"
	if got := strings.Join(story, ","); got != want {
		t.Errorf("the log reads\n%s\nwant\n%s", got, want)
	}
}

func TestAssembleNeedsWhatItIsMadeOf(t *testing.T) {
	good := func() Parts {
		return Parts{Cfg: config.Defaults(), Profile: safety.ProfileFor(config.Defaults()), Model: testModel(&scriptedModel{}, true), Agent: newGuestAgent(10, 10),
			Guest: prompt.Size{W: 400, H: 300}, Machine: NewMachine(nil)}
	}
	for name, mod := range map[string]func(*Parts){
		"no machine": func(p *Parts) { p.Machine = nil },
		"no agent":   func(p *Parts) { p.Agent = nil },
		"no model":   func(p *Parts) { p.Model.Backend = nil },
		"no screen":  func(p *Parts) { p.Guest = prompt.Size{} },
		"bad rules":  func(p *Parts) { p.Rules = []byte("rules: [this is not a rule") },
		"bad tools":  func(p *Parts) { p.Model.Setup.Profile = "enormous" },
	} {
		p := good()
		mod(&p)
		if _, err := Assemble(p); err == nil {
			t.Errorf("%s: a session was made", name)
		}
	}
	if _, err := Assemble(good()); err != nil {
		t.Errorf("the good parts: %v", err)
	}
}

func TestWithoutATreeTheModelIsNotOfferedTheToolsThatNeedOne(t *testing.T) {
	a := newAssembly(t, func(p *Parts) {
		p.Capabilities = []string{protocol.CapScreenshot, protocol.CapInput, protocol.CapExec}
	})
	a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	a.run("t")
	req := a.model.requests()[0]
	for _, tool := range []string{"read_accessibility_tree", "set_text"} {
		if req.Set.Has(tool) {
			t.Errorf("the model is offered %s with no tree to read", tool)
		}
	}
	if !req.Set.Has("exec") || !req.Set.Has("mouse") {
		t.Error("the tools that need no tree are gone too")
	}
	if sys := req.Messages[0].Text; strings.Contains(sys, "numbered boxes") {
		t.Errorf("the prompt speaks of marks with no tree:\n%s", sys)
	}
}

func TestInTextOnlyModeTheCoordinatesAreTheScreensOwn(t *testing.T) {
	a := newAssembly(t, func(p *Parts) { p.Model = testModel(p.Model.Backend, false); p.Model.Setup.Profile = "compact" })
	a.agent.on(protocol.TypeOCR, func(_ any, out any) error {
		*out.(*protocol.OCRRes) = protocol.OCRRes{Generation: "t1", Lines: []protocol.OCRLine{{ID: "o1", Text: "Hello", Bounds: protocol.Rect{1, 1, 50, 10}}}}
		return nil
	})
	a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	if out := a.run("t"); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	req := a.model.requests()[0]
	if sys := req.Messages[0].Text; !strings.Contains(sys, "400") || !strings.Contains(sys, "300") {
		t.Errorf("the prompt:\n%s", sys)
	}
	if len(req.Messages[len(req.Messages)-1].Images) != 0 {
		t.Error("a model that cannot see was sent a picture")
	}
	if txt := lastText(req); !strings.Contains(txt, "Hello") {
		t.Errorf("the screen's text is not in the observation:\n%s", txt)
	}
}

func TestTheUsersRulesAreInForce(t *testing.T) {
	a := newAssembly(t, func(p *Parts) {
		p.Rules = []byte("version: 1\nrules:\n  - id: USER-NO-WGET\n    class: deny\n    explain: no downloads here\n    match:\n      - command: wget\n")
	})
	a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"wget http://example.org/x","timeout_s":30}`)), calls(tc("c2", "done", `{"success":false,"summary":"refused"}`))}
	a.run("t")
	tools := toolTexts(a.model.requests()[1])
	if len(tools) != 1 || !strings.Contains(tools[0], "policy_denied") || !strings.Contains(tools[0], "USER-NO-WGET") {
		t.Errorf("results %q", tools)
	}
}

func TestAScriptIsReadInTheGuestBeforeItRuns(t *testing.T) {
	a := newAssembly(t)
	a.agent.peeked = []byte("#!/bin/sh\nrm -rf /\n")
	a.agent.on(protocol.TypeFilePeek, func(_ any, out any) error {
		*out.(*protocol.FilePeekRes) = protocol.FilePeekRes{Exists: true, Size: 19}
		return nil
	})
	a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"sh ./cleanup.sh","timeout_s":30}`)), calls(tc("c2", "done", `{"success":false,"summary":"refused"}`))}
	a.run("t")
	tools := toolTexts(a.model.requests()[1])
	if len(tools) != 1 || !strings.Contains(tools[0], "policy_denied") {
		t.Errorf("a script that wipes the disk was not refused: %q", tools)
	}
	peeks := 0
	for _, c := range a.agent.calls {
		if c.typ == protocol.TypeFilePeek {
			peeks++
		}
		if c.typ == protocol.TypeExec {
			t.Error("the script's command reached the guest")
		}
	}
	if peeks == 0 {
		t.Error("the script was not read")
	}
}

func TestTheScriptReaderSaysWhyWhenItCannotRead(t *testing.T) {
	a := newAssembly(t)
	a.agent.on(protocol.TypeFilePeek, func(any, any) error { return remote(protocol.CodeInternal, "no such device") })
	r := agentScripts{link: a.s.Link}
	if _, _, _, err := r.Peek(context.Background(), "/tmp/x.sh", 100); err == nil {
		t.Error("an unreadable script was read")
	}
	a.agent.on(protocol.TypeFilePeek, func(req any, out any) error {
		if got := req.(protocol.FilePeekReq); got.MaxBytes != 100 || got.Path != "/tmp/x.sh" {
			t.Errorf("request %+v", got)
		}
		*out.(*protocol.FilePeekRes) = protocol.FilePeekRes{Exists: true, Size: 5}
		return nil
	})
	a.agent.peeked = []byte("hello")
	head, size, exists, err := r.Peek(context.Background(), "/tmp/x.sh", 100)
	if err != nil || string(head) != "hello" || size != 5 || !exists {
		t.Errorf("%q %d %v %v", head, size, exists, err)
	}
	// Never more than a script is worth.
	a.agent.on(protocol.TypeFilePeek, func(req any, out any) error {
		if got := req.(protocol.FilePeekReq); got.MaxBytes != peekBytes {
			t.Errorf("asked for %d bytes", got.MaxBytes)
		}
		*out.(*protocol.FilePeekRes) = protocol.FilePeekRes{}
		return nil
	})
	if _, _, _, err := r.Peek(context.Background(), "/tmp/x.sh", 10<<20); err != nil {
		t.Fatal(err)
	}
}

func TestTheModelsKeyNeverReachesTheLogOrTheViewer(t *testing.T) {
	const key = "sk-test-0123456789abcdefghij"
	a := newAssembly(t, func(p *Parts) { p.Model.Target.APIKey = key })
	a.model.replies = []reply{calls(tc("c1", "exec", `{"command":"echo `+key+`","timeout_s":30}`)), calls(tc("c2", "done", `{"success":true,"summary":"the key is `+key+`"}`))}
	a.run("t")
	for _, r := range a.log.all() {
		if strings.Contains(string(r.Args)+string(r.Result)+string(r.Data), key) {
			t.Errorf("the key is in the log: %+v", r)
		}
	}
	for _, tr := range a.view.traces() {
		if strings.Contains(string(tr.Args), key) {
			t.Errorf("the key is in the viewer: %s", tr.Args)
		}
	}
}

func TestReadsOfAGuestThatIsSlowAreGivenMoreTime(t *testing.T) {
	a := newAssembly(t, func(p *Parts) { p.SlowBy = 4 })
	a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	a.run("t")
	var budget int
	for _, c := range a.agent.calls {
		if c.typ == protocol.TypeA11yTree {
			budget = c.req.(protocol.A11yTreeReq).BudgetMS
		}
	}
	if budget != int((treeBudgetBase * 4).Milliseconds()) {
		t.Errorf("the tree's budget is %d ms", budget)
	}
}

func TestTheNetworkAndTheSessionsLimitsAreWhatTheConfigurationSays(t *testing.T) {
	a := newAssembly(t, func(p *Parts) {
		p.Cfg.Net.Allow = true
		p.Cfg.Net.AllowDomains = []string{"example.org"}
		p.Cfg.Safety.ActionsPerResponse = 2
		p.Profile = safety.ProfileFor(p.Cfg)
	})
	a.model.replies = []reply{calls(tc("c1", "done", `{"success":true,"summary":"ok"}`))}
	a.run("t")
	req := a.model.requests()[0]
	if txt := lastText(req); !strings.Contains(txt, "network: enabled") {
		t.Errorf("observation:\n%s", txt)
	}
	if sys := req.Messages[0].Text; !strings.Contains(sys, "example.org") {
		t.Errorf("the prompt does not name the sites:\n%s", sys)
	}
	if req.MaxActions != 2 || a.s.Loop.MaxActions != 2 {
		t.Errorf("the limit on actions is %d", req.MaxActions)
	}
}

func TestWithAMonitorTheSessionGoesOnWhenTheAgentGoes(t *testing.T) {
	m := &fakeMonitor{w: 400, h: 300, shade: 5}
	a := newAssembly(t, func(p *Parts) { p.Monitor = m })
	a.agent.setUp(false)
	a.model.replies = []reply{calls(tc("c1", "mouse", `{"op":"click","x":100,"y":75}`)), calls(tc("c2", "exec", `{"command":"ls","timeout_s":10}`)), calls(tc("c3", "done", `{"success":true,"summary":"ok"}`))}
	if out := a.run("t"); out.End != EndDone {
		t.Fatalf("outcome %+v", out)
	}
	if got := strings.Join(m.events(), "|"); !strings.Contains(got, "btn left down") {
		t.Errorf("the click did not go through the monitor: %s", got)
	}
	if tools := toolTexts(a.model.requests()[2]); len(tools) != 1 || !strings.Contains(tools[0], "agent_unavailable") {
		t.Errorf("the command: %q", tools)
	}
	if ags := a.view.agents(); len(ags) != 1 || ags[0].Online {
		t.Errorf("agent messages %+v", ags)
	}
	if first := lastText(a.model.requests()[0]); !strings.Contains(first, "accessibility tree") {
		t.Errorf("the model was not told the tree is gone:\n%s", first)
	}
}
