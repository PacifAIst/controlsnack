// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import "strconv"

// The keysyms of the keys the fallback presses by name (X11/keysymdef.h), as the agent's key names resolve to them.
const (
	ksBackSpace = 0xff08
	ksTab       = 0xff09
	ksReturn    = 0xff0d
	ksEscape    = 0xff1b
	ksDelete    = 0xffff
	ksInsert    = 0xff63
	ksHome      = 0xff50
	ksEnd       = 0xff57
	ksLeft      = 0xff51
	ksUp        = 0xff52
	ksRight     = 0xff53
	ksDown      = 0xff54
	ksPageUp    = 0xff55
	ksPageDown  = 0xff56
	ksMenu      = 0xff67
	ksPrint     = 0xff61
	ksPause     = 0xff13
	ksScrollLk  = 0xff14
	ksNumLock   = 0xff7f
	ksCapsLock  = 0xffe5
	ksShiftL    = 0xffe1
	ksControlL  = 0xffe3
	ksAltL      = 0xffe9
	ksSuperL    = 0xffeb
	ksISOLevel3 = 0xfe03
	ksKPEnter   = 0xff8d
	ksKPMul     = 0xffaa
	ksKPAdd     = 0xffab
	ksKPSub     = 0xffad
	ksKPDec     = 0xffae
	ksKPDiv     = 0xffaf
	ksF1        = 0xffbe
	ksKP0       = 0xffb0
)

// namedQCodes are the keys that have a name and not a character, as QEMU's input-send-event calls them (qcode).
var namedQCodes = map[uint32]string{
	ksBackSpace: "backspace", ksTab: "tab", ksReturn: "ret", ksEscape: "esc", ksDelete: "delete", ksInsert: "insert",
	ksHome: "home", ksEnd: "end", ksLeft: "left", ksUp: "up", ksRight: "right", ksDown: "down", ksPageUp: "pgup", ksPageDown: "pgdn",
	ksMenu: "menu", ksPrint: "print", ksPause: "pause", ksScrollLk: "scroll_lock", ksNumLock: "num_lock", ksCapsLock: "caps_lock",
	ksShiftL: "shift", ksControlL: "ctrl", ksAltL: "alt", ksSuperL: "meta_l", ksISOLevel3: "alt_r",
	ksKPEnter: "kp_enter", ksKPMul: "kp_multiply", ksKPAdd: "kp_add", ksKPSub: "kp_subtract", ksKPDec: "kp_decimal", ksKPDiv: "kp_divide",
}

// punctuation is the keys of a US keyboard that carry symbols, by the character each gives: the qcode and whether shift is held.
var punctuation = map[uint32]struct {
	code  string
	shift bool
}{
	'-': {"minus", false}, '_': {"minus", true}, '=': {"equal", false}, '+': {"equal", true},
	'[': {"bracket_left", false}, '{': {"bracket_left", true}, ']': {"bracket_right", false}, '}': {"bracket_right", true},
	'\\': {"backslash", false}, '|': {"backslash", true}, ';': {"semicolon", false}, ':': {"semicolon", true},
	'\'': {"apostrophe", false}, '"': {"apostrophe", true}, '`': {"grave_accent", false}, '~': {"grave_accent", true},
	',': {"comma", false}, '<': {"comma", true}, '.': {"dot", false}, '>': {"dot", true}, '/': {"slash", false}, '?': {"slash", true},
	'!': {"1", true}, '@': {"2", true}, '#': {"3", true}, '$': {"4", true}, '%': {"5", true}, '^': {"6", true},
	'&': {"7", true}, '*': {"8", true}, '(': {"9", true}, ')': {"0", true},
}

// qcodeOf is the key that gives a keysym on a US keyboard, and whether shift must be held for it. It knows the keys the agent's
// key names resolve to; a keysym of the rest of Unicode has no key here.
func qcodeOf(ks uint32) (code string, shift, ok bool) {
	switch {
	case ks >= 'a' && ks <= 'z', ks >= '0' && ks <= '9':
		return string(rune(ks)), false, true
	case ks >= 'A' && ks <= 'Z':
		return string(rune(ks) + 'a' - 'A'), true, true
	case ks == ' ':
		return "spc", false, true
	case ks >= ksF1 && ks < ksF1+24:
		return "f" + strconv.Itoa(int(ks-ksF1)+1), false, true
	case ks >= ksKP0 && ks <= ksKP0+9:
		return "kp_" + strconv.Itoa(int(ks-ksKP0)), false, true
	}
	if c, found := namedQCodes[ks]; found {
		return c, false, true
	}
	if p, found := punctuation[ks]; found {
		return p.code, p.shift, true
	}
	return "", false, false
}
