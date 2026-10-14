// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package ollama

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// A call that reached the history with its arguments as a string that holds the object (as OpenAI-compatible servers send
// them) is sent back to Ollama as the object it holds.
func TestTheArgumentsOfAnEarlierCallComeBackAsAnObject(t *testing.T) {
	s := serve(t, 200, okAnswer)
	b := newBackend(t, s.URL, nil)
	asString := json.RawMessage(strconv.Quote(`{"command":"ls -la"}`))
	req := backend.Request{Mode: backend.ModeNative, Set: fullSet(t), Messages: []backend.Message{
		{Role: backend.RoleUser, Text: "task"},
		{Role: backend.RoleAssistant, Calls: []parse.ToolCall{{Name: "exec", Args: asString}}},
		{Role: backend.RoleTool, Tool: "exec", Text: "ok"},
		{Role: backend.RoleUser, Text: "next"},
	}}
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	fn := s.body["messages"].([]any)[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if args, _ := fn["arguments"].(map[string]any); args["command"] != "ls -la" {
		t.Errorf("arguments sent back: %v", fn["arguments"])
	}
}
