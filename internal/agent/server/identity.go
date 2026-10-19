// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// DefaultDisplay is what the agent reports until it reads the real screen size from X (Phase 4): the size the VM is
// started with.
var DefaultDisplay = protocol.Display{W: 1280, H: 800}

// FileSHA256 returns the SHA-256 of a file. The agent hashes its own executable: the stub checked those bytes
// against the push line, and the host compares this hash with the one it pushed.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // the agent's own executable
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GuestArch maps a Go architecture name to the name the protocol uses (what `uname -m` prints).
func GuestArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return protocol.ArchX86_64, nil
	case "arm64":
		return protocol.ArchAarch64, nil
	}
	return "", fmt.Errorf("the agent does not run on architecture %q", goarch)
}

// ParseDisplay reads "WIDTHxHEIGHT"; an empty string means DefaultDisplay.
func ParseDisplay(s string) (protocol.Display, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultDisplay, nil
	}
	w, h, ok := strings.Cut(strings.ToLower(s), "x")
	wi, werr := strconv.Atoi(w)
	hi, herr := strconv.Atoi(h)
	if !ok || werr != nil || herr != nil || wi < 320 || hi < 200 || wi > 7680 || hi > 4320 {
		return protocol.Display{}, fmt.Errorf("%q is not a screen size like 1280x800", s)
	}
	return protocol.Display{W: wi, H: hi}, nil
}

// ParseLogLevel reads a log level name (debug, info, warn, error; any case). An empty string means info.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("%q is not a log level (debug, info, warn or error)", s)
}
