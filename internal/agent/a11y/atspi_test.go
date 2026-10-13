// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package a11y

import (
	"slices"
	"testing"
)

// The numbers are the ones of atspi-constants.h (AtspiRole). They are written out here on purpose: a table that was
// shifted by one once (FOCUS_TRAVERSABLE is easy to forget) must not be able to agree with itself.
func TestRoleNamesAreTheNumbersOfAtSpi(t *testing.T) {
	anchors := map[uint32]string{
		0: "invalid", 7: "check box", 20: "filler", 21: "focus traversable", 22: "font chooser", 23: "frame",
		29: "label", 33: "menu", 35: "menu item", 39: "panel", 40: "password text", 43: "button", 44: "radio button",
		50: "separator", 51: "slider", 60: "terminal", 61: "text", 62: "toggle button", 69: "window", 75: "application",
		79: "entry", 82: "document frame", 88: "link", 91: "tree item", 94: "document text", 95: "document web",
		129: "push button menu", 130: "switch", 1: "accel label", 54: "statusbar", 77: "editbar", 78: "embedded",
	}
	for n, want := range anchors {
		if got := RoleName(n); got != want {
			t.Errorf("role %d is %q, want %q", n, got, want)
		}
	}
	seen := map[string]uint32{}
	for n := uint32(0); n <= 130; n++ {
		name := RoleName(n)
		if name == "unknown" && n != 67 {
			t.Errorf("role %d has no name", n)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("roles %d and %d are both %q", other, n, name)
		}
		seen[name] = n
	}
	for _, n := range []uint32{131, 500, 1 << 31} {
		if got := RoleName(n); got != "unknown" {
			t.Errorf("role %d is %q, want unknown", n, got)
		}
	}
	for n, want := range map[uint32]string{rolePushButton: "button", roleFrame: "frame", roleEntry: "entry", roleApp: "application",
		rolePassword: "password text", roleLabel: "label", roleMenuItem: "menu item", rolePanel: "panel", roleSeparator: "separator",
		roleSlider: "slider", roleFiller: "filler", roleCheckBox: "check box"} {
		if got := RoleName(n); got != want {
			t.Errorf("the test constant %d is %q, want %q", n, got, want)
		}
	}
}

func TestStateSet(t *testing.T) {
	// State 42 (has popup) lives in the second word, bit 10.
	s := NewStateSet([]uint32{1<<StateEnabled | 1<<StateSensitive | 1<<StateShowing | 1<<StateVisible | 1<<StateFocused | 1<<StateChecked,
		1 << (StateHasPopup - 32)})
	for _, st := range []uint{StateEnabled, StateSensitive, StateShowing, StateVisible, StateFocused, StateChecked, StateHasPopup} {
		if !s.Has(st) {
			t.Errorf("state %d should be set", st)
		}
	}
	for _, st := range []uint{StateActive, StateDefunct, StateEditable, StateReadOnly, 63, 64, 1000} {
		if s.Has(st) {
			t.Errorf("state %d should not be set", st)
		}
	}
	if !s.Showing() {
		t.Error("shown and visible is showing")
	}
	if want := []string{"focused", "checked", "has_popup"}; !slices.Equal(s.Names(), want) {
		t.Errorf("names %v, want %v", s.Names(), want)
	}

	if StateSet(1 << StateShowing).Showing() {
		t.Error("shown but not visible is not showing")
	}
	if StateSet(1 << StateVisible).Showing() {
		t.Error("visible but not shown is not showing")
	}
	if got := NewStateSet(nil); got != 0 || got.Showing() {
		t.Errorf("no words, no states: %v", got)
	}
	if got := NewStateSet([]uint32{1 << StateEditable, 0, 0xffffffff}); !got.Has(StateEditable) {
		t.Error("words beyond the second are ignored, not fatal")
	}

	disabled := NewStateSet([]uint32{1 << StateShowing})
	if !slices.Contains(disabled.Names(), "disabled") {
		t.Errorf("neither enabled nor sensitive is disabled: %v", disabled.Names())
	}
	if slices.Contains(NewStateSet([]uint32{1 << StateEnabled}).Names(), "disabled") {
		t.Error("enabled alone is not disabled")
	}
	all := NewStateSet([]uint32{1<<StateActive | 1<<StateBusy | 1<<StateCollapsed | 1<<StateEditable | 1<<StateExpanded | 1<<StateModal |
		1<<StateMultiLine | 1<<StatePressed | 1<<StateSelected | 1<<StateEnabled,
		1<<(StateIndeterminate-32) | 1<<(StateRequired-32) | 1<<(StateVisited-32) | 1<<(StateReadOnly-32)})
	if got := len(all.Names()); got != 13 {
		t.Errorf("%d states reported, want 13: %v", got, all.Names())
	}
}
