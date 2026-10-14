// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ollama

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// fixture reads an answer of Ollama written down in testdata/llm/ollama.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "llm", "ollama", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// What Ollama returns carries more than the client reads (timings, a thinking field, the index of a call): all of it must
// be ignored, and what the client does read must come through to the parser.
func TestFixtureNativeToolCall(t *testing.T) {
	s := serve(t, 200, fixture(t, "native-tool-call.json"))
	resp, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeNative, fullSet(t)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.PromptTokens != 1873 || resp.Usage.CompletionTokens != 62 || resp.Model != "qwen2.5vl:7b" || resp.Reply.Text != "" {
		t.Errorf("%+v", resp)
	}
	if len(resp.Reply.ToolCalls) != 2 || resp.Reply.ToolCalls[0].ID != "call_8f2k1x9q" || resp.Reply.ToolCalls[1].ID != "call_2" {
		t.Fatalf("%+v", resp.Reply.ToolCalls)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if res.Source != parse.SourceNative || len(res.Actions) != 2 || len(res.Failures) != 0 {
		t.Fatalf("%+v", res)
	}
	if a := res.Actions[0]; a.CallID != "call_8f2k1x9q" || a.Action.Tool != "key" || a.Action.Reason != "open a terminal" || a.Action.Args.(*actions.KeyArgs).Keys != "ctrl+alt+t" {
		t.Errorf("%+v", a)
	}
	if w := res.Actions[1].Action.Args.(*actions.WaitForArgs); w.Condition != "screen_idle" || w.TimeoutS != 5 {
		t.Errorf("%+v", w)
	}
}

// A model without native tools answers in JSON mode: a reasoning block, prose, a fenced JSON with commas that should not
// be there.
func TestFixtureJSONModeAnswer(t *testing.T) {
	s := serve(t, 200, fixture(t, "json-mode-fenced.json"))
	resp, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if err != nil || len(resp.Reply.ToolCalls) != 0 || !strings.Contains(resp.Reply.Text, "<think>") {
		t.Fatalf("%+v %v", resp, err)
	}
	res := parse.Parse(fullSet(t), resp.Reply, 3)
	if res.Source != parse.SourceJSON || len(res.Actions) != 2 || len(res.Failures) != 0 {
		t.Fatalf("%+v", res)
	}
	if res.Thought != "Open a terminal with the keyboard." || res.Actions[0].Action.Reason != "open a terminal" || res.Actions[1].Action.Tool != "wait_for" {
		t.Errorf("%+v", res)
	}
}

func TestFixtureErrors(t *testing.T) {
	s := serve(t, 404, fixture(t, "model-not-found.json"))
	_, err := newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeJSON, nil))
	if e := asBackendError(t, err); e.Kind != backend.KindNotFound || !strings.Contains(e.Message, "ollama pull qwen2.5vl:7b") {
		t.Errorf("%+v", e)
	}
	s = serve(t, 400, fixture(t, "no-tools-support.json"))
	_, err = newBackend(t, s.URL, nil).Complete(context.Background(), simple(backend.ModeNative, fullSet(t)))
	if e := asBackendError(t, err); e.Kind != backend.KindBadRequest || !strings.Contains(e.Message, "does not support tools") || s.calls.Load() != 1 {
		t.Errorf("%+v after %d calls", e, s.calls.Load())
	}
}
