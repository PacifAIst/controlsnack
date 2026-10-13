// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package x11

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"unicode/utf8"
)

// Keysyms the agent needs by name (X11/keysymdef.h). The names are xdotool's: the model is told to use them.
const (
	keysymBackSpace   = 0xff08
	keysymTab         = 0xff09
	keysymReturn      = 0xff0d
	keysymEscape      = 0xff1b
	keysymShiftL      = 0xffe1
	keysymControlL    = 0xffe3
	keysymAltL        = 0xffe9
	keysymSuperL      = 0xffeb
	keysymISOLevel3   = 0xfe03
	keysymSpace       = 0x20
	unicodeKeysymBase = 0x01000000
)

// namedKeys maps lower-case key names to keysyms. Single characters are not listed: they are their own names.
var namedKeys = map[string]uint32{
	"backspace": keysymBackSpace, "tab": keysymTab, "return": keysymReturn, "enter": keysymReturn, "escape": keysymEscape, "esc": keysymEscape,
	"delete": 0xffff, "del": 0xffff, "insert": 0xff63, "ins": 0xff63, "home": 0xff50, "end": 0xff57,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
	"page_up": 0xff55, "pageup": 0xff55, "prior": 0xff55, "pgup": 0xff55,
	"page_down": 0xff56, "pagedown": 0xff56, "next": 0xff56, "pgdn": 0xff56,
	"menu": 0xff67, "print": 0xff61, "pause": 0xff13, "scroll_lock": 0xff14, "num_lock": 0xff7f, "caps_lock": 0xffe5, "capslock": 0xffe5,
	"space":    keysymSpace,
	"kp_enter": 0xff8d, "kp_multiply": 0xffaa, "kp_add": 0xffab, "kp_subtract": 0xffad, "kp_decimal": 0xffae, "kp_divide": 0xffaf,
	// the names X gives to punctuation
	"exclam": '!', "quotedbl": '"', "numbersign": '#', "dollar": '$', "percent": '%', "ampersand": '&', "apostrophe": '\'',
	"parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+', "comma": ',', "minus": '-', "period": '.', "slash": '/',
	"colon": ':', "semicolon": ';', "less": '<', "equal": '=', "greater": '>', "question": '?', "at": '@',
	"bracketleft": '[', "backslash": '\\', "bracketright": ']', "asciicircum": '^', "underscore": '_', "grave": '`',
	"braceleft": '{', "bar": '|', "braceright": '}', "asciitilde": '~',
}

var (
	reFunctionKey = regexp.MustCompile(`^f([1-9]|1[0-9]|2[0-4])$`)
	reKeypadDigit = regexp.MustCompile(`^kp_([0-9])$`)
)

// modifierKeysyms maps modifier names to the keysym of the left-hand key.
var modifierKeysyms = map[string]uint32{
	"ctrl": keysymControlL, "control": keysymControlL, "alt": keysymAltL, "shift": keysymShiftL,
	"super": keysymSuperL, "win": keysymSuperL, "altgr": keysymISOLevel3,
}

// KeyError is a key name or combination the agent does not understand; it is the caller's mistake, not a failure.
type KeyError struct{ Msg string }

func (e *KeyError) Error() string { return e.Msg }

// RuneKeysym is the keysym of a character: the character itself for Latin-1 (X11 defines those keysyms to be the code
// points), 0x01000000 plus the code point for the rest of Unicode.
func RuneKeysym(r rune) (uint32, error) {
	switch {
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		return 0, &KeyError{fmt.Sprintf("U+%04X is a control character and has no key", r)}
	case !utf8.ValidRune(r):
		return 0, &KeyError{fmt.Sprintf("U+%04X is not a character", r)}
	case r <= 0xff:
		return uint32(r), nil
	}
	return unicodeKeysymBase + uint32(r), nil
}

// keysymByName resolves one key name: a single character, or one of the names in namedKeys, "f1" to "f24", "kp_0" to
// "kp_9". Letters in names are case-insensitive; a single character is taken as it is ("A" is shift and a).
func keysymByName(name string) (uint32, error) {
	if utf8.RuneCountInString(name) == 1 {
		r, _ := utf8.DecodeRuneInString(name)
		ks, err := RuneKeysym(r)
		if err != nil {
			return 0, err
		}
		return ks, nil
	}
	lower := strings.ToLower(name)
	if ks, ok := namedKeys[lower]; ok {
		return ks, nil
	}
	if m := reFunctionKey.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		return 0xffbe + uint32(n-1), nil //nolint:gosec // n is 1 to 24 by the pattern; F1 is 0xffbe
	}
	if m := reKeypadDigit.FindStringSubmatch(lower); m != nil {
		return 0xffb0 + uint32(m[1][0]-'0'), nil
	}
	return 0, &KeyError{fmt.Sprintf("%q is not a key name (examples: a, Return, Tab, Escape, F5, Page_Down, space, slash)", name)}
}

// Combo is one key combination: modifiers held while one key is pressed. Key is 0 for a combination of modifiers only.
type Combo struct {
	Modifiers []uint32 // keysyms of the modifiers, in the order given
	Key       uint32
}

// ParseCombo reads an xdotool-style combination such as "ctrl+l", "alt+F4", "Return" or "ctrl+shift+t". Modifier names
// are ctrl (control), alt, shift, super (win) and altgr, in any case; at most one other key may be named. A plus sign
// is the key "plus" or the last character of the string ("ctrl++").
func ParseCombo(s string) (Combo, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Combo{}, &KeyError{"the key combination is empty"}
	}
	if len(s) > 64 {
		return Combo{}, &KeyError{"the key combination is longer than 64 characters"}
	}
	// "ctrl++" and "+" mean the plus key.
	var parts []string
	switch {
	case s == "+":
		parts = []string{"plus"}
	case strings.HasSuffix(s, "++"):
		parts = append(strings.Split(strings.TrimSuffix(s, "++"), "+"), "plus")
	default:
		parts = strings.Split(s, "+")
	}
	var c Combo
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return Combo{}, &KeyError{fmt.Sprintf("%q has an empty part between the plus signs", s)}
		}
		if ks, ok := modifierKeysyms[strings.ToLower(p)]; ok && utf8.RuneCountInString(p) > 1 {
			if slicesContains(c.Modifiers, ks) {
				return Combo{}, &KeyError{fmt.Sprintf("%q names the modifier %q twice", s, p)}
			}
			c.Modifiers = append(c.Modifiers, ks)
			continue
		}
		if c.Key != 0 {
			return Combo{}, &KeyError{fmt.Sprintf("%q names more than one key (one combination per call; use several calls for a sequence)", s)}
		}
		ks, err := keysymByName(p)
		if err != nil {
			return Combo{}, err
		}
		c.Key = ks
	}
	return c, nil
}

func slicesContains(list []uint32, v uint32) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// KeyNames lists the named keys, for the message that tells a model what it can use.
func KeyNames() []string {
	names := make([]string, 0, len(namedKeys))
	for n := range namedKeys {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// keysymEuro is the legacy keysym for the euro sign; keyboard layouts carry it instead of the Unicode form.
const keysymEuro = 0x20ac

// keysymAlternates lists the keysyms that stand for the same character, most specific first. A keyboard layout may
// carry a character under its legacy keysym instead of 0x01000000 plus the code point; the euro sign is the one that
// matters in practice. A character with no key is typed through a borrowed keycode, which works in either case.
func keysymAlternates(keysym uint32) []uint32 {
	if keysym == unicodeKeysymBase+0x20ac {
		return []uint32{keysym, keysymEuro}
	}
	return []uint32{keysym}
}
