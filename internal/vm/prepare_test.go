// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

func TestNewPrepareChecksItsInput(t *testing.T) {
	ok := PrepareOptions{Install: qemu.Install{Arch: qemu.ArchX86_64}, Base: "/images/base.qcow2", Name: "s-1a2b3c4d", Dir: t.TempDir(),
		CPUs: 2, MemoryMiB: 2048, Width: 1280, Height: 800}
	if _, err := NewPrepare(ok); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mut  func(*PrepareOptions)
		want string
	}{
		{"no disk at all", func(o *PrepareOptions) { o.Base = "" }, "neither a base image nor a disk"},
		{"no directory", func(o *PrepareOptions) { o.Dir = "" }, "no session directory"},
		{"underscore in the name", func(o *PrepareOptions) { o.Name = "s_1a2b3c4d" }, "not a valid VM name"},
		{"upper case", func(o *PrepareOptions) { o.Name = "Session" }, "not a valid VM name"},
		{"empty name", func(o *PrepareOptions) { o.Name = "" }, "not a valid VM name"},
		{"too long", func(o *PrepareOptions) { o.Name = strings.Repeat("a", 41) }, "not a valid VM name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := ok
			tt.mut(&o)
			if _, err := NewPrepare(o); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%v", err)
			}
		})
	}
	// A kept VM's disk replaces the base image.
	o := ok
	o.Base, o.Disk = "", "/vms/work/disk.qcow2"
	if _, err := NewPrepare(o); err != nil {
		t.Errorf("a kept VM needs no base: %v", err)
	}
}

func TestParseResolution(t *testing.T) {
	for in, want := range map[string][2]int{"1280x800": {1280, 800}, "1920x1080": {1920, 1080}, "800x600": {800, 600}} {
		if w, h, err := ParseResolution(in); err != nil || w != want[0] || h != want[1] {
			t.Errorf("%s: %d %d %v", in, w, h, err)
		}
	}
	for _, bad := range []string{"", "1280", "x800", "1280x", "ax800", "1280xb", "0x800", "-5x800", "1280X800", "1280x800x2", "big"} {
		if _, _, err := ParseResolution(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
