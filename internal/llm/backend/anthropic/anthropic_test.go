// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package anthropic

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

// server is a fake Anthropic-compatible server: it keeps the last request and answers with what the test says.
type server struct {
	*httptest.Server
	path, method string
	header       http.Header
	body         map[string]any
	calls        atomic.Int32
}

func serve(t *testing.T, status int, answer string, headers map[string]string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.path, s.method, s.header = r.URL.Path, r.Method, r.Header.Clone()
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
	c := httpx.New("anthropic")
	c.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return c
}

func newBackend(t *testing.T, base string, mutate func(*Config)) *Backend {
	t.Helper()
	cfg := Config{BaseURL: base, Model: "claude-sonnet-5-5", APIKey: "sk-ant-test"}
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

const okAnswer = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":120,"output_tokens":7}}`

func simple(mode backend.Mode, set *actions.Set) backend.Request {
	return backend.Request{Mode: mode, Set: set, Messages: []backend.Message{{Role: backend.RoleSystem, Text: "You operate a computer."}, {Role: backend.RoleUser, Text: "Open a terminal."}}}
}

func TestNewChecksItsConfiguration(t *testing.T) {
	if _, err := New(Config{}, nil); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("a model is needed: %v", err)
	}
	for _, bad := range []string{"ftp://x", "https://u:p@host", "api.anthropic.com", "https://host?key=1"} {
		if _, err := New(Config{BaseURL: bad, Model: "m"}, nil); err == nil {
			t.Errorf("%q is not an address", bad)
		}
	}
	b, err := New(Config{Model: "m"}, nil)
	if err != nil || b.cfg.BaseURL != DefaultBaseURL || b.cfg.MaxTokens != 2048 || b.Name() != "anthropic" || APIVersion != "2023-06-01" {
		t.Errorf("defaults: %+v %v", b, err)
	}
}

func TestANativeRequest(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL, nil)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0}
	call := parse.ToolCall{ID: "toolu_1", Name: "exec", Args: json.RawMessage(`{"command":"uname -r"}`)}
	req := backend.Request{Mode: backend.ModeNative, Set: fullSet(t), Messages: []backend.Message{
		{Role: backend.RoleSystem, Text: "sys"},
		{Role: backend.RoleUser, Text: "task", Images: []backend.Image{{MIME: backend.MIMEJPEG, Data: jpeg}}},
		{Role: backend.RoleAssistant, Text: "I will look.", Calls: []parse.ToolCall{call, {Name: "key", Args: json.RawMessage(`"not an object"`)}}},
		{Role: backend.RoleTool, CallID: "toolu_1", Tool: "exec", Text: `{"ok":true}`},
		{Role: backend.RoleTool, CallID: "call_2", Text: ""},
		{Role: backend.RoleUser, Text: "observation", Images: []backend.Image{{MIME: backend.MIMEWebP, Data: []byte{1, 2}}}},
		{Role: backend.RoleAssistant},
		{Role: backend.RoleUser, Text: "again"},
	}}
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if s.method != "POST" || s.path != "/v1/messages" || s.header.Get("x-api-key") != "sk-ant-test" || s.header.Get("anthropic-version") != "2023-06-01" || s.header.Get("Content-Type") != "application/json" {
		t.Errorf("%s %s %v", s.method, s.path, s.header)
	}
	body := s.body
	if body["model"] != "claude-sonnet-5-5" || body["max_tokens"] != float64(2048) || body["temperature"] != float64(0) || body["stream"] != false || body["system"] != "sys" {
		t.Errorf("%v", body)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 15 || tools[0].(map[string]any)["input_schema"] == nil || tools[0].(map[string]any)["name"] == nil {
		t.Errorf("tools: %v", body["tools"])
	}
	msgs := body["messages"].([]any)
	roles := ""
	for _, m := range msgs {
		roles += m.(map[string]any)["role"].(string)[:1]
	}
	if roles != "uauau" {
		t.Fatalf("the roles alternate (results and observation are one user message): %s\n%v", roles, msgs)
	}
	blocks := func(i int) []any { return msgs[i].(map[string]any)["content"].([]any) }
	b0 := blocks(0)
	img := b0[0].(map[string]any)
	src := img["source"].(map[string]any)
	if len(b0) != 2 || img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/jpeg" || src["data"] != base64.StdEncoding.EncodeToString(jpeg) || b0[1].(map[string]any)["text"] != "task" {
		t.Errorf("a picture comes before its words: %v", b0)
	}
	b1 := blocks(1)
	use := b1[1].(map[string]any)
	if len(b1) != 3 || b1[0].(map[string]any)["text"] != "I will look." || use["type"] != "tool_use" || use["id"] != "toolu_1" || use["name"] != "exec" || use["input"].(map[string]any)["command"] != "uname -r" {
		t.Errorf("the assistant's words and calls: %v", b1)
	}
	if second := b1[2].(map[string]any); second["id"] != "call_2" || len(second["input"].(map[string]any)) != 0 {
		t.Errorf("a call with no id gets one; input that is not an object becomes {}: %v", second)
	}
	b2 := blocks(2)
	r0, r1 := b2[0].(map[string]any), b2[1].(map[string]any)
	if len(b2) != 4 || r0["type"] != "tool_result" || r0["tool_use_id"] != "toolu_1" || r0["content"] != `{"ok":true}` || r1["tool_use_id"] != "call_2" || r1["content"] != "(no text)" {
		t.Errorf("the results come first, an empty one has a stand-in: %v", b2)
	}
	if b2[2].(map[string]any)["type"] != "image" || b2[3].(map[string]any)["text"] != "observation" {
		t.Errorf("then the observation: %v", b2)
	}
	if b3 := blocks(3); len(b3) != 1 || b3[0].(map[string]any)["text"] != "(no text)" {
		t.Errorf("an assistant message with nothing in it: %v", b3)
	}
}

func TestJSONAndStructuredModesSendNoTools(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL, nil)
	for _, mode := range []backend.Mode{backend.ModeJSON, backend.ModeStructured} {
		if _, err := b.Complete(context.Background(), simple(mode, fullSet(t))); err != nil {
			t.Fatal(err)
		}
		if s.body["tools"] != nil || s.body["response_format"] != nil {
			t.Errorf("%s: this API has no response schema: %v", mode, s.body)
		}
	}
}

func TestPrefixAndNoKey(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL+"/anthropic/", func(c *Config) { c.APIKey = "" })
	if _, err := b.Complete(context.Background(), simple(backend.ModeJSON, nil)); err != nil || s.path != "/anthropic/v1/messages" || s.header.Get("x-api-key") != "" {
		t.Errorf("%v %s %v", err, s.path, s.header)
	}
}

func TestAConversationMustStartWithTheUser(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	req := backend.Request{Mode: backend.ModeJSON, Messages: []backend.Message{
		{Role: backend.RoleSystem, Text: "sys"}, {Role: backend.RoleAssistant, Text: "hi"}, {Role: backend.RoleUser, Text: "task"},
	}}
	_, err := newBackend(t, s.URL, nil).Complete(context.Background(), req)
	var e *backend.Error
	if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), "start with a user message") || s.calls.Load() != 0 {
		t.Errorf("%v after %d calls", err, s.calls.Load())
	}
}

func TestTheAnswer(t *testing.T) {
	resp, err := newBackend(t, serve(t, 200, okAnswer, nil).URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "hello" || len(resp.Reply.ToolCalls) != 0 || resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 7 || resp.Model != "claude-sonnet-5-5" || resp.Truncated || resp.Latency < 0 {
		t.Errorf("%+v", resp)
	}
	two := `{"type":"message","model":"m","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"tool_use","name":"key","input":{"keys":"Tab"}},{"type":"redacted_thinking","data":"x"}],"stop_reason":"tool_use"}`
	resp, err = newBackend(t, serve(t, 200, two, nil).URL, nil).Complete(context.Background(), simple(backend.ModeNative, fullSet(t)))
	if err != nil || resp.Reply.Text != "a\nb" || len(resp.Reply.ToolCalls) != 1 || resp.Reply.ToolCalls[0].ID != "call_3" || string(resp.Reply.ToolCalls[0].Args) != `{"keys":"Tab"}` {
		t.Errorf("%+v %v", resp, err)
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
		{"not json", 200, `<html>`, backend.KindProtocol, []string{"not JSON"}},
		{"an error object in a good status", 200, `{"type":"error","error":{"type":"x","message":"y"}}`, backend.KindProtocol, []string{"not a message"}},
		{"a wrong key", 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, backend.KindAuth, []string{"invalid x-api-key", "--api-key-env"}},
		{"no such model", 404, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope"}}`, backend.KindNotFound, []string{"claude-nope"}},
		{"a request that is refused", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`, backend.KindBadRequest, []string{"max_tokens: too large"}},
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
	s := serve(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, nil)
	_, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindServer || s.calls.Load() != 4 {
		t.Errorf("an overloaded API is retried, then reported: %+v after %d calls", e, s.calls.Load())
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
