// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClean(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain text", "hello\nworld\t!\r\n", "hello\nworld\t!\r\n"},
		{"colors are removed", "\x1b[31mred\x1b[0m and \x1b[1;32mgreen\x1b[m", "red and green"},
		{"cursor movement is removed", "a\x1b[2Kb\x1b[10;20Hc\x1b[?25lx", "abcx"},
		{"a window title is removed", "\x1b]0;my title\x07visible", "visible"},
		{"a link is removed", "\x1b]8;;http://x\x1b\\text\x1b]8;;\x1b\\", "text"},
		{"other controls become U+FFFD", "a\x01b\x7fc\x00d", "a�b�c�d"},
		{"C1 controls too", "x\u0085y", "x�y"},
		{"Unicode is kept", "héllo 日本語 😀 €", "héllo 日本語 😀 €"},
		{"a lone escape before a letter is not a sequence", "a\x1bz", "a�z"},
		{"a two-character escape is removed", "a\x1bMb", "ab"},
		{"an unterminated sequence at a cut", "text\x1b[3", "text�[3"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clean(tt.in)
			if !utf8.ValidString(got) {
				t.Errorf("invalid UTF-8: %q", got)
			}
			if got != tt.want {
				t.Errorf("%q, want %q", got, tt.want)
			}
		})
	}
}

func TestCleanReplacesInvalidUTF8(t *testing.T) {
	got := clean("ok \xff\xfe\xc3( end")
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "ok ") || !strings.HasSuffix(got, "( end") || !strings.Contains(got, "�") {
		t.Errorf("%q", got)
	}
}

// The worst output still fits a message: 128 KiB of control characters is 128 KiB of three-byte replacements, not
// six-character JSON escapes.
func TestCleanBoundsTheEncodedSize(t *testing.T) {
	got := clean(strings.Repeat("\x01", 128<<10))
	if len(got) > 3*128<<10 {
		t.Errorf("%d bytes", len(got))
	}
}
