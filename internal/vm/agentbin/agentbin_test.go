// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agentbin

import (
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"
)

func TestSumOf(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)
	sums := []byte(a + "  agent-linux-amd64\n" + b + "  agent-linux-amd64.gz\n\nbroken line\n" + strings.Repeat("c", 63) + "  short\n")
	if sumOf(sums, "agent-linux-amd64") != a || sumOf(sums, "agent-linux-amd64.gz") != strings.ToLower(b) {
		t.Error("lookup by exact name")
	}
	for _, name := range []string{"agent-linux", "short", "missing", ""} {
		if sumOf(sums, name) != "" {
			t.Errorf("%q found", name)
		}
	}
}

func TestForUnknownArchitecture(t *testing.T) {
	for _, arch := range []string{"", "riscv64", "amd64", "x86_64 "} {
		if _, err := For(arch); err == nil || !strings.Contains(err.Error(), "no guest agent for architecture") {
			t.Errorf("%q: %v", arch, err)
		}
	}
}

// TestForWhatIsEmbedded checks the real embedded files when they exist (after `go run ./tools/dev agent`), and
// that a clean checkout gets the instruction instead of a crash.
func TestForWhatIsEmbedded(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64"} {
		a, err := For(arch)
		if errors.Is(err, ErrMissing) {
			t.Logf("%s: not generated here: %v", arch, err)
			if !strings.Contains(err.Error(), "go run ./tools/dev agent") {
				t.Errorf("the message must say how to fix it: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if a.Arch != arch || len(a.Binary) < 1<<20 || a.SHA256 != hexSum(a.Binary) || !bytes.HasPrefix(a.Binary, []byte("\x7fELF")) {
			t.Errorf("%s: %d bytes, sha %s", arch, len(a.Binary), a.SHA256)
		}
		t.Logf("%s: %d bytes, sha256 %s…", arch, len(a.Binary), a.SHA256[:16])
	}
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestVerificationCatchesDamage(t *testing.T) {
	// The checks themselves are exercised on synthetic data through the helpers.
	raw := []byte("\x7fELF the agent")
	packed := gz(t, raw)
	if hexSum(raw) == hexSum(packed) {
		t.Fatal("distinct")
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil || !bytes.Equal(out.Bytes(), raw) {
		t.Error("gzip round trip")
	}
	// A truncated gzip stream is an error, not a short agent.
	if zr, err := gzip.NewReader(bytes.NewReader(packed[:len(packed)-6])); err == nil {
		var o bytes.Buffer
		if _, err := o.ReadFrom(zr); err == nil {
			t.Error("a truncated archive must not read cleanly")
		}
	}
}
