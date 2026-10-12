// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
)

func (a *app) newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "config",
		Short:       "Show the effective configuration and where it comes from",
		Args:        cobra.ArbitraryArgs,
		RunE:        group,
		Annotations: map[string]string{annotationConfig: cfgLoad},
	}

	var asJSON bool
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (flags > environment > file > defaults); secrets are masked",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			// Nothing is printed unredacted, ever (PLAN.md section 6.12).
			shown := a.cfg().Redacted()
			if asJSON {
				enc := json.NewEncoder(a.env.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(shown); err != nil {
					return err
				}
			} else {
				out, err := yaml.Marshal(shown)
				if err != nil {
					return err
				}
				if a.loaded.File != "" {
					fmt.Fprintf(a.env.Stdout, "# config file: %s\n", a.loaded.File)
				} else {
					fmt.Fprintln(a.env.Stdout, "# config file: none (built-in defaults, environment and flags)")
				}
				if _, err := a.env.Stdout.Write(out); err != nil {
					return err
				}
			}
			// Show first, then complain: seeing the values is how you fix an invalid configuration.
			if err := a.loaded.Validate(config.ValidateOptions{HostOS: a.env.HostOS, Getenv: a.env.Getenv}); err != nil {
				return exitcode.New(exitcode.Usage, err)
			}
			return nil
		},
	}
	show.Flags().BoolVar(&asJSON, "json", false, "print as JSON")

	path := &cobra.Command{
		Use:   "path",
		Short: "Print the config file path (and whether it exists)",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			p := a.configFile
			if p == "" {
				var err error
				if p, err = config.DefaultPath(a.env.UserConfigDir); err != nil {
					return err
				}
			}
			state := "exists"
			if a.loaded.File == "" {
				state = "does not exist yet"
			}
			fmt.Fprintf(a.env.Stdout, "%s (%s)\n", p, state)
			return nil
		},
	}

	cmd.AddCommand(show, path)
	return cmd
}
