// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
)

// FuzzParse reads whatever a model might write. It must never panic or take long; the actions it finds are valid ones of
// the session's tools, in order, at most the limit; and what it tells the model is clean text.
func FuzzParse(f *testing.F) {
	key := `{"tool":"key","args":{"keys":"ctrl+l"},"reason":"why"}`
	for _, text := range []string{
		``, `plain words`, `{"actions":[` + key + `]}`, "```json\n{\"actions\":[" + key + "]}\n```", "prose " + key + " prose",
		`<think>{"actions":[]}</think>{"actions":[` + key + `,]}`, `[` + key + `,` + key + `]`, `{"actions":[{"tool":"exec"}]}`,
		`{"thought":"` + strings.Repeat("é", 700) + `","actions":[` + key + `]}`, `{"a":"\u0000"}`, `{{{{{{{`, "```\n```", `</think>x`,
		// Tool calls written as words (calls.go): Python's form and Gemma's own, whole, cut short and mixed.
		`exec(command='ls', timeout_s=3)`, `exec{command:<|"|>uname -r<|"|>,reason:<|"|>x<|"|>}`, "- click_element(id='a1')\nkey(keys='Return')\nexec{command:<|\"|>ls",
		`exec(command="""a` + "\n" + `b""")`, `exec(command='it\'s', timeout_s=1e3)`, `read_screen_text(region=[1,2,[3],{'a':None}])`, `exec(((((((((`, `exec{<|"|><|"|><|"|>`,
	} {
		f.Add(text, "key", `{"keys":"Tab"}`, 3)
		f.Add(text, "exec", `"{\"command\":\"ls\"}"`, 1)
	}
	for _, args := range []string{``, `null`, `""`, `{}`, `[]`, `"x"`, `{"keys":"a+b+c"}`, `"{\"keys\":\"Tab\",}"`} {
		f.Add("words", "key", args, 3)
	}
	full, err := actions.NewSet(actions.ProfileFull)
	if err != nil {
		f.Fatal(err)
	}
	compact, err := actions.NewSet(actions.ProfileCompact)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, text, tool, args string, max int) {
		max = max%6 - 1 // -1..4: a limit that is not positive means the default
		for _, set := range []*actions.Set{full, compact} {
			reply := Reply{Text: text}
			if tool != "" {
				reply.ToolCalls = []ToolCall{{ID: "x", Name: tool, Args: json.RawMessage(args)}}
				if !json.Valid([]byte(args)) {
					reply.ToolCalls[0].Args = nil // a backend only hands over valid JSON
				}
			}
			res := Parse(set, reply, max)
			limit := max
			if limit < 1 {
				limit = actions.DefaultMaxActions
			}
			if len(res.Actions) > limit || res.Skipped < 0 || res.Dropped < 0 || (res.Source == SourceNone && len(res.Actions) > 0) {
				t.Fatalf("%q %q %q %d: %+v", text, tool, args, max, res)
			}
			for i, it := range res.Actions {
				if it.Action.Args == nil || !set.Has(it.Action.Tool) || it.Index < 1 || (i > 0 && it.Index <= res.Actions[i-1].Index) {
					t.Fatalf("%q: bad action %+v", text, it)
				}
				if utf8.RuneCountInString(it.Action.Reason) > actions.MaxReasonRunes || strings.ContainsAny(it.Action.Reason, "\n\r\x00") {
					t.Fatalf("%q: reason %q", text, it.Action.Reason)
				}
			}
			if utf8.RuneCountInString(res.Thought) > maxThoughtRunes || !utf8.ValidString(res.Thought) {
				t.Fatalf("%q: thought %q", text, res.Thought)
			}
			if len(res.Actions) == 0 && len(res.Failures) == 0 {
				t.Fatalf("%q %q %q: an answer with no action and no failure", text, tool, args)
			}
			for _, msg := range []string{RepairMessage(res, set, max, tool != ""), Feedback(res, max)} {
				if strings.ContainsAny(msg, "\x00\x07\x1b\r") || !utf8.ValidString(msg) {
					t.Fatalf("%q: unclean message %q", text, msg)
				}
			}
		}
	})
}
