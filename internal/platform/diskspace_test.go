// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"path/filepath"
	"testing"
)

func TestFreeSpace(t *testing.T) {
	dir := t.TempDir()
	free, err := FreeSpace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if free == 0 || free > 1<<60 {
		t.Errorf("implausible free space: %d", free)
	}
	t.Logf("%s: %.1f GiB free", dir, float64(free)/(1<<30))
	if _, err := FreeSpace(filepath.Join(dir, "does", "not", "exist")); err == nil {
		t.Error("a path that does not exist")
	}
}
