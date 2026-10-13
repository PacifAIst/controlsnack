// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package agentbin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The generated files (agent-linux-<arch>.gz and SHA256SUMS) sit next to PLACEHOLDER.txt, which is always there, so
// the embed pattern matches even in a clean checkout.
//
//go:embed *
var files embed.FS

// MaxAgentSize bounds the unpacked agent; it is also the stub's limit in the guest.
const MaxAgentSize = 64 << 20

// ErrMissing means this build carries no agent for the architecture.
var ErrMissing = errors.New("this build has no embedded guest agent — run `go run ./tools/dev agent`, then build again")

// Agent is an embedded guest agent, unpacked and checked.
type Agent struct {
	Arch   string // x86_64 or aarch64, as `uname -m` prints it
	Binary []byte
	SHA256 string // of Binary: what the guest reports in the handshake
}

var archFile = map[string]string{"x86_64": "agent-linux-amd64", "aarch64": "agent-linux-arm64"}

// For returns the embedded agent for a guest architecture. The unpacked bytes are checked against the SHA256SUMS
// that was generated with them, so a damaged or swapped file is an error and not an agent that gets pushed.
func For(arch string) (Agent, error) {
	return load(files, arch)
}

func load(fsys embed.FS, arch string) (Agent, error) {
	name, ok := archFile[arch]
	if !ok {
		return Agent{}, fmt.Errorf("there is no guest agent for architecture %q", arch)
	}
	gz, err := fsys.ReadFile(name + ".gz")
	if err != nil {
		return Agent{}, fmt.Errorf("%w (missing %s.gz)", ErrMissing, name)
	}
	sums, err := fsys.ReadFile("SHA256SUMS")
	if err != nil {
		return Agent{}, fmt.Errorf("%w (missing SHA256SUMS)", ErrMissing)
	}
	wantRaw, wantGz := sumOf(sums, name), sumOf(sums, name+".gz")
	if wantRaw == "" || wantGz == "" {
		return Agent{}, fmt.Errorf("SHA256SUMS does not list %s: the embedded files do not belong together", name)
	}
	if got := hexSum(gz); got != wantGz {
		return Agent{}, fmt.Errorf("%s.gz has sha256 %s, SHA256SUMS says %s", name, got[:16], wantGz[:16])
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return Agent{}, fmt.Errorf("%s.gz: %w", name, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, MaxAgentSize+1))
	if err != nil {
		return Agent{}, fmt.Errorf("%s.gz: %w", name, err)
	}
	if len(raw) > MaxAgentSize {
		return Agent{}, fmt.Errorf("%s unpacks to more than %d bytes", name, MaxAgentSize)
	}
	if got := hexSum(raw); got != wantRaw {
		return Agent{}, fmt.Errorf("%s has sha256 %s, SHA256SUMS says %s", name, got[:16], wantRaw[:16])
	}
	return Agent{Arch: arch, Binary: raw, SHA256: wantRaw}, nil
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// sumOf finds the hash of name in sha256sum-format text.
func sumOf(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if ok && file == name && len(sum) == 64 {
			return strings.ToLower(sum)
		}
	}
	return ""
}
