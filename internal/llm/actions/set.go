// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"encoding/json"
	"fmt"
	"slices"
)

// The tool profiles (PLAN.md section 5.3): which tools a session offers.
const (
	// ProfileAuto picks one of the others from the backend: compact for a local model and for text-only mode, full otherwise.
	ProfileAuto = "auto"
	// ProfileFull offers every tool (the default for remote APIs).
	ProfileFull = "full"
	// ProfileCompact offers exec, read_accessibility_tree, click_element, mouse, key, type_text, wait_for, ask_user and
	// done: fewer choices for a small model (the default for local backends and text-only mode).
	ProfileCompact = "compact"
)

// ResolveProfile turns the setting of --tools into full or compact: auto means compact for a local backend and for
// text-only mode, full for a remote one.
func ResolveProfile(requested string, local, textOnly bool) (string, error) {
	switch requested {
	case ProfileFull, ProfileCompact:
		return requested, nil
	case ProfileAuto, "":
		if local || textOnly {
			return ProfileCompact, nil
		}
		return ProfileFull, nil
	}
	return "", fmt.Errorf("tool profile %q is not auto, full or compact", requested)
}

// Set is the tools a session offers: what the model is told about, what it may call, and what a call is checked against.
type Set struct {
	profile string
	tools   []*Tool
}

// NewSet returns the tools of a profile, full or compact (see ResolveProfile for auto).
func NewSet(profile string) (*Set, error) {
	switch profile {
	case ProfileFull:
		return &Set{profile: profile, tools: Tools()}, nil
	case ProfileCompact:
		return &Set{profile: profile, tools: CompactTools()}, nil
	}
	return nil, fmt.Errorf("tool profile %q is not full or compact", profile)
}

// Profile is full or compact.
func (s *Set) Profile() string { return s.profile }

// Tools lists the tools of the set, in the order of the schema.
func (s *Set) Tools() []*Tool { return slices.Clone(s.tools) }

// Names lists the names of the tools of the set.
func (s *Set) Names() []string {
	names := make([]string, len(s.tools))
	for i, t := range s.tools {
		names[i] = t.Name
	}
	return names
}

// Has reports whether the set offers a tool.
func (s *Set) Has(name string) bool {
	return slices.ContainsFunc(s.tools, func(t *Tool) bool { return t.Name == name })
}

// Parse is Parse for the tools of the set: a tool that exists but is not offered in this session is as unknown to the
// model as one that does not exist, and it is told which tools it has.
func (s *Set) Parse(tool string, args json.RawMessage) (Action, error) {
	if !s.Has(tool) {
		return Action{}, newError(CodeUnknownTool, tool, Problem{Path: "tool", Message: fmt.Sprintf("there is no tool %q in this session; the tools are %s", tool, joinNames(s.Names()))})
	}
	return Parse(tool, args)
}

// Without returns the set minus the named tools. A session that cannot read the accessibility tree does not offer the tools
// that work on its ids: a small model told about click_element and given no id to use will invent ids until it is stopped.
// The profile name stays, since it says which tools the session started from.
func (s *Set) Without(names ...string) *Set {
	out := &Set{profile: s.profile}
	for _, t := range s.tools {
		if !slices.Contains(names, t.Name) {
			out.tools = append(out.tools, t)
		}
	}
	return out
}

// WithoutTree returns the set minus the tools that depend on the accessibility tree: reading it, setting the text of an
// element and, unless the recognized text of the screen can be read (it gives ids of its own), clicking an element.
func (s *Set) WithoutTree() *Set {
	drop := []string{ToolReadTree, ToolSetText}
	if !s.Has(ToolReadScreenText) {
		drop = append(drop, ToolClickElement)
	}
	return s.Without(drop...)
}
