// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PacifAIst/controlsnack/internal/exitcode"
)

// noArgs rejects positional arguments with a usage error (exit code 2).
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return exitcode.Usagef("%s takes no arguments (got %q)", cmd.CommandPath(), args[0])
	}
	return nil
}

// exactArgs requires exactly n positional arguments; what names them for the message, e.g. "an image id".
func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return exitcode.Usagef("%s needs %s (got %d argument(s))", cmd.CommandPath(), what, len(args))
		}
		return nil
	}
}

// maxArgs allows at most n positional arguments.
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return exitcode.Usagef("%s takes at most %d argument(s) (got %d)", cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}

// parseAge parses a duration such as "90m", "36h" or, additionally, "30d" and "2w".
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty age")
	}
	unit := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("%q is not an age such as 30d, 2w or 36h", s)
		}
		return d, nil
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not an age such as 30d, 2w or 36h", s)
	}
	return time.Duration(n) * unit, nil
}
