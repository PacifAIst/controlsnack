// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/PacifAIst/controlsnack/internal/buildinfo"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
)

// Annotation key and values that say how much of the configuration pipeline a command needs.
const (
	annotationConfig = "controlsnack/config"
	// cfgNone: do not read configuration at all. Used by `kill` and `version`, which must work even when the
	// config file is broken: the emergency stop may never depend on a parseable file.
	cfgNone = "none"
	// cfgLoad: load and log, but do not insist on a valid configuration (config show, audit, vm).
	cfgLoad = "load"
	// cfgValidate: load, then reject an invalid configuration.
	cfgValidate = "validate"
	// cfgValidateKey: like cfgValidate, and a hosted backend must have its API key.
	cfgValidateKey = "validate+key"
)

// app is the state shared by the commands of one invocation.
type app struct {
	env        Env
	root       *cobra.Command
	loaded     *config.Loaded
	log        *zap.Logger
	configFile string
	// started is set once the pre-run hook is reached; an error before that came from the command line parser.
	started bool
}

// Run executes the command tree with args (without the program name) and returns the process exit code.
func Run(ctx context.Context, env Env, args []string) int {
	env.fillDefaults()
	a := newApp(env)
	a.root.SetArgs(args)
	cmd, err := a.root.ExecuteContextC(ctx)
	if err == nil {
		return exitcode.OK
	}
	var coded *exitcode.Error
	if !a.started && !errors.As(err, &coded) {
		// Cobra rejected the command line itself: unknown command, unknown flag, wrong argument count.
		err = exitcode.New(exitcode.Usage, err)
	}
	code := exitcode.Of(err)
	fmt.Fprintf(env.Stderr, "controlsnack: %v\n", err)
	if code == exitcode.Usage {
		fmt.Fprintf(env.Stderr, "run '%s --help' for usage\n", cmd.CommandPath())
	}
	return code
}

func newApp(env Env) *app {
	a := &app{env: env, log: zap.NewNop()}
	info := buildinfo.Get()

	root := &cobra.Command{
		Use:   "controlsnack",
		Short: "Run a throwaway local Linux desktop VM that an LLM operates through a validated, audited tool API",
		Long: `controlsnack boots a throwaway Linux desktop in a local QEMU VM, shows it in a local viewer window and lets a
vision-capable (or, degraded, text-only) model operate it through a validated tool API. Every safety control is on
by default. Nothing leaves this machine except the model endpoint you choose.

Exit codes: 0 ok, 1 error, 2 usage, 3 environment (QEMU/accelerator), 4 image missing, 5 killed or interrupted,
6 timeout, 70 not implemented.`,
		Version:       info.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE:          group,
		// Help and version must never depend on a parseable config file; commands that need it say so themselves.
		Annotations: map[string]string{annotationConfig: cfgNone},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.prepare(cmd)
		},
	}
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.SetVersionTemplate("controlsnack {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return exitcode.New(exitcode.Usage, err) })

	d := config.Defaults()
	pf := root.PersistentFlags()
	pf.StringVar(&a.configFile, "config", "", "config file (default: <user config dir>/controlsnack/config.yaml)")
	pf.String("log-level", d.Log.Level, "log level: debug, info, warn, error")
	pf.String("log-format", d.Log.Format, "log format: text, json")

	root.AddCommand(
		a.newRunCmd(),
		a.newKillCmd(),
		a.newImageCmd(),
		a.newVMCmd(),
		a.newAuditCmd(),
		a.newDoctorCmd(),
		a.newLLMCmd(),
		a.newSchemaCmd(),
		a.newVersionCmd(),
		a.newConfigCmd(),
		a.newReaperCmd(),
	)
	a.root = root
	return a
}

// configMode reports how much configuration handling the command wants, looking up through its parents.
func configMode(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		if m, ok := c.Annotations[annotationConfig]; ok {
			return m
		}
	}
	return cfgLoad
}

// prepare loads the configuration, builds the logger and validates, as the command's annotation requires.
func (a *app) prepare(cmd *cobra.Command) error {
	a.started = true
	mode := configMode(cmd)
	if mode == cfgNone {
		return nil
	}

	unsafe := false
	if cmd.Flags().Lookup("unsafe") != nil {
		unsafe, _ = cmd.Flags().GetBool("unsafe")
	}
	loaded, err := config.Load(config.LoadOptions{
		ConfigFile:    a.configFile,
		Flags:         cmd.Flags(),
		Unsafe:        unsafe,
		Getenv:        a.env.Getenv,
		UserConfigDir: a.env.UserConfigDir,
	})
	if err != nil {
		return exitcode.New(exitcode.Usage, err)
	}
	a.loaded = loaded

	log, lerr := newLogger(a.env.Stderr, loaded.Config.Log.Level, loaded.Config.Log.Format)
	if lerr != nil {
		// Validation below reports the bad value; until then log at the default level.
		log, _ = newLogger(a.env.Stderr, "info", "text")
	}
	a.log = log
	for _, w := range loaded.Warnings {
		a.log.Warn(w)
	}
	if loaded.File != "" {
		a.log.Debug("config file loaded", zap.String("file", loaded.File))
	}

	if mode == cfgValidate || mode == cfgValidateKey {
		verr := loaded.Validate(config.ValidateOptions{
			HostOS: a.env.HostOS,
			Getenv: a.env.Getenv,
			ForRun: mode == cfgValidateKey && !loaded.Config.Session.NoLLM,
		})
		if verr != nil {
			return exitcode.New(exitcode.Usage, verr)
		}
	}
	return nil
}

// cfg returns the effective configuration; only valid in commands whose mode is not cfgNone.
func (a *app) cfg() config.Config { return a.loaded.Config }

// group returns the RunE of a command that only has subcommands: bare invocation prints help, anything else is
// an unknown subcommand.
func group(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return exitcode.Usagef("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return cmd.Help()
}
