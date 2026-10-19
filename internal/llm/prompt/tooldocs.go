// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PacifAIst/controlsnack/internal/llm/actions"
)

// ToolDocs describes the tools of a set in plain text, for a prompt of a mode where the tools are not passed to the
// backend as definitions: what each is for and its arguments, one line each. It is made from the same schemas that check
// the model's calls, so it cannot drift from them.
func ToolDocs(set *actions.Set) (string, error) {
	var b strings.Builder
	for i, t := range set.Tools() {
		if i > 0 {
			b.WriteByte('\n')
		}
		doc, err := toolDoc(t)
		if err != nil {
			return "", fmt.Errorf("describing the tool %s: %w", t.Name, err)
		}
		b.WriteString(doc)
	}
	return b.String(), nil
}

// property is what the docs read of a property of a tool's schema.
type property struct {
	Type        string            `json:"type"`
	Enum        []json.RawMessage `json:"enum"`
	Description string            `json:"description"`
	Minimum     *float64          `json:"minimum"`
	Maximum     *float64          `json:"maximum"`
	MaxLength   *int              `json:"maxLength"`
	MaxItems    *int              `json:"maxItems"`
	Default     json.RawMessage   `json:"default"`
	Items       *struct {
		Type string `json:"type"`
	} `json:"items"`
}

func toolDoc(t *actions.Tool) (string, error) {
	var s struct {
		Description string          `json:"description"`
		Required    []string        `json:"required"`
		Properties  json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(t.Schema, &s); err != nil {
		return "", err
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", t.Name, s.Description)
	if len(bytes.TrimSpace(s.Properties)) == 0 {
		b.WriteString("  (no arguments)\n")
		return b.String(), nil
	}
	names, props, err := orderedProperties(s.Properties)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		b.WriteString("  (no arguments)\n")
	}
	for _, name := range names {
		p := props[name]
		fmt.Fprintf(&b, "  %s (%s): %s\n", name, describeType(p, required[name]), p.Description)
	}
	return b.String(), nil
}

// orderedProperties reads the properties of a schema in the order the file lists them (a Go map would lose it).
func orderedProperties(raw json.RawMessage) ([]string, map[string]property, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, fmt.Errorf("the properties are not an object")
	}
	var names []string
	props := map[string]property{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name, ok := tok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("a property has no name")
		}
		var p property
		if err := dec.Decode(&p); err != nil {
			return nil, nil, fmt.Errorf("property %s: %w", name, err)
		}
		names = append(names, name)
		props[name] = p
	}
	return names, props, nil
}

// describeType writes the type of an argument with what bounds it: "string, required", "one of move, click, required",
// "integer 1 to 600, default 60".
func describeType(p property, required bool) string {
	var parts []string
	switch {
	case len(p.Enum) > 0:
		vals := make([]string, 0, len(p.Enum))
		for _, e := range p.Enum {
			var s string
			if json.Unmarshal(e, &s) == nil {
				vals = append(vals, s)
			} else {
				vals = append(vals, string(e))
			}
		}
		parts = append(parts, "one of "+strings.Join(vals, " | "))
	case p.Type == "array" && p.Items != nil:
		parts = append(parts, "list of "+p.Items.Type)
	default:
		parts = append(parts, p.Type)
	}
	switch {
	case p.Minimum != nil && p.Maximum != nil:
		parts = append(parts, fmt.Sprintf("%g to %g", *p.Minimum, *p.Maximum))
	case p.Minimum != nil:
		parts = append(parts, fmt.Sprintf("at least %g", *p.Minimum))
	case p.Maximum != nil:
		parts = append(parts, fmt.Sprintf("at most %g", *p.Maximum))
	}
	if p.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("up to %d characters", *p.MaxLength))
	}
	if p.MaxItems != nil {
		parts = append(parts, fmt.Sprintf("up to %d items", *p.MaxItems))
	}
	if len(p.Default) > 0 && string(p.Default) != "null" {
		parts = append(parts, "default "+string(p.Default))
	}
	if required {
		parts = append(parts, "required")
	}
	return strings.Join(parts, ", ")
}
