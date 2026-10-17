// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func modelsOf(t *testing.T, d *Detector, backendName, base string) []ModelInfo {
	t.Helper()
	tg, err := d.ResolveBackend(context.Background(), cfgOf(backendName, base, "x"))
	if err != nil {
		t.Fatal(err)
	}
	models, err := d.Models(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func TestOllamaModelsCarryWhatShowSays(t *testing.T) {
	v := strongModel("qwen2.5vl:7b")
	v.Digest, v.ContextLen = "sha256:1111", 128000
	text := strongModel("llama3.2:3b")
	text.Vision, text.ContextLen = false, 8192
	quiet := strongModel("old-server-model")
	quiet.Reported = false // /api/show says nothing about capabilities
	srv := newFake(t, "ollama", v, text, quiet)

	got := modelsOf(t, newDetector(), "ollama", srv.URL)
	if len(got) != 3 {
		t.Fatalf("models = %+v", got)
	}
	if g := got[0]; g.Name != "qwen2.5vl:7b" || g.Digest != "sha256:1111" || !g.Vision || !g.VisionKnown || !g.Tools || !g.ToolsKnown || g.ContextLength != 128000 {
		t.Errorf("vision model = %+v", g)
	}
	if g := got[1]; g.Vision || !g.VisionKnown || !g.Tools || g.ContextLength != 8192 {
		t.Errorf("text model = %+v", g)
	}
	if g := got[2]; g.Vision || g.VisionKnown || g.ToolsKnown {
		t.Errorf("a server that does not say leaves it unknown: %+v", g)
	}
}

func TestOllamaModelsAreAskedAboutOnlyUpToTheLimit(t *testing.T) {
	var ms []fakeModel
	for i := range maxModels + 5 {
		m := strongModel("m" + string(rune('A'+i/26)) + string(rune('a'+i%26)))
		ms = append(ms, m)
	}
	srv := newFake(t, "ollama", ms...)
	got := modelsOf(t, newDetector(), "ollama", srv.URL)
	if len(got) != maxModels+5 {
		t.Fatalf("all are listed: %d", len(got))
	}
	for i, g := range got {
		if (i < maxModels) != g.VisionKnown {
			t.Fatalf("model %d: VisionKnown = %v", i, g.VisionKnown)
		}
	}
}

func TestLMStudioModelsUseItsOwnList(t *testing.T) {
	a := strongModel("qwen2.5-vl-7b-instruct")
	a.Loaded = true
	b := strongModel("llama-3.2-3b")
	b.Vision, b.Tools = false, false
	emb := strongModel("nomic-embed")
	emb.Embeddings = true
	srv := newFake(t, "openai", a, b, emb)

	got := modelsOf(t, newDetector(), "lmstudio", srv.URL+"/v1")
	if len(got) != 2 {
		t.Fatalf("embedding models are not listed: %+v", got)
	}
	if g := got[0]; !g.Vision || !g.VisionKnown || !g.Loaded || !g.Tools || !g.ToolsKnown || g.ContextLength != 32768 {
		t.Errorf("vlm = %+v", g)
	}
	if g := got[1]; g.Vision || !g.VisionKnown || g.Loaded || g.Tools || !g.ToolsKnown {
		t.Errorf("llm = %+v", g)
	}
}

func TestLMStudioFallsBackToThePlainList(t *testing.T) {
	srv := newFake(t, "openai", strongModel("some-model"))
	srv.noV0 = true
	got := modelsOf(t, newDetector(), "lmstudio", srv.URL+"/v1")
	if len(got) != 1 || got[0].Name != "some-model" || got[0].Vision || got[0].VisionKnown {
		t.Fatalf("models = %+v", got)
	}
}

func TestPlainOpenAIListGivesNamesOnlyAndSendsTheKey(t *testing.T) {
	srv := newFake(t, "openai", strongModel("gpt-x"), strongModel("gpt-y"))
	srv.apiKey = "sk-secret"
	d := New(Options{Getenv: func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-secret"
		}
		return ""
	}})
	got := modelsOf(t, d, "openai", srv.URL)
	if len(got) != 2 || got[0].Name != "gpt-x" || got[0].VisionKnown {
		t.Fatalf("models = %+v", got)
	}
}

func TestLlamaCppUsesThePlainList(t *testing.T) {
	srv := newFake(t, "openai", strongModel("gguf-model"))
	got := modelsOf(t, newDetector(), "llamacpp", srv.URL)
	if len(got) != 1 || got[0].Name != "gguf-model" || got[0].VisionKnown {
		t.Fatalf("models = %+v", got)
	}
	if srv.lists != 1 {
		t.Fatalf("the list was asked for %d times", srv.lists)
	}
}

func TestAnthropicModelsAllSeeAndCallTools(t *testing.T) {
	srv := newFake(t, "anthropic", strongModel("claude-x"), strongModel("claude-y"))
	srv.apiKey = "sk-ant-secret"
	d := New(Options{Getenv: func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-secret"
		}
		return ""
	}})
	got := modelsOf(t, d, "anthropic", srv.URL)
	if len(got) != 2 || !got[0].Vision || !got[0].VisionKnown || !got[0].Tools || !got[1].ToolsKnown {
		t.Fatalf("models = %+v", got)
	}
}

func TestModelsReportsAWrongKeyAsAuth(t *testing.T) {
	srv := newFake(t, "openai", strongModel("gpt-x"))
	srv.apiKey = "right"
	d := New(Options{Getenv: func(string) string { return "wrong" }})
	tg, err := d.ResolveBackend(context.Background(), cfgOf("openai", srv.URL, "x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Models(context.Background(), tg)
	if err == nil || !strings.Contains(err.Error(), "listing the models of openai") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelsRefusesAnswersThatAreNotJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not a model server</html>"))
	}))
	t.Cleanup(srv.Close)
	d := newDetector()
	for _, b := range []string{"ollama", "openai", "anthropic"} {
		tg := Target{Backend: b, Protocol: protocolOf(b), BaseURL: srv.URL}
		if _, err := d.Models(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "not JSON") {
			t.Errorf("%s: err = %v", b, err)
		}
	}
	tg := Target{Backend: "lmstudio", Protocol: ProtocolOpenAI, BaseURL: srv.URL + "/v1"}
	if _, err := d.Models(context.Background(), tg); err == nil {
		t.Error("lmstudio: expected an error")
	}
}

func TestModelNamesAreMadeFitToPrint(t *testing.T) {
	m := strongModel("evil\x1b[31m\nname")
	srv := newFake(t, "openai", m)
	srv.noV0 = true
	got := modelsOf(t, newDetector(), "lmstudio", srv.URL+"/v1")
	if len(got) != 1 || strings.ContainsAny(got[0].Name, "\x1b\n") {
		t.Fatalf("name = %q", got[0].Name)
	}
}

func TestPickVision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		models []ModelInfo
		want   string
	}{
		{"none", nil, ""},
		{"no vision", []ModelInfo{{Name: "a"}, {Name: "b", Loaded: true}}, ""},
		{"first that sees", []ModelInfo{{Name: "a"}, {Name: "b", Vision: true}, {Name: "c", Vision: true}}, "b"},
		{"loaded wins", []ModelInfo{{Name: "b", Vision: true}, {Name: "c", Vision: true, Loaded: true}}, "c"},
		{"loaded without vision does not win", []ModelInfo{{Name: "b", Vision: true}, {Name: "c", Loaded: true}}, "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := pickVision(tc.models)
			if ok != (tc.want != "") || m.Name != tc.want {
				t.Fatalf("pickVision = %v %v", m.Name, ok)
			}
		})
	}
}

// llamacppList is what a real llama.cpp server answered to GET /v1/models, with a model loaded without a projector.
func llamacppList(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "llm", "openai", "llamacpp-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func listServer(t *testing.T, body []byte) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLlamaCppSaysTheContextItWasStartedWithAndWhetherItSees(t *testing.T) {
	d := newDetector()
	got := modelsOf(t, d, "llamacpp", listServer(t, llamacppList(t)))
	if len(got) != 1 || got[0].Name != "gemma-4-e4b" || got[0].ContextLength != 8192 || !got[0].VisionKnown || got[0].Vision || got[0].ToolsKnown {
		t.Fatalf("models = %+v", got)
	}
	withProjector := []byte(strings.Replace(string(llamacppList(t)), `"capabilities":["completion"]`, `"capabilities":["completion","multimodal"]`, 1))
	got = modelsOf(t, d, "llamacpp", listServer(t, withProjector))
	if len(got) != 1 || !got[0].Vision || !got[0].VisionKnown {
		t.Fatalf("with a projector: %+v", got)
	}
	// OpenAI's own list says nothing of either.
	plain := []byte(`{"object":"list","data":[{"id":"gpt-x","object":"model"},{"id":"","object":"model"}]}`)
	got = modelsOf(t, d, "llamacpp", listServer(t, plain))
	if len(got) != 1 || got[0].ContextLength != 0 || got[0].VisionKnown {
		t.Fatalf("plain: %+v", got)
	}
}
