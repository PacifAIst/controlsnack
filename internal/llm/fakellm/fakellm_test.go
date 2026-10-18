// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm_test

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/openai"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/fakellm"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/safety/untrusted"
)

func client(t testing.TB, s *fakellm.Server, key string) backend.Backend {
	t.Helper()
	c := httpx.New("openai")
	c.Backoff = nil
	b, err := openai.New(openai.Config{BaseURL: s.URL(), Model: s.Model(), APIKey: key}, c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ask(t testing.TB, b backend.Backend, text string) (backend.Response, error) {
	t.Helper()
	set, err := actions.NewSet(actions.ProfileCompact)
	if err != nil {
		t.Fatal(err)
	}
	return b.Complete(context.Background(), backend.Request{
		Messages: []backend.Message{{Role: backend.RoleSystem, Text: "system words"}, {Role: backend.RoleUser, Text: text}},
		Set:      set, Mode: backend.ModeNative,
	})
}

func TestTheScriptIsAnsweredInOrderAndRecorded(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{
		fakellm.Say("hello"),
		fakellm.Act(fakellm.Tool("exec", map[string]any{"command": "uname -r"}), fakellm.Tool("key", `{"keys":"ctrl+l"}`)),
		fakellm.Fn(func(r *fakellm.Request) fakellm.Reply { return fakellm.Reply{Text: "you said: " + r.Observation()} }),
	}})
	b := client(t, s, "")

	r1, err := ask(t, b, "first")
	if err != nil || r1.Reply.Text != "hello" || len(r1.Reply.ToolCalls) != 0 || r1.Model != "fake-vlm" || r1.Usage.PromptTokens == 0 {
		t.Fatalf("first = %+v %v", r1, err)
	}
	r2, err := ask(t, b, "second")
	if err != nil || len(r2.Reply.ToolCalls) != 2 || r2.Reply.ToolCalls[0].Name != "exec" || r2.Reply.ToolCalls[1].Name != "key" || r2.Reply.ToolCalls[0].ID == "" || r2.Reply.ToolCalls[0].ID == r2.Reply.ToolCalls[1].ID {
		t.Fatalf("second = %+v %v", r2, err)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(object(r2.Reply.ToolCalls[0].Args)), &args); err != nil || args["command"] != "uname -r" {
		t.Errorf("args = %s %v", r2.Reply.ToolCalls[0].Args, err)
	}
	r3, err := ask(t, b, "third")
	if err != nil || r3.Reply.Text != "you said: third" {
		t.Fatalf("third = %+v %v", r3, err)
	}
	if s.Remaining() != 0 {
		t.Errorf("remaining = %d", s.Remaining())
	}

	reqs := s.Requests()
	if len(reqs) != 3 || reqs[0].System() != "system words" || reqs[0].Observation() != "first" || !reqs[0].HasTool("exec") || !reqs[0].HasTool("done") || reqs[0].HasTool("screenshot") {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[0].Pictures() != 0 || reqs[0].Temperature == nil || *reqs[0].Temperature != 0 || reqs[0].MaxTokens == 0 || reqs[0].Stream {
		t.Errorf("request = %+v", reqs[0])
	}
}

func TestTheStepsOfAScriptCanBeConditionalLateOrFail(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{
		fakellm.Say("ok").Expect(func(r *fakellm.Request) bool { return r.Observation() == "right" }),
		fakellm.Say("slow").After(150 * time.Millisecond),
		fakellm.Fail(http.StatusBadRequest, ""),
		fakellm.Fn(func(*fakellm.Request) fakellm.Reply { return fakellm.Reply{Text: "cut", Finish: "length"} }),
		fakellm.Fn(func(*fakellm.Request) fakellm.Reply { return fakellm.Reply{Garbage: "<html>not an API</html>"} }),
	}})
	b := client(t, s, "")
	if r, err := ask(t, b, "right"); err != nil || r.Reply.Text != "ok" {
		t.Fatalf("matching step: %+v %v", r, err)
	}
	begin := time.Now()
	if r, err := ask(t, b, "x"); err != nil || r.Reply.Text != "slow" || time.Since(begin) < 140*time.Millisecond {
		t.Fatalf("slow step: %+v %v after %v", r, err, time.Since(begin))
	}
	_, err := ask(t, b, "x")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Kind != httpx.KindBadRequest {
		t.Fatalf("failing step: %v", err)
	}
	if r, err := ask(t, b, "x"); err != nil || !r.Truncated {
		t.Fatalf("length step: %+v %v", r, err)
	}
	if _, err := ask(t, b, "x"); err == nil {
		t.Fatal("garbage step: expected an error")
	}
}

func TestARequestThatIsNotWhatTheScriptExpectedFailsTheTest(t *testing.T) {
	s, err := fakellm.Serve(fakellm.Options{Script: []fakellm.Step{fakellm.Say("ok").Expect(func(r *fakellm.Request) bool { return r.Observation() == "right" })}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = ask(t, client(t, s, ""), "wrong")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("err = %v", err)
	}
	errs := s.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0], "request 1 did not match what step 1 expected") || !strings.Contains(errs[0], `"wrong"`) {
		t.Errorf("errors = %q", errs)
	}
	s.ClearErrors()
	if len(s.Errors()) != 0 {
		t.Error("ClearErrors")
	}
}

func TestAfterTheScriptEnds(t *testing.T) {
	s, err := fakellm.Serve(fakellm.Options{Script: []fakellm.Step{fakellm.Say("one")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := client(t, s, "")
	if _, err := ask(t, b, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ask(t, b, "2"); err == nil {
		t.Fatal("a request after the script has no answer")
	}
	if errs := s.Errors(); len(errs) != 1 || !strings.Contains(errs[0], "after the script of 1 answers had ended") {
		t.Errorf("errors = %q", errs)
	}
	then := fakellm.Start(t, fakellm.Options{Then: func(r *fakellm.Request) fakellm.Reply { return fakellm.Reply{Text: "always: " + r.Observation()} }})
	for _, q := range []string{"a", "b"} {
		if r, err := ask(t, client(t, then, ""), q); err != nil || r.Reply.Text != "always: "+q {
			t.Errorf("Then: %+v %v", r, err)
		}
	}
}

func TestAHangingRequestEndsWithTheClientOrTheServer(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{fakellm.Hang(), fakellm.Hang()}})
	b := client(t, s, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	set, _ := actions.NewSet(actions.ProfileCompact)
	_, err := b.Complete(ctx, backend.Request{Messages: []backend.Message{{Role: backend.RoleUser, Text: "x"}}, Set: set, Mode: backend.ModeNative})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	// Closing the server frees a request that is waiting.
	done := make(chan error, 1)
	go func() {
		_, err := ask(t, b, "x")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	s.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the hanging request got an answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not free the hanging request")
	}
}

func TestTheKeyIsRequiredWhenAsked(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{APIKey: "sk-test", Script: []fakellm.Step{fakellm.Say("ok")}})
	_, err := ask(t, client(t, s, "wrong"), "x")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Kind != httpx.KindAuth {
		t.Errorf("a wrong key: %v", err)
	}
	if r, err := ask(t, client(t, s, "sk-test"), "x"); err != nil || r.Reply.Text != "ok" {
		t.Errorf("the right key: %+v %v", r, err)
	}
}

func TestAnotherModelIsNotFound(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Model: "alpha"})
	c := httpx.New("openai")
	c.Backoff = nil
	b, _ := openai.New(openai.Config{BaseURL: s.URL(), Model: "beta"}, c)
	_, err := ask(t, b, "x")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Kind != httpx.KindNotFound {
		t.Errorf("err = %v", err)
	}
}

func TestARequestForWhatTheProfileLacksIsRefusedLikeARealServerDoes(t *testing.T) {
	blind := fakellm.AllCapabilities()
	blind.Vision, blind.Native = false, false
	s, err := fakellm.Serve(fakellm.Options{Profile: &blind, Script: []fakellm.Step{fakellm.Say("never used")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := client(t, s, "")
	set, _ := actions.NewSet(actions.ProfileCompact)

	_, err = b.Complete(context.Background(), backend.Request{Messages: []backend.Message{{Role: backend.RoleUser, Text: "x"}}, Set: set, Mode: backend.ModeNative})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Kind != httpx.KindBadRequest || !strings.Contains(he.Error(), "does not support tools") {
		t.Errorf("tools: %v", err)
	}
	png, _ := imaging.Encode(testImage(), imaging.PNG, 0)
	_, err = b.Complete(context.Background(), backend.Request{Messages: []backend.Message{{Role: backend.RoleUser, Text: "x", Images: []backend.Image{{MIME: backend.MIMEPNG, Data: png}}}}, Mode: backend.ModeJSON})
	if !errors.As(err, &he) || he.Kind != httpx.KindBadRequest || !strings.Contains(he.Error(), "does not accept images") {
		t.Errorf("image: %v", err)
	}
	if len(s.Errors()) != 2 {
		t.Errorf("each refusal is a mistake of the test's session: %q", s.Errors())
	}
	if s.Remaining() != 1 || len(s.Requests()) != 0 {
		t.Errorf("a refused request does not use the script: remaining %d requests %d", s.Remaining(), len(s.Requests()))
	}
	s.ClearErrors()
}

func TestAJSONModeScriptIsReadByTheParser(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{
		fakellm.Actions("open the terminal", fakellm.Action{Tool: "key", Args: map[string]any{"keys": "ctrl+alt+t"}, Reason: "shortcut"}, fakellm.Action{Tool: "wait_for", Args: map[string]any{"condition": "screen_idle", "timeout_s": 5}}),
		fakellm.Act(fakellm.Tool("done", map[string]any{"success": true, "summary": "ok"})),
	}})
	set, _ := actions.NewSet(actions.ProfileCompact)
	b := client(t, s, "")
	resp, err := b.Complete(context.Background(), backend.Request{Messages: []backend.Message{{Role: backend.RoleUser, Text: "x"}}, Set: set, Mode: backend.ModeJSON})
	if err != nil {
		t.Fatal(err)
	}
	res := parse.Parse(set, resp.Reply, 3)
	if res.Source != parse.SourceJSON || len(res.Actions) != 2 || res.Actions[0].Action.Tool != "key" || res.Actions[0].Action.Reason != "shortcut" || res.Thought != "open the terminal" || len(res.Failures) != 0 {
		t.Fatalf("result = %+v", res)
	}
	resp, err = b.Complete(context.Background(), backend.Request{Messages: []backend.Message{{Role: backend.RoleUser, Text: "x"}}, Set: set, Mode: backend.ModeNative})
	if err != nil {
		t.Fatal(err)
	}
	res = parse.Parse(set, resp.Reply, 3)
	if res.Source != parse.SourceNative || len(res.Actions) != 1 || res.Actions[0].Action.Tool != "done" {
		t.Fatalf("native result = %+v", res)
	}
}

// --- the probe ---------------------------------------------------------------------------------------------------

func TestTheRealProbeFindsWhatTheProfileSays(t *testing.T) {
	for name, p := range map[string]fakellm.Profile{
		"everything":          fakellm.AllCapabilities(),
		"no webp":             {Vision: true, Native: true, Structured: true, JSONPrompt: true},
		"no vision":           {Native: true, Structured: true, JSONPrompt: true},
		"no tools":            {Vision: true, WebP: true, Structured: true, JSONPrompt: true},
		"no schema":           {Vision: true, WebP: true, Native: true, JSONPrompt: true},
		"json only":           {Vision: true, WebP: true, JSONPrompt: true},
		"nothing":             {},
		"vision, no json":     {Vision: true, WebP: true, Native: true, Structured: true},
		"points on the scale": {Vision: true, WebP: true, Native: true, Structured: true, JSONPrompt: true, Relative: true},
	} {
		for _, kind := range []string{"lmstudio", "llamacpp"} { // one whose list says what the model can do, one whose list does not
			t.Run(name+" through "+kind, func(t *testing.T) {
				s := fakellm.Start(t, fakellm.Options{Profile: &p, Script: []fakellm.Step{fakellm.Say("untouched")}})
				d := detect.New(detect.Options{Getenv: func(string) string { return "" }})
				target, err := d.ResolveBackend(context.Background(), cfg(kind, s.URL(), s.Model()))
				if err != nil {
					t.Fatal(err)
				}
				caps, err := d.Probe(context.Background(), target, true)
				if err != nil {
					t.Fatal(err)
				}
				if caps.Vision != p.Vision || caps.WebP != (p.Vision && p.WebP) || caps.NativeTools != p.Native || caps.Structured != p.Structured || caps.JSONPrompt != p.JSONPrompt || caps.Coords != wantCoords(p) {
					t.Errorf("profile %+v was found as %+v", p, caps)
				}
				// Pixels is also what a model is left at when its answer is on neither scale: that the check passed says it was found.
				for _, c := range caps.Checks {
					if c.Name == detect.CheckPointing && c.OK != p.Vision {
						t.Errorf("the pointing check of %+v: %+v", p, c)
					}
				}
				if s.Remaining() != 1 || len(s.Requests()) != 0 || s.Probes() == 0 || len(s.Errors()) != 0 {
					t.Errorf("the probe is not part of the script: remaining %d, requests %d, probes %d, errors %q", s.Remaining(), len(s.Requests()), s.Probes(), s.Errors())
				}
			})
		}
	}
}

func TestLMStudioDetectionSeesTheFakeAsAVisionModel(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Model: "qwen-fake-vl"})
	d := detect.New(detect.Options{Candidates: []detect.Candidate{{Backend: "lmstudio", BaseURL: s.URL(), Path: "/models"}}, Getenv: func(string) string { return "" }})
	target, models, err := d.Resolve(context.Background(), cfg("auto", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if target.Backend != "lmstudio" || target.Model != "qwen-fake-vl" || !target.Local || len(models) != 1 || !models[0].Vision || !models[0].Tools || !models[0].Loaded || models[0].ContextLength != 32768 {
		t.Errorf("target = %+v models = %+v", target, models)
	}

	blind := fakellm.AllCapabilities()
	blind.Vision, blind.Native = false, false
	s2 := fakellm.Start(t, fakellm.Options{Profile: &blind})
	d2 := detect.New(detect.Options{Getenv: func(string) string { return "" }})
	tg, err := d2.ResolveBackend(context.Background(), cfg("lmstudio", s2.URL(), ""))
	if err != nil {
		t.Fatal(err)
	}
	list, err := d2.Models(context.Background(), tg)
	if err != nil || len(list) != 1 || list[0].Vision || list[0].Tools {
		t.Errorf("a model without vision: %+v %v", list, err)
	}
}

// --- finding things in an observation -------------------------------------------------------------------------

func observation(t testing.TB) string {
	t.Helper()
	o := prompt.Observation{
		Turn: 3, Screen: prompt.Size{W: 1280, H: 800}, Network: "disabled", Mode: "text-only", Tools: "compact",
		UI: &prompt.UITree{Generation: "g2", Scope: "desktop", Nodes: []prompt.Node{
			{ID: "a1", Role: "frame", Name: "Terminal", Bounds: prompt.Rect{0, 27, 727, 291}},
			{ID: "a2", Role: "menu", Name: `The "File" menu`, Bounds: prompt.Rect{5, 56, 37, 27}},
			{ID: "a9", Role: "push button", Name: "日本語 OK", Bounds: prompt.Rect{600, 500, 80, 30}},
			{ID: "w3", Role: "link", Name: "docs", Bounds: prompt.Rect{10, 10, 20, 20}},
			{ID: "a12", Role: "text", States: []string{"focused", "editable"}, Bounds: prompt.Rect{6, 86, 638, 447}},
		}},
		OCR: &prompt.OCRBlock{Generation: "g1", Lines: []prompt.OCRLine{{ID: "o1", Text: "Applications", Bounds: prompt.Rect{21, 8, 73, 13}}, {ID: "o14", Text: "TEXT ONLY MARKER 4711", Bounds: prompt.Rect{6, 87, 209, 12}}}},
	}
	f, err := untrusted.New()
	if err != nil {
		t.Fatal(err)
	}
	return prompt.Renderer{Fencer: f}.Observation(o, prompt.Limits{})
}

func TestFindingElementsInAnObservation(t *testing.T) {
	text := observation(t)
	els := fakellm.Elements(text)
	if len(els) != 5 || els[0].ID != "a1" || els[1].Name != `The "File" menu` || els[3].ID != "w3" || els[4].ID != "a12" || els[4].Role != "text" || els[4].Name != "" || els[4].Bounds != [4]int{6, 86, 638, 447} {
		t.Fatalf("elements = %+v", els)
	}
	e, ok := fakellm.Find(text, "push_button", "ok")
	if !ok || e.ID != "a9" || e.Bounds != [4]int{600, 500, 80, 30} {
		t.Fatalf("Find = %+v %v", e, ok)
	}
	if x, y := e.Center(); x != 640 || y != 515 {
		t.Errorf("Center = %d,%d", x, y)
	}
	if e, ok := fakellm.Find(text, "", "file"); !ok || e.ID != "a2" {
		t.Errorf("any role, ignoring case: %+v %v", e, ok)
	}
	if _, ok := fakellm.Find(text, "push_button", "file"); ok {
		t.Error("the role must match")
	}
	if _, ok := fakellm.Find(text, "", "nonexistent"); ok {
		t.Error("no such element")
	}
	line, ok := fakellm.FindText(text, "marker 4711")
	if !ok || line.ID != "o14" || line.Bounds != [4]int{6, 87, 209, 12} || line.Role != "" {
		t.Errorf("FindText = %+v %v", line, ok)
	}
	if len(fakellm.Lines(text)) != 2 {
		t.Errorf("lines = %+v", fakellm.Lines(text))
	}
	if _, ok := fakellm.FindText(text, "absent"); ok {
		t.Error("no such text")
	}
	if len(fakellm.Elements("nothing here")) != 0 || len(fakellm.Lines("")) != 0 {
		t.Error("an observation with nothing in it")
	}
}

func TestAScriptCanClickWhatTheObservationShows(t *testing.T) {
	text := observation(t)
	s := fakellm.Start(t, fakellm.Options{Script: []fakellm.Step{fakellm.Fn(func(r *fakellm.Request) fakellm.Reply {
		e, ok := fakellm.Find(r.Observation(), "push_button", "OK")
		if !ok {
			return fakellm.Reply{Text: "nothing to click"}
		}
		return fakellm.Reply{Calls: []fakellm.Call{fakellm.Tool("click_element", map[string]any{"id": e.ID})}}
	})}})
	resp, err := ask(t, client(t, s, ""), text)
	if err != nil || len(resp.Reply.ToolCalls) != 1 || object(resp.Reply.ToolCalls[0].Args) != `{"id":"a9"}` {
		t.Fatalf("resp = %+v %v", resp, err)
	}
}

// --- the server itself ----------------------------------------------------------------------------------------

func TestTheServerIsOnLoopbackAndAnswersTheListsOfModels(t *testing.T) {
	s := fakellm.Start(t, fakellm.Options{Model: "m1"})
	host, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimSuffix(s.URL(), "/v1"), "http://"))
	if err != nil || host != "127.0.0.1" || !strings.HasSuffix(s.URL(), "/v1") {
		t.Fatalf("URL = %s", s.URL())
	}
	for _, path := range []string{"/v1/models", "/models", "/api/v0/models"} {
		resp := do(t, http.MethodGet, strings.TrimSuffix(s.URL(), "/v1")+path, "")
		if resp.StatusCode != 200 {
			t.Errorf("%s: %v", path, resp.Status)
		}
		resp.Body.Close()
	}
	resp := do(t, http.MethodGet, strings.TrimSuffix(s.URL(), "/v1")+"/nowhere", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown route: %v", resp.Status)
	}
	resp.Body.Close()
	resp = do(t, http.MethodPost, s.URL()+"/chat/completions", "not json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body: %v", resp.Status)
	}
	resp.Body.Close()
}

func TestRequestsAtTheSameTimeAreSafe(t *testing.T) {
	var script []fakellm.Step
	for range 40 {
		script = append(script, fakellm.Say("x"))
	}
	s := fakellm.Start(t, fakellm.Options{Script: script})
	b := client(t, s, "")
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ask(t, b, "q"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if s.Remaining() != 0 || len(s.Requests()) != 40 {
		t.Errorf("remaining %d requests %d", s.Remaining(), len(s.Requests()))
	}
}

func cfg(backendName, base, model string) config.Config {
	c := config.Defaults()
	c.LLM.Backend, c.LLM.BaseURL, c.LLM.Model = backendName, base, model
	return c
}

func testImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	return img
}

// object is the arguments of a call as the JSON object they are: OpenAI-compatible servers send them as a string that holds
// it, and the parser takes both.
func object(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
