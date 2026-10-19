// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"encoding/json"
	"fmt"

	"github.com/PacifAIst/controlsnack/schema"
)

// The renderings of a Set (PLAN.md section 5.3): what each kind of backend is given.
const (
	// FormatOpenAI is the `tools` array of an OpenAI-compatible chat request: one function for each tool.
	FormatOpenAI = "openai"
	// FormatOllama is the `tools` array of an Ollama chat request (the same functions).
	FormatOllama = "ollama"
	// FormatAnthropic is the `tools` array of an Anthropic messages request: a name, a description and an input_schema.
	FormatAnthropic = "anthropic"
	// FormatJSONMode is the schema of a model's whole answer, for a backend that takes a response format, or for the
	// prompt of a backend that does not.
	FormatJSONMode = "jsonmode"
)

// reasonDescription is what the model is told about the optional "reason" every tool takes in a native tool call.
const reasonDescription = "A short note on why you do this; the user sees it."

// parameters is the schema of a tool's arguments as a backend is given it: the schema file without the keywords that
// name the file or repeat the tool's description, every enum with its type (a few servers want one), and, when
// withReason is set, the optional "reason" that a native tool call carries beside the arguments.
func (t *Tool) parameters(withReason bool) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(t.Schema, &doc); err != nil {
		return nil, fmt.Errorf("the schema of %s is not JSON: %w", t.Name, err)
	}
	for _, k := range []string{"$schema", "$id", "title", "description"} {
		delete(doc, k)
	}
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		doc["properties"] = props
	}
	for _, v := range props {
		if p, ok := v.(map[string]any); ok {
			if _, typed := p["type"]; !typed {
				if _, isEnum := p["enum"]; isEnum {
					p["type"] = "string"
				}
			}
		}
	}
	if withReason {
		props["reason"] = map[string]any{"type": "string", "maxLength": MaxReasonRunes, "description": reasonDescription}
	}
	return doc, nil
}

// Render writes what a backend is given of the set, as JSON: the `tools` array for openai, ollama and anthropic, the
// response schema (up to DefaultMaxActions actions) for jsonmode.
func (s *Set) Render(format string) (json.RawMessage, error) {
	switch format {
	case FormatOpenAI, FormatOllama:
		list := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			p, err := t.parameters(true)
			if err != nil {
				return nil, err
			}
			list = append(list, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": p}})
		}
		return json.Marshal(list)
	case FormatAnthropic:
		list := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			p, err := t.parameters(true)
			if err != nil {
				return nil, err
			}
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "input_schema": p})
		}
		return json.Marshal(list)
	case FormatJSONMode:
		return s.Envelope(DefaultMaxActions, false)
	}
	return nil, fmt.Errorf("there is no rendering %q (the formats are %s, %s, %s and %s)", format, FormatOpenAI, FormatAnthropic, FormatOllama, FormatJSONMode)
}

// Envelope is the JSON Schema of a model's answer in JSON mode: the tool of each action is one of the set's tools, and
// there are at most maxActions of them. With strict set, the arguments of each action must also follow the schema of
// the tool it names (a oneOf with one branch for each tool), which a backend that builds a grammar from the schema can
// enforce while the model writes; without it args is any object and Parse checks it afterwards.
func (s *Set) Envelope(maxActions int, strict bool) (json.RawMessage, error) {
	if maxActions < 1 {
		return nil, fmt.Errorf("a response holds at least one action, not %d", maxActions)
	}
	var doc map[string]any
	if err := json.Unmarshal(schema.Response, &doc); err != nil {
		return nil, fmt.Errorf("the response schema is not JSON: %w", err)
	}
	props, _ := doc["properties"].(map[string]any)
	list, _ := props["actions"].(map[string]any)
	item, _ := list["items"].(map[string]any)
	itemProps, _ := item["properties"].(map[string]any)
	if list == nil || item == nil || itemProps == nil {
		return nil, fmt.Errorf("the response schema has no actions")
	}
	list["maxItems"] = maxActions
	itemProps["tool"] = map[string]any{"enum": s.Names()}

	if strict {
		branches := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			args, err := t.parameters(false)
			if err != nil {
				return nil, err
			}
			dropLongBounds(args)
			branches = append(branches, map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"tool", "args"},
				"properties": map[string]any{
					"tool":   map[string]any{"const": t.Name},
					"args":   args,
					"reason": itemProps["reason"],
				},
			})
		}
		list["items"] = map[string]any{"oneOf": branches}
	}
	return json.Marshal(doc)
}

// grammarMaxLength is the longest bound on a string that stays in the strict envelope. A server that builds a grammar from
// the schema (llama.cpp, and Ollama, which uses it) turns a bound into that many repetitions and refuses more than about
// two thousand ("number of repetitions exceeds sane defaults"), so a command of up to 4000 characters, or an input of up
// to 65536, would make the whole schema unusable. The bound is not lost: Set.Parse checks the length of what the model
// wrote against the tool's own schema, which keeps it.
const grammarMaxLength = 1000

// dropLongBounds removes the length bounds above grammarMaxLength from the properties of a tool's argument schema.
func dropLongBounds(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	for _, v := range props {
		p, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if n, isNum := p["maxLength"].(float64); isNum && n > grammarMaxLength {
			delete(p, "maxLength")
		}
		if items, ok := p["items"].(map[string]any); ok {
			if n, isNum := items["maxLength"].(float64); isNum && n > grammarMaxLength {
				delete(items, "maxLength")
			}
		}
	}
}
