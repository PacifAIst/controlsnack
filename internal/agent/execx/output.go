// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// readCapped returns what a stream holds, at most max bytes of it: when it is longer, the first three quarters and the
// last quarter, with a line between them that says how much was left out. The text is always clean (see clean): the
// cuts can fall inside a character, and the program may have written bytes that are not text at all.
func readCapped(f *os.File, max int) (text string, truncated bool) {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return "", false
	}
	total := fi.Size()
	if total <= int64(max) {
		buf := make([]byte, total)
		n, _ := f.ReadAt(buf, 0)
		return clean(string(buf[:n])), false
	}
	head := max * 3 / 4
	tail := max - head
	hb, tb := make([]byte, head), make([]byte, tail)
	hn, _ := f.ReadAt(hb, 0)
	tn, _ := f.ReadAt(tb, total-int64(tail))
	omitted := total - int64(hn) - int64(tn)
	return clean(string(hb[:hn])) + marker(omitted) + clean(string(tb[:tn])), true
}

func marker(omitted int64) string {
	return fmt.Sprintf("\n[... %d bytes omitted ...]\n", omitted)
}

// terminalCodes matches the escape sequences a program writes for a terminal: colors and cursor movement (CSI) and
// window titles and links (OSC, ended by BEL or ESC \). They mean nothing to a reader of the text and cost tokens.
var terminalCodes = regexp.MustCompile("\x1b\\[[0-9;:?<=>]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

// replacement stands for what cannot be shown.
const replacement = "�"

// clean makes command output safe to carry in a message: terminal escape sequences are removed, every other control
// character except newline, tab and carriage return becomes U+FFFD, and bytes that are not UTF-8 do too. Besides
// being what a reader wants, this bounds the size of the encoded text: a control character would otherwise cost six
// characters of JSON (\u0001), which could make a response too large to send.
func clean(s string) string {
	s = terminalCodes.ReplaceAllString(s, "")
	s = strings.ToValidUTF8(s, replacement)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return []rune(replacement)[0]
		}
		return r
	}, s)
}
