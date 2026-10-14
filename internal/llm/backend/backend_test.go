// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package backend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
	"github.com/PacifAIst/controlsnack-local/internal/llm/parse"
)

func fullSet(t *testing.T) *actions.Set {
	t.Helper()
	s, err := actions.NewSet(actions.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func shot() Image { return Image{MIME: MIMEJPEG, Data: []byte{0xff, 0xd8, 0xff}} }

func TestValidateAcceptsAConversation(t *testing.T) {
	set := fullSet(t)
	call := parse.ToolCall{ID: "c1", Name: "exec", Args: json.RawMessage(`{"command":"ls"}`)}
	for name, r := range map[string]Request{
		"a system and a user message": {Mode: ModeJSON, Messages: []Message{{Role: RoleSystem, Text: "s"}, {Role: RoleUser, Text: "task", Images: []Image{shot()}}}},
		"a native conversation": {Mode: ModeNative, Set: set, Messages: []Message{
			{Role: RoleSystem, Text: "s"}, {Role: RoleUser, Text: "task"},
			{Role: RoleAssistant, Calls: []parse.ToolCall{call}}, {Role: RoleTool, CallID: "c1", Tool: "exec", Text: "{}"},
			{Role: RoleUser, Text: "next", Images: []Image{shot()}},
		}},
		"two results for two calls": {Mode: ModeNative, Set: set, Messages: []Message{
			{Role: RoleUser, Text: "task"}, {Role: RoleAssistant, Calls: []parse.ToolCall{call, call}},
			{Role: RoleTool, CallID: "c1"}, {Role: RoleTool, Tool: "exec"},
		}},
		"structured":        {Mode: ModeStructured, Set: set, Messages: []Message{{Role: RoleUser, Text: "task"}}},
		"no system message": {Mode: ModeJSON, Messages: []Message{{Role: RoleUser, Text: "task"}}},
	} {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateRefusesWhatABackendCannotUse(t *testing.T) {
	set := fullSet(t)
	user := Message{Role: RoleUser, Text: "task"}
	for _, c := range []struct {
		name string
		r    Request
		word string
	}{
		{"a call that nothing answers, then the user", Request{Mode: ModeNative, Set: set, Messages: []Message{user,
			{Role: RoleAssistant, Calls: []parse.ToolCall{{ID: "a", Name: "exec"}, {ID: "b", Name: "key"}}}, {Role: RoleTool, CallID: "a"}, user}}, "1 of the tool calls"},
		{"a call that nothing answers, then the assistant", Request{Mode: ModeNative, Set: set, Messages: []Message{user,
			{Role: RoleAssistant, Calls: []parse.ToolCall{{ID: "a", Name: "exec"}}}, {Role: RoleAssistant, Text: "?"}, user}}, "1 of the tool calls"},
		{"a call that nothing answers, at the end", Request{Mode: ModeNative, Set: set, Messages: []Message{user,
			{Role: RoleAssistant, Calls: []parse.ToolCall{{ID: "a", Name: "exec"}, {ID: "b", Name: "key"}}}, {Role: RoleTool, CallID: "a"}}}, "no tool message"},
		{"more results than calls", Request{Mode: ModeNative, Set: set, Messages: []Message{user,
			{Role: RoleAssistant, Calls: []parse.ToolCall{{ID: "a", Name: "exec"}}}, {Role: RoleTool, CallID: "a"}, {Role: RoleTool, CallID: "b"}}}, "more tool messages"},
		{"a result after an assistant message that made no call", Request{Mode: ModeNative, Set: set, Messages: []Message{user,
			{Role: RoleAssistant, Text: "no calls"}, {Role: RoleTool, CallID: "a"}}}, "more tool messages"},
		{"no messages", Request{Mode: ModeJSON}, "no messages"},
		{"too many messages", Request{Mode: ModeJSON, Messages: make([]Message, 401)}, "more than"},
		{"an unknown mode", Request{Mode: "chat", Messages: []Message{user}}, "native, structured or json"},
		{"native without tools", Request{Mode: ModeNative, Messages: []Message{user}}, "tools of the session"},
		{"structured without tools", Request{Mode: ModeStructured, Messages: []Message{user}}, "tools of the session"},
		{"a second system message", Request{Mode: ModeJSON, Messages: []Message{{Role: RoleSystem}, {Role: RoleSystem}, user}}, "comes first"},
		{"a system message late", Request{Mode: ModeJSON, Messages: []Message{user, {Role: RoleSystem}, user}}, "comes first"},
		{"an unknown role", Request{Mode: ModeJSON, Messages: []Message{{Role: "narrator"}, user}}, "role"},
		{"an image in a system message", Request{Mode: ModeJSON, Messages: []Message{{Role: RoleSystem, Images: []Image{shot()}}, user}}, "only a user message"},
		{"an image in an assistant message", Request{Mode: ModeJSON, Messages: []Message{user, {Role: RoleAssistant, Images: []Image{shot()}}, user}}, "only a user message"},
		{"a GIF", Request{Mode: ModeJSON, Messages: []Message{{Role: RoleUser, Images: []Image{{MIME: "image/gif", Data: []byte{1}}}}}}, "JPEG, PNG or WebP"},
		{"an empty image", Request{Mode: ModeJSON, Messages: []Message{{Role: RoleUser, Images: []Image{{MIME: MIMEPNG}}}}}, "no data"},
		{"five images", Request{Mode: ModeJSON, Messages: []Message{{Role: RoleUser, Images: []Image{shot(), shot(), shot(), shot(), shot()}}}}, "images"},
		{"calls outside native mode", Request{Mode: ModeJSON, Messages: []Message{user, {Role: RoleAssistant, Calls: []parse.ToolCall{{Name: "exec"}}}, user}}, "native mode"},
		{"a tool result with no call", Request{Mode: ModeNative, Set: set, Messages: []Message{user, {Role: RoleAssistant}, {Role: RoleTool}}}, "which call"},
		{"a tool result out of place", Request{Mode: ModeNative, Set: set, Messages: []Message{user, {Role: RoleTool, CallID: "x"}}}, "follows the assistant"},
		{"a tool result first", Request{Mode: ModeNative, Set: set, Messages: []Message{{Role: RoleTool, CallID: "x"}}}, "follows the assistant"},
		{"ends with the assistant", Request{Mode: ModeJSON, Messages: []Message{user, {Role: RoleAssistant, Text: "done"}}}, "nothing to answer"},
	} {
		err := c.r.Validate()
		if err == nil || !strings.Contains(err.Error(), c.word) {
			t.Errorf("%s: %v, want a refusal mentioning %q", c.name, err, c.word)
		}
	}
}

func TestActionLimit(t *testing.T) {
	if (Request{}).ActionLimit() != actions.DefaultMaxActions || (Request{MaxActions: -1}).ActionLimit() != actions.DefaultMaxActions || (Request{MaxActions: 5}).ActionLimit() != 5 {
		t.Error("ActionLimit")
	}
}

func TestErrorKindsAreTheHTTPClients(t *testing.T) {
	e := &Error{Kind: KindRateLimited, Backend: "b", Status: 429}
	if e.Kind != KindRateLimited || KindUnavailable == KindTimeout || e.Error() == "" {
		t.Errorf("%v", e)
	}
	for _, k := range []Kind{KindUnavailable, KindTimeout, KindRateLimited, KindServer, KindAuth, KindBadRequest, KindNotFound, KindProtocol} {
		if k.String() == "unknown" {
			t.Errorf("%d has no name", k)
		}
	}
}
