// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/agent/x11"
)

// Parse validates the arguments of a tool as the model sent them and returns the action. args is the JSON object of the
// arguments; an empty value or null means no arguments. A native tool call may carry the model's reason among the
// arguments ("reason"): it is taken out and becomes the action's Reason. On failure the error is an *Error with every
// problem found.
func Parse(tool string, args json.RawMessage) (Action, error) {
	t, ok := Lookup(tool)
	if !ok {
		return Action{}, newError(CodeUnknownTool, tool, Problem{Path: "tool", Message: fmt.Sprintf("there is no tool %q; the tools are %s", tool, toolList())})
	}
	fail := func(ps ...Problem) (Action, error) { return Action{}, newError(CodeInvalidArgs, tool, ps...) }

	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return fail(Problem{Path: "args", Message: "is not valid JSON: " + err.Error()})
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return fail(Problem{Path: "args", Message: "must be an object of the tool's arguments"})
	}

	var reason string
	var problems []Problem
	if v, has := obj["reason"]; has && !t.takes("reason") {
		delete(obj, "reason")
		switch r := v.(type) {
		case string:
			if utf8.RuneCountInString(r) > MaxReasonRunes {
				problems = append(problems, Problem{Path: "args.reason", Message: fmt.Sprintf("is longer than %d characters", MaxReasonRunes)})
			}
			reason = r
		case nil:
		default:
			problems = append(problems, Problem{Path: "args.reason", Message: "must be a string"})
		}
	}

	t.normalize(obj)
	if err := t.compiled.Validate(obj); err != nil {
		return fail(append(problems, t.problemsOf(err)...)...)
	}
	if len(problems) > 0 {
		return fail(problems...)
	}

	raw, err := json.Marshal(obj)
	if err != nil {
		return fail(Problem{Path: "args", Message: err.Error()})
	}
	a := t.newArgs()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(a); err != nil {
		return fail(Problem{Path: "args", Message: err.Error()})
	}
	if d, ok := a.(interface{ applyDefaults() }); ok {
		d.applyDefaults()
	}
	if t.check != nil {
		if ps := t.check(a); len(ps) > 0 {
			return fail(ps...)
		}
	}
	return Action{Tool: tool, Args: a, Reason: reason, Raw: raw}, nil
}

// Bounds on what an error repeats of the model's own words (a tool name, the name of an argument that does not exist,
// a value that is not allowed): it reaches the model again, the viewer and the audit log, so it is one clean, short line.
const (
	maxToolRunes    = 64
	maxProblemRunes = 300
)

// newError makes an Error whose texts are safe to show and to log.
func newError(code, tool string, problems ...Problem) *Error {
	clean := make([]Problem, len(problems))
	for i, p := range problems {
		clean[i] = Problem{Path: textclean.Line(p.Path, maxProblemRunes), Message: textclean.Line(p.Message, maxProblemRunes)}
	}
	return &Error{Code: code, Tool: textclean.Line(tool, maxToolRunes), Problems: clean}
}

// takes reports whether the tool's schema has an argument of its own with this name.
func (t *Tool) takes(name string) bool {
	_, ok := t.props[name]
	return ok
}

// normalize writes the numbers of integer arguments that are whole but written with a fraction or an exponent (640.0,
// 6.4e2) as integers: a model that says 640.0 means 640.
func (t *Tool) normalize(obj map[string]any) {
	for name, p := range t.props {
		v, ok := obj[name]
		if !ok {
			continue
		}
		switch {
		case p.Type == "integer":
			obj[name] = wholeNumber(v)
		case p.Type == "array" && p.Items != nil && p.Items.Type == "integer":
			if list, ok := v.([]any); ok {
				for i := range list {
					list[i] = wholeNumber(list[i])
				}
			}
		}
	}
}

func wholeNumber(v any) any {
	n, ok := v.(json.Number)
	if !ok || !strings.ContainsAny(string(n), ".eE") {
		return v
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return v
	}
	return json.Number(strconv.FormatInt(int64(f), 10))
}

// problemsOf turns what the schema validator says into problems, one for each thing wrong.
func (t *Tool) problemsOf(err error) []Problem {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Problem{{Path: "args", Message: err.Error()}}
	}
	var out []Problem
	seen := map[string]bool{}
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if u.Error != nil {
			msg := u.Error.String()
			if strings.HasPrefix(msg, "additional properties") {
				msg += "; the arguments of " + t.Name + " are " + strings.Join(t.argNames(), ", ")
			}
			p := Problem{Path: pathOf(u.InstanceLocation), Message: msg}
			if !seen[p.String()] {
				seen[p.String()] = true
				out = append(out, p)
			}
		}
		for _, c := range u.Errors {
			walk(c)
		}
	}
	walk(*ve.BasicOutput())
	if len(out) == 0 {
		out = append(out, Problem{Path: "args", Message: "does not match the schema of " + t.Name})
	}
	return out
}

func (t *Tool) argNames() []string {
	names := make([]string, 0, len(t.props))
	for n := range t.props {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"(none)"}
	}
	return names
}

// pathOf writes a JSON pointer as a path: "" is "args", "/region/2" is "args.region[2]".
func pathOf(pointer string) string {
	path := "args"
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if part == "" {
			continue
		}
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		if _, err := strconv.Atoi(part); err == nil {
			path += "[" + part + "]"
		} else {
			path += "." + part
		}
	}
	return path
}

// The checks beyond the schema: what depends on the value of another argument, and the limits counted in bytes.

func checkReadTree(a *ReadTreeArgs) []Problem {
	if !slices.Contains(Scopes, a.Scope) {
		return []Problem{{Path: "args.scope", Message: "must be one of " + strings.Join(Scopes, ", ")}}
	}
	return nil
}

func checkReadScreenText(a *ReadScreenTextArgs) []Problem {
	if a.Region != nil && (a.Region[2] < 1 || a.Region[3] < 1) {
		return []Problem{{Path: "args.region", Message: "needs a width and a height of at least 1: [x, y, width, height]"}}
	}
	return nil
}

func checkExec(a *ExecArgs) []Problem {
	var ps []Problem
	if strings.TrimSpace(a.Command) == "" {
		ps = append(ps, Problem{Path: "args.command", Message: "is empty"})
	}
	if len(a.Stdin) > MaxStdinBytes {
		ps = append(ps, Problem{Path: "args.stdin", Message: fmt.Sprintf("is longer than %d bytes", MaxStdinBytes)})
	}
	return ps
}

func checkBrowser(a *BrowserArgs) []Problem {
	var ps []Problem
	need := func(field, value, why string) {
		if value == "" {
			ps = append(ps, Problem{Path: "args." + field, Message: "is required for op " + a.Op + why})
		}
	}
	switch a.Op {
	case "open", "navigate":
		need("url", a.URL, " (the address to go to)")
	case "click":
		need("target", a.Target, " (an id such as w12 from ax_tree, or a CSS selector)")
	case "type":
		need("target", a.Target, " (an id such as w12 from ax_tree, or a CSS selector)")
		need("text", a.Text, " (the text to enter; clear a field with set_text)")
	case "switch_tab":
		if a.Tab == 0 {
			ps = append(ps, Problem{Path: "args.tab", Message: "is required for op switch_tab (the tab's number as tabs lists it, from 1)"})
		}
	}
	if len(a.URL) > MaxURLBytes {
		ps = append(ps, Problem{Path: "args.url", Message: fmt.Sprintf("is longer than %d bytes", MaxURLBytes)})
	}
	return ps
}

func checkMouse(a *MouseArgs) []Problem {
	if a.Op == "drag" && (a.ToX == nil || a.ToY == nil) {
		return []Problem{{Path: "args.to_x", Message: "and to_y are required for op drag (where the drag ends)"}}
	}
	return nil
}

func checkKey(a *KeyArgs) []Problem {
	if _, err := x11.ParseCombo(a.Keys); err != nil {
		return []Problem{{Path: "args.keys", Message: err.Error()}}
	}
	return nil
}

func checkWindow(a *WindowArgs) []Problem {
	if (a.Op == "focus" || a.Op == "maximize") && a.WindowID == "" {
		return []Problem{{Path: "args.window_id", Message: "is required for op " + a.Op + " (an id from op list)"}}
	}
	return nil
}

func checkWaitFor(a *WaitForArgs) []Problem {
	switch a.Condition {
	case "text_visible", "window_title", "element":
		if strings.TrimSpace(a.Value) == "" {
			return []Problem{{Path: "args.value", Message: "is required for condition " + a.Condition + " (the text to wait for)"}}
		}
	}
	return nil
}
