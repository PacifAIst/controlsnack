// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"github.com/PacifAIst/controlsnack-local/internal/llm/prompt"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// Window converts the active window to the picture's coordinates; nil stays nil.
func (m Mapper) Window(w *protocol.Window) *prompt.Window {
	if w == nil {
		return nil
	}
	return &prompt.Window{Class: w.Class, Title: w.Title, Bounds: m.Rect(w.Bounds)}
}

// UITree converts the accessibility tree the agent returned to the picture's coordinates. scope is the scope that was asked
// for (the agent does not echo it).
func (m Mapper) UITree(res protocol.A11yTreeRes, scope string) *prompt.UITree {
	t := &prompt.UITree{Generation: res.Generation, Scope: scope, Truncated: res.Truncated, Note: res.Note}
	t.Nodes = m.Nodes(res.Nodes)
	return t
}

// Nodes converts accessibility nodes (of the desktop or of a web page) to the picture's coordinates.
func (m Mapper) Nodes(nodes []protocol.A11yNode) []prompt.Node {
	out := make([]prompt.Node, len(nodes))
	for i, n := range nodes {
		out[i] = prompt.Node{
			ID: n.ID, Role: n.Role, Name: n.Name, Description: n.Description, Value: n.Value,
			States: n.States, Actions: n.Actions, Bounds: m.Rect(n.Bounds),
		}
	}
	return out
}

// OCR converts recognized text to the picture's coordinates.
func (m Mapper) OCR(res protocol.OCRRes) *prompt.OCRBlock {
	b := &prompt.OCRBlock{Generation: res.Generation, Truncated: res.Truncated}
	b.Lines = make([]prompt.OCRLine, len(res.Lines))
	for i, l := range res.Lines {
		b.Lines[i] = prompt.OCRLine{ID: l.ID, Text: l.Text, Bounds: m.Rect(l.Bounds)}
	}
	return b
}
