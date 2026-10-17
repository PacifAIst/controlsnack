// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWhenNoServerAnswersTheErrorSaysHowToStartOne(t *testing.T) {
	d := newDetector(Candidate{Backend: "ollama", BaseURL: deadServer(), Path: "/api/tags"})
	_, _, err := d.Resolve(context.Background(), cfgOf("auto", "", ""))
	var ns *NoServerError
	if !errors.As(err, &ns) || len(ns.Tried) != 1 {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{
		"no local model server answered (ollama at 127.0.0.1:", "Developer tab", "lms server start", "port 1234", "Require Authentication",
		"ollama serve", "ollama pull", "--llm openai --base-url https://openrouter.ai/api/v1", "--api-key-env",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q:\n%s", want, err)
		}
	}
	if strings.HasPrefix(ns.Instructions(), "no local model") || !strings.HasPrefix(ns.Instructions(), "To start one:") {
		t.Errorf("the instructions stand alone, for a notice in the viewer: %q", ns.Instructions())
	}
}

func TestAServerThatWantsAKeyIsStillFoundAndTheNextCallSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API Key","type":"authentication_error","code":401}}`))
	}))
	t.Cleanup(srv.Close)
	d := newDetector(Candidate{Backend: "lmstudio", BaseURL: srv.URL + "/v1", Path: "/models"})
	_, _, err := d.Resolve(context.Background(), cfgOf("auto", "", ""))
	var ns *NoServerError
	if err == nil || errors.As(err, &ns) {
		t.Fatalf("a server that answers 401 is there; err = %v", err)
	}
	if !strings.Contains(err.Error(), "listing the models of lmstudio") || !strings.Contains(err.Error(), "api-key-env") {
		t.Errorf("err = %v", err)
	}
}

func TestLMStudioIsFoundAtThePortItsOwnStatusFileNames(t *testing.T) {
	home := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newFake(t, "openai", strongModel("qwen3-vl-4b"))
	u, _ := url.Parse(srv.URL)
	write(filepath.Join(".cache", "lm-studio", ".internal", "http-server-last-status.json"), `{"port":`+u.Port()+`}`)

	d := New(Options{HomeDir: func() (string, error) { return home, nil }, Getenv: func(string) string { return "" }, DetectTimeout: 2e9})
	target, _, err := d.Resolve(context.Background(), cfgOf("auto", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if target.Backend != "lmstudio" || target.BaseURL != srv.URL+"/v1" || target.Model != "qwen3-vl-4b" {
		t.Fatalf("target = %+v", target)
	}
}

func TestStatusFilesOfLMStudio(t *testing.T) {
	home := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(home, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bases := func() []string {
		var out []string
		for _, c := range candidatesFor(home) {
			out = append(out, c.Backend+" "+c.BaseURL)
		}
		return out
	}
	defaults := []string{"ollama http://127.0.0.1:11434", "lmstudio http://127.0.0.1:1234/v1"}
	if got := strings.Join(bases(), ","); got != strings.Join(defaults, ",") {
		t.Fatalf("no files: %v", got)
	}
	for name, content := range map[string]string{"garbage": "not json", "zero": `{"port":0}`, "too high": `{"port":70000}`, "negative": `{"port":-5}`, "default": `{"port":1234}`, "empty": ``} {
		write(filepath.Join(".cache", "lm-studio", ".internal", "http-server-last-status.json"), content)
		if got := strings.Join(bases(), ","); got != strings.Join(defaults, ",") {
			t.Errorf("%s: %v", name, got)
		}
	}
	write(filepath.Join(".cache", "lm-studio", ".internal", "http-server-last-status.json"), `{"port":53224}`)
	write(filepath.Join(".lmstudio", ".internal", "http-server-last-status.json"), `{"port":4242}`)
	if got := strings.Join(bases(), ","); got != strings.Join(append(defaults, "lmstudio http://127.0.0.1:53224/v1", "lmstudio http://127.0.0.1:4242/v1"), ",") {
		t.Errorf("both files: %v", got)
	}
	if len(candidatesFor("")) != 2 {
		t.Error("no home, no extra candidates")
	}
	// A candidate the file names is on loopback by construction.
	for _, c := range candidatesFor(home) {
		if !strings.HasPrefix(c.BaseURL, "http://127.0.0.1:") {
			t.Errorf("candidate %s is not on loopback", c.BaseURL)
		}
	}
}
