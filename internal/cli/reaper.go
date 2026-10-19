// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// reaperReportFD is the file descriptor the controller passes the pid report pipe on (exec.Cmd.ExtraFiles[0]).
const reaperReportFD = 3

// newReaperCmd is the hidden command the controller runs QEMU under on macOS, where the OS cannot end a child
// when its parent dies (PLAN.md section 7.4): `controlsnack __reaper --parent <pid> -- <qemu> <args>`. It is not
// listed in the help and is not meant to be typed.
func (a *app) newReaperCmd() *cobra.Command {
	return &cobra.Command{
		Use:                qemu.ReaperCommand,
		Hidden:             true,
		DisableFlagParsing: true, // everything after -- belongs to QEMU
		Annotations:        map[string]string{annotationConfig: cfgNone},
		RunE: func(_ *cobra.Command, args []string) error {
			parent, argv, err := parseReaperArgs(args)
			if err != nil {
				return exitcode.Usagef("%v", err)
			}
			code := qemu.RunReaper(parent, argv, os.NewFile(reaperReportFD, "reaper-report"), a.env.Stdout, a.env.Stderr)
			if code == 0 {
				return nil
			}
			return exitcode.New(code, fmt.Errorf("the VM process ended with status %d", code))
		},
	}
}

func parseReaperArgs(args []string) (parent int, argv []string, err error) {
	if len(args) < 4 || args[0] != "--parent" || args[2] != "--" {
		return 0, nil, fmt.Errorf("usage: %s --parent <pid> -- <command> [args...]", qemu.ReaperCommand)
	}
	parent, err = strconv.Atoi(args[1])
	if err != nil || parent <= 1 {
		return 0, nil, fmt.Errorf("%s: %q is not a process id", qemu.ReaperCommand, args[1])
	}
	return parent, args[3:], nil
}
