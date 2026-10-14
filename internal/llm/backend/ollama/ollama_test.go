// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// server is a fake Ollama: it keeps the last request it was sent and answers with what the test says.
type server struct {
	*httptest.Server
	path   string
	method string
	body   map[string]any
	calls  atomic.Int32
}

func serve(t *testing.T, status int, answer string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.path, s.method = r.URL.Path, r.Method
		raw, _ := io.ReadAll(r.Body)
		s.body = nil
		_ = json.Unmarshal(raw, &s.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(s.Close)
	return s
}

func client() *httpx.Client {
	c := httpx.New("ollama")
	c.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return c
}

func newBackend(t *testing.T, base string, mutate func(*Config)) *Backend {
	t.Helper()
	cfg := Config{BaseURL: base, Model: "qwen2.5vl:7b"}
	if mutate != nil {
		mutate(&cfg)
	}
	b, err := New(cfg, client())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fullSet(t *testing.T) *actions.Set {
	t.Helper()
	s, err := actions.NewSet(actions.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const okAnswer = `{"model":"qwen2.5vl:7b","message":{"role":"assistant","content":"hello"},"done":true,"done_reason":"stop","prompt_eval_count":120,"eval_count":7}`

func simple(mode backend.Mode, set *actions.Set) backend.Request {
	return backend.Request{Mode: mode, Set: set, Messages: []backend.Message{{Role: backend.RoleSystem, Text: "You operate a computer."}, {Role: backend.RoleUser, Text: "Open a terminal."}}}
}

func TestNewChecksItsConfiguration(t *testing.T) {
	if _, err := New(Config{Model: ""}, nil); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("a model is needed: %v", err)
	}
	for _, bad := range []string{"ftp://x", "http://u:p@host", "localhost:11434"} {
		if _, err := New(Config{BaseURL: bad, Model: "m"}, nil); err == nil {
			t.Errorf("%q is not an address", bad)
		}
	}
	b, err := New(Config{Model: "m"}, nil)
	if err != nil || b.cfg.BaseURL != DefaultBaseURL || b.cfg.NumCtx != 16384 || b.cfg.MaxTokens != 2048 || b.cfg.KeepAlive != "30m" || b.Name() != "ollama" {
		t.Errorf("defaults: %+v %v", b, err)
	}
}

func TestANativeRequest(t *testing.T) {
	s := serve(t, 200, okAnswer)
	b := newBackend(t, s.URL, nil)
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	call := parse.ToolCall{ID: "c1", Name: "exec", Args: json.RawMessage(`{"command":"uname -r"}`)}
	req := backend.Request{Mode: backend.ModeNative, Set: fullSet(t), Messages: []backend.Message{
		{Role: backend.RoleSystem, Text: "sys"},
		{Role: backend.RoleUser, Text: "task", Images: []backend.Image{{MIME: backend.MIMEPNG, Data: png}}},
		{Role: backend.RoleAssistant, Text: "I will look.", Calls: []parse.ToolCall{call, {Name: "key", Args: json.RawMessage(`"not an object"`)}}},
		{Role: backend.RoleTool, CallID: "c1", Tool: "exec", Text: `{"ok":true}`},
		{Role: backend.RoleUser, Text: "next"},
	}}
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if s.method != "POST" || s.path != "/api/chat" {
		t.Errorf("%s %s", s.method, s.path)
	}
	body := s.body
	if body["model"] != "qwen2.5vl:7b" || body["stream"] != false || body["keep_alive"] != "30m" || body["format"] != nil {
		t.Errorf("%v", body)
	}
	opts := body["options"].(map[string]any)
	if opts["num_ctx"] != float64(16384) || opts["temperature"] != float64(0) || opts["num_predict"] != float64(2048) {
		t.Errorf("options %v", opts)
	}
	if tools, _ := body["tools"].([]any); len(tools) != 15 || tools[0].(map[string]any)["type"] != "function" {
		t.Errorf("tools: %v", body["tools"])
	}
	msgs := body["messages"].([]any)
	m := func(i int) map[string]any { return msgs[i].(map[string]any) }
	if len(msgs) != 5 || m(0)["role"] != "system" || m(0)["content"] != "sys" {
		t.Fatalf("messages %v", msgs)
	}
	if imgs := m(1)["images"].([]any); len(imgs) != 1 || imgs[0] != base64.StdEncoding.EncodeToString(png) {
		t.Errorf("the image goes as base64: %v", m(1))
	}
	calls := m(2)["tool_calls"].([]any)
	first := calls[0].(map[string]any)["function"].(map[string]any)
	if m(2)["content"] != "I will look." || len(calls) != 2 || first["name"] != "exec" || first["arguments"].(map[string]any)["command"] != "uname -r" {
		t.Errorf("the assistant's calls: %v", m(2))
	}
	if second := calls[1].(map[string]any)["function"].(map[string]any); second["arguments"] == nil || len(second["arguments"].(map[string]any)) != 0 {
		t.Errorf("arguments that are not an object go as {}: %v", second)
	}
	if m(3)["role"] != "tool" || m(3)["tool_name"] != "exec" || m(3)["content"] != `{"ok":true}` {
		t.Errorf("the tool result: %v", m(3))
	}
	if _, has := m(4)["tool_name"]; has || m(4)["images"] != nil {
		t.Errorf("a user message: %v", m(4))
	}
}

func TestTheCompactProfileSendsFewerTools(t *testing.T) {
	s := serve(t, 200, okAnswer)
	compact, _ := actions.NewSet(actions.ProfileCompact)
	if _, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeNative, compact)); err != nil {
		t.Fatal(err)
	}
	if tools, _ := s.body["tools"].([]any); len(tools) != 9 {
		t.Errorf("%d tools", len(tools))
	}
}

func TestStructuredAndJSONModes(t *testing.T) {
	s := serve(t, 200, okAnswer)
	b := newBackend(t, s.URL, nil)
	req := simple(backend.ModeStructured, fullSet(t))
	req.MaxActions = 2
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	format, _ := s.body["format"].(map[string]any)
	list := format["properties"].(map[string]any)["actions"].(map[string]any)
	if s.body["tools"] != nil || list["maxItems"] != float64(2) || list["items"].(map[string]any)["oneOf"] == nil {
		t.Errorf("structured: the strict schema as the format, no tools: %v", s.body)
	}
	if _, err := b.Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil {
		t.Fatal(err)
	}
	if s.body["format"] != "json" || s.body["tools"] != nil {
		t.Errorf("json mode: %v", s.body)
	}
}

func TestOptionsAreTheOnesGiven(t *testing.T) {
	s := serve(t, 200, okAnswer)
	b := newBackend(t, s.URL, func(c *Config) { c.NumCtx, c.MaxTokens, c.KeepAlive = 8192, 512, "5m" })
	if _, err := b.Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil {
		t.Fatal(err)
	}
	opts := s.body["options"].(map[string]any)
	if opts["num_ctx"] != float64(8192) || opts["num_predict"] != float64(512) || s.body["keep_alive"] != "5m" {
		t.Errorf("%v %v", opts, s.body["keep_alive"])
	}
}

func TestAnAddressWithAPathPrefix(t *testing.T) {
	s := serve(t, 200, okAnswer)
	b := newBackend(t, s.URL+"/ollama/", nil)
	if _, err := b.Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil || s.path != "/ollama/api/chat" {
		t.Errorf("%v %s", err, s.path)
	}
}

func TestTheAnswer(t *testing.T) {
	s := serve(t, 200, okAnswer)
	resp, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "hello" || len(resp.Reply.ToolCalls) != 0 || resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 7 ||
		resp.Model != "qwen2.5vl:7b" || resp.Truncated || resp.Latency < 0 {
		t.Errorf("%+v", resp)
	}

	calls := `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"exec","arguments":{"command":"ls"}}},{"id":"abc","function":{"name":"key","arguments":{"keys":"Tab"}}}]},"done":true,"done_reason":"stop"}`
	s2 := serve(t, 200, calls)
	resp, err = newBackend(t, s2.URL, nil).Complete(context.Background(), simple(backend.ModeNative, fullSet(t)))
	if err != nil || len(resp.Reply.ToolCalls) != 2 {
		t.Fatalf("%+v %v", resp, err)
	}
	c0, c1 := resp.Reply.ToolCalls[0], resp.Reply.ToolCalls[1]
	if c0.ID != "call_1" || c0.Name != "exec" || string(c0.Args) != `{"command":"ls"}` || c1.ID != "abc" || c1.Name != "key" {
		t.Errorf("%+v %+v", c0, c1)
	}
	// What the parser makes of it.
	set := fullSet(t)
	if res := parse.Parse(set, resp.Reply, 3); len(res.Actions) != 2 || res.Source != parse.SourceNative {
		t.Errorf("%+v", res)
	}

	s3 := serve(t, 200, `{"model":"m","message":{"role":"assistant","content":"{\"actions\":[{\"tool\":\"scr"},"done":true,"done_reason":"length"}`)
	if resp, err := newBackend(t, s3.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil || !resp.Truncated {
		t.Errorf("an answer cut off by the limit on length: %+v %v", resp, err)
	}
}

func asBackendError(t *testing.T, err error) *backend.Error {
	t.Helper()
	var e *backend.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a backend error: %v", err)
	}
	return e
}

func TestFailures(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		kind   backend.Kind
		words  []string
	}{
		{"no such model", 404, `{"error":"model 'qwen2.5vl:7b' not found"}`, backend.KindNotFound, []string{"not found", "ollama pull qwen2.5vl:7b"}},
		{"another 404", 404, `404 page not found`, backend.KindNotFound, []string{"404"}},
		{"no tools", 400, `{"error":"registry.ollama.ai/library/gemma:2b does not support tools"}`, backend.KindBadRequest, []string{"does not support tools"}},
		{"an error in a good status", 200, `{"error":"model requires more system memory"}`, backend.KindBadRequest, []string{"more system memory"}},
		{"not json", 200, `<html>proxy</html>`, backend.KindProtocol, []string{"not JSON"}},
		{"no message", 200, `{"model":"m","done":true}`, backend.KindProtocol, []string{"no assistant message"}},
	} {
		s := serve(t, c.status, c.body)
		_, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
		e := asBackendError(t, err)
		if e.Kind != c.kind {
			t.Errorf("%s: %+v", c.name, e)
		}
		for _, w := range c.words {
			if !strings.Contains(e.Message, w) {
				t.Errorf("%s: %q is missing from %q", c.name, w, e.Message)
			}
		}
	}

	// A server error is retried before it is reported.
	s := serve(t, 500, `{"error":"llama runner process has terminated"}`)
	_, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindServer || s.calls.Load() != 4 {
		t.Errorf("%+v after %d calls", e, s.calls.Load())
	}
}

func TestNothingListening(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	_, err := newBackend(t, url, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindUnavailable || !strings.Contains(e.Message, "is Ollama running") {
		t.Errorf("%+v", e)
	}
}

func TestAMalformedRequestIsNotSent(t *testing.T) {
	s := serve(t, 200, okAnswer)
	b := newBackend(t, s.URL, nil)
	_, err := b.Complete(context.Background(), backend.Request{Mode: backend.ModeJSON})
	if err == nil || !strings.Contains(err.Error(), "no messages") || s.calls.Load() != 0 {
		t.Errorf("%v after %d calls", err, s.calls.Load())
	}
	var e *backend.Error
	if errors.As(err, &e) {
		t.Error("a bug of the caller is not a failure of the backend")
	}
}

func TestACanceledCall(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := newBackend(t, srv.URL, nil).Complete(ctx, simple(backend.ModeJSON, nil))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}
