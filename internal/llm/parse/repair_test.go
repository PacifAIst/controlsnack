// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/llm/actions"
)

func contains(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("%q is missing from:\n%s", p, text)
		}
	}
}

func TestRepairMessageForNativeCalls(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	res := Parse(full, Reply{ToolCalls: []ToolCall{
		call("exec", `{"command":"ls","timeout_s":900}`),
		call("mouse", `{"op":"drag","x":1,"y":2}`),
		call("key", `{"keys":"Tab"}`),
		call("key", `{"keys":"Tab"}`),
		call("key", `{"keys":"Tab"}`),
	}}, 3)
	msg := RepairMessage(res, full, 3, true)
	contains(t, msg,
		"could not be used, so nothing was done",
		"- action 1 (exec): args.timeout_s: ", "600",
		"- action 2 (mouse): args.to_x: ",
		"1 valid action after the first mistake was not run either",
		"2 actions beyond the limit were dropped",
		"tool calls, at most 3 actions",
		"The tools are: screenshot, read_accessibility_tree")
	if strings.Contains(msg, "JSON object") {
		t.Errorf("native backends are not asked for JSON:\n%s", msg)
	}
}

func TestRepairMessageForJSONAnswers(t *testing.T) {
	compact := setFor(t, actions.ProfileCompact)
	res := Parse(compact, Reply{Text: "I will open the browser."}, 2)
	msg := RepairMessage(res, compact, 2, false)
	contains(t, msg, "- the answer: answer: has no tool call and no JSON with actions in it",
		`one JSON object and nothing else, with at most 2 actions`, `"actions": [{"tool"`)
	if !strings.Contains(msg, "exec, ") || strings.Contains(msg, "browser") {
		t.Errorf("only the session's tools are listed:\n%s", msg)
	}
	one := RepairMessage(res, compact, 1, false)
	contains(t, one, "at most 1 action:")
	if strings.Contains(one, "1 actions") {
		t.Errorf("a plural for one:\n%s", one)
	}
}

func TestFeedbackNextToTheResultsOfWhatRan(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	good := call("key", `{"keys":"Tab"}`)
	if got := Feedback(Parse(full, Reply{ToolCalls: []ToolCall{good, good}}, 3), 3); got != "" {
		t.Errorf("nothing to say about a clean answer: %q", got)
	}
	res := Parse(full, Reply{ToolCalls: []ToolCall{good, call("exec", `{}`), good, good, good}}, 3)
	got := Feedback(res, 3)
	contains(t, got, "Not run, because it was not valid:", "- action 2 (exec): args: ", "command",
		"1 valid action after the first mistake was not run", "2 actions beyond the limit of 3 in one answer were dropped")
	if n := strings.Count(got, "\n"); n != 3 {
		t.Errorf("one line for each thing, %d breaks:\n%s", n, got)
	}
}

// What goes back to the model is clean text: the model's own words in it are one line each and bounded.
func TestMessagesAreCleanText(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	res := Parse(full, Reply{ToolCalls: []ToolCall{
		call("format\n\x1b[2Jdisk", `{}`),
		call("exec", "{\"command\":\"ls\",\"evil\\nkey\\u0007\":1}"),
	}}, 3)
	for _, msg := range []string{RepairMessage(res, full, 3, true), Feedback(res, 3)} {
		if strings.ContainsAny(msg, "\x00\x07\x1b\r") {
			t.Errorf("control characters in:\n%q", msg)
		}
		for _, line := range strings.Split(msg, "\n") {
			if len([]rune(line)) > 2000 {
				t.Errorf("a line of %d characters", len([]rune(line)))
			}
		}
	}
}
