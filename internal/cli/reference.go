// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CommandRef is one visible command of the program and the flags it defines itself. The flags it inherits from its parent
// belong to the parent's entry.
type CommandRef struct {
	// Path is the command as it is typed, without the program name: "run", "image build". It is empty for the program itself.
	Path string
	// Flags are the names (without the dashes) of the flags the command defines, sorted; the help flag is left out.
	Flags []string
}

// Reference lists the visible commands of the program with their own flags, the completion commands included. It is what the
// documentation is checked against (internal/sitecheck): a command or a flag the page does not describe fails the gate.
func Reference() []CommandRef {
	var env Env
	env.fillDefaults()
	root := newApp(env).root
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	root.InitDefaultVersionFlag()

	var refs []CommandRef
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" {
			return
		}
		ref := CommandRef{Path: strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), root.Name()))}
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name != "help" && !f.Hidden {
				ref.Flags = append(ref.Flags, f.Name)
			}
		})
		sort.Strings(ref.Flags)
		refs = append(refs, ref)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return refs
}
