// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

// Stroke is how to produce a keysym on the keyboard: which keycode, and which modifiers must be held.
type Stroke struct {
	Code   byte
	Shift  bool // the keysym is in the shifted column of the key
	Level3 bool // it needs AltGr (ISO_Level3_Shift)
}

// Keymap is the server's keyboard mapping (GetKeyboardMapping): for every keycode from Min, Per keysyms, one per column
// (plain, shift, AltGr, shift+AltGr, then other groups, which are not used).
type Keymap struct {
	Min   byte
	Per   int
	syms  []uint32
	index map[uint32]Stroke
}

// NewKeymap indexes a mapping. syms holds Per entries for each keycode starting at min, as the X reply does.
func NewKeymap(min byte, per int, syms []uint32) Keymap {
	k := Keymap{Min: min, Per: per, syms: syms, index: map[uint32]Stroke{}}
	if per <= 0 {
		return k
	}
	// Prefer the plainest way to a keysym: the lowest column, then the lowest keycode.
	best := map[uint32]int{}
	for i := 0; i+per <= len(syms); i += per {
		code := int(min) + i/per
		if code > 255 {
			break
		}
		for col := 0; col < per && col < 4; col++ {
			ks := syms[i+col]
			if ks == 0 {
				continue
			}
			if b, seen := best[ks]; seen && b <= col {
				continue
			}
			best[ks] = col
			k.index[ks] = Stroke{Code: byte(code), Shift: col == 1 || col == 3, Level3: col == 2 || col == 3} //nolint:gosec // code is at most 255: checked above
		}
	}
	return k
}

// Lookup finds how to type a keysym on this keyboard.
func (k Keymap) Lookup(keysym uint32) (Stroke, bool) {
	s, ok := k.index[keysym]
	return s, ok
}

// Spare is a keycode that has no keysym at all, which can carry a mapping for a character the keyboard does not have.
// The highest one is taken: the low codes are the ones real keys use.
func (k Keymap) Spare() (byte, bool) {
	if s := k.Spares(1); len(s) > 0 {
		return s[0], true
	}
	return 0, false
}

// Spares lists up to limit keycodes that have no keysym at all, the highest first.
func (k Keymap) Spares(limit int) []byte {
	var out []byte
	if k.Per <= 0 {
		return out
	}
	for i := len(k.syms) - k.Per; i >= 0 && len(out) < limit; i -= k.Per {
		empty := true
		for _, ks := range k.syms[i : i+k.Per] {
			if ks != 0 {
				empty = false
				break
			}
		}
		if code := int(k.Min) + i/k.Per; empty && code <= 255 && code >= int(k.Min) {
			out = append(out, byte(code)) //nolint:gosec // code is between Min and 255: checked in the condition
		}
	}
	return out
}
