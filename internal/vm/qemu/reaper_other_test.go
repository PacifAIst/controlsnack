// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package qemu

import (
	"strings"
	"testing"
)

// reaperHelper does not exist where the reaper is not used; the mode is unreachable in the tests there.
func reaperHelper([]string) {}

// dispatchReaper has nothing to do where there is no reaper.
func dispatchReaper() {}

func TestReaperIsNotUsedHere(t *testing.T) {
	var report strings.Builder
	if code := RunReaper(1, []string{"x"}, &report, nil, nil); code == 0 || !strings.Contains(report.String(), "error=") {
		t.Errorf("%d %q", code, report.String())
	}
}
