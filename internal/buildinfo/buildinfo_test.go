// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package buildinfo

import (
	"runtime"
	"strings"
	"testing"
)

func TestGetReportsRuntime(t *testing.T) {
	got := Get()
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Fatalf("Get() = %s/%s, want %s/%s", got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if got.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
}

func TestStringMentionsEveryField(t *testing.T) {
	s := Info{Version: "v1.2.3", Commit: "abc123", Date: "2026-10-12", GoVersion: "go1.26", OS: "linux", Arch: "arm64"}.String()
	for _, want := range []string{"v1.2.3", "abc123", "2026-10-12", "go1.26", "linux/arm64"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}
