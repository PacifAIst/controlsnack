// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import "testing"

func TestAllIn(t *testing.T) {
	set := map[uint32]bool{10: true, 11: true}
	for _, tt := range []struct {
		name   string
		values []uint32
		want   bool
	}{
		{"a dock", []uint32{10}, true},
		{"two shell types", []uint32{10, 11}, true},
		{"normal", []uint32{99}, false},
		{"a dialog that may also be a menu, preference order", []uint32{99, 10}, false},
		{"no type at all is a normal window", nil, false},
		{"empty", []uint32{}, false},
	} {
		if got := allIn(tt.values, set); got != tt.want {
			t.Errorf("%s: %v", tt.name, got)
		}
	}
}

func TestMarkPrimary(t *testing.T) {
	one := []MonitorInfo{{ID: 0, W: 1280, H: 800}}
	markPrimary(one)
	if !one[0].Primary {
		t.Error("a lone monitor with no primary flagged is the primary one")
	}
	two := []MonitorInfo{{ID: 0}, {ID: 1, X: 1280}}
	markPrimary(two)
	if !two[0].Primary || two[1].Primary {
		t.Errorf("the first becomes primary: %+v", two)
	}
	flagged := []MonitorInfo{{ID: 0}, {ID: 1, X: 1280, Primary: true}}
	markPrimary(flagged)
	if flagged[0].Primary || !flagged[1].Primary {
		t.Errorf("a primary that RandR set stays: %+v", flagged)
	}
	markPrimary(nil) // must not panic
}
