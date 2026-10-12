// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package platform

import "testing"

func TestParseProcStatStart(t *testing.T) {
	// Real /proc/PID/stat lines; the second has spaces and a ')' in the command name.
	tests := []struct {
		stat, want string
		ok         bool
	}{
		{"1234 (bash) S 1000 1234 1234 34816 1234 4194304 2000 0 0 0 5 3 0 0 20 0 1 0 987654 12345678 1000 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 2 0 0 0 0 0", "linux:987654", true},
		{"77 (my (weird) prog) R 1 77 77 0 -1 4194560 100 0 0 0 1 1 0 0 20 0 3 0 555 100000 500 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0", "linux:555", true},
		{"no parentheses at all", "", false},
		{"1 (init) S 0 1", "", false},
	}
	for _, tt := range tests {
		got, err := parseProcStatStart(tt.stat)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("%q -> %q, %v; want %q ok=%v", tt.stat, got, err, tt.want, tt.ok)
		}
	}
}
