// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/accessibility"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
)

// Bounds on what a page says about its elements: it is untrusted text.
const (
	maxNameRunes  = 300
	maxDescRunes  = 200
	maxValueRunes = 500
	// passwordRole is how a password field is reported, as the accessibility service reports it for desktop programs, so
	// the policy that asks for confirmation before typing into one needs only one rule.
	passwordRole = "password text"
)

// operable roles are reported even without a name; named roles only with one (a heading, an image with a description,
// a dialog). Everything else (text runs, paragraphs, generic containers, layout) is left out of the tree.
var (
	operableRoles = map[string]bool{
		"button": true, "link": true, "textbox": true, "searchbox": true, "combobox": true, "listbox": true, "option": true,
		"checkbox": true, "radio": true, "switch": true, "slider": true, "spinbutton": true, "menuitem": true,
		"menuitemcheckbox": true, "menuitemradio": true, "tab": true, "treeitem": true, "disclosuretriangle": true,
		"popupbutton": true, "listboxoption": true,
	}
	namedRoles = map[string]bool{"heading": true, "img": true, "image": true, "dialog": true, "alertdialog": true, "alert": true}
	// roles whose value is the content of a field or a position: the only ones whose value is reported.
	valueRoles = map[string]bool{"textbox": true, "searchbox": true, "combobox": true, "spinbutton": true, "slider": true}
)

// jsonString reads the text of a protocol value: a string, or a number or a boolean written as text.
func jsonString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var x any
	if err := json.Unmarshal([]byte(v.Value), &x); err != nil {
		return ""
	}
	switch t := x.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// property finds a property of a node by name.
func property(n *accessibility.Node, name accessibility.PropertyName) (string, bool) {
	for _, p := range n.Properties {
		if p != nil && p.Name == name {
			return jsonString(p.Value), true
		}
	}
	return "", false
}

// states lists the properties of a node a model needs to know about, in a fixed order.
func states(n *accessibility.Node) []string {
	var out []string
	on := func(name accessibility.PropertyName) bool {
		v, ok := property(n, name)
		return ok && v == "true"
	}
	if on(accessibility.PropertyNameFocused) {
		out = append(out, "focused")
	}
	if on(accessibility.PropertyNameDisabled) {
		out = append(out, "disabled")
	}
	switch v, _ := property(n, accessibility.PropertyNameChecked); v {
	case "true":
		out = append(out, "checked")
	case "mixed":
		out = append(out, "indeterminate")
	}
	switch v, _ := property(n, accessibility.PropertyNamePressed); v {
	case "true":
		out = append(out, "pressed")
	case "mixed":
		out = append(out, "indeterminate")
	}
	if on(accessibility.PropertyNameSelected) {
		out = append(out, "selected")
	}
	if v, ok := property(n, accessibility.PropertyNameExpanded); ok {
		if v == "true" {
			out = append(out, "expanded")
		} else {
			out = append(out, "collapsed")
		}
	}
	if _, ok := property(n, accessibility.PropertyNameEditable); ok {
		out = append(out, "editable")
	}
	if on(accessibility.PropertyNameMultiline) {
		out = append(out, "multi_line")
	}
	if on(accessibility.PropertyNameRequired) {
		out = append(out, "required")
	}
	if on(accessibility.PropertyNameReadonly) {
		out = append(out, "read_only")
	}
	if v, ok := property(n, accessibility.PropertyNameInvalid); ok && v != "false" && v != "" {
		out = append(out, "invalid")
	}
	if v, ok := property(n, accessibility.PropertyNameHasPopup); ok && v != "false" && v != "" {
		out = append(out, "has_popup")
	}
	if on(accessibility.PropertyNameModal) {
		out = append(out, "modal")
	}
	if on(accessibility.PropertyNameBusy) {
		out = append(out, "busy")
	}
	return out
}

// MapAX turns the nodes of the browser's accessibility tree into the ones worth reporting, in the order given (document
// order). passwords holds the backend ids of password fields, which are reported as such and whose value is never read.
func MapAX(nodes []*accessibility.Node, passwords map[int64]bool) []AXNode {
	var out []AXNode
	for _, n := range nodes {
		if n == nil || n.Ignored || n.BackendDOMNodeID == 0 {
			continue
		}
		role := strings.ToLower(jsonString(n.Role))
		name := textclean.Line(jsonString(n.Name), maxNameRunes)
		isPassword := passwords[int64(n.BackendDOMNodeID)]
		if isPassword {
			role = passwordRole
		}
		switch {
		case isPassword, operableRoles[role]:
		case namedRoles[role] && name != "":
		default:
			continue
		}
		node := AXNode{
			Node: int64(n.BackendDOMNodeID), Role: role, Name: name,
			Description: textclean.Line(jsonString(n.Description), maxDescRunes), States: states(n),
		}
		if valueRoles[role] && !isPassword {
			node.Value = textclean.Block(jsonString(n.Value), maxValueRunes)
		}
		out = append(out, node)
	}
	return out
}
