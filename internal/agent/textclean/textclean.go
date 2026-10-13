// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package textclean makes text that other programs chose (window titles, element names, field contents) safe to carry
// in a message and to show: it is untrusted, so it is cleaned and bounded before it leaves the agent.
package textclean

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Line cleans a single line of text: invalid UTF-8 and control characters are dropped, runs of white space become one
// space, leading and trailing white space goes, and at most maxRunes characters are kept.
func Line(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r) && !unicode.IsSpace(r):
			continue
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		}
		need := 1
		if space {
			need = 2 // a pending space and the character after it, or neither
		}
		if n+need > maxRunes {
			break
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Block cleans text that may span lines (the contents of a text field): line breaks and tabs are kept, other control
// characters and invalid UTF-8 are dropped, and at most maxRunes characters are kept.
func Block(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue
		case r == '\n' || r == '\t':
		case r == '\r':
			continue
		case unicode.IsControl(r):
			continue
		}
		if n >= maxRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
