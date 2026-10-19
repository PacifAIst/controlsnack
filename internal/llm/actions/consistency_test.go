// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack/schema"
)

// schemaOf reads a tool's schema file as a generic document.
func schemaOf(t *testing.T, tool *Tool) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(tool.Schema, &doc); err != nil {
		t.Fatalf("%s: %v", tool.Name, err)
	}
	return doc
}

func props(doc map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	raw, _ := doc["properties"].(map[string]any)
	for name, v := range raw {
		out[name], _ = v.(map[string]any)
	}
	return out
}

// goType is the JSON Schema type of a Go field type.
func goType(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int:
		return "integer"
	case reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Array, reflect.Slice:
		return "array"
	}
	return "?" + t.String()
}

// Every tool has a schema file, every schema file belongs to a tool, and the schema agrees with the Go struct of the
// tool's arguments: the same names, the same types, the same required arguments.
func TestSchemasAndArgumentStructsAgree(t *testing.T) {
	files, err := fs.Glob(schema.Tools, "tools/*.json")
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, f := range files {
		onDisk = append(onDisk, strings.TrimSuffix(strings.TrimPrefix(f, "tools/"), ".json"))
	}
	if want := slices.Sorted(slices.Values(Names())); !slices.Equal(slices.Sorted(slices.Values(onDisk)), want) {
		t.Fatalf("schema files %v, tools %v", onDisk, want)
	}

	for _, tool := range Tools() {
		doc := schemaOf(t, tool)
		if doc["title"] != tool.Name || doc["$id"] != "https://controlsnack.com/schema/v1/tools/"+tool.Name+".json" {
			t.Errorf("%s: title %v, id %v", tool.Name, doc["title"], doc["$id"])
		}
		if doc["type"] != "object" || doc["additionalProperties"] != false {
			t.Errorf("%s: the arguments are an object with no other properties", tool.Name)
		}
		if tool.Description == "" || !strings.HasSuffix(tool.Description, ".") {
			t.Errorf("%s: the description is a sentence: %q", tool.Name, tool.Description)
		}

		st := reflect.TypeOf(tool.newArgs()).Elem()
		if tool.newArgs().Tool() != tool.Name {
			t.Errorf("%s: the arguments say they belong to %s", tool.Name, tool.newArgs().Tool())
		}
		goFields := map[string]reflect.StructField{}
		goRequired := map[string]bool{}
		for i := 0; i < st.NumField(); i++ {
			f := st.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			goFields[name] = f
			goRequired[name] = !strings.Contains(opts, "omitempty")
		}
		schemaProps := props(doc)
		for name, f := range goFields {
			p, ok := schemaProps[name]
			if !ok {
				t.Errorf("%s: the Go field %s has no property %q in the schema", tool.Name, f.Name, name)
				continue
			}
			wantType := goType(f.Type)
			gotType, _ := p["type"].(string)
			if _, isEnum := p["enum"]; isEnum && gotType == "" {
				gotType = "string"
			}
			if gotType != wantType {
				t.Errorf("%s.%s: the schema says %q, the Go field is %q", tool.Name, name, gotType, wantType)
			}
			if d, _ := p["description"].(string); d == "" {
				t.Errorf("%s.%s has no description (the model reads it)", tool.Name, name)
			}
		}
		for name := range schemaProps {
			if _, ok := goFields[name]; !ok {
				t.Errorf("%s: the schema has a property %q that the Go struct does not", tool.Name, name)
			}
		}
		var required []string
		if r, ok := doc["required"].([]any); ok {
			for _, v := range r {
				required = append(required, v.(string))
			}
		}
		for name, req := range goRequired {
			if req != slices.Contains(required, name) {
				t.Errorf("%s.%s: required in Go %v, in the schema %v", tool.Name, name, req, slices.Contains(required, name))
			}
		}
	}
}

// The values of the enums are the same in the schema files and in the Go lists the checks use.
func TestEnumsAgree(t *testing.T) {
	for _, c := range []struct {
		tool, prop string
		want       []string
	}{
		{ToolReadTree, "scope", Scopes},
		{ToolClickElement, "button", Buttons},
		{ToolBrowser, "op", BrowserOps},
		{ToolMouse, "op", MouseOps},
		{ToolWindow, "op", WindowOps},
		{ToolWaitFor, "condition", WaitConditions},
	} {
		tool, _ := Lookup(c.tool)
		var got []string
		for _, v := range props(schemaOf(t, tool))[c.prop]["enum"].([]any) {
			got = append(got, v.(string))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s.%s: the schema lists %v, Go lists %v", c.tool, c.prop, got, c.want)
		}
	}
}

// The limits are the ones of PLAN.md section 5.3.
func TestLimitsAreThoseOfThePlan(t *testing.T) {
	type bound map[string]float64
	for _, c := range []struct {
		tool, prop string
		want       bound
	}{
		{ToolReadTree, "query", bound{"maxLength": 100}},
		{ToolReadTree, "max_nodes", bound{"minimum": 1, "maximum": 400, "default": 150}},
		{ToolSetText, "text", bound{"maxLength": 4000}},
		{ToolExec, "command", bound{"maxLength": 4000, "minLength": 1}},
		{ToolExec, "timeout_s", bound{"minimum": 1, "maximum": 600, "default": 60}},
		{ToolExec, "stdin", bound{"maxLength": 64 << 10}},
		{ToolScroll, "dy", bound{"minimum": -20, "maximum": 20}},
		{ToolScroll, "dx", bound{"minimum": -20, "maximum": 20}},
		{ToolKey, "repeat", bound{"minimum": 1, "maximum": 20, "default": 1}},
		{ToolTypeText, "text", bound{"maxLength": 4000}},
		{ToolWaitFor, "timeout_s", bound{"minimum": 0.1, "maximum": 60}},
		{ToolAskUser, "question", bound{"maxLength": 1000}},
		{ToolDone, "summary", bound{"maxLength": 2000}},
	} {
		tool, _ := Lookup(c.tool)
		p := props(schemaOf(t, tool))[c.prop]
		for k, want := range c.want {
			if got, _ := p[k].(float64); got != want {
				t.Errorf("%s.%s %s: %v, the plan says %v", c.tool, c.prop, k, p[k], want)
			}
		}
	}
	for tool, pattern := range map[string]string{ToolClickElement: "^[awo][0-9]{1,5}$", ToolSetText: "^[aw][0-9]{1,5}$"} {
		tl, _ := Lookup(tool)
		if got := props(schemaOf(t, tl))["id"]["pattern"]; got != pattern {
			t.Errorf("%s id pattern %v, want %s", tool, got, pattern)
		}
	}
	// Defaults of the Go side are the schema's.
	tl, _ := Lookup(ToolReadTree)
	if got := props(schemaOf(t, tl))["scope"]["default"]; got != DefaultScope {
		t.Errorf("scope default %v, Go %s", got, DefaultScope)
	}
	tl, _ = Lookup(ToolClickElement)
	if got := props(schemaOf(t, tl))["button"]["default"]; got != DefaultButton {
		t.Errorf("button default %v, Go %s", got, DefaultButton)
	}
}

// The envelope names exactly the tools of the registry, in order, and the limits on the model's words are the plan's.
func TestEnvelopeNamesTheTools(t *testing.T) {
	var doc struct {
		Required   []string `json:"required"`
		Properties struct {
			Thought struct {
				MaxLength int `json:"maxLength"`
			} `json:"thought"`
			Actions struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required   []string `json:"required"`
					Properties struct {
						Tool struct {
							Enum []string `json:"enum"`
						} `json:"tool"`
						Reason struct {
							MaxLength int `json:"maxLength"`
						} `json:"reason"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"actions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema.Response, &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(doc.Properties.Actions.Items.Properties.Tool.Enum, Names()) {
		t.Errorf("envelope tools %v, registry %v", doc.Properties.Actions.Items.Properties.Tool.Enum, Names())
	}
	if doc.Properties.Thought.MaxLength != 600 || doc.Properties.Actions.Items.Properties.Reason.MaxLength != MaxReasonRunes ||
		doc.Properties.Actions.MinItems != 1 || doc.Properties.Actions.MaxItems != DefaultMaxActions {
		t.Errorf("limits of the envelope: %+v", doc.Properties)
	}
	if !slices.Equal(doc.Required, []string{"actions"}) || !slices.Equal(doc.Properties.Actions.Items.Required, []string{"tool", "args"}) {
		t.Errorf("required: %v, %v", doc.Required, doc.Properties.Actions.Items.Required)
	}

	env, err := fullSet(t).Envelope(5, false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Properties struct {
			Actions struct {
				MaxItems int `json:"maxItems"`
			} `json:"actions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(env, &got); err != nil || got.Properties.Actions.MaxItems != 5 {
		t.Errorf("the limit is set to 5: %v %s", err, env)
	}
	if _, err := fullSet(t).Envelope(0, false); err == nil {
		t.Error("a response holds at least one action")
	}
}

func TestCompactProfileIsThePlans(t *testing.T) {
	var names []string
	for _, tool := range CompactTools() {
		names = append(names, tool.Name)
	}
	want := []string{ToolReadTree, ToolClickElement, ToolExec, ToolMouse, ToolKey, ToolTypeText, ToolWaitFor, ToolAskUser, ToolDone}
	if !slices.Equal(slices.Sorted(slices.Values(names)), slices.Sorted(slices.Values(want))) {
		t.Errorf("compact tools %v, the plan lists %v", names, want)
	}
	if len(Tools()) != 15 {
		t.Errorf("the plan lists 15 tools, there are %d", len(Tools()))
	}
}

func fullSet(t *testing.T) *Set {
	t.Helper()
	s, err := NewSet(ProfileFull)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
