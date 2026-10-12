// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// NewToken returns a random token of n bytes (at least 16) as lowercase hex. Session secrets (the one-time viewer
// URL token, the CSRF value, the kill token) are made with it.
func NewToken(n int) (string, error) {
	if n < 16 {
		return "", fmt.Errorf("a token needs at least 16 random bytes, asked for %d", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("no randomness available: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Equal compares two secrets in constant time. Both are hashed first, so neither the content nor the length of
// the expected secret leaks through timing.
func Equal(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// Mark replaces redacted text.
const Mark = "***"

// MinSecretLength is the shortest exact value the redactor will mask. Masking a two-letter "secret" everywhere
// would only destroy the text around it.
const MinSecretLength = 6

// MaskPassword is how text typed into a password field appears in the viewer and the audit log: the length and a
// short hash let a reader tell two entries apart without revealing either (PLAN.md section 6.5).
func MaskPassword(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("••••(len %d, sha256:%s)", len([]rune(text)), hex.EncodeToString(sum[:])[:8])
}

// Redactor removes secrets from text that is logged, audited or shown. It knows the exact secrets it was told
// about (the API key in use, the session tokens) and recognizes the usual shapes of credentials. It is safe for
// concurrent use.
type Redactor struct {
	mu       sync.RWMutex
	secrets  []string
	patterns []*regexp.Regexp
	keyed    *regexp.Regexp
}

// credentialPatterns match well-known token formats outright.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{50,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=\-]{8,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
}

// keyedValue matches `name = value` and `"name": "value"` forms for names that suggest a secret, keeping the name
// and replacing only the value.
var keyedValue = regexp.MustCompile(`(?i)((?:authorization|x-api-key|api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|passw(?:or)?d|passwd)["']?\s*[:=]\s*["']?)([^\s"',;&]{6,})`)

// NewRedactor creates a redactor that knows the built-in credential shapes.
func NewRedactor() *Redactor {
	return &Redactor{patterns: append([]*regexp.Regexp(nil), credentialPatterns...), keyed: keyedValue}
}

// AddSecret teaches the redactor an exact value to mask wherever it appears. Values shorter than MinSecretLength
// are ignored.
func (r *Redactor) AddSecret(value string) {
	if len(value) < MinSecretLength {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if s == value {
			return
		}
	}
	r.secrets = append(r.secrets, value)
	// Longest first, so a secret that contains another one is masked whole.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// AddPattern adds a user-configured pattern; everything it matches is masked.
func (r *Redactor) AddPattern(re *regexp.Regexp) {
	if re == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.patterns = append(r.patterns, re)
}

// Redact returns s with every known secret and credential-shaped string masked.
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Mark)
		}
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllString(s, Mark)
	}
	return r.keyed.ReplaceAllString(s, "${1}"+Mark)
}

// RedactBytes is Redact for byte slices.
func (r *Redactor) RedactBytes(b []byte) []byte { return []byte(r.Redact(string(b))) }
