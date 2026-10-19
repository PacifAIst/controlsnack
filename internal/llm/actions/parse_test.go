// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
)

func parse(t *testing.T, tool, args string) Action {
	t.Helper()
	a, err := Parse(tool, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", tool, args, err)
	}
	if a.Tool != tool || a.Args == nil || a.Args.Tool() != tool {
		t.Fatalf("%s: %+v", tool, a)
	}
	return a
}

func intp(n int) *int { return &n }

func TestParseAcceptsValidArguments(t *testing.T) {
	for _, c := range []struct {
		tool, args string
		want       Args
	}{
		{ToolScreenshot, ``, &ScreenshotArgs{}},
		{ToolScreenshot, `null`, &ScreenshotArgs{}},
		{ToolScreenshot, `{}`, &ScreenshotArgs{}},
		{ToolReadTree, `{}`, &ReadTreeArgs{Scope: "active_window", MaxNodes: 150}},
		{ToolReadTree, `{"scope":"desktop","query":"Save","max_nodes":50}`, &ReadTreeArgs{Scope: "desktop", Query: "Save", MaxNodes: 50}},
		{ToolClickElement, `{"id":"a17"}`, &ClickElementArgs{ID: "a17", Button: "left"}},
		{ToolClickElement, `{"id":"o3","button":"right","double":true}`, &ClickElementArgs{ID: "o3", Button: "right", Double: true}},
		{ToolSetText, `{"id":"w4","text":"héllo 日本語","submit":true}`, &SetTextArgs{ID: "w4", Text: "héllo 日本語", Submit: true}},
		{ToolSetText, `{"id":"a1","text":""}`, &SetTextArgs{ID: "a1"}},
		{ToolReadScreenText, `{}`, &ReadScreenTextArgs{}},
		{ToolReadScreenText, `{"region":[10,20,300,40]}`, &ReadScreenTextArgs{Region: &[4]int{10, 20, 300, 40}}},
		{ToolReadScreenText, `{"region":[10.0,20.0,300,40.0]}`, &ReadScreenTextArgs{Region: &[4]int{10, 20, 300, 40}}},
		{ToolExec, `{"command":"uname -r"}`, &ExecArgs{Command: "uname -r", TimeoutS: 60}},
		{ToolExec, `{"command":"cat","cwd":"/tmp","timeout_s":600.0,"stdin":"x"}`, &ExecArgs{Command: "cat", Cwd: "/tmp", TimeoutS: 600, Stdin: "x"}},
		{ToolBrowser, `{"op":"open","url":"https://example.com/"}`, &BrowserArgs{Op: "open", URL: "https://example.com/"}},
		{ToolBrowser, `{"op":"click","target":"w12"}`, &BrowserArgs{Op: "click", Target: "w12"}},
		{ToolBrowser, `{"op":"type","target":"input[name=q]","text":"cats","submit":true}`, &BrowserArgs{Op: "type", Target: "input[name=q]", Text: "cats", Submit: true}},
		{ToolBrowser, `{"op":"switch_tab","tab":2}`, &BrowserArgs{Op: "switch_tab", Tab: 2}},
		{ToolBrowser, `{"op":"tabs"}`, &BrowserArgs{Op: "tabs"}},
		{ToolMouse, `{"op":"click","x":640,"y":400}`, &MouseArgs{Op: "click", X: 640, Y: 400}},
		{ToolMouse, `{"op":"click","x":640.0,"y":400.0}`, &MouseArgs{Op: "click", X: 640, Y: 400}},
		{ToolMouse, `{"op":"drag","x":10,"y":20,"to_x":300,"to_y":-4}`, &MouseArgs{Op: "drag", X: 10, Y: 20, ToX: intp(300), ToY: intp(-4)}},
		{ToolScroll, `{"x":5,"y":6,"dy":-3}`, &ScrollArgs{X: 5, Y: 6, DY: -3}},
		{ToolScroll, `{"x":5,"y":6,"dy":0,"dx":2}`, &ScrollArgs{X: 5, Y: 6, DX: 2}},
		{ToolKey, `{"keys":"ctrl+l"}`, &KeyArgs{Keys: "ctrl+l", Repeat: 1}},
		{ToolKey, `{"keys":"Return","repeat":3}`, &KeyArgs{Keys: "Return", Repeat: 3}},
		{ToolTypeText, `{"text":"line one\nline two é"}`, &TypeTextArgs{Text: "line one\nline two é"}},
		{ToolWindow, `{"op":"list"}`, &WindowArgs{Op: "list"}},
		{ToolWindow, `{"op":"focus","window_id":"0x1a00003"}`, &WindowArgs{Op: "focus", WindowID: "0x1a00003"}},
		{ToolWaitFor, `{"condition":"seconds","timeout_s":2.5}`, &WaitForArgs{Condition: "seconds", TimeoutS: 2.5}},
		{ToolWaitFor, `{"condition":"text_visible","value":"Saved","timeout_s":10}`, &WaitForArgs{Condition: "text_visible", Value: "Saved", TimeoutS: 10}},
		{ToolAskUser, `{"question":"Which file?"}`, &AskUserArgs{Question: "Which file?"}},
		{ToolDone, `{"success":false,"summary":"The file does not exist."}`, &DoneArgs{Summary: "The file does not exist."}},
	} {
		got := parse(t, c.tool, c.args)
		if !reflect.DeepEqual(got.Args, c.want) {
			t.Errorf("%s %s: got %+v, want %+v", c.tool, c.args, got.Args, c.want)
		}
	}
}

func TestParseTakesTheReasonOutOfTheArguments(t *testing.T) {
	a := parse(t, ToolExec, `{"command":"ls","reason":"see what is in the folder"}`)
	if a.Reason != "see what is in the folder" {
		t.Errorf("reason %q", a.Reason)
	}
	if strings.Contains(string(a.Raw), "reason") || !strings.Contains(string(a.Raw), `"command":"ls"`) {
		t.Errorf("the raw arguments leave the reason out: %s", a.Raw)
	}
	if a := parse(t, ToolScreenshot, `{"reason":null}`); a.Reason != "" {
		t.Errorf("a null reason is no reason: %q", a.Reason)
	}
	// Integers written with a fraction are written as integers in the raw form.
	a = parse(t, ToolExec, `{"command":"ls","timeout_s":30.0}`)
	if !strings.Contains(string(a.Raw), `"timeout_s":30`) || strings.Contains(string(a.Raw), "30.0") {
		t.Errorf("raw %s", a.Raw)
	}
}

func problems(t *testing.T, tool, args string, code string) []Problem {
	t.Helper()
	_, err := Parse(tool, json.RawMessage(args))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("%s %s: no error (%v)", tool, args, err)
	}
	if e.Code != code || e.Tool != tool || len(e.Problems) == 0 {
		t.Fatalf("%s %s: %+v", tool, args, e)
	}
	return e.Problems
}

func mentions(ps []Problem, path string, texts ...string) bool {
	for _, p := range ps {
		if p.Path != path {
			continue
		}
		ok := true
		for _, s := range texts {
			ok = ok && strings.Contains(p.Message, s)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestParseRefusesWhatIsNotValid(t *testing.T) {
	for _, c := range []struct {
		name, tool, args string
		path             string
		texts            []string
	}{
		{"not an object", ToolExec, `["ls"]`, "args", []string{"object"}},
		{"a number", ToolExec, `12`, "args", []string{"object"}},
		{"not json", ToolExec, `{"command":`, "args", []string{"JSON"}},
		{"nothing for a tool that needs something", ToolExec, ``, "args", []string{"command"}},
		{"command missing", ToolExec, `{"cwd":"/tmp"}`, "args", []string{"command"}},
		{"command empty", ToolExec, `{"command":""}`, "args.command", nil},
		{"command blank", ToolExec, `{"command":"   "}`, "args.command", []string{"empty"}},
		{"command too long", ToolExec, `{"command":"` + strings.Repeat("a", 4001) + `"}`, "args.command", nil},
		{"timeout too long", ToolExec, `{"command":"ls","timeout_s":900}`, "args.timeout_s", []string{"600"}},
		{"timeout zero", ToolExec, `{"command":"ls","timeout_s":0}`, "args.timeout_s", []string{"1"}},
		{"timeout as text", ToolExec, `{"command":"ls","timeout_s":"ten"}`, "args.timeout_s", []string{"integer"}},
		{"timeout with a fraction", ToolExec, `{"command":"ls","timeout_s":1.5}`, "args.timeout_s", []string{"integer"}},
		{"stdin too long in bytes", ToolExec, `{"command":"cat","stdin":"` + strings.Repeat("é", 40000) + `"}`, "args.stdin", []string{"bytes"}},
		{"an argument the tool does not have", ToolExec, `{"command":"ls","shell":"zsh"}`, "args", []string{"additional properties 'shell'", "command, cwd, stdin, timeout_s"}},
		{"arguments for a tool without any", ToolScreenshot, `{"scale":2}`, "args", []string{"additional properties 'scale'", "(none)"}},
		{"id of the wrong kind", ToolClickElement, `{"id":"x17"}`, "args.id", []string{"match"}},
		{"id too long", ToolClickElement, `{"id":"a1234567"}`, "args.id", nil},
		{"set_text cannot use a screen text id", ToolSetText, `{"id":"o3","text":"x"}`, "args.id", nil},
		{"set_text text missing", ToolSetText, `{"id":"a3"}`, "args", []string{"text"}},
		{"unknown mouse op", ToolMouse, `{"op":"tap","x":1,"y":2}`, "args.op", nil},
		{"drag without an end", ToolMouse, `{"op":"drag","x":1,"y":2}`, "args.to_x", []string{"to_y", "drag"}},
		{"position as text", ToolMouse, `{"op":"click","x":"640","y":2}`, "args.x", []string{"integer"}},
		{"position missing", ToolMouse, `{"op":"click","x":640}`, "args", []string{"y"}},
		{"scroll too far", ToolScroll, `{"x":1,"y":2,"dy":21}`, "args.dy", []string{"20"}},
		{"scroll without a direction", ToolScroll, `{"x":1,"y":2}`, "args", []string{"dy"}},
		{"two keys", ToolKey, `{"keys":"ctrl+a+b"}`, "args.keys", []string{"more than one key"}},
		{"a key that does not exist", ToolKey, `{"keys":"Foo"}`, "args.keys", []string{"not a key name"}},
		{"repeat too many", ToolKey, `{"keys":"Tab","repeat":21}`, "args.repeat", []string{"20"}},
		{"browser op unknown", ToolBrowser, `{"op":"refresh"}`, "args.op", nil},
		{"browser open without an address", ToolBrowser, `{"op":"open"}`, "args.url", []string{"open"}},
		{"browser click without a target", ToolBrowser, `{"op":"click"}`, "args.target", []string{"click"}},
		{"browser type without text", ToolBrowser, `{"op":"type","target":"w3"}`, "args.text", []string{"type"}},
		{"browser tab missing", ToolBrowser, `{"op":"switch_tab"}`, "args.tab", []string{"switch_tab"}},
		{"browser tab zero", ToolBrowser, `{"op":"switch_tab","tab":0}`, "args.tab", nil},
		{"browser address too long", ToolBrowser, `{"op":"open","url":"https://example.com/` + strings.Repeat("é", 1500) + `"}`, "args.url", []string{"bytes"}},
		{"window focus without an id", ToolWindow, `{"op":"focus"}`, "args.window_id", []string{"focus"}},
		{"window id of the wrong kind", ToolWindow, `{"op":"focus","window_id":"main"}`, "args.window_id", nil},
		{"wait without a value", ToolWaitFor, `{"condition":"text_visible","timeout_s":5}`, "args.value", []string{"text_visible"}},
		{"wait too short", ToolWaitFor, `{"condition":"seconds","timeout_s":0.05}`, "args.timeout_s", []string{"0.1"}},
		{"wait too long", ToolWaitFor, `{"condition":"seconds","timeout_s":61}`, "args.timeout_s", []string{"60"}},
		{"wait without a time", ToolWaitFor, `{"condition":"screen_idle"}`, "args", []string{"timeout_s"}},
		{"wait for something else", ToolWaitFor, `{"condition":"forever","timeout_s":5}`, "args.condition", nil},
		{"region of the wrong size", ToolReadScreenText, `{"region":[1,2,3]}`, "args.region", nil},
		{"region with no width", ToolReadScreenText, `{"region":[1,2,0,5]}`, "args.region", []string{"width"}},
		{"region with a negative corner", ToolReadScreenText, `{"region":[-1,2,3,4]}`, "args.region[0]", nil},
		{"tree scope unknown", ToolReadTree, `{"scope":"screen"}`, "args.scope", nil},
		{"tree too many nodes", ToolReadTree, `{"max_nodes":401}`, "args.max_nodes", []string{"400"}},
		{"done without a summary", ToolDone, `{"success":true}`, "args", []string{"summary"}},
		{"done success as text", ToolDone, `{"success":"yes","summary":"ok"}`, "args.success", []string{"boolean"}},
		{"reason not text", ToolExec, `{"command":"ls","reason":5}`, "args.reason", []string{"string"}},
		{"reason too long", ToolExec, `{"command":"ls","reason":"` + strings.Repeat("x", 301) + `"}`, "args.reason", []string{"300"}},
	} {
		ps := problems(t, c.tool, c.args, CodeInvalidArgs)
		if !mentions(ps, c.path, c.texts...) {
			t.Errorf("%s: want a problem at %s mentioning %v, got %v", c.name, c.path, c.texts, ps)
		}
	}
}

func TestParseListsEveryProblemAtOnce(t *testing.T) {
	ps := problems(t, ToolExec, `{"command":5,"timeout_s":0,"shell":"zsh","reason":7}`, CodeInvalidArgs)
	for _, path := range []string{"args.command", "args.timeout_s", "args", "args.reason"} {
		if !mentions(ps, path) {
			t.Errorf("no problem at %s: %v", path, ps)
		}
	}
	_, err := Parse(ToolExec, json.RawMessage(`{"command":5,"timeout_s":0}`))
	text := err.Error()
	if !strings.HasPrefix(text, "exec: ") || !strings.Contains(text, "args.command") || !strings.Contains(text, "args.timeout_s") {
		t.Errorf("the error reads: %s", text)
	}
}

func TestParseRefusesAnUnknownTool(t *testing.T) {
	ps := problems(t, "format_disk", `{}`, CodeUnknownTool)
	if !mentions(ps, "tool", "format_disk", "screenshot", "exec", "done") {
		t.Errorf("the model is told what the tools are: %v", ps)
	}
	if _, err := Parse("", nil); err == nil {
		t.Error("an empty tool name is unknown")
	}
}

func TestPathsOfProblems(t *testing.T) {
	for in, want := range map[string]string{"": "args", "/timeout_s": "args.timeout_s", "/region/2": "args.region[2]", "/a~1b": "args.a/b"} {
		if got := pathOf(in); got != want {
			t.Errorf("pathOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEveryToolParsesItsOwnMinimalExample(t *testing.T) {
	minimal := map[string]string{
		ToolScreenshot: `{}`, ToolReadTree: `{}`, ToolClickElement: `{"id":"a1"}`, ToolSetText: `{"id":"a1","text":"x"}`,
		ToolReadScreenText: `{}`, ToolExec: `{"command":"true"}`, ToolBrowser: `{"op":"tabs"}`, ToolMouse: `{"op":"move","x":1,"y":2}`,
		ToolScroll: `{"x":1,"y":2,"dy":1}`, ToolKey: `{"keys":"Tab"}`, ToolTypeText: `{"text":"x"}`, ToolWindow: `{"op":"list"}`,
		ToolWaitFor: `{"condition":"seconds","timeout_s":1}`, ToolAskUser: `{"question":"why?"}`, ToolDone: `{"success":true,"summary":"ok"}`,
	}
	for _, name := range Names() {
		args, ok := minimal[name]
		if !ok {
			t.Errorf("no example for %s", name)
			continue
		}
		parse(t, name, args)
	}
}

func TestJSONModeDocument(t *testing.T) {
	doc, err := fullSet(t).JSONMode(3)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Response map[string]any            `json:"response"`
		Tools    map[string]map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	if len(got.Tools) != 15 || got.Response["$id"] != "https://controlsnack.com/schema/v1/response.json" {
		t.Errorf("%d tools, response %v", len(got.Tools), got.Response["$id"])
	}
	for _, name := range Names() {
		if got.Tools[name]["title"] != name {
			t.Errorf("tool %s: %v", name, got.Tools[name]["title"])
		}
	}
	if !bytes.Contains(doc, []byte("\n  ")) {
		t.Error("the document is indented")
	}
}

func TestPrinterPrintsEveryFormatOfEveryProfile(t *testing.T) {
	for _, format := range []string{"jsonmode", "openai", "anthropic", "ollama"} {
		for _, profile := range []string{"full", "compact"} {
			var out bytes.Buffer
			if err := NewPrinter().Print(&out, format, profile); err != nil || !json.Valid(out.Bytes()) || !strings.HasSuffix(out.String(), "\n") {
				t.Errorf("%s %s: %v, valid %v", format, profile, err, json.Valid(out.Bytes()))
			}
		}
	}
	var out bytes.Buffer
	if err := NewPrinter().Print(&out, "openai", "tiny"); exitcode.Of(err) != exitcode.Usage {
		t.Errorf("an unknown profile is a usage error: %v", err)
	}
	if err := NewPrinter().Print(&out, "xml", "full"); err == nil {
		t.Error("an unknown format is refused")
	}
}

// What an error repeats of the model's words is one clean, short line: it goes to the model again, the viewer and the log.
func TestErrorsRepeatTheModelsWordsAsOneCleanLine(t *testing.T) {
	long := strings.Repeat("x", 1000)
	for _, c := range []struct{ tool, args string }{
		{"format\n\x1b[31mdisk\x07", `{}`},
		{strings.Repeat("t", 500), `{}`},
		{ToolExec, `{"command":"ls","evil\nname\u0007\u001b[2J":1}`},
		{ToolExec, `{"command":"ls","` + long + `":1}`},
		{ToolMouse, `{"op":"` + long + `\nsecond line","x":1,"y":2}`},
	} {
		_, err := Parse(c.tool, json.RawMessage(c.args))
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("%q %q: no error", c.tool, c.args)
		}
		texts := append([]string{e.Tool, e.Error()}, "")
		for _, p := range e.Problems {
			texts = append(texts, p.Path, p.Message)
			if n := len([]rune(p.Message)); n > maxProblemRunes {
				t.Errorf("a problem of %d characters: %.80q", n, p.Message)
			}
		}
		if n := len([]rune(e.Tool)); n > maxToolRunes {
			t.Errorf("a tool name of %d characters", n)
		}
		for _, s := range texts {
			if strings.ContainsAny(s, "\n\r\x00\x07\x1b") {
				t.Errorf("control characters in %q", s)
			}
		}
	}
}

func TestASetWithoutTheTools(t *testing.T) {
	full, _ := NewSet(ProfileFull)
	compact, _ := NewSet(ProfileCompact)

	less := full.Without(ToolExec, "not a tool")
	if less.Has(ToolExec) || !full.Has(ToolExec) || len(less.Tools()) != len(full.Tools())-1 || less.Profile() != ProfileFull {
		t.Errorf("Without: %v", less.Names())
	}
	if _, err := less.Parse(ToolExec, json.RawMessage(`{"command":"ls"}`)); err == nil || !strings.Contains(err.Error(), "no tool") {
		t.Errorf("a tool that was taken out is as unknown to the model as one that does not exist: %v", err)
	}

	// With the recognized text to give ids, clicking stays; the compact profile has no such tool.
	f := full.WithoutTree()
	for _, gone := range []string{ToolReadTree, ToolSetText} {
		if f.Has(gone) {
			t.Errorf("full without a tree still has %s", gone)
		}
	}
	if !f.Has(ToolClickElement) || !f.Has(ToolReadScreenText) {
		t.Errorf("full without a tree keeps %v", f.Names())
	}
	c := compact.WithoutTree()
	if c.Has(ToolReadTree) || c.Has(ToolClickElement) || !c.Has(ToolMouse) || !c.Has(ToolDone) {
		t.Errorf("compact without a tree: %v", c.Names())
	}
	if _, err := c.Envelope(3, true); err != nil {
		t.Errorf("the reduced set still makes its schemas: %v", err)
	}
	raw, err := c.Render(FormatOpenAI)
	var defs []struct {
		Function struct{ Name string } `json:"function"`
	}
	if err != nil || json.Unmarshal(raw, &defs) != nil || len(defs) != len(c.Names()) {
		t.Fatalf("the reduced set is rendered with its own tools: %v", err)
	}
	for _, d := range defs {
		if d.Function.Name == ToolClickElement || d.Function.Name == ToolReadTree {
			t.Errorf("the definition of %s is still offered", d.Function.Name)
		}
	}
}
