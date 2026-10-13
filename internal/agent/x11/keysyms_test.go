// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseCombo(t *testing.T) {
	tests := []struct {
		in   string
		mods []uint32
		key  uint32
	}{
		{"ctrl+l", []uint32{keysymControlL}, 'l'},
		{"Control+L", []uint32{keysymControlL}, 'L'},
		{"CTRL+SHIFT+t", []uint32{keysymControlL, keysymShiftL}, 't'},
		{"alt+F4", []uint32{keysymAltL}, 0xffc1},
		{"Return", nil, keysymReturn},
		{"enter", nil, keysymReturn},
		{"esc", nil, keysymEscape},
		{"Escape", nil, keysymEscape},
		{"tab", nil, keysymTab},
		{"BackSpace", nil, keysymBackSpace},
		{"super+d", []uint32{keysymSuperL}, 'd'},
		{"win+e", []uint32{keysymSuperL}, 'e'},
		{"ctrl+alt+Delete", []uint32{keysymControlL, keysymAltL}, 0xffff},
		{"Page_Down", nil, 0xff56},
		{"pgup", nil, 0xff55},
		{"F1", nil, 0xffbe},
		{"f12", nil, 0xffc9},
		{"F24", nil, 0xffd5},
		{"kp_5", nil, 0xffb5},
		{"space", nil, ' '},
		{"ctrl+slash", []uint32{keysymControlL}, '/'},
		{"ctrl+/", []uint32{keysymControlL}, '/'},
		{"altgr+e", []uint32{keysymISOLevel3}, 'e'},
		{"ctrl+alt", []uint32{keysymControlL, keysymAltL}, 0}, // modifiers only
		{"shift", []uint32{keysymShiftL}, 0},
		{"+", nil, '+'},
		{"ctrl++", []uint32{keysymControlL}, '+'},
		{"plus", nil, '+'},
		{"é", nil, 0xe9},
		{"日", nil, unicodeKeysymBase + 0x65e5},
		{" ctrl+a ", []uint32{keysymControlL}, 'a'},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			c, err := ParseCombo(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(c.Modifiers, tt.mods) || c.Key != tt.key {
				t.Errorf("mods %#x key %#x, want mods %#x key %#x", c.Modifiers, c.Key, tt.mods, tt.key)
			}
		})
	}
}

func TestParseComboRefusals(t *testing.T) {
	long := strings.Repeat("a", 65)
	tests := []struct {
		in   string
		want string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"ctrl+", "empty part"},
		{"+a", "empty part"},
		{"ctrl++a", "empty part"},
		{"ctrl+a+b", "more than one key"},
		{"a+b", "more than one key"},
		{"ctrl+ctrl+a", "twice"},
		{"nosuchkey", "not a key name"},
		{"ctrl+Foo", "not a key name"},
		{"F25", "not a key name"},
		{"F0", "not a key name"},
		{"kp_10", "not a key name"},
		{long, "longer than 64"},
		{"ctrl+\x07", "control character"},
		{"\x00", "control character"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := ParseCombo(tt.in)
			var ke *KeyError
			if !errors.As(err, &ke) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestRuneKeysym(t *testing.T) {
	for r, want := range map[rune]uint32{'a': 'a', 'Z': 'Z', ' ': ' ', '~': '~', 'ÿ': 0xff, 'ñ': 0xf1, 'Ā': unicodeKeysymBase + 0x100, '€': unicodeKeysymBase + 0x20ac, '😀': unicodeKeysymBase + 0x1f600} {
		if got, err := RuneKeysym(r); err != nil || got != want {
			t.Errorf("%q: %#x %v, want %#x", r, got, err, want)
		}
	}
	for _, bad := range []rune{0, 0x07, 0x1b, 0x7f, 0x80, 0x9f, 0x110000, 0xd800} {
		if _, err := RuneKeysym(bad); err == nil {
			t.Errorf("U+%04X accepted", bad)
		}
	}
}

func TestKeysymAlternates(t *testing.T) {
	if got := keysymAlternates(unicodeKeysymBase + 0x20ac); !slices.Equal(got, []uint32{unicodeKeysymBase + 0x20ac, keysymEuro}) {
		t.Errorf("%#x", got)
	}
	if got := keysymAlternates('a'); !slices.Equal(got, []uint32{'a'}) {
		t.Errorf("%#x", got)
	}
}

func TestKeyNamesAreParseable(t *testing.T) {
	names := KeyNames()
	if len(names) < 40 || !slices.IsSorted(names) {
		t.Fatalf("%d names", len(names))
	}
	for _, n := range names {
		if _, err := ParseCombo(n); err != nil {
			t.Errorf("a name the agent advertises must parse: %q: %v", n, err)
		}
	}
}

func TestKeymapLookupPrefersThePlainestRoute(t *testing.T) {
	// keycode 10: a / A; keycode 11: A (plain) again, as some layouts duplicate keys; keycode 12: euro on AltGr and
	// shift+AltGr; keycode 13: nothing.
	km := NewKeymap(10, 4, []uint32{
		'a', 'A', 0, 0,
		'A', 0, 0, 0,
		0, 0, 0x20ac, 0x20ac,
		0, 0, 0, 0,
	})
	check := func(ks uint32, want Stroke) {
		t.Helper()
		got, ok := km.Lookup(ks)
		if !ok || got != want {
			t.Errorf("%#x: %+v %v, want %+v", ks, got, ok, want)
		}
	}
	check('a', Stroke{Code: 10})
	check('A', Stroke{Code: 11}) // plain on another key beats shifted on the first
	check(0x20ac, Stroke{Code: 12, Level3: true})
	if _, ok := km.Lookup('z'); ok {
		t.Error("z is not on this keyboard")
	}
	if code, ok := km.Spare(); !ok || code != 13 {
		t.Errorf("the empty keycode is the spare one: %d %v", code, ok)
	}
	// The shifted column is shift, the fourth is shift and AltGr.
	km2 := NewKeymap(8, 4, []uint32{0, '!', 0, 'ß'})
	if s, _ := km2.Lookup('!'); s != (Stroke{Code: 8, Shift: true}) {
		t.Errorf("%+v", s)
	}
	if s, _ := km2.Lookup('ß'); s != (Stroke{Code: 8, Shift: true, Level3: true}) {
		t.Errorf("%+v", s)
	}
}

func TestKeymapEdgeCases(t *testing.T) {
	empty := NewKeymap(8, 0, nil)
	if _, ok := empty.Lookup('a'); ok {
		t.Error("an empty keymap has nothing")
	}
	if _, ok := empty.Spare(); ok {
		t.Error("an empty keymap has no spare keycode")
	}
	// Columns beyond the fourth belong to other groups and are not used.
	km := NewKeymap(8, 6, []uint32{0, 0, 0, 0, 'q', 'Q'})
	if _, ok := km.Lookup('q'); ok {
		t.Error("keysyms of the second group are not offered")
	}
	full := NewKeymap(8, 2, []uint32{'a', 0, 'b', 0})
	if _, ok := full.Spare(); ok {
		t.Error("every keycode has a keysym")
	}
	// A keymap that reaches past keycode 255 does not wrap around.
	big := NewKeymap(250, 1, []uint32{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j'})
	if s, _ := big.Lookup('f'); s.Code != 255 {
		t.Errorf("%+v", s)
	}
	if _, ok := big.Lookup('g'); ok {
		t.Error("keycodes stop at 255")
	}
}
