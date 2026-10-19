// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveNamesAreOnePerHostTargetAndWindowsGetsAZip(t *testing.T) {
	if len(hostTargets) != 5 {
		t.Fatalf("%d host targets: a release has five archives", len(hostTargets))
	}
	seen := map[string]bool{}
	for _, target := range hostTargets {
		name := archiveName("v1.2.3", target)
		if seen[name] {
			t.Errorf("%s is the name of two archives", name)
		}
		seen[name] = true
		want := ".tar.gz"
		if target.GOOS == "windows" {
			want = ".zip"
		}
		if !strings.HasSuffix(name, want) || !strings.HasPrefix(name, archiveBase("v1.2.3", target)) || !strings.Contains(name, "v1.2.3_"+target.GOOS+"_"+target.GOARCH) {
			t.Errorf("%s: not the name of the archive of %s", name, target)
		}
	}
}

// The notes of a release say which file is for which system, with the names the tool gives the archives.
func TestTheReleaseNotesNameEveryArchiveTheToolMakes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "release-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	notes := string(data)
	for _, target := range hostTargets {
		if name := archiveName("{{TAG}}", target); !strings.Contains(notes, "`"+name+"`") {
			t.Errorf("the release notes do not name %s", name)
		}
	}
	if !strings.Contains(notes, "SHA256SUMS") || !strings.Contains(notes, "{{TAG}}") {
		t.Error("the notes must explain the checksums and use the tag")
	}
	if strings.Contains(notes, "controlsnack.com") {
		t.Error("the notes name an address that was given up")
	}
}
