// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestParseReaperArgs(t *testing.T) {
	parent, argv, err := parseReaperArgs([]string{"--parent", "4242", "--", "/usr/bin/qemu-system-x86_64", "-S", "-name", "x"})
	if err != nil || parent != 4242 || !slices.Equal(argv, []string{"/usr/bin/qemu-system-x86_64", "-S", "-name", "x"}) {
		t.Errorf("%d %v %v", parent, argv, err)
	}
	for _, bad := range [][]string{nil, {"--parent"}, {"--parent", "1", "--"}, {"--parent", "x", "--", "q"}, {"--parent", "1", "--", "q"},
		{"--parent", "-5", "--", "q"}, {"4242", "--", "q"}, {"--parent", "4242", "q", "r"}} {
		if _, _, err := parseReaperArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestReaperCommandIsHiddenFromHelp(t *testing.T) {
	res := runCLI(t, nil, "linux", Services{}, "--help")
	if res.code != 0 || strings.Contains(res.out, "__reaper") {
		t.Errorf("%d: the reaper is an implementation detail of the controller:\n%s", res.code, res.out)
	}
	// A wrong invocation is a usage error, not a panic.
	res = runCLI(t, nil, "linux", Services{}, "__reaper", "nonsense")
	if res.code != 2 || !strings.Contains(res.errOut, "usage") {
		t.Errorf("%d %q", res.code, res.errOut)
	}
}
