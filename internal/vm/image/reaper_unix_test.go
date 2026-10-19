// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package image

import (
	"os"
	"strconv"

	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// dispatchReaper makes the test binary the reaper when it is started as one: on macOS every start of QEMU goes through
// `<this executable> __reaper --parent N -- argv...`, the hidden command of the program, and the provisioning tests start the
// test binary itself as the QEMU they provision with. Nothing in the test binary answered to the command, so on macOS they all failed.
func dispatchReaper() {
	if len(os.Args) > 5 && os.Args[1] == qemu.ReaperCommand && os.Args[2] == "--parent" && os.Args[4] == "--" {
		parent, _ := strconv.Atoi(os.Args[3])
		os.Exit(qemu.RunReaper(parent, os.Args[5:], os.NewFile(3, "report"), os.Stdout, os.Stderr))
	}
}
