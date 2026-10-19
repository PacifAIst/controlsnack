// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// TestE2EProbe boots the real guest and runs a shell script in it, for exploring what the guest looks like while
// building something new: CONTROLSNACK_PROBE_FILE names the script (the test skips without it), the output is logged.
func TestE2EProbe(t *testing.T) {
	path := os.Getenv("CONTROLSNACK_PROBE_FILE")
	if path == "" {
		t.Skip("set CONTROLSNACK_PROBE_FILE to a shell script to run in the guest")
	}
	script, err := os.ReadFile(path) //nolint:gosec // the developer's own script
	if err != nil {
		t.Fatal(err)
	}
	m, _ := bootE2E(t, 1280, 800)
	time.Sleep(15 * time.Second) // the desktop session settles
	var res protocol.ExecRes
	if _, err := m.Client.Call(context.Background(), protocol.TypeExec,
		protocol.ExecReq{Command: string(script), TimeoutMS: 400000, MaxOutput: 128 << 10}, nil, &res); err != nil {
		t.Fatal(err)
	}
	t.Logf("exit %d after %dms\n--- stdout ---\n%s\n--- stderr ---\n%s", res.ExitCode, res.DurationMS, res.Stdout, res.Stderr)
}
