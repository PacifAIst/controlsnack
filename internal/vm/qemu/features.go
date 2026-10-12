// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MinMajor is the oldest QEMU major version that is accepted (PLAN.md D-02). The version only rules out releases
// known to be too old; what the binary can actually do is learned by asking it (Features).
const MinMajor = 8

// SandboxArg is the argument of `-sandbox` for every VM we start (PLAN.md section 7.2). It is probed once at
// discovery because the option needs a Linux kernel with seccomp.
const SandboxArg = "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny"

// Guest architectures, spelled the way `uname -m` and QEMU's binary names spell them (and the protocol package).
const (
	ArchX86_64  = "x86_64"
	ArchAarch64 = "aarch64"
)

// Version is a QEMU release number.
type Version struct{ Major, Minor, Patch int }

// String formats the version as major.minor.patch.
func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// AtLeast reports whether v is major.minor or newer.
func (v Version) AtLeast(major, minor int) bool {
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

// The release may be followed by "-rc2" or a build description, and builds that are not QEMU's own say "QEMU
// Windows emulator version" or similar, so only "QEMU ... version N.N" is required.
var reVersion = regexp.MustCompile(`(?i)^QEMU\b.*?\bversion (\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion reads the first line of `qemu-system-* --version`, e.g.
// "QEMU emulator version 8.2.2 (Debian 1:8.2.2+ds-0ubuntu1.18)". It returns the banner line as well.
func ParseVersion(output string) (Version, string, error) {
	banner, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	banner = strings.TrimSpace(banner)
	m := reVersion.FindStringSubmatch(banner)
	if m == nil {
		return Version{}, banner, fmt.Errorf("%q is not a QEMU version banner", truncate(banner, 80))
	}
	var v Version
	for i, dst := range []*int{&v.Major, &v.Minor, &v.Patch} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, banner, fmt.Errorf("%q is not a QEMU version banner", truncate(banner, 80))
		}
		*dst = n
	}
	return v, banner, nil
}

// ParseAccels reads `-accel help`: a header line, then one accelerator per line.
func ParseAccels(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.ContainsAny(name, " \t:") { // the header line has spaces and a colon
			continue
		}
		out = append(out, name)
	}
	return unique(out)
}

// ParseMachines reads `-machine help`: a header line, then "name  description" rows.
func ParseMachines(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasSuffix(f[0], ":") || f[0] == "Supported" {
			continue
		}
		out = append(out, f[0])
	}
	return unique(out)
}

var reDeviceName = regexp.MustCompile(`(?:^|,\s*)(?:name|alias) "([^"]+)"`)

// ParseDevices reads `-device help`: rows like `name "usb-tablet", bus usb-bus, alias "x"`. Aliases count as
// names, since `-device` accepts both.
func ParseDevices(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		for _, m := range reDeviceName.FindAllStringSubmatch(strings.TrimSpace(line), -1) {
			out = append(out, m[1])
		}
	}
	return unique(out)
}

// requiredDevices are the devices the runtime argv (PLAN.md section 7.2) names, per guest architecture.
var requiredDevices = map[string][]string{
	ArchX86_64:  {"virtio-blk-pci", "virtio-vga", "qemu-xhci", "usb-tablet", "usb-kbd", "virtio-rng-pci", "virtio-serial-pci", "virtserialport"},
	ArchAarch64: {"virtio-blk-pci", "virtio-gpu-pci", "qemu-xhci", "usb-tablet", "usb-kbd", "virtio-rng-pci", "virtio-serial-pci", "virtserialport"},
}

// requiredMachine is the machine type of the runtime argv, per guest architecture.
var requiredMachine = map[string]string{ArchX86_64: "q35", ArchAarch64: "virt"}

// FeatureError says what this QEMU build lacks.
type FeatureError struct {
	Missing []string
}

func (e *FeatureError) Error() string {
	return "this QEMU build lacks what the VM needs: " + strings.Join(e.Missing, ", ")
}

// VersionError says the QEMU release is older than the minimum.
type VersionError struct {
	Have Version
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("QEMU %s is too old: %d.0 or newer is required", e.Have, MinMajor)
}

func unique(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
