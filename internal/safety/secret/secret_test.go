// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestNewToken(t *testing.T) {
	a, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken(32)
	if len(a) != 64 || a == b {
		t.Errorf("tokens must be 64 hex characters and unique: %q %q", a, b)
	}
	if strings.Trim(a, "0123456789abcdef") != "" {
		t.Errorf("not lowercase hex: %q", a)
	}
	for _, n := range []int{-1, 0, 8, 15} {
		if _, err := NewToken(n); err == nil {
			t.Errorf("a %d-byte token is too weak and must be refused", n)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		tok, _ := NewToken(16)
		if seen[tok] {
			t.Fatal("collision in 1000 tokens")
		}
		seen[tok] = true
	}
}

func TestEqual(t *testing.T) {
	tok, _ := NewToken(32)
	if !Equal(tok, tok) || !Equal("", "") {
		t.Error("equal secrets must compare equal")
	}
	for _, other := range []string{"", tok[:63], tok + "0", strings.ToUpper(tok), "x"} {
		if Equal(tok, other) {
			t.Errorf("%q must not equal the token", other)
		}
	}
	if Equal("a", "b") {
		t.Error("different values")
	}
}

func TestMaskPassword(t *testing.T) {
	m := MaskPassword("hunter2")
	if strings.Contains(m, "hunter2") || !strings.Contains(m, "len 7") || !strings.HasPrefix(m, "••••") {
		t.Errorf("%q", m)
	}
	if MaskPassword("hunter2") != m || MaskPassword("hunter3") == m {
		t.Error("the mask is stable per value and differs between values")
	}
	if !strings.Contains(MaskPassword("pässwörd"), "len 8") {
		t.Error("length counts characters, not bytes")
	}
	if !strings.Contains(MaskPassword(""), "len 0") {
		t.Error("empty password")
	}
}

func TestRedactKnownFormats(t *testing.T) {
	r := NewRedactor()
	tests := []struct{ name, in, secret string }{
		{"openai key", "key is sk-proj-abcdefghijklmnopqrstuvwxyz0123456789 ok", "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"anthropic key", "x-key: sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWX end", "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWX"},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"github fine-grained", "github_pat_" + strings.Repeat("A1_", 20), "github_pat_" + strings.Repeat("A1_", 20)},
		{"slack token", "xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
		{"aws key id", "AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"google key", "AIzaSyA-1234567890abcdefghijklmnopqrstuv", "AIzaSyA-1234567890abcdefghijklmnopqrstuv"},
		{"jwt", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r"},
		{"bearer", "Authorization: Bearer abc123DEF456ghi789", "abc123DEF456ghi789"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nmore\n-----END RSA PRIVATE KEY-----", "MIIEowIBAAKCAQEA"},
		{"unterminated private key", "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg", "MIIEvQIBADANBg"},
		{"key=value", "export API_KEY=supersecretvalue123", "supersecretvalue123"},
		{"json password", `{"user":"bob","password":"correct horse battery"}`, "correct"},
		{"url query token", "https://x/y?token=abcdef123456&x=1", "abcdef123456"},
		{"header", "x-api-key: 0123456789abcdef", "0123456789abcdef"},
		{"quoted", `secret = 'hunter2hunter2'`, "hunter2hunter2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := r.Redact(tt.in)
			if strings.Contains(out, tt.secret) {
				t.Errorf("secret survived: %q -> %q", tt.in, out)
			}
			if !strings.Contains(out, Mark) {
				t.Errorf("no mark in %q", out)
			}
		})
	}
}

func TestRedactKeepsOrdinaryText(t *testing.T) {
	r := NewRedactor()
	for _, s := range []string{
		"", "hello world", "rm -rf /tmp/x", "the token ring network", "password", "password: ", "a secret", "tokens are great",
		"sk-short", "skills and sk-ills", "ls -la /home/cs/work", "https://example.com/path?x=1&y=2", "exit code 0",
		"password=ab", // shorter than six characters: not worth masking
	} {
		if got := r.Redact(s); got != s {
			t.Errorf("%q was changed to %q", s, got)
		}
	}
}

func TestRedactKeepsTheKeyName(t *testing.T) {
	r := NewRedactor()
	got := r.Redact(`curl -H "Authorization: abcdef123456" --data password=hunter2000`)
	if !strings.Contains(got, "Authorization: "+Mark) || !strings.Contains(got, "password="+Mark) || strings.Contains(got, "abcdef123456") || strings.Contains(got, "hunter2000") {
		t.Errorf("%q", got)
	}
}

func TestExactSecrets(t *testing.T) {
	r := NewRedactor()
	r.AddSecret("my-very-private-value")
	r.AddSecret("short") // under six characters: ignored
	r.AddSecret("my-very-private-value-with-more")
	r.AddSecret("my-very-private-value") // duplicate
	in := "a my-very-private-value-with-more b my-very-private-value c short"
	got := r.Redact(in)
	if got != "a *** b *** c short" {
		t.Errorf("%q (the longer secret must be masked whole, the short one left alone)", got)
	}
	if len(r.secrets) != 2 {
		t.Errorf("duplicates and too-short values are not stored: %d", len(r.secrets))
	}
}

func TestCustomPatternsAndBytes(t *testing.T) {
	r := NewRedactor()
	r.AddPattern(regexp.MustCompile(`EMP-[0-9]{6}`))
	r.AddPattern(nil)
	if got := r.Redact("badge EMP-123456 here"); got != "badge *** here" {
		t.Errorf("%q", got)
	}
	if got := string(r.RedactBytes([]byte("EMP-654321"))); got != Mark {
		t.Errorf("%q", got)
	}
}

func TestRedactorIsConcurrencySafe(t *testing.T) {
	// Run under -race in CI: adding secrets while redacting must not race.
	r := NewRedactor()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.AddSecret("concurrent-secret-value") }()
		go func() {
			defer wg.Done()
			out := r.Redact("x concurrent-secret-value y")
			if out != "x concurrent-secret-value y" && out != "x *** y" {
				t.Errorf("half-masked output %q", out)
			}
		}()
	}
	wg.Wait()
	if got := r.Redact("x concurrent-secret-value y"); got != "x *** y" {
		t.Errorf("after all additions: %q", got)
	}
}
