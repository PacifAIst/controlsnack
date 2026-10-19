// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Command dev is the cross-platform developer task runner (PLAN.md D-29). It works on Windows, macOS and Linux
// without make or a POSIX shell:
//
//	go run ./tools/dev check      the quality gate (PLAN.md section 13.1)
//	go run ./tools/dev build      build the host binary for this machine into dist/
//	go run ./tools/dev cross      build every release target (5 host + 2 agent) with CGO_ENABLED=0
//	go run ./tools/dev agent      build, gzip and hash the guest agents into internal/vm/agentbin
//	go run ./tools/dev licenses   enforce the dependency license policy (D-25)
//	go run ./tools/dev fuzz       run every Fuzz target for a short time
//	go run ./tools/dev e2e        run the end-to-end tests (-tags e2e)
//	go run ./tools/dev release    build release archives and SHA256SUMS into dist/release
//	go run ./tools/dev mutate     change one thing on purpose and see whether a test notices (restores the file)
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
)

const (
	hostPkg  = "./cmd/controlsnack"
	agentPkg = "./cmd/controlsnack-agent"
	// buildinfoPkg is the import path whose variables are stamped with -ldflags -X.
	buildinfoPkg = "github.com/PacifAIst/controlsnack/internal/buildinfo"
	// agentbinDir is where `agent` writes the embedded guest binaries.
	agentbinDir = "internal/vm/agentbin"
	// allowedLicenses is the D-25 dependency license policy.
	allowedLicenses = "Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC"
)

// target is one GOOS/GOARCH pair.
type target struct{ GOOS, GOARCH string }

func (t target) String() string { return t.GOOS + "/" + t.GOARCH }

// hostTargets are the five platforms of the host binary (PLAN.md section 1.1).
var hostTargets = []target{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"},
}

// agentTargets are the two guest architectures (the guest is always Linux, D-01).
var agentTargets = []target{{"linux", "amd64"}, {"linux", "arm64"}}

type command struct {
	name    string
	summary string
	run     func(args []string) error
}

func commands() []command {
	return []command{
		{"check", "run the quality gate", cmdCheck},
		{"build", "build the host binary for this machine into dist/", cmdBuild},
		{"cross", "cross-build all host and agent targets (CGO_ENABLED=0)", cmdCross},
		{"agent", "build the guest agents into internal/vm/agentbin", cmdAgent},
		{"licenses", "enforce the dependency license policy", cmdLicenses},
		{"fuzz", "run every Fuzz target for a short time", cmdFuzz},
		{"e2e", "run the end-to-end tests (-tags e2e)", cmdE2E},
		{"release", "build release archives and SHA256SUMS", cmdRelease},
		{"mutate", "one mutation check: -run TESTS -file F -old TEXT -new TEXT", cmdMutate},
	}
}

func main() {
	cmds := commands()
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage(cmds)
		if len(os.Args) < 2 {
			os.Exit(2)
		}
		return
	}
	for _, c := range cmds {
		if c.name != os.Args[1] {
			continue
		}
		if _, err := chdirToRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "dev:", err)
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		devCtx = ctx
		err := c.run(os.Args[2:])
		stop()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\ndev %s: %v\n", c.name, err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "dev: unknown command %q\n\n", os.Args[1])
	usage(cmds)
	os.Exit(2)
}

func usage(cmds []command) {
	width := 0
	for _, c := range cmds {
		width = max(width, len(c.name))
	}
	var b strings.Builder
	b.WriteString("usage: go run ./tools/dev <command> [flags]\n\ncommands:\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.name, c.summary)
	}
	fmt.Fprint(os.Stderr, b.String())
}

// chdirToRoot moves to the module root so every command behaves the same wherever it is started from.
func chdirToRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found in any parent directory")
		}
		dir = parent
	}
}
