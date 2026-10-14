// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/PacifAIst/controlsnack-local/schema"
)

// Tool is one tool the model can use: its name, what it is for, the schema of its arguments and how to check them.
type Tool struct {
	Name string
	// Description is what the model is told the tool is for (the "description" of its schema).
	Description string
	// Compact is whether the tool is in the compact profile (the default for local models and text-only mode).
	Compact bool
	// Schema is the JSON Schema of the arguments (schema/tools/<name>.json), exactly as embedded.
	Schema json.RawMessage

	props    map[string]propSchema
	compiled *jsonschema.Schema
	newArgs  func() Args
	check    func(Args) []Problem
}

// propSchema is what the checks need to know about a property of a tool's schema.
type propSchema struct {
	Type  string `json:"type"`
	Items *struct {
		Type string `json:"type"`
	} `json:"items"`
}

// definition ties a tool to its argument struct and its checks beyond the schema.
type definition struct {
	name    string
	compact bool
	newArgs func() Args
	check   func(Args) []Problem
}

func def[T any](name string, compact bool, check func(*T) []Problem) definition {
	d := definition{name: name, compact: compact, newArgs: func() Args { return any(new(T)).(Args) }}
	if check != nil {
		d.check = func(a Args) []Problem { return check(any(a).(*T)) }
	}
	return d
}

// definitions lists the tools in the order of the schema's envelope.
var definitions = []definition{
	def[ScreenshotArgs](ToolScreenshot, false, nil),
	def(ToolReadTree, true, checkReadTree),
	def[ClickElementArgs](ToolClickElement, true, nil),
	def[SetTextArgs](ToolSetText, false, nil),
	def(ToolReadScreenText, false, checkReadScreenText),
	def(ToolExec, true, checkExec),
	def(ToolBrowser, false, checkBrowser),
	def(ToolMouse, true, checkMouse),
	def[ScrollArgs](ToolScroll, false, nil),
	def(ToolKey, true, checkKey),
	def[TypeTextArgs](ToolTypeText, true, nil),
	def(ToolWindow, false, checkWindow),
	def(ToolWaitFor, true, checkWaitFor),
	def[AskUserArgs](ToolAskUser, true, nil),
	def[DoneArgs](ToolDone, true, nil),
}

var (
	loadOnce sync.Once
	tools    []*Tool
	byName   map[string]*Tool
)

// load reads and compiles the schema of every tool. A schema that does not load is a build error, which the tests catch.
func load() {
	loadOnce.Do(func() {
		byName = map[string]*Tool{}
		for _, d := range definitions {
			data, err := schema.Tools.ReadFile("tools/" + d.name + ".json")
			if err != nil {
				panic("actions: no schema for the tool " + d.name + ": " + err.Error())
			}
			var head struct {
				ID          string                `json:"$id"`
				Description string                `json:"description"`
				Properties  map[string]propSchema `json:"properties"`
			}
			if err := json.Unmarshal(data, &head); err != nil {
				panic("actions: the schema of " + d.name + " is not JSON: " + err.Error())
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
			if err != nil {
				panic("actions: the schema of " + d.name + " is not JSON: " + err.Error())
			}
			c := jsonschema.NewCompiler()
			if err := c.AddResource(head.ID, doc); err != nil {
				panic("actions: the schema of " + d.name + ": " + err.Error())
			}
			compiled, err := c.Compile(head.ID)
			if err != nil {
				panic("actions: the schema of " + d.name + " does not compile: " + err.Error())
			}
			t := &Tool{Name: d.name, Description: head.Description, Compact: d.compact, Schema: data,
				props: head.Properties, compiled: compiled, newArgs: d.newArgs, check: d.check}
			tools = append(tools, t)
			byName[d.name] = t
		}
	})
}

// Lookup finds a tool by name.
func Lookup(name string) (*Tool, bool) {
	load()
	t, ok := byName[name]
	return t, ok
}

// Tools lists every tool, in the order of the schema.
func Tools() []*Tool {
	load()
	return slices.Clone(tools)
}

// Names lists the names of every tool, in the order of the schema.
func Names() []string {
	load()
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// CompactTools lists the tools of the compact profile.
func CompactTools() []*Tool {
	load()
	var out []*Tool
	for _, t := range tools {
		if t.Compact {
			out = append(out, t)
		}
	}
	return out
}

// DefaultMaxActions is the number of actions a response may hold unless the session says otherwise.
const DefaultMaxActions = 3

// Envelope is the JSON Schema of a model's answer in JSON mode, with the limit on the number of actions set to
// maxActions.
func Envelope(maxActions int) (json.RawMessage, error) {
	if maxActions < 1 {
		return nil, fmt.Errorf("a response holds at least one action, not %d", maxActions)
	}
	var doc map[string]any
	if err := json.Unmarshal(schema.Response, &doc); err != nil {
		return nil, fmt.Errorf("the response schema is not JSON: %w", err)
	}
	props, _ := doc["properties"].(map[string]any)
	list, _ := props["actions"].(map[string]any)
	if list == nil {
		return nil, fmt.Errorf("the response schema has no actions")
	}
	list["maxItems"] = maxActions
	return json.Marshal(doc)
}

// toolList is the names for messages: "screenshot, exec, ...".
func toolList() string { return strings.Join(Names(), ", ") }
