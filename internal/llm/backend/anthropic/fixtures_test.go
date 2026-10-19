// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package anthropic

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

// fixture reads an answer written down in testdata/llm/anthropic.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "llm", "anthropic", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A real answer: a thinking block the client does not use, words, and two tool_use blocks, with fields the client does
// not read (cache counts, the service tier).
func TestFixtureToolUse(t *testing.T) {
	s := serve(t, 200, fixture(t, "tool-use.json"), nil)
	resp, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeNative, fullSet(t)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "I will read the kernel version and wait a moment." || len(resp.Reply.ToolCalls) != 2 || resp.Reply.ToolCalls[0].ID != "toolu_01A09q90qw90lq917835lq9" ||
		resp.Usage.PromptTokens != 2095 || resp.Usage.CompletionTokens != 112 || resp.Model != "claude-sonnet-5-5" || resp.Truncated {
		t.Fatalf("%+v", resp)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if res.Source != parse.SourceNative || len(res.Actions) != 2 || len(res.Failures) != 0 {
		t.Fatalf("%+v", res)
	}
	if a := res.Actions[0]; a.CallID != "toolu_01A09q90qw90lq917835lq9" || a.Action.Tool != "exec" || a.Action.Reason != "find the kernel version" || a.Action.Args.(*actions.ExecArgs).Command != "uname -r" {
		t.Errorf("%+v", a)
	}
	if res.Thought != "I will read the kernel version and wait a moment." {
		t.Errorf("thought %q", res.Thought)
	}
}

func TestFixtureJSONInText(t *testing.T) {
	s := serve(t, 200, fixture(t, "text-json.json"), nil)
	resp, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if err != nil {
		t.Fatal(err)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if res.Source != parse.SourceJSON || len(res.Actions) != 1 || res.Actions[0].Action.Tool != "key" || res.Thought != "Open a terminal." {
		t.Errorf("%+v", res)
	}
}

func TestFixtureCutOffByTheLimit(t *testing.T) {
	s := serve(t, 200, fixture(t, "max-tokens.json"), nil)
	resp, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if err != nil || !resp.Truncated {
		t.Fatalf("%+v %v", resp, err)
	}
	if res := parse.Parse(fullSet(t), resp.Reply, 3); res.Usable() {
		t.Errorf("half an answer is not an action: %+v", res)
	}
}

func TestFixtureErrors(t *testing.T) {
	auth := serve(t, 401, fixture(t, "error-auth.json"), nil)
	_, err := newBackend(t, auth.URL, func(c *Config) { c.APIKey = "sk-ant-api03-SECRETKEY987654" }).Complete(context.Background(), simple(backend.ModeJSON, nil))
	e := asBackendError(t, err)
	if e.Kind != backend.KindAuth || strings.Contains(e.Error(), "SECRETKEY987654") || !strings.Contains(e.Message, "invalid x-api-key") {
		t.Errorf("%q", e.Error())
	}

	over := serve(t, 529, fixture(t, "error-overloaded.json"), nil)
	_, err = newBackend(t, over.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindServer || e.Status != 529 || e.Message != "Overloaded" || over.calls.Load() != 4 {
		t.Errorf("%+v after %d calls", e, over.calls.Load())
	}

	bad := serve(t, 400, fixture(t, "error-invalid-request.json"), nil)
	_, err = newBackend(t, bad.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindBadRequest || !strings.Contains(e.Message, "tool_result") || bad.calls.Load() != 1 {
		t.Errorf("%+v after %d calls", e, bad.calls.Load())
	}

	limited := serve(t, 429, fixture(t, "error-rate-limit.json"), map[string]string{"Retry-After": "90"})
	_, err = newBackend(t, limited.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindRateLimited || e.RetryAfter != 90*time.Second || limited.calls.Load() != 1 {
		t.Errorf("a long wait is not waited for: %+v after %d calls", e, limited.calls.Load())
	}
}
