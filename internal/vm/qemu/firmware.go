// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"os"
	"path/filepath"
)

// firmwareSets are the file names the aarch64 UEFI firmware travels under: a code image and a variable-store
// template, both sized for a pflash unit.
var firmwareSets = []struct{ code, vars string }{
	{"edk2-aarch64-code.fd", "edk2-arm-vars.fd"},        // QEMU's own share directory (Homebrew, Windows, Debian qemu-system-data)
	{"AAVMF_CODE.fd", "AAVMF_VARS.fd"},                  // Debian and Ubuntu, package qemu-efi-aarch64
	{"QEMU_EFI-pflash.raw", "vars-template-pflash.raw"}, // Fedora, edk2-aarch64
	{"QEMU_CODE.fd", "QEMU_VARS.fd"},                    // Arch, edk2-aarch64
}

// findFirmware looks for aarch64 UEFI firmware next to the installation first and in the standard locations
// after. x86_64 guests boot from the BIOS built into QEMU and need none.
func findFirmware(goos string, inst Install) *Firmware {
	if inst.Arch != ArchAarch64 {
		return nil
	}
	for _, dir := range firmwareDirs(goos, inst) {
		for _, set := range firmwareSets {
			code, vars := filepath.Join(dir, set.code), filepath.Join(dir, set.vars)
			if isFile(code) && isFile(vars) {
				return &Firmware{Code: code, Vars: vars}
			}
		}
	}
	return nil
}

func firmwareDirs(goos string, inst Install) []string {
	bases := []string{inst.Dir}
	if real, err := filepath.EvalSymlinks(inst.System); err == nil { // Homebrew's bin/ holds symlinks into the Cellar
		if d := filepath.Dir(real); d != inst.Dir {
			bases = append(bases, d)
		}
	}
	var dirs []string
	for _, b := range bases {
		parent := filepath.Dir(b)
		dirs = append(dirs,
			filepath.Join(parent, "share", "qemu"), filepath.Join(parent, "share"), // Unix layouts: <prefix>/bin + <prefix>/share
			filepath.Join(b, "share", "qemu"), filepath.Join(b, "share")) // Windows layout: <dir>\share
	}
	switch goos {
	case "linux":
		dirs = append(dirs, "/usr/share/qemu", "/usr/share/AAVMF", "/usr/share/edk2/aarch64", "/usr/local/share/qemu")
	case "darwin":
		dirs = append(dirs, "/opt/homebrew/share/qemu", "/usr/local/share/qemu", "/opt/local/share/qemu")
	}
	return dirs
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
