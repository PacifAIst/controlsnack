// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package openai

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// An OpenAI-compatible server sends the arguments of a call as a string that holds the object, and that is how the call
// reaches the history. Sent back, it must still be the arguments: a model that is shown its earlier calls with {} loses the
// thread of what it did.
func TestTheArgumentsOfAnEarlierCallComeBackAsTheyWere(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL+"/v1", nil)
	asString := json.RawMessage(strconv.Quote(`{"command":"ls -la","timeout_s":5}`))
	req := backend.Request{Mode: backend.ModeNative, Set: fullSet(t), Messages: []backend.Message{
		{Role: backend.RoleUser, Text: "task"},
		{Role: backend.RoleAssistant, Calls: []parse.ToolCall{{ID: "c1", Name: "exec", Args: asString}, {ID: "c2", Name: "key", Args: json.RawMessage(`{"keys":"Return"}`)}}},
		{Role: backend.RoleTool, CallID: "c1", Tool: "exec", Text: "ok"},
		{Role: backend.RoleTool, CallID: "c2", Tool: "key", Text: "ok"},
		{Role: backend.RoleUser, Text: "next"},
	}}
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	calls := s.body["messages"].([]any)[1].(map[string]any)["tool_calls"].([]any)
	got := func(i int) string {
		return calls[i].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	}
	if got(0) != `{"command":"ls -la","timeout_s":5}` || got(1) != `{"keys":"Return"}` {
		t.Errorf("arguments sent back: %s and %s", got(0), got(1))
	}
}
