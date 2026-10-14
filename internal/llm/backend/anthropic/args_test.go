// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package anthropic

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

// A call that reached the history with its arguments as a string that holds the object (as OpenAI-compatible servers send
// them) is sent back as the input object of a tool_use block.
func TestTheArgumentsOfAnEarlierCallComeBackAsTheInput(t *testing.T) {
	s := serve(t, 200, okAnswer, nil)
	b := newBackend(t, s.URL, nil)
	asString := json.RawMessage(strconv.Quote(`{"command":"ls -la"}`))
	req := backend.Request{Mode: backend.ModeNative, Set: fullSet(t), Messages: []backend.Message{
		{Role: backend.RoleUser, Text: "task"},
		{Role: backend.RoleAssistant, Calls: []parse.ToolCall{{ID: "toolu_1", Name: "exec", Args: asString}}},
		{Role: backend.RoleTool, CallID: "toolu_1", Tool: "exec", Text: "ok"},
		{Role: backend.RoleUser, Text: "next"},
	}}
	if _, err := b.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	blocks := s.body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	use := blocks[len(blocks)-1].(map[string]any)
	if input, _ := use["input"].(map[string]any); use["type"] != "tool_use" || input["command"] != "ls -la" {
		t.Errorf("input sent back: %v", use)
	}
}
