// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/schema"
)

func (a *app) newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "version",
		Short:       "Print the version, commit, Go version and platform",
		Args:        noArgs,
		Annotations: map[string]string{annotationConfig: cfgNone},
		RunE: func(*cobra.Command, []string) error {
			info := buildinfo.Get()
			if asJSON {
				enc := json.NewEncoder(a.env.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Fprintf(a.env.Stdout, "controlsnack %s\n  commit:  %s\n  built:   %s\n  go:      %s\n  os/arch: %s/%s\n",
				info.Version, info.Commit, info.Date, info.GoVersion, info.OS, info.Arch)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func (a *app) newKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill",
		Short: "Kill the running session: SIGKILL its QEMU and close its viewer",
		Long: `Kill the session that is running as this user, from any terminal. It works even if the configuration file is
broken, and falls back to ending QEMU and the controller directly if the session does not answer within a second.`,
		Args:        noArgs,
		Annotations: map[string]string{annotationConfig: cfgNone},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.env.Services.Killer.Kill(cmd.Context(), a.env.Stdout)
		},
	}
}

func (a *app) newDoctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check what this machine can run and say which accelerator a session would use",
		Long: `Read-only report on the host, its virtualization support, QEMU, the base image, browsers and local model
servers. It never elevates and never changes settings. The last line is the verdict a run would act on:
will use kvm, whpx or hvf, or will use tcg (slow). Exit code 0 when a VM can run, 3 when it cannot.`,
		Args:        noArgs,
		Annotations: map[string]string{annotationConfig: cfgLoad},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.env.Services.Doctor.Run(cmd.Context(), a.env.Stdout, a.cfg(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	addVMFlags(cmd.Flags())
	return cmd
}

func (a *app) newSchemaCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:         "schema",
		Short:       "Print the tool/action JSON schema in a backend's format",
		Args:        noArgs,
		Annotations: map[string]string{annotationConfig: cfgNone},
		RunE: func(*cobra.Command, []string) error {
			for _, f := range schema.Formats {
				if f == format {
					return a.env.Services.Schema.Print(a.env.Stdout, format)
				}
			}
			return exitcode.Usagef("unknown schema format %q; use one of: %v", format, schema.Formats)
		},
	}
	cmd.Flags().StringVar(&format, "format", "jsonmode", "format: jsonmode, openai, anthropic, ollama")
	return cmd
}

func (a *app) newVMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "vm",
		Short:       "Manage kept VMs (created with run --keep-vm)",
		Args:        cobra.ArbitraryArgs,
		RunE:        group,
		Annotations: map[string]string{annotationConfig: cfgLoad},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "ls", Short: "List kept VMs", Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return a.env.Services.VMs.List(cmd.Context(), a.env.Stdout)
			},
		},
		&cobra.Command{
			Use: "rm NAME", Short: "Delete a kept VM", Args: exactArgs(1, "a VM name"),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.env.Services.VMs.Remove(cmd.Context(), a.env.Stdout, args[0])
			},
		},
	)
	return cmd
}

func (a *app) newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "audit",
		Short:       "Inspect, verify and prune the hash-chained audit logs",
		Args:        cobra.ArbitraryArgs,
		RunE:        group,
		Annotations: map[string]string{annotationConfig: cfgLoad},
	}
	var raw bool
	show := &cobra.Command{
		Use: "show REF", Short: "Print an audit log (a file path or a session id)", Args: exactArgs(1, "a log file or session id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.env.Services.Audit.Show(cmd.Context(), a.env.Stdout, args[0], raw)
		},
	}
	show.Flags().BoolVar(&raw, "raw", false, "print the JSON lines as stored instead of one readable line per record")

	var olderThan string
	prune := &cobra.Command{
		Use:   "prune",
		Short: "Delete audit logs older than the retention period",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			age := time.Duration(a.cfg().Audit.RetentionDays) * 24 * time.Hour
			if olderThan != "" {
				var err error
				if age, err = parseAge(olderThan); err != nil {
					return exitcode.New(exitcode.Usage, err)
				}
			}
			return a.env.Services.Audit.Prune(cmd.Context(), a.env.Stdout, age)
		},
	}
	prune.Flags().StringVar(&olderThan, "older-than", "", "age such as 30d, 2w or 36h (default: audit.retention_days)")
	cmd.AddCommand(
		&cobra.Command{
			Use: "verify FILE", Short: "Check a log's hash chain for edits, deletions, reordering and truncation",
			Args: exactArgs(1, "an audit log file"),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.env.Services.Audit.Verify(cmd.Context(), a.env.Stdout, args[0])
			},
		},
		&cobra.Command{
			Use: "ls", Short: "List audit logs", Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return a.env.Services.Audit.List(cmd.Context(), a.env.Stdout)
			},
		},
		show,
		prune,
	)
	return cmd
}

func (a *app) newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "image",
		Short:       "Build, verify and manage the base VM image",
		Args:        cobra.ArbitraryArgs,
		RunE:        group,
		Annotations: map[string]string{annotationConfig: cfgLoad},
	}

	var opts image.BuildOptions
	build := &cobra.Command{
		Use:   "build",
		Short: "Download Debian, provision the desktop image locally and seal it",
		Long: `Download the official Debian genericcloud image (checksum-verified), provision XFCE, Chromium and the other
guest packages in a one-off trusted boot that is the only profile with outbound networking, then seal the result.
Software emulation (--slow) works but can take over 30 minutes.`,
		Args:        noArgs,
		Annotations: map[string]string{annotationConfig: cfgValidate},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.env.Services.Images.Build(cmd.Context(), a.env.Stdout, a.cfg(), opts)
		},
	}
	build.Flags().BoolVar(&opts.Yes, "yes", false, "do not ask before downloading")
	build.Flags().BoolVar(&opts.Force, "force", false, "rebuild even if an image with the same recipe exists")
	addVMFlags(build.Flags())

	var manifest string
	importCmd := &cobra.Command{
		Use:   "import QCOW2",
		Short: "Import a base image built elsewhere (for offline machines)",
		Args:  exactArgs(1, "a qcow2 file"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if manifest == "" {
				return exitcode.Usagef("image import needs --manifest FILE")
			}
			return a.env.Services.Images.Import(cmd.Context(), a.env.Stdout, args[0], manifest)
		},
	}
	importCmd.Flags().StringVar(&manifest, "manifest", "", "manifest.json that came with the image")

	cmd.AddCommand(
		build,
		&cobra.Command{
			Use: "ls", Short: "List built images", Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return a.env.Services.Images.List(cmd.Context(), a.env.Stdout)
			},
		},
		&cobra.Command{
			Use: "rm ID", Short: "Delete an image", Args: exactArgs(1, "an image id"),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.env.Services.Images.Remove(cmd.Context(), a.env.Stdout, args[0])
			},
		},
		&cobra.Command{
			Use: "verify [ID]", Short: "Check an image against its manifest (default: the newest)", Args: maxArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.env.Services.Images.Verify(cmd.Context(), a.env.Stdout, firstOrEmpty(args))
			},
		},
		importCmd,
		&cobra.Command{
			Use: "path [ID]", Short: "Print an image's path (default: the newest)", Args: maxArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.env.Services.Images.Path(cmd.Context(), a.env.Stdout, firstOrEmpty(args))
			},
		},
	)
	return cmd
}

func firstOrEmpty(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func (a *app) newLLMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "llm",
		Short:       "Detect and test model backends",
		Args:        cobra.ArbitraryArgs,
		RunE:        group,
		Annotations: map[string]string{annotationConfig: cfgValidateKey},
	}
	d := config.Defaults()
	flags := cmd.PersistentFlags()
	flags.String("llm", d.LLM.Backend, "backend: auto, ollama, lmstudio, llamacpp, openai, anthropic")
	flags.String("base-url", d.LLM.BaseURL, "backend base URL (needs an explicit --llm)")
	flags.String("model", d.LLM.Model, "model name")
	flags.String("api-key-env", d.LLM.APIKeyEnv, "NAME of the environment variable that holds the API key")

	cmd.AddCommand(
		&cobra.Command{
			Use: "probe", Short: "Test vision, WebP, native tool calls and structured output on the backend", Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return a.env.Services.LLM.Probe(cmd.Context(), a.env.Stdout, a.cfg())
			},
		},
		&cobra.Command{
			Use: "models", Short: "List the models the backend offers", Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return a.env.Services.LLM.Models(cmd.Context(), a.env.Stdout, a.cfg())
			},
		},
	)
	return cmd
}
