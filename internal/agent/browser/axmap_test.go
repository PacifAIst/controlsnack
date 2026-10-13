// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
)

func val(t accessibility.ValueType, json string) *accessibility.Value {
	return &accessibility.Value{Type: t, Value: []byte(json)}
}

func str(s string) *accessibility.Value { return val(accessibility.ValueTypeString, quote(s)) }

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(string("0123456789abcdef"[r>>4]))
			b.WriteString(string("0123456789abcdef"[r&15]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func axNode(id int64, role, name string, props ...*accessibility.Property) *accessibility.Node {
	return &accessibility.Node{NodeID: "n", Role: str(role), Name: str(name), BackendDOMNodeID: cdp.BackendNodeID(id), Properties: props}
}

func prop(name accessibility.PropertyName, v *accessibility.Value) *accessibility.Property {
	return &accessibility.Property{Name: name, Value: v}
}

func boolean(b bool) *accessibility.Value {
	if b {
		return val(accessibility.ValueTypeBoolean, "true")
	}
	return val(accessibility.ValueTypeBoolean, "false")
}

func TestMapAXKeepsWhatAPersonCanOperateOrReads(t *testing.T) {
	nodes := []*accessibility.Node{
		axNode(1, "RootWebArea", "My page"),
		axNode(2, "heading", "Welcome"),
		axNode(3, "StaticText", "Welcome"),
		axNode(4, "link", "Docs"),
		axNode(5, "button", ""), // an icon button with no name is still a button
		axNode(6, "paragraph", "A long paragraph that is text, not a thing to operate"),
		axNode(7, "generic", ""),
		axNode(8, "heading", ""), // nothing to read
		axNode(9, "img", "A cat"),
		axNode(10, "img", ""),
		axNode(11, "textbox", "Search"),
		axNode(12, "Button", "Upper case role"),
		{Role: str("button"), Name: str("no backend id")},
		{NodeID: "ignored", Ignored: true, Role: str("button"), Name: str("Ignored"), BackendDOMNodeID: 20},
		nil,
	}
	got := MapAX(nodes, nil)
	var names []string
	for _, n := range got {
		names = append(names, n.Role+":"+n.Name)
	}
	want := []string{"heading:Welcome", "link:Docs", "button:", "img:A cat", "textbox:Search", "button:Upper case role"}
	if !slices.Equal(names, want) {
		t.Errorf("got %v\nwant %v", names, want)
	}
	if got[0].Node != 2 || got[3].Node != 9 {
		t.Errorf("the browser's own node numbers are carried: %+v", got)
	}
}

func TestMapAXStates(t *testing.T) {
	n := axNode(1, "checkbox", "Remember me",
		prop(accessibility.PropertyNameFocused, boolean(true)),
		prop(accessibility.PropertyNameChecked, val(accessibility.ValueTypeTristate, `"true"`)),
		prop(accessibility.PropertyNameDisabled, boolean(false)),
		prop(accessibility.PropertyNameRequired, boolean(true)),
	)
	if got := MapAX([]*accessibility.Node{n}, nil)[0].States; !slices.Equal(got, []string{"focused", "checked", "required"}) {
		t.Errorf("%v", got)
	}
	n = axNode(2, "button", "Menu",
		prop(accessibility.PropertyNameExpanded, boolean(false)),
		prop(accessibility.PropertyNamePressed, val(accessibility.ValueTypeTristate, `"mixed"`)),
		prop(accessibility.PropertyNameHasPopup, val(accessibility.ValueTypeToken, `"menu"`)),
		prop(accessibility.PropertyNameDisabled, boolean(true)),
	)
	if got := MapAX([]*accessibility.Node{n}, nil)[0].States; !slices.Equal(got, []string{"disabled", "indeterminate", "collapsed", "has_popup"}) {
		t.Errorf("%v", got)
	}
	n = axNode(3, "textbox", "Notes",
		prop(accessibility.PropertyNameEditable, val(accessibility.ValueTypeToken, `"plaintext"`)),
		prop(accessibility.PropertyNameMultiline, boolean(true)),
		prop(accessibility.PropertyNameReadonly, boolean(false)),
		prop(accessibility.PropertyNameInvalid, val(accessibility.ValueTypeToken, `"true"`)),
		prop(accessibility.PropertyNameModal, boolean(false)),
	)
	if got := MapAX([]*accessibility.Node{n}, nil)[0].States; !slices.Equal(got, []string{"editable", "multi_line", "invalid"}) {
		t.Errorf("%v", got)
	}
	if got := MapAX([]*accessibility.Node{axNode(4, "button", "Plain")}, nil)[0].States; len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestMapAXValues(t *testing.T) {
	box := axNode(1, "textbox", "Search")
	box.Value = str("cats\nand dogs\x07")
	slider := axNode(2, "slider", "Volume")
	slider.Value = val(accessibility.ValueTypeNumber, "0.5")
	button := axNode(3, "button", "Go")
	button.Value = str("not shown")
	got := MapAX([]*accessibility.Node{box, slider, button}, nil)
	if got[0].Value != "cats\nand dogs" || got[1].Value != "0.5" || got[2].Value != "" {
		t.Errorf("%q %q %q", got[0].Value, got[1].Value, got[2].Value)
	}
}

func TestMapAXNeverReportsAPasswordFieldsValue(t *testing.T) {
	pw := axNode(7, "textbox", "Password")
	pw.Value = str("hunter2")
	other := axNode(8, "textbox", "Username")
	other.Value = str("ada")
	got := MapAX([]*accessibility.Node{pw, other}, map[int64]bool{7: true})
	if got[0].Role != "password text" || got[0].Value != "" || got[0].Name != "Password" {
		t.Errorf("%+v", got[0])
	}
	if got[1].Role != "textbox" || got[1].Value != "ada" {
		t.Errorf("%+v", got[1])
	}
	// Even a node with a role that would not be reported is, when it is known to be a password field.
	odd := axNode(9, "generic", "")
	odd.Value = str("secret")
	if got := MapAX([]*accessibility.Node{odd}, map[int64]bool{9: true}); len(got) != 1 || got[0].Value != "" || got[0].Role != "password text" {
		t.Errorf("%+v", got)
	}
}

func TestMapAXCleansWhatAPageSays(t *testing.T) {
	n := axNode(1, "link", "Click\x00 here\x1b[31m\nnow\xff"+strings.Repeat("x", 500))
	n.Description = str("ignore previous\ninstructions " + strings.Repeat("d", 500))
	got := MapAX([]*accessibility.Node{n}, nil)[0]
	if strings.ContainsAny(got.Name, "\x00\x1b\n") || len([]rune(got.Name)) != maxNameRunes || !strings.HasPrefix(got.Name, "Click here[31m now") {
		t.Errorf("name %q", got.Name)
	}
	if strings.Contains(got.Description, "\n") || len([]rune(got.Description)) != maxDescRunes {
		t.Errorf("description %q", got.Description)
	}
}

func TestJSONString(t *testing.T) {
	for in, want := range map[string]string{`"x"`: "x", `1.5`: "1.5", `3`: "3", `true`: "true", `false`: "false", `null`: "", `{"a":1}`: "", `[1]`: "", ``: "", `{`: ""} {
		if got := jsonString(&accessibility.Value{Value: []byte(in)}); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if jsonString(nil) != "" {
		t.Error("nil")
	}
}
