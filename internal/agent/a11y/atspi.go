// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

// Role numbers of AT-SPI (atspi-constants.h, AtspiRole) and the names the agent reports. The names are AT-SPI's own
// (GetRoleName), lower case with spaces; the end-to-end test compares this table with what the real guest says for
// every node it sees.
var roleNames = [...]string{
	0: "invalid", 1: "accel label", 2: "alert", 3: "animation",
	4: "arrow", 5: "calendar", 6: "canvas", 7: "check box",
	8: "check menu item", 9: "color chooser", 10: "column header", 11: "combo box",
	12: "date editor", 13: "desktop icon", 14: "desktop frame", 15: "dial",
	16: "dialog", 17: "directory pane", 18: "drawing area", 19: "file chooser",
	20: "filler", 21: "focus traversable", 22: "font chooser", 23: "frame",
	24: "glass pane", 25: "html container", 26: "icon", 27: "image",
	28: "internal frame", 29: "label", 30: "layered pane", 31: "list",
	32: "list item", 33: "menu", 34: "menu bar", 35: "menu item",
	36: "option pane", 37: "page tab", 38: "page tab list", 39: "panel",
	40: "password text", 41: "popup menu", 42: "progress bar", 43: "button",
	44: "radio button", 45: "radio menu item", 46: "root pane", 47: "row header",
	48: "scroll bar", 49: "scroll pane", 50: "separator", 51: "slider",
	52: "spin button", 53: "split pane", 54: "statusbar", 55: "table",
	56: "table cell", 57: "table column header", 58: "table row header", 59: "tear off menu item",
	60: "terminal", 61: "text", 62: "toggle button", 63: "tool bar",
	64: "tool tip", 65: "tree", 66: "tree table", 67: "unknown",
	68: "viewport", 69: "window", 70: "extended", 71: "header",
	72: "footer", 73: "paragraph", 74: "ruler", 75: "application",
	76: "autocomplete", 77: "editbar", 78: "embedded", 79: "entry",
	80: "chart", 81: "caption", 82: "document frame", 83: "heading",
	84: "page", 85: "section", 86: "redundant object", 87: "form",
	88: "link", 89: "input method window", 90: "table row", 91: "tree item",
	92: "document spreadsheet", 93: "document presentation", 94: "document text", 95: "document web",
	96: "document email", 97: "comment", 98: "list box", 99: "grouping",
	100: "image map", 101: "notification", 102: "info bar", 103: "level bar",
	104: "title bar", 105: "block quote", 106: "audio", 107: "video",
	108: "definition", 109: "article", 110: "landmark", 111: "log",
	112: "marquee", 113: "math", 114: "rating", 115: "timer",
	116: "static", 117: "math fraction", 118: "math root", 119: "subscript",
	120: "superscript", 121: "description list", 122: "description term", 123: "description value",
	124: "footnote", 125: "content deletion", 126: "content insertion", 127: "mark",
	128: "suggestion", 129: "push button menu", 130: "switch"}

// RoleName names an AT-SPI role number; numbers this table does not know are "unknown".
func RoleName(role uint32) string {
	if int(role) < len(roleNames) && roleNames[role] != "" {
		return roleNames[role]
	}
	return "unknown"
}

// States of AT-SPI (AtspiStateType): the bit numbers of the 64-bit state set.
const (
	StateActive        = 1
	StateBusy          = 3
	StateChecked       = 4
	StateCollapsed     = 5
	StateDefunct       = 6
	StateEditable      = 7
	StateEnabled       = 8
	StateExpandable    = 9
	StateExpanded      = 10
	StateFocusable     = 11
	StateFocused       = 12
	StateHasPopup      = 42
	StateIndeterminate = 32
	StateModal         = 16
	StateMultiLine     = 17
	StatePressed       = 20
	StateReadOnly      = 43
	StateRequired      = 33
	StateSelected      = 23
	StateSensitive     = 24
	StateShowing       = 25
	StateSingleLine    = 26
	StateVisible       = 30
	StateVisited       = 40
)

// StateSet is the 64 bits of an AT-SPI state set (two 32-bit words as the bus carries them, the first holding states
// 0 to 31).
type StateSet uint64

// NewStateSet joins the two words the bus delivers.
func NewStateSet(words []uint32) StateSet {
	var s StateSet
	if len(words) > 0 {
		s |= StateSet(words[0])
	}
	if len(words) > 1 {
		s |= StateSet(words[1]) << 32
	}
	return s
}

// Has reports whether the state is set.
func (s StateSet) Has(state uint) bool { return state < 64 && s&(1<<state) != 0 }

// reported lists the states a model is told about, in this order. Most states are noise (opaque, resizable,
// selectable, ...): these are the ones that decide what to do with an element. "disabled" is the absence of enabled
// and sensitive, which is the exception worth mentioning.
var reported = []struct {
	state uint
	name  string
}{
	{StateActive, "active"}, {StateFocused, "focused"}, {StateChecked, "checked"}, {StateSelected, "selected"},
	{StateExpanded, "expanded"}, {StateCollapsed, "collapsed"}, {StateEditable, "editable"}, {StatePressed, "pressed"},
	{StateModal, "modal"}, {StateMultiLine, "multi_line"}, {StateIndeterminate, "indeterminate"}, {StateRequired, "required"},
	{StateReadOnly, "read_only"}, {StateVisited, "visited"}, {StateHasPopup, "has_popup"}, {StateBusy, "busy"},
}

// Names returns the reported states that are set, plus "disabled" when the element is neither enabled nor sensitive.
func (s StateSet) Names() []string {
	var out []string
	for _, r := range reported {
		if s.Has(r.state) {
			out = append(out, r.name)
		}
	}
	if !s.Has(StateEnabled) && !s.Has(StateSensitive) {
		out = append(out, "disabled")
	}
	return out
}

// Showing reports whether an element is on screen: shown and visible. Elements that are not, and everything below
// them, are not part of what a person sees.
func (s StateSet) Showing() bool { return s.Has(StateShowing) && s.Has(StateVisible) }
