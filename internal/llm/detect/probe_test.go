// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/imaging"
	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// probeWith probes one model of a fake server and returns what it found.
func probeWith(t *testing.T, protocol string, m fakeModel) (Capabilities, *fakeServer, error) {
	t.Helper()
	srv := newFake(t, protocol, m)
	caps, err := probeAt(t, srv, protocol, m.Name)
	return caps, srv, err
}

func probeAt(t *testing.T, srv *fakeServer, protocol, model string) (Capabilities, error) {
	t.Helper()
	d := New(Options{Getenv: func(string) string { return "key" }, CacheDir: t.TempDir()})
	backendName := map[string]string{"ollama": "ollama", "openai": "llamacpp", "anthropic": "anthropic"}[protocol]
	tg, err := d.ResolveBackend(context.Background(), cfgOf(backendName, srv.URL, model))
	if err != nil {
		t.Fatal(err)
	}
	return d.Probe(context.Background(), tg, true)
}

func checkOf(t *testing.T, c Capabilities, name string) Check {
	t.Helper()
	for _, ch := range c.Checks {
		if ch.Name == name {
			return ch
		}
	}
	t.Fatalf("no check %q in %+v", name, c.Checks)
	return Check{}
}

func TestProbeOfACapableOllamaModel(t *testing.T) {
	caps, srv, err := probeWith(t, "ollama", strongModel("qwen2.5vl:7b"))
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Vision || !caps.WebP || !caps.NativeTools || !caps.Structured || !caps.JSONPrompt {
		t.Fatalf("caps = %+v", caps)
	}
	if mode, ok := caps.Mode(); !ok || mode != backend.ModeNative || caps.ImageFormat() != imaging.WebP || !caps.Usable() {
		t.Fatalf("mode = %v %v format = %v", mode, ok, caps.ImageFormat())
	}
	if caps.Model != "qwen2.5vl:7b" || caps.Digest != "sha256:aaaa" || caps.ContextLength != 32768 || caps.Version != ProbeVersion || caps.ProbedAt.IsZero() || caps.Cached {
		t.Fatalf("caps = %+v", caps)
	}
	want := []seen{
		{ImageKind: "png"}, {ImageKind: "webp"}, {Tools: true}, {Format: "schema"}, {Format: "json"},
	}
	got := srv.seenChats()
	if len(got) != len(want) {
		t.Fatalf("%d questions asked: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].ImageKind != w.ImageKind || got[i].Tools != w.Tools || got[i].Format != w.Format && (w.Format != "" || got[i].ImageKind == "") {
			t.Errorf("question %d = %+v, want %+v", i+1, got[i], w)
		}
	}
	for _, name := range []string{CheckVision, CheckWebP, CheckNativeTools, CheckStructured, CheckJSON} {
		if ch := checkOf(t, caps, name); !ch.OK || ch.Skipped || ch.Detail == "" {
			t.Errorf("%s = %+v", name, ch)
		}
	}
}

func TestProbeWhenWebPIsNotSeenSendsJPEG(t *testing.T) {
	m := strongModel("m")
	m.Sees = map[string]bool{"png": true, "jpeg": true}
	caps, _, err := probeWith(t, "ollama", m)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Vision || caps.WebP || caps.ImageFormat() != imaging.JPEG {
		t.Fatalf("caps = %+v", caps)
	}
	if ch := checkOf(t, caps, CheckWebP); ch.OK || !strings.Contains(ch.Detail, "white") {
		t.Fatalf("webp check = %+v", ch)
	}
}

func TestProbeSkipsPicturesWhenTheBackendSaysThereIsNoVision(t *testing.T) {
	m := strongModel("text")
	m.Vision = false
	caps, srv, err := probeWith(t, "ollama", m)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Vision || caps.WebP || !caps.Usable() {
		t.Fatalf("caps = %+v", caps)
	}
	for _, s := range srv.seenChats() {
		if s.ImageKind != "" {
			t.Fatal("a picture was sent to a model the backend says cannot see")
		}
	}
	if ch := checkOf(t, caps, CheckVision); !ch.Skipped || ch.OK {
		t.Fatalf("vision check = %+v", ch)
	}
}

func TestProbeFindsOutWhenTheBackendDoesNotSay(t *testing.T) {
	// A plain OpenAI-compatible server lists names only: the probe finds out. This model does not see the picture.
	m := strongModel("blind")
	m.Sees = nil
	caps, srv, err := probeWith(t, "openai", m)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Vision || caps.WebP {
		t.Fatalf("caps = %+v", caps)
	}
	pics := 0
	for _, s := range srv.seenChats() {
		if s.ImageKind != "" {
			pics++
		}
	}
	if pics != 1 {
		t.Fatalf("%d pictures sent: after a miss on PNG, WebP is not tried", pics)
	}
	if ch := checkOf(t, caps, CheckWebP); !ch.Skipped {
		t.Fatalf("webp check = %+v", ch)
	}
}

func TestProbeTakesAServerThatRefusesPicturesAsNoVision(t *testing.T) {
	m := strongModel("m")
	m.RejectImages = true
	caps, _, err := probeWith(t, "openai", m)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Vision {
		t.Fatalf("caps = %+v", caps)
	}
	if ch := checkOf(t, caps, CheckVision); ch.OK || !strings.Contains(ch.Detail, "HTTP 400") {
		t.Fatalf("vision check = %+v", ch)
	}
}

func TestProbeFallsBackFromNativeToStructuredToJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*fakeModel)
		mode   backend.Mode
		usable bool
	}{
		{"no tools", func(m *fakeModel) { m.Native = false }, backend.ModeStructured, true},
		{"tools written as text", func(m *fakeModel) { m.Native, m.NativeAsText = false, true }, backend.ModeStructured, true},
		{"no tools, no schema", func(m *fakeModel) { m.Native, m.Structured = false, false }, backend.ModeJSON, true},
		{"nothing", func(m *fakeModel) { m.Native, m.Structured, m.JSONPrompt = false, false, false }, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := strongModel("m")
			tc.edit(&m)
			caps, _, err := probeWith(t, "openai", m)
			if err != nil {
				t.Fatal(err)
			}
			mode, ok := caps.Mode()
			if ok != tc.usable || mode != tc.mode || caps.Usable() != tc.usable {
				t.Fatalf("mode = %q %v, want %q %v (caps %+v)", mode, ok, tc.mode, tc.usable, caps)
			}
			if tc.name == "tools written as text" {
				if ch := checkOf(t, caps, CheckNativeTools); !strings.Contains(ch.Detail, "no tool call") {
					t.Fatalf("native check = %+v", ch)
				}
			}
		})
	}
}

func TestProbeOfAnthropicSkipsTheSchemaCheckAndSendsItsHeaders(t *testing.T) {
	srv := newFake(t, "anthropic", strongModel("claude-x"))
	srv.apiKey = "key"
	caps, err := probeAt(t, srv, "anthropic", "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Vision || !caps.NativeTools || caps.Structured || !caps.JSONPrompt {
		t.Fatalf("caps = %+v", caps)
	}
	if ch := checkOf(t, caps, CheckStructured); !ch.Skipped {
		t.Fatalf("structured check = %+v", ch)
	}
	for _, s := range srv.seenChats() {
		if s.Key != "key" || s.Version == "" || s.Auth != "" {
			t.Fatalf("headers of %+v", s)
		}
		if s.Format == "schema" {
			t.Fatal("a schema was sent to Anthropic")
		}
	}
}

func TestProbeStopsOnAnErrorThatSaysNothingOfTheModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*fakeModel)
		kind httpx.Kind
	}{
		{"wrong key", func(m *fakeModel) { m.ChatStatus, m.ChatBody = 401, `{"error":{"message":"invalid api key"}}` }, httpx.KindAuth},
		{"model missing", func(m *fakeModel) { m.ChatStatus, m.ChatBody = 404, `{"error":"model not found"}` }, httpx.KindNotFound},
		{"rate limit", func(m *fakeModel) { m.ChatStatus, m.ChatBody = 429, `{"error":"slow down"}` }, httpx.KindRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := strongModel("m")
			tc.edit(&m)
			caps, srv, err := probeWith(t, "openai", m)
			var he *httpx.Error
			if !errors.As(err, &he) || he.Kind != tc.kind {
				t.Fatalf("err = %v", err)
			}
			if len(caps.Checks) != 0 || caps.Vision {
				t.Fatalf("an infrastructure error says nothing about the model: %+v", caps)
			}
			if srv.chatCount() != 1 {
				t.Fatalf("%d questions asked after the first failed", srv.chatCount())
			}
		})
	}
}

func TestProbeOfAServerThatIsGone(t *testing.T) {
	srv := newFake(t, "ollama", strongModel("m"))
	d := New(Options{})
	tg, err := d.ResolveBackend(context.Background(), cfgOf("ollama", srv.URL, "m"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, err = d.Probe(context.Background(), tg, true)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Kind != httpx.KindUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeOfAFailingServerIsAModelThatCannotDoIt(t *testing.T) {
	m := strongModel("m")
	m.ChatStatus, m.ChatBody = 500, `{"error":"the model crashed"}`
	d := New(Options{Getenv: func(string) string { return "" }})
	srv := newFake(t, "openai", m)
	tg, err := d.ResolveBackend(context.Background(), cfgOf("llamacpp", srv.URL, "m"))
	if err != nil {
		t.Fatal(err)
	}
	caps, err := d.Probe(context.Background(), tg, true)
	if err != nil || caps.Usable() || caps.Vision {
		t.Fatalf("caps = %+v err = %v", caps, err)
	}
}

func TestProbeStopsWhenTheContextEnds(t *testing.T) {
	srv := newFake(t, "ollama", strongModel("m"))
	d := New(Options{})
	tg, _ := d.ResolveBackend(context.Background(), cfgOf("ollama", srv.URL, "m"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Probe(ctx, tg, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeNeedsAModel(t *testing.T) {
	if _, err := New(Options{}).Probe(context.Background(), Target{Backend: "ollama"}, true); err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheTestPictureIsAWhiteImageWithARedSquare(t *testing.T) {
	data, err := imaging.Encode(probeImage(), imaging.PNG, 0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != probeSide || b.Dy() != probeSide {
		t.Fatalf("bounds = %v", b)
	}
	cr, cg, cb, _ := img.At(probeSide/2, probeSide/2).RGBA()
	wr, wg, wb, _ := img.At(2, 2).RGBA()
	if cr>>8 < 200 || cg>>8 > 40 || cb>>8 > 40 {
		t.Errorf("the middle is not red: %d %d %d", cr>>8, cg>>8, cb>>8)
	}
	if wr>>8 != 255 || wg>>8 != 255 || wb>>8 != 255 {
		t.Errorf("the corner is not white: %d %d %d", wr>>8, wg>>8, wb>>8)
	}
	for _, f := range []imaging.Format{imaging.WebP, imaging.JPEG} {
		if d, err := imaging.Encode(probeImage(), f, 0); err != nil || len(d) == 0 || len(d) > 8<<10 {
			t.Errorf("%s: %d bytes, %v", f, len(d), err)
		}
	}
}

func TestCapabilitiesChooseTheBestWay(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Capabilities
		mode backend.Mode
		ok   bool
		fmt  imaging.Format
	}{
		{"nothing", Capabilities{}, "", false, imaging.JPEG},
		{"json only", Capabilities{JSONPrompt: true}, backend.ModeJSON, true, imaging.JPEG},
		{"structured beats json", Capabilities{JSONPrompt: true, Structured: true}, backend.ModeStructured, true, imaging.JPEG},
		{"native beats all", Capabilities{JSONPrompt: true, Structured: true, NativeTools: true, WebP: true}, backend.ModeNative, true, imaging.WebP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, ok := tc.c.Mode()
			if mode != tc.mode || ok != tc.ok || tc.c.Usable() != tc.ok || tc.c.ImageFormat() != tc.fmt {
				t.Fatalf("mode = %q %v format = %v", mode, ok, tc.c.ImageFormat())
			}
		})
	}
}

func TestDoneInJudgesWhatTheModelDid(t *testing.T) {
	set, err := actions.NewSet(actions.ProfileCompact)
	if err != nil {
		t.Fatal(err)
	}
	p := &prober{set: set}
	call := func(name, args string) backend.Response {
		return backend.Response{Reply: parse.Reply{ToolCalls: []parse.ToolCall{{ID: "1", Name: name, Args: json.RawMessage(args)}}}}
	}
	for _, tc := range []struct {
		name string
		resp backend.Response
		want string
		ok   bool
	}{
		{"as asked", call("done", `{"success":true,"summary":"ok"}`), "called done as asked (native)", true},
		{"wrong tool", call("key", `{"keys":"a"}`), "called key, not done", false},
		{"not successful", call("done", `{"success":false,"summary":"no"}`), "success false", false},
		{"bad arguments", call("done", `{"success":"yes"}`), "", false},
		{"words only", backend.Response{Reply: parse.Reply{Text: "Sure thing!"}}, "no action in the answer", false},
		{"json in words", backend.Response{Reply: parse.Reply{Text: doneJSON}}, "called done as asked (json)", true},
		{"reasoning is not an answer", backend.Response{Reply: parse.Reply{Text: "<think>" + doneJSON + "</think>Done."}}, "no action", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, ok := p.doneIn(tc.resp)
			if ok != tc.ok || !strings.Contains(detail, tc.want) || detail == "" {
				t.Fatalf("detail = %q ok = %v", detail, ok)
			}
		})
	}
}

func TestProbeRecordsHowLongEachQuestionTook(t *testing.T) {
	caps, _, err := probeWith(t, "ollama", strongModel("m"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range caps.Checks {
		if ch.Latency < 0 || ch.Latency > time.Minute {
			t.Errorf("%s took %v", ch.Name, ch.Latency)
		}
	}
}

func TestAnErrorLateInTheProbeEndsItToo(t *testing.T) {
	for name, kind := range map[string]func(seen) bool{
		"native":     func(s seen) bool { return s.Tools },
		"structured": func(s seen) bool { return s.Format == "schema" },
		"json":       func(s seen) bool { return s.ImageKind == "" && !s.Tools && s.Format == "" },
	} {
		t.Run(name, func(t *testing.T) {
			m := strongModel("m")
			m.FailWhen = func(s seen) int {
				if kind(s) {
					return 401
				}
				return 0
			}
			caps, srv, err := probeWith(t, "openai", m)
			var he *httpx.Error
			if !errors.As(err, &he) || he.Kind != httpx.KindAuth {
				t.Fatalf("err = %v", err)
			}
			if caps.Version != 0 || srv.chatCount() < 3 {
				t.Fatalf("caps = %+v after %d questions", caps, srv.chatCount())
			}
		})
	}
}
