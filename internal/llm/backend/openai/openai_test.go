// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package openai

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

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
)

// server is a fake OpenAI-compatible server: it keeps the last request and answers with what the test says.
type server struct {
	*httptest.Server
	path, method, auth string
	body               map[string]any
	calls              atomic.Int32
	header             http.Header
}

func serve(t *testing.T, status int, answer string, headers map[string]string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.path, s.method, s.auth, s.header = r.URL.Path, r.Method, r.Header.Get("Authorization"), r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		s.body = nil
		_ = json.Unmarshal(raw, &s.body)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(s.Close)
	return s
}

func client() *httpx.Client {
	c := httpx.New("openai")
	c.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return c
}

func newBackend(t *testing.T, base string, mutate func(*Config)) *Backend {
	t.Helper()
	cfg := Config{BaseURL: base, Model: "qwen2.5-vl-7b-instruct"}
	if mutate != nil {
		mutate(&cfg)
	}
	b, err := New(cfg, client())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mustNew is newBackend with an HTTP client of the test's own.
func mustNew(t *testing.T, base string, c *httpx.Client) *Backend {
	t.Helper()
	b, err := New(Config{BaseURL: base, Model: "m"}, c)
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

const okAnswer = `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":7}}`

func simple(mode backend.Mode, set *actions.Set) backend.Request {
	return backend.Request{Mode: mode, Set: set, Messages: []backend.Message{{Role: backend.RoleSystem, Text: "You operate a computer."}, {Role: backend.RoleUser, Text: "Open a terminal."}}}
}

func TestNewChecksItsConfiguration(t *testing.T) {
	if _, err := New(Config{}, nil); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("a model is needed: %v", err)
	}
	for _, bad := range []string{"ftp://x", "http://u:p@host/v1", "localhost:1234", "http://host/v1?key=1"} {
		if _, err := New(Config{BaseURL: bad, Model: "m"}, nil); err == nil {
			t.Errorf("%q is not an address", bad)
		}
	}
	b, err := New(Config{Model: "gpt-4o"}, nil)
	if err != nil || b.cfg.BaseURL != DefaultBaseURL || b.cfg.MaxTokens != 2048 || !b.official || b.Name() != "openai" {
		t.Errorf("defaults: %+v %v", b, err)
	}
	if b, _ := New(Config{BaseURL: LMStudioBaseURL, Model: "m"}, nil); b.official {
		t.Error("LM Studio is not OpenAI")
	}
	if LlamaCppBaseURL != "http://127.0.0.1:8080/v1" || LMStudioBaseURL != "http://127.0.0.1:1234/v1" {
		t.Error("the addresses of the plan")
	}
}

func TestANativeRequest(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL+"/v1", func(c *Config) { c.APIKey = "sk-test" })
	png := []byte{0x89, 'P', 'N', 'G', 7}
	req := backend.Request{Mode: backend.ModeNative, Set: fullSet(t), Messages: []backend.Message{
		{Role: backend.RoleSystem, Text: "sys"},
		{Role: backend.RoleUser, Text: "task", Images: []backend.Image{{MIME: backend.MIMEPNG, Data: png}}},
		{Role: backend.RoleAssistant, Calls: []parse.ToolCall{
			{ID: "c1", Name: "exec", Args: json.RawMessage(`{"command":"uname -r"}`)},
			{Name: "key", Args: json.RawMessage(`"not an object"`)},
		}},
		{Role: backend.RoleTool, CallID: "c1", Tool: "exec", Text: `{"ok":true}`},
		{Role: backend.RoleTool, CallID: "call_2", Text: `{"ok":false}`},
		{Role: backend.RoleUser, Text: "next"},
		{Role: backend.RoleAssistant, Text: "thinking out loud", Calls: []parse.ToolCall{{ID: "c3", Name: "done", Args: json.RawMessage(`{}`)}}},
		{Role: backend.RoleTool, Tool: "done", Text: "{}"},
	}}
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if s.method != "POST" || s.path != "/v1/chat/completions" || s.auth != "Bearer sk-test" {
		t.Errorf("%s %s auth %q", s.method, s.path, s.auth)
	}
	body := s.body
	if body["model"] != "qwen2.5-vl-7b-instruct" || body["stream"] != false || body["temperature"] != float64(0) || body["max_tokens"] != float64(2048) ||
		body["max_completion_tokens"] != nil || body["tool_choice"] != "auto" || body["response_format"] != nil {
		t.Errorf("%v", body)
	}
	if tools, _ := body["tools"].([]any); len(tools) != 15 || tools[0].(map[string]any)["type"] != "function" {
		t.Errorf("tools: %v", body["tools"])
	}
	msgs := body["messages"].([]any)
	m := func(i int) map[string]any { return msgs[i].(map[string]any) }
	if m(0)["role"] != "system" || m(0)["content"] != "sys" {
		t.Errorf("system: %v", m(0))
	}
	parts := m(1)["content"].([]any)
	img := parts[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if len(parts) != 2 || parts[0].(map[string]any)["text"] != "task" || img != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png) {
		t.Errorf("a user message with a picture is parts, the picture a data URL: %v", m(1)["content"])
	}
	if m(5)["content"] != "next" {
		t.Errorf("a user message with no picture is a string: %v", m(5))
	}
	if m(2)["content"] != nil {
		t.Errorf("an assistant message that only calls tools has no content: %v", m(2))
	}
	calls := m(2)["tool_calls"].([]any)
	f0 := calls[0].(map[string]any)
	if f0["id"] != "c1" || f0["type"] != "function" || f0["function"].(map[string]any)["arguments"] != `{"command":"uname -r"}` {
		t.Errorf("the arguments of a call are JSON text: %v", f0)
	}
	if f1 := calls[1].(map[string]any); f1["id"] != "call_2" || f1["function"].(map[string]any)["arguments"] != `{}` {
		t.Errorf("a call with no id gets one; arguments that are not an object become {}: %v", f1)
	}
	if m(3)["role"] != "tool" || m(3)["tool_call_id"] != "c1" || m(3)["content"] != `{"ok":true}` || m(4)["tool_call_id"] != "call_2" || m(7)["tool_call_id"] != "done" {
		t.Errorf("tool results: %v %v %v", m(3), m(4), m(7))
	}
	if m(6)["content"] != "thinking out loud" {
		t.Errorf("an assistant message with words and calls keeps the words: %v", m(6))
	}
}

func TestNoKeyNoAuthorizationHeader(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	if _, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil {
		t.Fatal(err)
	}
	if s.auth != "" || s.header.Get("Content-Type") != "application/json" {
		t.Errorf("auth %q, headers %v", s.auth, s.header)
	}
}

func TestStructuredAndJSONModes(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL, nil)
	req := simple(backend.ModeStructured, fullSet(t))
	req.MaxActions = 2
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rf, _ := s.body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	list := schema["properties"].(map[string]any)["actions"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "controlsnack_response" || js["strict"] != false || list["maxItems"] != float64(2) || list["items"].(map[string]any)["oneOf"] == nil {
		t.Errorf("structured: %v", s.body)
	}
	if s.body["tools"] != nil || s.body["tool_choice"] != nil {
		t.Errorf("structured mode sends no tools: %v", s.body)
	}
	if _, err := b.Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil {
		t.Fatal(err)
	}
	if s.body["response_format"] != nil || s.body["tools"] != nil {
		t.Errorf("json mode asks in the prompt only: %v", s.body)
	}
}

func TestTheLimitOnTheAnswerAndTheTemperature(t *testing.T) {
	official, err := New(Config{Model: "gpt-4o", MaxTokens: 300}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := official.body(simple(backend.ModeJSON, nil))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if body["max_completion_tokens"] != float64(300) || body["max_tokens"] != nil || body["temperature"] != float64(0) {
		t.Errorf("OpenAI itself: %v", body)
	}

	local, err := New(Config{BaseURL: LlamaCppBaseURL, Model: "m", MaxTokens: 300, NoTemperature: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = local.body(simple(backend.ModeJSON, nil))
	body = nil
	_ = json.Unmarshal(raw, &body)
	if body["max_tokens"] != float64(300) || body["max_completion_tokens"] != nil || body["temperature"] != nil {
		t.Errorf("a local server, no temperature: %v", body)
	}
}

func TestTheAnswer(t *testing.T) {
	resp, err := newBackend(t, serve(t, 200, okAnswer, nil).URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "hello" || len(resp.Reply.ToolCalls) != 0 || resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 7 || resp.Model != "m" || resp.Truncated || resp.Latency < 0 {
		t.Errorf("%+v", resp)
	}
	for name, content := range map[string]string{"null": `null`, "missing": ``, "parts": `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`} {
		field := ""
		if content != "" {
			field = `,"content":` + content
		}
		answer := `{"choices":[{"message":{"role":"assistant"` + field + `},"finish_reason":"stop"}]}`
		resp, err := newBackend(t, serve(t, 200, answer, nil).URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
		want := map[string]string{"null": "", "missing": "", "parts": "a\nb"}[name]
		if err != nil || resp.Reply.Text != want {
			t.Errorf("content %s: %q %v", name, resp.Reply.Text, err)
		}
	}
	cut := `{"choices":[{"message":{"role":"assistant","content":"{\"actions\":[{\"to"},"finish_reason":"length"}]}`
	if resp, err := newBackend(t, serve(t, 200, cut, nil).URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil || !resp.Truncated {
		t.Errorf("cut by the limit on length: %+v %v", resp, err)
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
		{"no choices", 200, `{"choices":[]}`, backend.KindProtocol, []string{"no choices"}},
		{"an error in a good status", 200, `{"error":{"message":"model is loading"}}`, backend.KindBadRequest, []string{"model is loading"}},
		{"a plain error in a good status", 200, `{"error":"out of memory"}`, backend.KindBadRequest, []string{"out of memory"}},
		{"an error with no words", 200, `{"error":{"code":7}}`, backend.KindBadRequest, []string{"reported an error"}},
		{"not json", 200, `<html>`, backend.KindProtocol, []string{"not JSON"}},
		{"a wrong key", 401, `{"error":{"message":"invalid api key"}}`, backend.KindAuth, []string{"invalid api key", "--api-key-env"}},
		{"no such model", 404, `{"error":{"message":"The model 'x' does not exist"}}`, backend.KindNotFound, []string{"does not exist"}},
		{"a request the server refuses", 400, `{"error":{"message":"tools are not supported"}}`, backend.KindBadRequest, []string{"tools are not supported"}},
	} {
		s := serve(t, c.status, c.body, nil)
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

	s := serve(t, 503, `{"error":{"message":"overloaded"}}`, nil)
	_, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindServer || s.calls.Load() != 4 {
		t.Errorf("%+v after %d calls", e, s.calls.Load())
	}
}

func TestTheKeyNeverAppearsInAnError(t *testing.T) {
	s := serve(t, 401, `{"error":{"message":"Incorrect API key provided: sk-secret-key-123."}}`, nil)
	_, err := newBackend(t, s.URL, func(c *Config) { c.APIKey = "sk-secret-key-123" }).Complete(context.Background(), simple(backend.ModeJSON, nil))
	e := asBackendError(t, err)
	if strings.Contains(e.Error(), "sk-secret-key-123") || !strings.Contains(e.Message, "[key]") {
		t.Errorf("%q", e.Error())
	}
}

func TestRateLimitsHonorRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(okAnswer))
	}))
	defer srv.Close()
	var slept []time.Duration
	c := httpx.New("openai")
	c.Sleep = func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	b, err := New(Config{BaseURL: srv.URL, Model: "m"}, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil || len(slept) != 1 || slept[0] != 3*time.Second {
		t.Errorf("%v pauses %v", err, slept)
	}
}

func TestNothingListening(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	_, err := newBackend(t, url, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindUnavailable || !strings.Contains(e.Message, "is the server running") {
		t.Errorf("%+v", e)
	}
}

func TestAMalformedRequestIsNotSent(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	_, err := newBackend(t, s.URL, nil).Complete(context.Background(), backend.Request{Mode: backend.ModeJSON})
	var e *backend.Error
	if err == nil || errors.As(err, &e) || s.calls.Load() != 0 {
		t.Errorf("%v after %d calls", err, s.calls.Load())
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
	if _, err := newBackend(t, srv.URL, nil).Complete(ctx, simple(backend.ModeJSON, nil)); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}
