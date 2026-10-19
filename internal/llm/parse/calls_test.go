// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package parse

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
)

func callSet(t testing.TB) *actions.Set {
	t.Helper()
	set, err := actions.NewSet(actions.ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// What Gemma 4 wrote on a real llama.cpp server when the server did not recognize its tool-call format.
const gemmaCall = `click_element(id='a3', reason='Click the Open button as requested by the task.')`

func TestACallWrittenAsAFunctionIsRead(t *testing.T) {
	res := Parse(callSet(t), Reply{Text: gemmaCall}, 3)
	if res.Source != SourceCall || !res.Usable() || len(res.Actions) != 1 || len(res.Failures) != 0 {
		t.Fatalf("res = %+v", res)
	}
	a := res.Actions[0].Action
	if a.Tool != "click_element" || a.Reason != "Click the Open button as requested by the task." || string(a.Raw) != `{"id":"a3"}` {
		t.Errorf("action = %+v raw %s", a, a.Raw)
	}
}

func TestFunctionCallForms(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		tool  string
		field string // a substring of the validated arguments
	}{
		"double quotes":        {`exec(command="uname -r")`, "exec", `"command":"uname -r"`},
		"mixed escapes":        {`exec(command='echo \'hi\' "there"\n')`, "exec", `"command":"echo 'hi' \"there\"\n"`},
		"parentheses in text":  {`exec(command='echo (a) (b)')`, "exec", `echo (a) (b)`},
		"a number":             {`exec(command='sleep 1', timeout_s=30)`, "exec", `"timeout_s":30`},
		"a bool":               {`click_element(id='a1', double=True)`, "click_element", `"double":true`},
		"lowercase bool":       {`click_element(id='a1', double=false)`, "click_element", `"id":"a1"`},
		"a float":              {`wait_for(condition='seconds', timeout_s=2.5)`, "wait_for", `"timeout_s":2.5`},
		"a list":               {`read_screen_text(region=[10, 20, 300, 40])`, "read_screen_text", `"region":[10,20,300,40]`},
		"in a code fence":      {"```python\nexec(command='ls')\n```", "exec", `"command":"ls"`},
		"in a bullet":          {"- exec(command='ls')", "exec", `"command":"ls"`},
		"numbered":             {"1. exec(command='ls')", "exec", `"command":"ls"`},
		"back ticks":           {"`exec(command='ls')`", "exec", `"command":"ls"`},
		"labeled":              {"call: exec(command='ls')", "exec", `"command":"ls"`},
		"triple quotes":        {`exec(command="""echo "a"` + "\n" + `echo b""")`, "exec", `echo \"a\"\necho b`},
		"over several lines":   {"type_text(\n  text='hello\\nworld',\n)", "type_text", `"text":"hello\nworld"`},
		"spaces around equals": {`exec( command = 'ls' )`, "exec", `"command":"ls"`},
		"unicode escape":       {`type_text(text='café')`, "type_text", `café`},
		"a trailing semicolon": {`exec(command='ls');`, "exec", `"command":"ls"`},
	} {
		t.Run(name, func(t *testing.T) {
			res := Parse(callSet(t), Reply{Text: tc.text}, 3)
			if res.Source != SourceCall || len(res.Actions) != 1 || len(res.Failures) != 0 {
				t.Fatalf("res = %+v", res)
			}
			a := res.Actions[0].Action
			if a.Tool != tc.tool || !strings.Contains(string(a.Raw), tc.field) {
				t.Errorf("tool %s raw %s, want %s with %s", a.Tool, a.Raw, tc.tool, tc.field)
			}
		})
	}
}

func TestSeveralCallsAndTheWordsAroundThem(t *testing.T) {
	text := "I will open the file and then finish.\nclick_element(id='a3')\nwait_for(condition='screen_idle', timeout_s=3)\ndone(success=True, summary='opened')"
	res := Parse(callSet(t), Reply{Text: text}, 3)
	if res.Source != SourceCall || len(res.Actions) != 3 || res.Thought != "I will open the file and then finish." {
		t.Fatalf("res = %+v", res)
	}
	if res.Actions[0].Index != 1 || res.Actions[2].Action.Tool != "done" {
		t.Errorf("actions = %+v", res.Actions)
	}
	more := text + "\nkey(keys='Return')"
	res = Parse(callSet(t), Reply{Text: more}, 3)
	if len(res.Actions) != 3 || res.Dropped != 1 {
		t.Errorf("the limit holds: %d actions, %d dropped", len(res.Actions), res.Dropped)
	}
}

func TestAMistakeInACallIsToldInTheModelsTerms(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"positional":       {`exec('ls')`, "name=value"},
		"unquoted text":    {`exec(command=ls)`, "text must be written in quotes"},
		"unknown argument": {`exec(command='ls', shade='red')`, "shade"},
		"missing argument": {`exec()`, "command"},
		"twice":            {`exec(command='a', command='b')`, "given twice"},
		"no comma":         {`exec(command='a' timeout_s=3)`, "comma"},
		"bad list":         {`read_screen_text(region=[1, 2, x])`, "JSON"},
		"wrong type":       {`click_element(id=3)`, "id"},
	} {
		t.Run(name, func(t *testing.T) {
			res := Parse(callSet(t), Reply{Text: tc.text}, 3)
			if res.Source != SourceCall || res.Usable() || len(res.Failures) != 1 || !strings.Contains(res.Failures[0].Err.Error(), tc.want) {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func TestToolsNamedInProseAreNotCalls(t *testing.T) {
	for name, text := range map[string]string{
		"mid-sentence":         "To list the files you could use exec(command='ls') in a terminal.",
		"not a tool":           "print(command='ls')",
		"a tool without call":  "exec is the tool I want",
		"not closed":           "exec(command='ls'",
		"quote not closed":     "exec(command='ls)",
		"a name that contains": "myexec(command='ls')",
		"a different bracket":  "exec[command='ls']",
		"empty":                "",
	} {
		t.Run(name, func(t *testing.T) {
			res := Parse(callSet(t), Reply{Text: text}, 3)
			if res.Source != SourceNone || res.Usable() {
				t.Errorf("res = %+v", res)
			}
		})
	}
}

func TestJSONAndNativeCallsComeFirst(t *testing.T) {
	// An answer with JSON in it is a JSON answer, even if a line also looks like a call.
	text := "exec(command='no')\n```json\n{\"actions\":[{\"tool\":\"done\",\"args\":{\"success\":true,\"summary\":\"x\"}}]}\n```"
	if res := Parse(callSet(t), Reply{Text: text}, 3); res.Source != SourceJSON || res.Actions[0].Action.Tool != "done" {
		t.Errorf("res = %+v", res)
	}
	native := Reply{Text: gemmaCall, ToolCalls: []ToolCall{{ID: "1", Name: "done", Args: []byte(`{"success":true,"summary":"x"}`)}}}
	if res := Parse(callSet(t), native, 3); res.Source != SourceNative || res.Actions[0].Action.Tool != "done" {
		t.Errorf("res = %+v", res)
	}
}

func TestACallOfAToolOutsideTheSetIsToldSo(t *testing.T) {
	set, err := actions.NewSet(actions.ProfileCompact)
	if err != nil {
		t.Fatal(err)
	}
	res := Parse(set, Reply{Text: `browser(op='open', url='https://example.org')`}, 3)
	if res.Source != SourceCall || res.Usable() || !strings.Contains(res.Failures[0].Err.Error(), "no tool") {
		t.Errorf("res = %+v", res)
	}
}

func TestValueForms(t *testing.T) {
	for in, want := range map[string]string{
		`'a\\b'`:       `a\b`,
		`"tab\there"`:  "tab\there",
		`'it\'s'`:      "it's",
		`'\q'`:         "q",
		`'trailing\\'`: `trailing\`,
	} {
		v, n, problem := parseValue(in)
		if problem != "" || v != want || n != len(in) {
			t.Errorf("parseValue(%s) = %q, %d, %q; want %q", in, v, n, problem, want)
		}
	}
	for _, in := range []string{``, `'open`, `[1, 2`, `{"a": 1`, `word`} {
		if _, _, problem := parseValue(in); problem == "" {
			t.Errorf("parseValue(%q) should be refused", in)
		}
	}
	if v, _, _ := parseValue(`[True, 'a', None, {"k": False}]`); v == nil {
		t.Error("a list with Python spellings")
	}
}

// What Gemma 4 wrote on a real llama.cpp server for the kernel-version task, when the server did not recognize its format.
const gemmaBrace = `exec{command:<|"|>uname -r<|"|>,cwd:<|"|>/home/cs<|"|>,reason:<|"|>Find the Linux kernel version<|"|>}`

func TestGemmasOwnToolCallFormatIsRead(t *testing.T) {
	res := Parse(callSet(t), Reply{Text: gemmaBrace}, 3)
	if res.Source != SourceCall || len(res.Actions) != 1 || len(res.Failures) != 0 {
		t.Fatalf("res = %+v", res)
	}
	a := res.Actions[0].Action
	if a.Tool != "exec" || a.Reason != "Find the Linux kernel version" || !strings.Contains(string(a.Raw), `"command":"uname -r"`) || !strings.Contains(string(a.Raw), `"cwd":"/home/cs"`) {
		t.Errorf("action = %+v raw %s", a, a.Raw)
	}
}

func TestBraceCallForms(t *testing.T) {
	for name, tc := range map[string]struct{ text, tool, field string }{
		"quotes inside the text":   {`type_text{text:<|"|>say "hi" and don't go<|"|>}`, "type_text", `say \"hi\" and don't go`},
		"braces and parens inside": {`exec{command:<|"|>echo {a} (b) [c]<|"|>}`, "exec", `echo {a} (b) [c]`},
		"numbers and bools":        {`wait_for{condition:<|"|>seconds<|"|>,timeout_s:3}`, "wait_for", `"timeout_s":3`},
		"a bool":                   {`click_element{id:<|"|>a1<|"|>,double:true}`, "click_element", `"double":true`},
		"a list":                   {`read_screen_text{region:[1, 2, 30, 40]}`, "read_screen_text", `"region":[1,2,30,40]`},
		"JSON with quoted keys":    {`exec{"command": "ls -la"}`, "exec", `"command":"ls -la"`},
		"equals instead of colons": {`exec{command='ls'}`, "exec", `"command":"ls"`},
		"over several lines":       {"exec{\n  command:<|\"|>ls\nls -a<|\"|>\n}", "exec", `ls\nls -a`},
		"after words":              {"I will run it.\nexec{command:<|\"|>ls<|\"|>}", "exec", `"command":"ls"`},
		"two calls":                {"key{keys:<|\"|>ctrl+l<|\"|>}\ntype_text{text:<|\"|>example.org<|\"|>}", "key", `"keys":"ctrl+l"`},
	} {
		t.Run(name, func(t *testing.T) {
			res := Parse(callSet(t), Reply{Text: tc.text}, 3)
			if res.Source != SourceCall || len(res.Actions) == 0 || len(res.Failures) != 0 {
				t.Fatalf("res = %+v", res)
			}
			a := res.Actions[0].Action
			if a.Tool != tc.tool || !strings.Contains(string(a.Raw), tc.field) {
				t.Errorf("tool %s raw %s, want %s with %s", a.Tool, a.Raw, tc.tool, tc.field)
			}
		})
	}
	if res := Parse(callSet(t), Reply{Text: "key{keys:<|\"|>ctrl+l<|\"|>}\ntype_text{text:<|\"|>example.org<|\"|>}"}, 3); len(res.Actions) != 2 {
		t.Errorf("two calls: %+v", res)
	}
}

func TestBraceCallMistakes(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"not closed":        {`exec{command:<|"|>ls<|"|>`, ""},
		"string not closed": {`exec{command:<|"|>ls}`, ""},
		"wrong closer":      {`exec{command:<|"|>ls<|"|>)`, ""},
		"no value":          {`exec{command:}`, "command"},
		"unknown argument":  {`exec{command:<|"|>ls<|"|>,shade:<|"|>red<|"|>}`, "shade"},
		"bare word":         {`exec{command:ls}`, "text must be written in quotes"},
		"prose with braces": {`To run it, write exec{command:...} yourself.`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			res := Parse(callSet(t), Reply{Text: tc.text}, 3)
			if tc.want == "" {
				if res.Source != SourceNone || res.Usable() {
					t.Errorf("res = %+v", res)
				}
				return
			}
			if res.Source != SourceCall || res.Usable() || len(res.Failures) != 1 || !strings.Contains(res.Failures[0].Err.Error(), tc.want) {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}
