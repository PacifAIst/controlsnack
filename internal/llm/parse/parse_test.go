// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
)

func setFor(t *testing.T, profile string) *actions.Set {
	t.Helper()
	s, err := actions.NewSet(profile)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(name, args string) ToolCall {
	return ToolCall{ID: "call_" + name, Name: name, Args: json.RawMessage(args)}
}

func tools(res Result) []string {
	var names []string
	for _, it := range res.Actions {
		names = append(names, it.Action.Tool)
	}
	return names
}

func equal(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func problemText(res Result) string {
	var parts []string
	for _, f := range res.Failures {
		parts = append(parts, f.Err.Error())
	}
	return strings.Join(parts, " | ")
}

func TestNativeCalls(t *testing.T) {
	full := setFor(t, actions.ProfileFull)

	res := Parse(full, Reply{ToolCalls: []ToolCall{call("exec", `{"command":"uname -r"}`)}}, 3)
	if res.Source != SourceNative || !res.Usable() || len(res.Failures) != 0 || res.Actions[0].CallID != "call_exec" || res.Actions[0].Index != 1 {
		t.Fatalf("%+v", res)
	}
	if args := res.Actions[0].Action.Args.(*actions.ExecArgs); args.Command != "uname -r" || args.TimeoutS != 60 {
		t.Errorf("%+v", args)
	}

	// OpenAI-compatible servers send the arguments as a string that holds the JSON.
	stringified := `"{\"command\":\"ls -l\",\"timeout_s\":30}"`
	res = Parse(full, Reply{ToolCalls: []ToolCall{call("exec", stringified)}}, 3)
	if !res.Usable() || res.Actions[0].Action.Args.(*actions.ExecArgs).TimeoutS != 30 {
		t.Fatalf("stringified arguments: %+v %s", res, problemText(res))
	}
	// ... possibly with the mistake that is forgiven.
	res = Parse(full, Reply{ToolCalls: []ToolCall{call("exec", `"{\"command\":\"ls\",}"`)}}, 3)
	if !res.Usable() {
		t.Errorf("a trailing comma in stringified arguments: %s", problemText(res))
	}

	// No arguments: a string that is empty, null, nothing.
	for _, args := range []string{``, `null`, `""`, `"{}"`, `{}`, `"null"`} {
		if res := Parse(full, Reply{ToolCalls: []ToolCall{call("screenshot", args)}}, 3); !res.Usable() {
			t.Errorf("screenshot with %q: %s", args, problemText(res))
		}
	}
	if res := Parse(full, Reply{ToolCalls: []ToolCall{call("exec", `""`)}}, 3); res.Usable() || !strings.Contains(problemText(res), "command") {
		t.Errorf("exec without arguments is missing its command: %s", problemText(res))
	}

	for name, args := range map[string]string{"not json": `"not json at all"`, "an array": `[1,2]`, "a number": `7`, "an array in a string": `"[1]"`} {
		res := Parse(full, Reply{ToolCalls: []ToolCall{call("exec", args)}}, 3)
		if res.Usable() || len(res.Failures) != 1 || res.Failures[0].Index != 1 || res.Failures[0].Err.Code != actions.CodeInvalidArgs {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestSeveralCallsAndLimits(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	good := func(i string) ToolCall { return ToolCall{ID: i, Name: "key", Args: json.RawMessage(`{"keys":"Tab"}`)} }

	res := Parse(full, Reply{ToolCalls: []ToolCall{good("a"), good("b")}}, 3)
	if len(res.Actions) != 2 || res.Actions[1].CallID != "b" || res.Actions[1].Index != 2 || res.Dropped != 0 {
		t.Errorf("%+v", res)
	}
	// More than the limit: the first ones run, the rest are dropped without being looked at.
	res = Parse(full, Reply{ToolCalls: []ToolCall{good("a"), good("b"), good("c"), good("d"), call("exec", `{"nonsense":1}`)}}, 3)
	if len(res.Actions) != 3 || res.Dropped != 2 || len(res.Failures) != 0 {
		t.Errorf("%+v", res)
	}
	// An invalid action stops the run: the valid ones after it are skipped, the ones before it run.
	res = Parse(full, Reply{ToolCalls: []ToolCall{good("a"), call("mouse", `{"op":"drag","x":1,"y":2}`), good("c"), good("d")}}, 3)
	if len(res.Actions) != 1 || res.Actions[0].CallID != "a" || len(res.Failures) != 1 || res.Failures[0].Index != 2 || res.Skipped != 1 || res.Dropped != 1 {
		t.Errorf("%+v", res)
	}
	if !strings.Contains(problemText(res), "to_y") {
		t.Errorf("the problem is named: %s", problemText(res))
	}
	// The first one invalid: nothing to run.
	res = Parse(full, Reply{ToolCalls: []ToolCall{call("exec", `{}`), good("b")}}, 3)
	if res.Usable() || res.Skipped != 1 || len(res.Failures) != 1 {
		t.Errorf("%+v", res)
	}
	// Every invalid action is reported, not only the first.
	res = Parse(full, Reply{ToolCalls: []ToolCall{call("exec", `{}`), call("scroll", `{"x":1}`)}}, 3)
	if len(res.Failures) != 2 || res.Failures[0].Index != 1 || res.Failures[1].Index != 2 {
		t.Errorf("%+v", res)
	}
	// A limit that is not positive means the default.
	if res := Parse(full, Reply{ToolCalls: []ToolCall{good("a"), good("b"), good("c"), good("d")}}, 0); len(res.Actions) != actions.DefaultMaxActions || res.Dropped != 1 {
		t.Errorf("%+v", res)
	}
	// A reason that runs on is cut, not refused.
	long := `{"keys":"Tab","reason":"` + strings.Repeat("é", 500) + `"}`
	if res := Parse(full, Reply{ToolCalls: []ToolCall{call("key", long)}}, 3); !res.Usable() || utf8.RuneCountInString(res.Actions[0].Action.Reason) != actions.MaxReasonRunes {
		t.Errorf("a long reason in native arguments: %s", problemText(res))
	}
}

func TestToolsOutsideTheProfileAreUnknown(t *testing.T) {
	compact := setFor(t, actions.ProfileCompact)
	res := Parse(compact, Reply{ToolCalls: []ToolCall{call("browser", `{"op":"tabs"}`)}}, 3)
	if res.Usable() || res.Failures[0].Err.Code != actions.CodeUnknownTool || strings.Contains(problemText(res), "scroll") || !strings.Contains(problemText(res), "exec") {
		t.Errorf("%s", problemText(res))
	}
	res = Parse(compact, Reply{ToolCalls: []ToolCall{call("format_disk", `{}`)}}, 3)
	if res.Failures[0].Err.Code != actions.CodeUnknownTool {
		t.Errorf("%s", problemText(res))
	}
}

func TestWordsNextToNativeCalls(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	calls := []ToolCall{call("key", `{"keys":"Tab"}`), call("key", `{"keys":"Return"}`)}

	res := Parse(full, Reply{Text: "I will press Tab and then Return.", ToolCalls: calls}, 3)
	if res.Thought != "I will press Tab and then Return." || res.Actions[0].Action.Reason != "I will press Tab and then Return." || res.Actions[1].Action.Reason != "" {
		t.Errorf("the words become the reason of the first action: %+v", res)
	}
	// A reason of its own is kept.
	own := []ToolCall{call("key", `{"keys":"Tab","reason":"move on"}`)}
	if res := Parse(full, Reply{Text: "Some words.", ToolCalls: own}, 3); res.Actions[0].Action.Reason != "move on" || res.Thought != "Some words." {
		t.Errorf("%+v", res)
	}
	// Reasoning is not part of it; long words are cut; lines are one line.
	res = Parse(full, Reply{Text: "<think>hmm, the screen shows\na dialog</think>\nClose the dialog first.\nThen go on.", ToolCalls: calls[:1]}, 3)
	if res.Actions[0].Action.Reason != "Close the dialog first. Then go on." {
		t.Errorf("reason %q", res.Actions[0].Action.Reason)
	}
	res = Parse(full, Reply{Text: strings.Repeat("word ", 400), ToolCalls: calls[:1]}, 3)
	if n := utf8.RuneCountInString(res.Actions[0].Action.Reason); n > actions.MaxReasonRunes || n < 200 {
		t.Errorf("reason of %d characters", n)
	}
	if n := utf8.RuneCountInString(res.Thought); n > maxThoughtRunes {
		t.Errorf("thought of %d characters", n)
	}
	// Nothing runs: no reason is invented.
	res = Parse(full, Reply{Text: "I will try.", ToolCalls: []ToolCall{call("exec", `{}`)}}, 3)
	if res.Usable() || res.Thought != "I will try." {
		t.Errorf("%+v", res)
	}
}

const key = `{"tool":"key","args":{"keys":"ctrl+l"},"reason":"focus the address bar"}`

func TestJSONAnswers(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	envelope := `{"thought":"Open the address bar.","actions":[` + key + `,{"tool":"type_text","args":{"text":"example.com\n"}}]}`
	for name, text := range map[string]string{
		"the whole text":        envelope,
		"white space around":    "\n\n  " + envelope + "  \n",
		"a fenced block":        "Sure, here is what I do:\n```json\n" + envelope + "\n```\nThat should work.",
		"a fence without lang":  "```\n" + envelope + "\n```",
		"an upper case fence":   "```JSON\n" + envelope + "\n```",
		"in prose":              "I will use the keyboard. " + envelope + " Let me know.",
		"after a placeholder":   "First {think} about it, then do {this}: " + envelope,
		"trailing commas":       `{"thought":"Open the address bar.","actions":[{"tool":"key","args":{"keys":"ctrl+l",},"reason":"focus the address bar",},{"tool":"type_text","args":{"text":"example.com\n"}},],}`,
		"after another object":  `{"note":"not it"} ` + envelope,
		"a reasoning block":     "<think>Maybe " + `{"actions":[{"tool":"done","args":{"success":true,"summary":"x"}}]}` + " would do?</think>\n" + envelope,
		"a cut-off first fence": "```json\n{\"actions\": [\n```\n" + "```json\n" + envelope + "\n```",
	} {
		res := Parse(full, Reply{Text: text}, 3)
		if res.Source != SourceJSON || !equal(tools(res), []string{"key", "type_text"}) || len(res.Failures) != 0 {
			t.Errorf("%s: %+v %s", name, res, problemText(res))
			continue
		}
		if res.Thought != "Open the address bar." || res.Actions[0].Action.Reason != "focus the address bar" {
			t.Errorf("%s: thought %q reason %q", name, res.Thought, res.Actions[0].Action.Reason)
		}
		if got := res.Actions[1].Action.Args.(*actions.TypeTextArgs).Text; got != "example.com\n" {
			t.Errorf("%s: text %q", name, got)
		}
	}
}

func TestOtherShapesOfActions(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	for name, text := range map[string]string{
		"one action":              key,
		"an array of actions":     `[` + key + `]`,
		"an array in a fence":     "```json\n[" + key + "]\n```",
		"one action with prose":   "Pressing it: " + key,
		"args left out":           `{"tool":"screenshot"}`,
		"args as a string":        `{"tool":"key","args":"{\"keys\":\"Tab\"}"}`,
		"unknown keys are fine":   `{"observation":"a dialog","actions":[{"tool":"screenshot","id":7}],"confidence":0.9}`,
		"a brace inside a string": `The answer {"actions":[{"tool":"type_text","args":{"text":"a } b { c ] ["}}]} is above.`,
	} {
		res := Parse(full, Reply{Text: text}, 3)
		if res.Source != SourceJSON || len(res.Actions) != 1 || len(res.Failures) != 0 {
			t.Errorf("%s: %+v %s", name, res, problemText(res))
		}
	}
	res := Parse(full, Reply{Text: `{"actions":[{"tool":"type_text","args":{"text":"a } b { c ] ["}}]}`}, 3)
	if got := res.Actions[0].Action.Args.(*actions.TypeTextArgs).Text; got != "a } b { c ] [" {
		t.Errorf("text %q", got)
	}
}

func TestJSONAnswersThatAreWrong(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	for _, c := range []struct {
		name, text string
		path, word string
	}{
		{"no actions", `{"thought":"hm","actions":[]}`, "actions", "empty"},
		{"a tool is missing", `{"actions":[{"args":{}}]}`, "tool", "missing"},
		{"an action that is not an object", `{"actions":["screenshot"]}`, "action", "object"},
		{"a tool that is not a string", `{"actions":[{"tool":7,"args":{}}]}`, "tool", "missing"},
		{"args that are not an object", `{"actions":[{"tool":"exec","args":[1]}]}`, "args", "object"},
		{"exec without a command", `{"actions":[{"tool":"exec","args":{}}]}`, "args", "command"},
		{"an unknown tool", `{"actions":[{"tool":"rm_rf","args":{}}]}`, "tool", "rm_rf"},
	} {
		res := Parse(full, Reply{Text: c.text}, 3)
		if res.Usable() || len(res.Failures) == 0 {
			t.Errorf("%s: %+v", c.name, res)
			continue
		}
		found := false
		for _, p := range res.Failures[0].Err.Problems {
			found = found || (p.Path == c.path || strings.HasSuffix(p.Path, "."+c.path)) && strings.Contains(p.Message, c.word)
		}
		if !found {
			t.Errorf("%s: want a problem at %s mentioning %q: %s", c.name, c.path, c.word, problemText(res))
		}
	}
}

func TestLimitsOfJSONAnswers(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	shot := `{"tool":"screenshot","args":{}}`
	res := Parse(full, Reply{Text: `{"actions":[` + strings.Join([]string{shot, shot, shot, shot, shot}, ",") + `]}`}, 3)
	if len(res.Actions) != 3 || res.Dropped != 2 {
		t.Errorf("%+v", res)
	}
	// The thought and the reason are cut to their limits; that is not a mistake.
	long := strings.Repeat("é", 900)
	res = Parse(full, Reply{Text: `{"thought":"` + long + `","actions":[{"tool":"screenshot","args":{},"reason":"` + long + `"}]}`}, 3)
	if !res.Usable() || utf8.RuneCountInString(res.Thought) != maxThoughtRunes || utf8.RuneCountInString(res.Actions[0].Action.Reason) != actions.MaxReasonRunes {
		t.Errorf("thought %d, reason %d", utf8.RuneCountInString(res.Thought), utf8.RuneCountInString(res.Actions[0].Action.Reason))
	}
	// Whole numbers written with a fraction are integers.
	res = Parse(full, Reply{Text: `{"actions":[{"tool":"mouse","args":{"op":"click","x":640.0,"y":400.0}}]}`}, 3)
	if !res.Usable() || res.Actions[0].Action.Args.(*actions.MouseArgs).X != 640 {
		t.Errorf("%+v %s", res, problemText(res))
	}
}

func TestAnswersWithoutActions(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	for name, text := range map[string]string{
		"prose":               "I think the task is finished. The file was created.",
		"empty":               "",
		"blank":               " \n\t ",
		"an unrelated object": `The settings are {"width": 1280, "height": 800}.`,
		"broken json":         `{"actions": [{"tool": "exec", "args": {"command": "ls"}`,
		"only reasoning":      "<think>I should look at the screen first</think>",
		"unfinished thinking": "<think>I should look at " + key,
		"an unclosed fence":   "```json\n" + `{"actions":[{"tool":"screenshot"` + "\n",
		"a lone array":        `[1, 2, 3]`,
	} {
		res := Parse(full, Reply{Text: text}, 3)
		if res.Usable() || res.Source != SourceNone || len(res.Failures) != 1 || res.Failures[0].Index != 0 {
			t.Errorf("%s: %+v", name, res)
		}
	}
	res := Parse(full, Reply{Text: "I think the task is finished."}, 3)
	if res.Thought != "I think the task is finished." || !strings.Contains(problemText(res), "no tool call") {
		t.Errorf("%+v", res)
	}
	if res := Parse(full, Reply{}, 3); !strings.Contains(problemText(res), "empty") {
		t.Errorf("%s", problemText(res))
	}
}

// An answer with many braces costs no more than a moment. (A pile of 300000 unclosed braces took a minute when every
// brace was tried as the start of an object.)
func TestParsingIsBounded(t *testing.T) {
	full := setFor(t, actions.ProfileFull)
	if res := Parse(full, Reply{Text: strings.Repeat("{", 10) + key}, 3); !res.Usable() {
		t.Error("a few unclosed braces do not hide the action")
	}
	begin := time.Now()
	res := Parse(full, Reply{Text: strings.Repeat("{", 300000) + key}, 3)
	if res.Usable() {
		t.Error("a pile of unclosed braces hides the action: only the first braces are tried")
	}
	// Linear it takes a moment (seconds under the race detector on a busy machine); the quadratic way took a minute without it.
	if took := time.Since(begin); took > 30*time.Second {
		t.Errorf("a pile of braces took %s", took)
	}
	res = Parse(full, Reply{Text: strings.Repeat(`{"a":1} `, 100000) + `{"actions":[{"tool":"screenshot"}]}`}, 3)
	if res.Usable() {
		t.Errorf("only the first %d objects of an answer are tried", maxCandidates)
	}
	res = Parse(full, Reply{Text: "```\n" + strings.Repeat("x", 600000)}, 3)
	if res.Usable() {
		t.Error("nothing here")
	}
}

func TestObjectNormalizesTheArgumentsOfACall(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"object":             {`{"a":1,"b":[2]}`, `{"a":1,"b":[2]}`},
		"object with spaces": {"  {\"a\": 1}\n", `{"a": 1}`},
		"string holding one": {`"{\"a\":1,\"z\":\"x\"}"`, `{"a":1,"z":"x"}`},
		"string with spaces": {`" {\"a\":1} "`, `{"a":1}`},
		"empty":              {``, `{}`},
		"null":               {`null`, `{}`},
		"empty string":       {`""`, `{}`},
		"string of words":    {`"not an object"`, `{}`},
		"string of an array": {`"[1,2]"`, `{}`},
		"an array":           {`[1,2]`, `{}`},
		"a number":           {`7`, `{}`},
		"broken":             {`{"a":`, `{}`},
		"broken string":      {`"abc`, `{}`},
		"string of a string": {`"\"{}\""`, `{}`},
	} {
		if got := string(Object(json.RawMessage(tc.in))); got != tc.want {
			t.Errorf("%s: Object(%s) = %s, want %s", name, tc.in, got, tc.want)
		}
	}
}

func TestOutcomesAnswerEveryCall(t *testing.T) {
	set, err := actions.NewSet(actions.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	call := func(id, name, args string) ToolCall { return ToolCall{ID: id, Name: name, Args: json.RawMessage(args)} }
	calls := []ToolCall{
		call("1", "exec", `{"command":"ls"}`),
		call("2", "key", `{}`),                              // invalid: keys is required
		call("3", "exec", `{"command":"pwd"}`),              // valid, but after a mistake
		call("4", "done", `{"success":true,"summary":"x"}`), // beyond the limit of 3
		call("5", "done", `{"success":true,"summary":"x"}`),
	}
	res := Parse(set, Reply{ToolCalls: calls}, 3)
	got := Outcomes(calls, res)
	want := []Disposition{DispValid, DispInvalid, DispNotRun, DispDropped, DispDropped}
	if len(got) != len(want) {
		t.Fatalf("%d outcomes for %d calls", len(got), len(calls))
	}
	for i, o := range got {
		if o.Disposition != want[i] || o.Call.ID != calls[i].ID {
			t.Errorf("call %d: %+v, want %v", i+1, o.Disposition, want[i])
		}
	}
	if got[0].Item == nil || got[0].Item.Action.Tool != "exec" || got[0].Failure != nil {
		t.Errorf("a valid call carries its item: %+v", got[0])
	}
	if got[1].Failure == nil || got[1].Failure.Err == nil || got[1].Item != nil {
		t.Errorf("an invalid call carries its failure: %+v", got[1])
	}
	if got[2].Item != nil || got[2].Failure != nil {
		t.Errorf("a call that is not run carries neither: %+v", got[2])
	}

	all := Parse(set, Reply{ToolCalls: calls[:1]}, 3)
	if o := Outcomes(calls[:1], all); len(o) != 1 || o[0].Disposition != DispValid {
		t.Errorf("outcomes = %+v", o)
	}
	if Outcomes(nil, Parse(set, Reply{Text: `{"actions":[{"tool":"done","args":{"success":true,"summary":"x"}}]}`}, 3)) != nil {
		t.Error("an answer without native calls has nothing to answer")
	}
}
