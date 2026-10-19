// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package openai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/parse"
)

// fixture reads an answer written down in testdata/llm/openai.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "llm", "openai", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func complete(t *testing.T, status int, name string, mode backend.Mode, headers map[string]string) (backend.Response, error) {
	t.Helper()
	s := serve(t, status, fixture(t, name), headers)
	return newBackend(t, s.URL, nil).Complete(context.Background(), simple(mode, fullSet(t)))
}

// OpenAI's own answer: content null, the arguments as JSON text (once with line breaks), a lot of fields the client does
// not read.
func TestFixtureOpenAINativeCalls(t *testing.T) {
	resp, err := complete(t, 200, "native-tool-call.json", backend.ModeNative, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "" || len(resp.Reply.ToolCalls) != 2 || resp.Reply.ToolCalls[0].ID != "call_9sZ3kQ1vYx" ||
		resp.Usage.PromptTokens != 2034 || resp.Usage.CompletionTokens != 58 || resp.Model != "gpt-4o-2024-08-06" || resp.Truncated {
		t.Fatalf("%+v", resp)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if res.Source != parse.SourceNative || len(res.Actions) != 2 || len(res.Failures) != 0 {
		t.Fatalf("%+v", res)
	}
	if a := res.Actions[0]; a.CallID != "call_9sZ3kQ1vYx" || a.Action.Tool != "exec" || a.Action.Reason != "find out the kernel version" || a.Action.Args.(*actions.ExecArgs).Command != "uname -r" {
		t.Errorf("%+v", a)
	}
	if w := res.Actions[1].Action.Args.(*actions.WaitForArgs); w.Condition != "seconds" || w.TimeoutS != 1 {
		t.Errorf("%+v", w)
	}
}

// llama.cpp's server puts words next to the call and adds timings.
func TestFixtureLlamaCppToolCall(t *testing.T) {
	resp, err := complete(t, 200, "llamacpp-tool-call.json", backend.ModeNative, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if len(res.Actions) != 1 || res.Actions[0].Action.Tool != "click_element" || res.Actions[0].Action.Reason != "I will click the OK button." || res.Thought != "I will click the OK button." {
		t.Errorf("%+v", res)
	}
	if resp.Usage.PromptTokens != 1650 {
		t.Errorf("%+v", resp.Usage)
	}
}

// LM Studio with a reasoning model: no native tools, a <think> block and the JSON answer in the content.
func TestFixtureLMStudioJSONContent(t *testing.T) {
	resp, err := complete(t, 200, "lmstudio-json-content.json", backend.ModeStructured, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if res.Source != parse.SourceJSON || len(res.Actions) != 1 || res.Actions[0].Action.Tool != "key" || res.Thought != "Open a terminal." {
		t.Errorf("%+v", res)
	}
}

// Servers that deviate from the API in ways that cost nothing to accept.
func TestFixtureContentAsParts(t *testing.T) {
	resp, err := complete(t, 200, "content-parts.json", backend.ModeJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res := parse.Parse(fullSet(t), resp.Reply, 3); len(res.Actions) != 1 || res.Actions[0].Action.Tool != "screenshot" {
		t.Errorf("%+v", res)
	}
}

func TestFixtureArgumentsAsAnObject(t *testing.T) {
	resp, err := complete(t, 200, "arguments-as-object.json", backend.ModeNative, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reply.ToolCalls) != 2 || resp.Reply.ToolCalls[1].ID != "call_2" {
		t.Fatalf("%+v", resp.Reply.ToolCalls)
	}
	// An empty string for the arguments of the second call is "no arguments": key needs its keys, so it is refused, with
	// a reason the model can act on; the first call, with its arguments as an object, is fine.
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if len(res.Actions) != 1 || res.Actions[0].Action.Args.(*actions.TypeTextArgs).Text != "hello é" || len(res.Failures) != 1 || !strings.Contains(res.Failures[0].Err.Error(), "keys") {
		t.Errorf("%+v", res)
	}
}

func TestFixtureErrors(t *testing.T) {
	key := serve(t, 401, fixture(t, "error-invalid-key.json"), nil)
	_, err := newBackend(t, key.URL, func(c *Config) { c.APIKey = "sk-proj-SECRET123456789" }).Complete(context.Background(), simple(backend.ModeJSON, nil))
	e := asBackendError(t, err)
	if e.Kind != backend.KindAuth || strings.Contains(e.Error(), "SECRET123456789") || !strings.Contains(e.Message, "Incorrect API key") {
		t.Errorf("%q", e.Error())
	}

	_, err = complete(t, 400, "error-context-length.json", backend.ModeJSON, nil)
	e = asBackendError(t, err)
	if e.Kind != backend.KindBadRequest || !strings.Contains(e.Message, "maximum context length is 8192 tokens") {
		t.Errorf("%+v", e)
	}

	limited := serve(t, 429, fixture(t, "error-rate-limit.json"), map[string]string{"Retry-After": "120"})
	c := client()
	_, err = mustNew(t, limited.URL, c).Complete(context.Background(), simple(backend.ModeJSON, nil))
	e = asBackendError(t, err)
	if e.Kind != backend.KindRateLimited || e.RetryAfter != 120*time.Second || limited.calls.Load() != 1 || !strings.Contains(e.Message, "Rate limit reached") {
		t.Errorf("a long wait is not waited for: %+v after %d calls", e, limited.calls.Load())
	}
}
