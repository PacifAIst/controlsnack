// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"testing"
)

func TestAllocateEndpoints(t *testing.T) {
	eps, err := AllocateEndpoints(context.Background(), "linux", "/tmp/cs-x", "agent0", "qmp0", "net0")
	if err != nil || len(eps) != 3 || eps[0].Path != "/tmp/cs-x/agent0.sock" || eps[2].Path != "/tmp/cs-x/net0.sock" || eps[1].Port != 0 {
		t.Errorf("%+v %v", eps, err)
	}
	win, err := AllocateEndpoints(context.Background(), "windows", "", "agent0", "qmp0", "net0")
	if err != nil || len(win) != 3 {
		t.Fatalf("%+v %v", win, err)
	}
	seen := map[int]bool{}
	for _, e := range win {
		if e.Path != "" || e.Port < 1 || e.Port > 65535 || seen[e.Port] {
			t.Errorf("distinct loopback ports expected: %+v", win)
		}
		seen[e.Port] = true
	}
	if eps, err := AllocateEndpoints(context.Background(), "darwin", "/tmp/x"); err != nil || len(eps) != 0 {
		t.Errorf("%v %v", eps, err)
	}
}

func TestNewSocketDir(t *testing.T) {
	dir, cleanup, err := NewSocketDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) > 60 {
		t.Errorf("a short path is needed for unix sockets: %q", dir)
	}
	cleanup()
	cleanup() // idempotent
}
