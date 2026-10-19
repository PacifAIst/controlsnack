// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
)

// newDetector makes a Detector that looks only where the test says.
func newDetector(cands ...Candidate) *Detector {
	return New(Options{Candidates: cands, Getenv: func(string) string { return "" }, DetectTimeout: 2 * time.Second})
}

func cfgOf(backendName, baseURL, model string) config.Config {
	c := config.Defaults()
	c.LLM.Backend, c.LLM.BaseURL, c.LLM.Model = backendName, baseURL, model
	return c
}

// deadServer is the address of a server that is not there any more.
func deadServer() string {
	s := httptest.NewServer(nil)
	u := s.URL
	s.Close()
	return u
}

func vision(name string) fakeModel {
	m := strongModel(name)
	return m
}

func blind(name string) fakeModel {
	m := strongModel(name)
	m.Vision = false
	return m
}

func TestAutoFindsOllamaFirst(t *testing.T) {
	oll := newFake(t, "ollama", vision("qwen2.5vl:7b"))
	lms := newFake(t, "openai", vision("other"))
	d := newDetector(
		Candidate{Backend: "ollama", BaseURL: oll.URL, Path: "/api/tags"},
		Candidate{Backend: "lmstudio", BaseURL: lms.URL + "/v1", Path: "/models"},
	)
	tg, models, err := d.Resolve(context.Background(), cfgOf("auto", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if tg.Backend != "ollama" || tg.Protocol != ProtocolOllama || tg.BaseURL != oll.URL || tg.Model != "qwen2.5vl:7b" || !tg.Local {
		t.Fatalf("target = %+v", tg)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	if lms.lists != 0 {
		t.Fatal("LM Studio was asked although Ollama answered")
	}
}

func TestAutoFallsBackToLMStudio(t *testing.T) {
	m := vision("qwen2.5-vl-7b-instruct")
	m.Loaded = true
	lms := newFake(t, "openai", m)
	d := newDetector(
		Candidate{Backend: "ollama", BaseURL: deadServer(), Path: "/api/tags"},
		Candidate{Backend: "lmstudio", BaseURL: lms.URL + "/v1", Path: "/models"},
	)
	tg, _, err := d.Resolve(context.Background(), cfgOf("", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if tg.Backend != "lmstudio" || tg.Protocol != ProtocolOpenAI || tg.Model != "qwen2.5-vl-7b-instruct" || tg.BaseURL != lms.URL+"/v1" {
		t.Fatalf("target = %+v", tg)
	}
}

func TestAutoSaysWhatItTriedWhenNothingAnswers(t *testing.T) {
	d := newDetector(
		Candidate{Backend: "ollama", BaseURL: deadServer(), Path: "/api/tags"},
		Candidate{Backend: "lmstudio", BaseURL: deadServer() + "/v1", Path: "/models"},
	)
	_, _, err := d.Resolve(context.Background(), cfgOf("auto", "", ""))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"no local model server answered", "ollama at 127.0.0.1", "lmstudio at 127.0.0.1", "--llm"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestAutoNeverTriesAddressesOffThisMachine(t *testing.T) {
	// A candidate that is not on this machine is skipped without a request: a model somewhere else is never picked for
	// the user. 192.0.2.1 is reserved for documentation; a request to it would hang until the timeout.
	d := newDetector(Candidate{Backend: "ollama", BaseURL: "http://192.0.2.1:11434", Path: "/api/tags"})
	start := time.Now()
	_, _, err := d.Resolve(context.Background(), cfgOf("auto", "", ""))
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestAutoStopsWhenTheContextEnds(t *testing.T) {
	d := newDetector(Candidate{Backend: "ollama", BaseURL: deadServer(), Path: "/api/tags"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.Resolve(ctx, cfgOf("auto", "", "")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestExplicitBackendsAndTheirAddresses(t *testing.T) {
	d := New(Options{Getenv: func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-test-key"
		}
		return ""
	}})
	for _, tc := range []struct {
		name, backend, base string
		proto               Protocol
		wantBase            string
		local, key          bool
	}{
		{"ollama default", "ollama", "", ProtocolOllama, "http://127.0.0.1:11434", true, false},
		{"lmstudio default", "lmstudio", "", ProtocolOpenAI, "http://127.0.0.1:1234/v1", true, false},
		{"llamacpp default", "llamacpp", "", ProtocolOpenAI, "http://127.0.0.1:8080/v1", true, false},
		{"anthropic default", "anthropic", "", ProtocolAnthropic, "https://api.anthropic.com", false, false},
		{"openai default", "openai", "", ProtocolOpenAI, "https://api.openai.com/v1", false, true},
		{"trailing slash", "openai", "https://llm.example.com/v1/", ProtocolOpenAI, "https://llm.example.com/v1", false, true},
		{"localhost name", "ollama", "http://localhost:9999", ProtocolOllama, "http://localhost:9999", true, false},
		{"lan address is remote", "ollama", "https://192.168.1.20:11434", ProtocolOllama, "https://192.168.1.20:11434", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg, err := d.ResolveBackend(context.Background(), cfgOf(tc.backend, tc.base, "m"))
			if err != nil {
				t.Fatal(err)
			}
			if tg.Protocol != tc.proto || tg.BaseURL != tc.wantBase || tg.Local != tc.local || tg.Model != "m" || (tg.APIKey != "") != (tc.key && tc.backend == "openai") {
				t.Fatalf("target = %+v", tg)
			}
			if tg.Remote() == tc.local {
				t.Fatal("Remote and Local agree")
			}
			if (tg.Notice() == "") != tc.local {
				t.Fatalf("notice = %q for local=%v", tg.Notice(), tc.local)
			}
		})
	}
}

func TestResolveRefusesWhatCannotWork(t *testing.T) {
	d := newDetector()
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"base url needs a protocol", cfgOf("auto", "http://127.0.0.1:1", ""), "explicit --llm"},
		{"unknown backend", cfgOf("gemini", "", ""), "not ollama, lmstudio"},
		{"credentials in the address", cfgOf("ollama", "http://user:pw@127.0.0.1:1", ""), "user name or password"},
		{"not http", cfgOf("ollama", "ftp://127.0.0.1", ""), "not http or https"},
		{"query", cfgOf("ollama", "http://127.0.0.1:1?x=1", ""), "query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := d.Resolve(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.name != "auto" && exitcode.Of(err) != exitcode.Usage {
				t.Fatalf("exit code = %d, want usage", exitcode.Of(err))
			}
		})
	}
}

func TestResolveKeepsTheModelItIsGivenWithoutAsking(t *testing.T) {
	srv := newFake(t, "ollama")
	d := newDetector()
	tg, models, err := d.Resolve(context.Background(), cfgOf("ollama", srv.URL, "  my-model "))
	if err != nil || tg.Model != "my-model" || models != nil || srv.lists != 0 {
		t.Fatalf("target = %+v models = %v lists = %d err = %v", tg, models, srv.lists, err)
	}
}

func TestResolvePicksTheLoadedVisionModel(t *testing.T) {
	a, b, c := blind("text-only"), vision("vision-cold"), vision("vision-loaded")
	b.Loaded, c.Loaded = false, true
	srv := newFake(t, "openai", a, b, c)
	d := newDetector()
	tg, models, err := d.Resolve(context.Background(), cfgOf("lmstudio", srv.URL+"/v1", ""))
	if err != nil || tg.Model != "vision-loaded" || len(models) != 3 {
		t.Fatalf("target = %+v models = %d err = %v", tg, len(models), err)
	}
	c.Loaded = false
	srv.setModels(a, b, c)
	if tg, _, err = d.Resolve(context.Background(), cfgOf("lmstudio", srv.URL+"/v1", "")); err != nil || tg.Model != "vision-cold" {
		t.Fatalf("with none loaded the first that sees is used: %+v %v", tg, err)
	}
}

func TestResolveListsTheModelsWhenNoneSees(t *testing.T) {
	srv := newFake(t, "ollama", blind("llama3.2:3b"), blind("phi4"))
	d := newDetector()
	_, models, err := d.Resolve(context.Background(), cfgOf("ollama", srv.URL, ""))
	if err == nil || len(models) != 2 {
		t.Fatalf("err = %v models = %d", err, len(models))
	}
	for _, want := range []string{"no model of ollama reports vision", "llama3.2:3b", "phi4", "--model", "text-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	empty := newFake(t, "ollama")
	if _, _, err = d.Resolve(context.Background(), cfgOf("ollama", empty.URL, "")); err == nil || !strings.Contains(err.Error(), "offers no models") {
		t.Fatalf("err = %v", err)
	}
}

func TestNoVisionTruncatesALongList(t *testing.T) {
	var ms []fakeModel
	for i := range 30 {
		ms = append(ms, blind("model-"+string(rune('a'+i%26))+string(rune('a'+i/26))))
	}
	srv := newFake(t, "openai", ms...)
	_, _, err := newDetector().Resolve(context.Background(), cfgOf("lmstudio", srv.URL+"/v1", ""))
	if err == nil || !strings.Contains(err.Error(), "and 10 more") {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoteNoticeNamesTheHost(t *testing.T) {
	tg := Target{Backend: "openai", BaseURL: "https://api.openai.com/v1"}
	n := tg.Notice()
	if !strings.Contains(n, "api.openai.com") || !strings.Contains(n, "not on this machine") || !strings.Contains(n, "screenshots") {
		t.Fatalf("notice = %q", n)
	}
	if (Target{Local: true}).Notice() != "" {
		t.Fatal("a local backend has no notice")
	}
}

func TestBackendMakesTheClientOfEachProtocol(t *testing.T) {
	d := newDetector()
	for _, tc := range []struct {
		proto Protocol
		name  string
	}{{ProtocolOllama, "ollama"}, {ProtocolOpenAI, "openai"}, {ProtocolAnthropic, "anthropic"}} {
		b, err := d.Backend(Target{Protocol: tc.proto, BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "k"})
		if err != nil || b.Name() != tc.name {
			t.Errorf("%s: %v %v", tc.proto, b, err)
		}
	}
	if _, err := d.Backend(Target{Protocol: "grpc"}); err == nil {
		t.Fatal("expected an error for an unknown protocol")
	}
}
