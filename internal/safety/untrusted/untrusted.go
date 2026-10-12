// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package untrusted

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultMaxBytes is the longest piece of untrusted text one fence carries unless the caller says otherwise.
const DefaultMaxBytes = 16 << 10

// Fencer wraps everything that came from the guest in a labeled fence the model is told to treat as data
// (PLAN.md section 6.10). One Fencer exists per session; its nonce is random, so content cannot name the fence
// that wraps it, and `<<` sequences inside content are neutralized so it could not close or forge a fence even
// if it guessed the nonce.
type Fencer struct{ nonce string }

// New creates a Fencer with a fresh random nonce.
func New() (*Fencer, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("no randomness available for the fence nonce: %w", err)
	}
	return &Fencer{nonce: hex.EncodeToString(b[:])}, nil
}

// WithNonce creates a Fencer with a given nonce, for tests and for rendering golden prompts.
func WithNonce(nonce string) *Fencer { return &Fencer{nonce: nonce} }

// Nonce returns the session's fence nonce; the system prompt names it so the model can recognize real fences.
func (f *Fencer) Nonce() string { return f.nonce }

// Open and Close are the fence markers.
func (f *Fencer) Open(source string) string {
	return "<<untrusted:" + f.nonce + " " + cleanSource(source) + ">>"
}

// Close is the end marker of a fence.
func (f *Fencer) Close() string { return "<<end:" + f.nonce + ">>" }

// Fence sanitizes content and wraps it: <<untrusted:NONCE source>> content <<end:NONCE>>. source says where the
// text came from (window.title, ui, exec.stdout, ocr, browser.text ...). Content longer than maxBytes (0 selects
// DefaultMaxBytes) is cut with an explicit marker.
func (f *Fencer) Fence(source, content string, maxBytes int) string {
	body := Sanitize(content)
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	body = truncate(body, maxBytes)
	if strings.HasSuffix(body, "<") {
		body += " " // keep a trailing "<" from running into the closing marker
	}
	return f.Open(source) + body + f.Close()
}

var (
	// ANSI/VT escape sequences: CSI (ESC [ ... final) and OSC (ESC ] ... BEL or ESC \), plus lone two-byte escapes.
	reANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-Z\\-_]`)
	// Runs of two or more '<'.
	reAngles = regexp.MustCompile(`<{2,}`)
	reSource = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// Sanitize makes guest-originated text safe to show a model: it repairs invalid UTF-8, removes terminal escape
// sequences, control characters (except newline and tab), bidirectional overrides, zero-width and "tag"
// characters, and breaks up every run of `<<` so the text cannot imitate a fence marker. The order matters:
// invisible characters go first, otherwise "<", a zero-width space and "<" would become "<<" after the fact.
func Sanitize(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	s = reANSI.ReplaceAllString(s, "")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if dropRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return reAngles.ReplaceAllStringFunc(b.String(), func(run string) string {
		// "<<" -> "< <", "<<<" -> "< < <": still readable, no "<<" left.
		return strings.TrimSuffix(strings.Repeat("< ", len(run)), " ")
	})
}

// dropRune reports whether r must not reach the model.
func dropRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case unicode.IsControl(r): // C0, DEL and C1 controls
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0x200E || r == 0x200F || r == 0x061C: // directional marks
		return true
	case r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF, r == 0x180E, r == 0x00AD: // zero-width and soft hyphen
		return true
	case r >= 0xE0000 && r <= 0xE007F: // Unicode "tag" characters, an invisible smuggling channel
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r == 0xFFFD:
		return false
	case !unicode.IsPrint(r) && !unicode.IsSpace(r):
		return true
	}
	return false
}

// truncate cuts s to at most max bytes at a character boundary and says how much was cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s…[truncated %d bytes]", s[:cut], len(s)-cut)
}

// cleanSource limits a source label to a short, harmless token.
func cleanSource(s string) string {
	s = reSource.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "unknown"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
