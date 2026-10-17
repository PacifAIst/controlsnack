// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden reports in testdata/llm/detect")

func serviceFor(cands ...Candidate) Service {
	return NewServiceFor(New(Options{Candidates: cands, Getenv: func(string) string { return "" }, DetectTimeout: 2 * time.Second}))
}

func TestServiceProbeReportsWhatTheModelCanDo(t *testing.T) {
	srv := newFake(t, "ollama", strongModel("qwen2.5vl:7b"))
	var out bytes.Buffer
	err := serviceFor(Candidate{Backend: "ollama", BaseURL: srv.URL, Path: "/api/tags"}).Probe(context.Background(), &out, cfgOf("auto", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Backend  ollama at " + srv.URL + " (this machine)", "Model    qwen2.5vl:7b",
		"vision", "webp", "native_tools", "structured", "json", "ok",
		"Context window   32768 tokens", "Screenshots      webp", "Actions          native tool calls",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "FAILED") || strings.Contains(text, "NOT this machine") {
		t.Errorf("report:\n%s", text)
	}
}

func TestServiceProbeSaysWhenThereIsNoVision(t *testing.T) {
	m := strongModel("llama3.2:3b")
	m.Vision = false
	srv := newFake(t, "ollama", m)
	var out bytes.Buffer
	err := serviceFor().Probe(context.Background(), &out, cfgOf("ollama", srv.URL, "llama3.2:3b"))
	if err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "text-only mode") || !strings.Contains(text, "skipped") {
		t.Errorf("report:\n%s", text)
	}
}

func TestServiceProbeFailsForAModelThatCannotDriveASession(t *testing.T) {
	m := strongModel("useless")
	m.Native, m.Structured, m.JSONPrompt = false, false, false
	srv := newFake(t, "openai", m)
	var out bytes.Buffer
	err := serviceFor().Probe(context.Background(), &out, cfgOf("llamacpp", srv.URL, "useless"))
	if err == nil || !strings.Contains(err.Error(), "use another model") {
		t.Fatalf("err = %v", err)
	}
	if text := out.String(); !strings.Contains(text, "FAILED") || !strings.Contains(text, "none of the ways worked") {
		t.Errorf("the report is still printed:\n%s", text)
	}
}

func TestServiceProbeChoosesAVisionModelWhenNoneIsNamed(t *testing.T) {
	text, vis := strongModel("text-only"), strongModel("sees")
	text.Vision = false
	srv := newFake(t, "ollama", text, vis)
	var out bytes.Buffer
	if err := serviceFor().Probe(context.Background(), &out, cfgOf("ollama", srv.URL, "")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Model    sees") {
		t.Errorf("report:\n%s", out.String())
	}
	if got := srv.seenChats(); len(got) == 0 || got[0].ImageKind != "png" {
		t.Fatalf("questions asked: %+v", got)
	}
}

func TestServiceProbeWithoutAnyVisionModelListsThem(t *testing.T) {
	a := strongModel("a")
	a.Vision = false
	srv := newFake(t, "ollama", a)
	err := serviceFor().Probe(context.Background(), &bytes.Buffer{}, cfgOf("ollama", srv.URL, ""))
	if err == nil || !strings.Contains(err.Error(), "found: a") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceProbeAlwaysAsksAgain(t *testing.T) {
	srv := newFake(t, "ollama", strongModel("m"))
	svc := NewServiceFor(New(Options{CacheDir: t.TempDir(), Getenv: func(string) string { return "" }}))
	cfg := cfgOf("ollama", srv.URL, "m")
	if err := svc.Probe(context.Background(), &bytes.Buffer{}, cfg); err != nil {
		t.Fatal(err)
	}
	asked := srv.chatCount()
	var out bytes.Buffer
	if err := svc.Probe(context.Background(), &out, cfg); err != nil {
		t.Fatal(err)
	}
	if srv.chatCount() != 2*asked || strings.Contains(out.String(), "earlier probe") {
		t.Fatalf("`llm probe` must test again: %d questions, report:\n%s", srv.chatCount(), out.String())
	}
}

func TestServiceReturnsTheErrorOfAServerThatIsNotThere(t *testing.T) {
	svc := serviceFor()
	for _, run := range []func() error{
		func() error {
			return svc.Probe(context.Background(), &bytes.Buffer{}, cfgOf("ollama", deadServer(), "m"))
		},
		func() error {
			return svc.Models(context.Background(), &bytes.Buffer{}, cfgOf("ollama", deadServer(), ""))
		},
		func() error { return svc.Models(context.Background(), &bytes.Buffer{}, cfgOf("gemini", "", "")) },
	} {
		if err := run(); err == nil {
			t.Fatal("expected an error")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := newFake(t, "ollama", strongModel("m"))
	if err := svc.Probe(ctx, &bytes.Buffer{}, cfgOf("ollama", srv.URL, "m")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceModelsListsWhatTheBackendOffers(t *testing.T) {
	a, b := strongModel("qwen2.5vl:7b"), strongModel("llama3.2:3b")
	b.Vision, b.Tools, b.ContextLen = false, false, 0
	unknown := strongModel("mystery")
	unknown.Reported = false
	srv := newFake(t, "ollama", a, b, unknown)
	var out bytes.Buffer
	if err := serviceFor().Models(context.Background(), &out, cfgOf("ollama", srv.URL, "")); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"MODEL", "VISION", "TOOLS", "CONTEXT", "LOADED", "qwen2.5vl:7b", "llama3.2:3b", "mystery", "32768", "Without --model, a session would use qwen2.5vl:7b."} {
		if !strings.Contains(text, want) {
			t.Errorf("list lacks %q:\n%s", want, text)
		}
	}
	line := func(name string) string {
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, name) {
				return strings.Join(strings.Fields(l), " ")
			}
		}
		return ""
	}
	if got := line("qwen2.5vl:7b"); got != "qwen2.5vl:7b yes yes 32768" {
		t.Errorf("row = %q", got)
	}
	if got := line("llama3.2:3b"); got != "llama3.2:3b no no ?" {
		t.Errorf("row = %q", got)
	}
	if got := line("mystery"); got != "mystery ? ? 32768" {
		t.Errorf("row = %q", got)
	}
}

func TestServiceModelsWhenNoneSeesOrNoneExists(t *testing.T) {
	a := strongModel("a")
	a.Vision = false
	var out bytes.Buffer
	srv := newFake(t, "ollama", a)
	if err := serviceFor().Models(context.Background(), &out, cfgOf("ollama", srv.URL, "")); err != nil || !strings.Contains(out.String(), "No model reports vision") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	out.Reset()
	empty := newFake(t, "ollama")
	if err := serviceFor().Models(context.Background(), &out, cfgOf("ollama", empty.URL, "")); err != nil || !strings.Contains(out.String(), "offers no models") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	out.Reset()
	if err := serviceFor().Models(context.Background(), &out, cfgOf("ollama", srv.URL, "a")); err != nil || strings.Contains(out.String(), "Without --model") {
		t.Fatalf("a named model needs no suggestion: %v\n%s", err, out.String())
	}
}

func TestHeaderOfARemoteBackendSaysSoAndNeverShowsTheKey(t *testing.T) {
	var out bytes.Buffer
	header(&out, Target{Backend: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-should-not-appear"})
	text := out.String()
	if !strings.Contains(text, "NOT this machine") || !strings.Contains(text, "api.openai.com") || !strings.Contains(text, "not on this machine") || strings.Contains(text, "sk-") {
		t.Fatalf("header:\n%s", text)
	}
}

// The report is what a person reads to decide: it is held to a golden file.
func TestReportsAreAsWritten(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for name, c := range map[string]Capabilities{
		"capable": {
			Vision: true, WebP: true, NativeTools: true, Structured: true, JSONPrompt: true, ContextLength: 32768, Coords: CoordsRelative, ProbedAt: at,
			Checks: []Check{
				{Name: CheckVision, OK: true, Detail: `answered {"color":"red"}`, Latency: 2300 * time.Millisecond},
				{Name: CheckWebP, OK: true, Detail: `answered {"color":"red"}`, Latency: 900 * time.Millisecond},
				{Name: CheckNativeTools, OK: true, Detail: "called done as asked (native)", Latency: 1100 * time.Millisecond},
				{Name: CheckStructured, OK: true, Detail: "called done as asked (json)", Latency: 1400 * time.Millisecond},
				{Name: CheckJSON, OK: true, Detail: "called done as asked (json)", Latency: 20 * time.Millisecond},
			},
		},
		"text-only": {
			NativeTools: true, JSONPrompt: true, ProbedAt: at,
			Checks: []Check{
				{Name: CheckVision, Skipped: true, Detail: "the backend says the model does not take images"},
				{Name: CheckWebP, Skipped: true, Detail: "no vision"},
				{Name: CheckNativeTools, OK: true, Detail: "called done as asked (native)", Latency: 800 * time.Millisecond},
				{Name: CheckStructured, Detail: "no action in the answer: I would rather explain it in words.", Latency: 700 * time.Millisecond},
				{Name: CheckJSON, OK: true, Detail: "called done as asked (json)", Latency: 600 * time.Millisecond},
			},
		},
		"jpeg-cached": {
			Vision: true, Structured: true, Cached: true, ProbedAt: at,
			Checks: []Check{
				{Name: CheckVision, OK: true, Detail: `answered {"color":"red"}`, Latency: time.Second},
				{Name: CheckWebP, Detail: `answered {"color":"white"}`, Latency: time.Second},
				{Name: CheckNativeTools, Detail: "ollama: bad request (HTTP 400): x does not support tools", Latency: 50 * time.Millisecond},
				{Name: CheckStructured, OK: true, Detail: "called done as asked (json)", Latency: 3 * time.Second},
				{Name: CheckJSON, Skipped: true, Detail: "not asked"},
			},
		},
		"json-only": {
			Vision: true, WebP: true, JSONPrompt: true, ContextLength: 4096, Coords: CoordsPixels, ProbedAt: at,
			Checks: []Check{
				{Name: CheckVision, OK: true, Detail: `answered {"color":"red"}`, Latency: 4 * time.Second},
				{Name: CheckWebP, OK: true, Detail: `answered {"color":"red"}`, Latency: 4 * time.Second},
				{Name: CheckNativeTools, Detail: "ollama: bad request (HTTP 400): x does not support tools", Latency: 100 * time.Millisecond},
				{Name: CheckStructured, Skipped: true, Detail: "the API has no way to hold a model to a schema"},
				{Name: CheckJSON, OK: true, Detail: "called done as asked (json)", Latency: 3 * time.Second},
			},
		},
		"nothing": {
			Checks: []Check{{Name: CheckNativeTools, Detail: "no tool call in the answer: Sure!"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			writeReport(&out, c)
			path := filepath.Join("..", "..", "..", "testdata", "llm", "detect", "report-"+name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), out.Bytes()) {
				t.Errorf("report differs from %s (run `go test ./internal/llm/detect -update` if the change is meant):\n%s", path, out.String())
			}
		})
	}
}
