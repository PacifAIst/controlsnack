// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var update = flag.Bool("update", false, "rewrite the golden renderings in testdata/render")

func compileSchema(t *testing.T, doc any) *jsonschema.Schema {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem:///rendered.json", parsed); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("mem:///rendered.json")
	if err != nil {
		t.Fatalf("a rendered schema must be a schema: %v\n%s", err, data)
	}
	return s
}

func validates(t *testing.T, s *jsonschema.Schema, instance string) bool {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(instance))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(v) == nil
}

func setOf(t *testing.T, profile string) *Set {
	t.Helper()
	s, err := NewSet(profile)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProfiles(t *testing.T) {
	full, compact := setOf(t, ProfileFull), setOf(t, ProfileCompact)
	if !slices.Equal(full.Names(), Names()) || len(full.Tools()) != 15 {
		t.Errorf("the full profile offers every tool: %v", full.Names())
	}
	if want := []string{"read_accessibility_tree", "click_element", "exec", "mouse", "key", "type_text", "wait_for", "ask_user", "done"}; !slices.Equal(compact.Names(), want) {
		t.Errorf("compact: %v, want %v (in the order of the schema)", compact.Names(), want)
	}
	if full.Profile() != "full" || compact.Profile() != "compact" || compact.Has("browser") || !compact.Has("exec") || compact.Has("nope") {
		t.Error("Profile and Has")
	}
	if _, err := NewSet("auto"); err == nil {
		t.Error("auto is resolved before a set is made")
	}
	// A copy: changing what Tools returns does not change the set.
	tools := compact.Tools()
	tools[0] = nil
	if compact.Tools()[0] == nil {
		t.Error("Tools returns a copy")
	}
}

func TestResolveProfile(t *testing.T) {
	for _, c := range []struct {
		requested       string
		local, textOnly bool
		want            string
		wantErr         bool
	}{
		{"auto", false, false, "full", false},
		{"", false, false, "full", false},
		{"auto", true, false, "compact", false},
		{"auto", false, true, "compact", false},
		{"full", true, true, "full", false},
		{"compact", false, false, "compact", false},
		{"huge", false, false, "", true},
	} {
		got, err := ResolveProfile(c.requested, c.local, c.textOnly)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%+v: %q, %v", c, got, err)
		}
	}
}

// A call to a tool that exists but is not offered is as unknown to the model as one that does not exist.
func TestSetRefusesToolsItDoesNotOffer(t *testing.T) {
	compact := setOf(t, ProfileCompact)
	_, err := compact.Parse(ToolBrowser, json.RawMessage(`{"op":"tabs"}`))
	var e *Error
	if !asError(err, &e) || e.Code != CodeUnknownTool || !mentions(e.Problems, "tool", "browser", "exec", "done") || strings.Contains(e.Problems[0].Message, "screenshot") {
		t.Fatalf("%v", err)
	}
	if a, err := compact.Parse(ToolExec, json.RawMessage(`{"command":"true"}`)); err != nil || a.Tool != ToolExec {
		t.Errorf("a tool of the set parses: %v", err)
	}
	if _, err := setOf(t, ProfileFull).Parse(ToolBrowser, json.RawMessage(`{"op":"tabs"}`)); err != nil {
		t.Errorf("the full set offers browser: %v", err)
	}
}

func asError(err error, target **Error) bool { return errors.As(err, target) }

type renderedParams struct {
	Type                 string                    `json:"type"`
	AdditionalProperties bool                      `json:"additionalProperties"`
	Required             []string                  `json:"required"`
	Properties           map[string]map[string]any `json:"properties"`
	Extra                map[string]any            `json:"-"`
}

// Every tool of the function renderings carries its schema (without the keywords that name the file), the tool's
// description, and an optional reason; Anthropic's input_schema is the same schema.
func TestFunctionRenderings(t *testing.T) {
	for _, profile := range []string{ProfileFull, ProfileCompact} {
		set := setOf(t, profile)
		for _, format := range []string{FormatOpenAI, FormatOllama} {
			raw, err := set.Render(format)
			if err != nil {
				t.Fatal(err)
			}
			var list []struct {
				Type     string `json:"type"`
				Function struct {
					Name        string         `json:"name"`
					Description string         `json:"description"`
					Parameters  renderedParams `json:"parameters"`
				} `json:"function"`
			}
			if err := json.Unmarshal(raw, &list); err != nil {
				t.Fatalf("%s %s: %v", format, profile, err)
			}
			if len(list) != len(set.Tools()) {
				t.Fatalf("%s %s: %d functions for %d tools", format, profile, len(list), len(set.Tools()))
			}
			for i, tool := range set.Tools() {
				f := list[i]
				if f.Type != "function" || f.Function.Name != tool.Name || f.Function.Description != tool.Description {
					t.Errorf("%s %s %s: %+v", format, profile, tool.Name, f)
				}
				checkParameters(t, format+" "+profile+" "+tool.Name, tool, f.Function.Parameters, raw)
			}
		}

		raw, err := set.Render(FormatAnthropic)
		if err != nil {
			t.Fatal(err)
		}
		var list []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema renderedParams `json:"input_schema"`
		}
		if err := json.Unmarshal(raw, &list); err != nil || len(list) != len(set.Tools()) {
			t.Fatalf("anthropic %s: %v", profile, err)
		}
		for i, tool := range set.Tools() {
			if list[i].Name != tool.Name || list[i].Description != tool.Description {
				t.Errorf("anthropic %s: %+v", tool.Name, list[i])
			}
			checkParameters(t, "anthropic "+profile+" "+tool.Name, tool, list[i].InputSchema, raw)
		}
	}
}

func checkParameters(t *testing.T, what string, tool *Tool, p renderedParams, raw []byte) {
	t.Helper()
	if p.Type != "object" || p.AdditionalProperties {
		t.Errorf("%s: %+v", what, p)
	}
	var file map[string]any
	_ = json.Unmarshal(tool.Schema, &file)
	for name := range props(file) {
		if _, ok := p.Properties[name]; !ok {
			t.Errorf("%s: the property %s is missing", what, name)
		}
	}
	reason, ok := p.Properties["reason"]
	if !ok || reason["type"] != "string" || reason["maxLength"] != float64(MaxReasonRunes) || reason["description"] == "" {
		t.Errorf("%s: reason %v", what, reason)
	}
	if len(p.Properties) != len(props(file))+1 {
		t.Errorf("%s: %d properties for %d arguments and a reason", what, len(p.Properties), len(props(file)))
	}
	if reasonRequired := slices.Contains(p.Required, "reason"); reasonRequired {
		t.Errorf("%s: the reason is optional", what)
	}
	for name, prop := range p.Properties {
		if _, enum := prop["enum"]; enum && prop["type"] != "string" {
			t.Errorf("%s.%s: an enum without its type", what, name)
		}
	}
	want, _ := file["required"].([]any)
	if len(p.Required) != len(want) {
		t.Errorf("%s: required %v, the schema says %v", what, p.Required, want)
	}
}

func TestRenderedSchemasValidateCalls(t *testing.T) {
	set := setOf(t, ProfileFull)
	raw, err := set.Render(FormatOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Function struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	byTool := map[string]*jsonschema.Schema{}
	for _, f := range list {
		byTool[f.Function.Name] = compileSchema(t, f.Function.Parameters)
	}
	for _, c := range []struct {
		tool, args string
		ok         bool
	}{
		{ToolExec, `{"command":"ls","reason":"look"}`, true},
		{ToolExec, `{"command":"ls","timeout_s":601}`, false},
		{ToolExec, `{"reason":"look"}`, false},
		{ToolExec, `{"command":"ls","reason":"` + strings.Repeat("x", 301) + `"}`, false},
		{ToolScreenshot, `{}`, true},
		{ToolScreenshot, `{"reason":"look at the screen"}`, true},
		{ToolMouse, `{"op":"click","x":1,"y":2}`, true},
		{ToolMouse, `{"op":"tap","x":1,"y":2}`, false},
	} {
		if got := validates(t, byTool[c.tool], c.args); got != c.ok {
			t.Errorf("%s %s: valid %v, want %v", c.tool, c.args, got, c.ok)
		}
	}
}

func envelopeOf(t *testing.T, set *Set, max int, strict bool) *jsonschema.Schema {
	t.Helper()
	raw, err := set.Envelope(max, strict)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return compileSchema(t, doc)
}

// The plain envelope offers the set's tools and no others, and at most the number of actions asked for.
func TestEnvelopeOffersTheSetsTools(t *testing.T) {
	compact := envelopeOf(t, setOf(t, ProfileCompact), 2, false)
	call := func(tool string) string {
		return `{"actions":[{"tool":"` + tool + `","args":{}}]}`
	}
	if !validates(t, compact, call("exec")) || validates(t, compact, call("browser")) || validates(t, compact, call("nope")) {
		t.Error("the compact envelope offers exec and not browser")
	}
	full := envelopeOf(t, setOf(t, ProfileFull), 2, false)
	if !validates(t, full, call("browser")) {
		t.Error("the full envelope offers browser")
	}
	three := `{"actions":[{"tool":"screenshot","args":{}},{"tool":"screenshot","args":{}},{"tool":"screenshot","args":{}}]}`
	if validates(t, full, three) || !validates(t, envelopeOf(t, setOf(t, ProfileFull), 3, false), three) {
		t.Error("the envelope limits the number of actions")
	}
	if validates(t, full, `{"actions":[]}`) || validates(t, full, `{}`) || validates(t, full, `{"actions":[{"tool":"done"}]}`) {
		t.Error("an answer has at least one action, each with its tool and arguments")
	}
	if !validates(t, full, `{"thought":"hm","actions":[{"tool":"done","args":{},"reason":"why"}]}`) {
		t.Error("thought and reason are allowed")
	}
}

// The strict envelope ties each action's arguments to the schema of its tool.
func TestStrictEnvelopeChecksTheArguments(t *testing.T) {
	strict := envelopeOf(t, setOf(t, ProfileFull), 3, true)
	for _, c := range []struct {
		name, doc string
		ok        bool
	}{
		{"valid exec", `{"actions":[{"tool":"exec","args":{"command":"ls"},"reason":"look"}]}`, true},
		{"two actions", `{"thought":"x","actions":[{"tool":"key","args":{"keys":"ctrl+l"}},{"tool":"type_text","args":{"text":"a"}}]}`, true},
		{"exec without a command", `{"actions":[{"tool":"exec","args":{}}]}`, false},
		{"exec with the arguments of another tool", `{"actions":[{"tool":"exec","args":{"keys":"Tab"}}]}`, false},
		{"mouse with an unknown op", `{"actions":[{"tool":"mouse","args":{"op":"tap","x":1,"y":2}}]}`, false},
		{"unknown tool", `{"actions":[{"tool":"nope","args":{}}]}`, false},
		{"four actions", `{"actions":[{"tool":"done","args":{"success":true,"summary":"a"}},{"tool":"done","args":{"success":true,"summary":"a"}},{"tool":"done","args":{"success":true,"summary":"a"}},{"tool":"done","args":{"success":true,"summary":"a"}}]}`, false},
	} {
		if got := validates(t, strict, c.doc); got != c.ok {
			t.Errorf("%s: valid %v, want %v", c.name, got, c.ok)
		}
	}
	compact := envelopeOf(t, setOf(t, ProfileCompact), 3, true)
	if validates(t, compact, `{"actions":[{"tool":"browser","args":{"op":"tabs"}}]}`) {
		t.Error("the compact strict envelope does not offer browser")
	}
}

func TestRenderRefusesWhatItCannotDo(t *testing.T) {
	set := setOf(t, ProfileFull)
	if _, err := set.Render("xml"); err == nil || !strings.Contains(err.Error(), "openai") {
		t.Errorf("an unknown format is refused with the list of formats: %v", err)
	}
	if _, err := set.Envelope(0, false); err == nil {
		t.Error("a response holds at least one action")
	}
	if doc, err := set.Render(FormatJSONMode); err != nil || !json.Valid(doc) {
		t.Errorf("jsonmode: %v", err)
	}
}

// What each backend is given is written down: a change to a schema file, to a description or to the way they are
// rendered shows here. `go test ./internal/llm/actions -update` rewrites the files.
func TestRenderingsAreAsWritten(t *testing.T) {
	for _, profile := range []string{ProfileFull, ProfileCompact} {
		set := setOf(t, profile)
		for _, format := range []string{FormatOpenAI, FormatAnthropic, FormatOllama, FormatJSONMode} {
			raw, err := set.Render(format)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("size: %s, %s profile: %d bytes", format, profile, len(raw))
			golden(t, format+"-"+profile, raw)
		}
		strict, err := set.Envelope(DefaultMaxActions, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("size: strict jsonmode, %s profile: %d bytes", profile, len(strict))
		golden(t, "jsonmode-strict-"+profile, strict)
	}
}

func golden(t *testing.T, name string, raw []byte) {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	out.WriteByte('\n')
	path := filepath.Join("testdata", "render", name+".json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil { //nolint:gosec // a test fixture
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/llm/actions -update` to create it)", err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Errorf("%s differs from its golden file (run `go test ./internal/llm/actions -update` if the change is meant)", name)
	}
}
