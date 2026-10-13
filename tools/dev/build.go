// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// cmdBuild builds the host binary for the machine it runs on.
func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	out := fs.String("o", "dist", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The host embeds the guest agents (D-04), so they are built first.
	if err := cmdAgent(nil); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	t := target{runtime.GOOS, runtime.GOARCH}
	return goBuild(t, hostPkg, filepath.Join(*out, exeName("controlsnack", t)), false)
}

// cmdCross builds every host and agent target to prove they all compile without cgo.
func cmdCross(args []string) error {
	fs := flag.NewFlagSet("cross", flag.ContinueOnError)
	out := fs.String("o", "", "keep the binaries in this directory (default: a temporary directory that is removed)")
	vet := fs.Bool("vet", false, "also run go vet for every non-native host target")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := *out
	if dir == "" {
		tmp, err := os.MkdirTemp("", "controlsnack-cross-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		dir = tmp
	}
	return crossBuild(dir, *vet)
}

// crossBuild compiles the five host targets and the two agent targets into dir/<os>-<arch>/, all CGO_ENABLED=0.
func crossBuild(dir string, vet bool) error {
	native := target{runtime.GOOS, runtime.GOARCH}
	for _, t := range hostTargets {
		sub := filepath.Join(dir, t.GOOS+"-"+t.GOARCH)
		if err := os.MkdirAll(sub, 0o750); err != nil {
			return err
		}
		if err := goBuild(t, hostPkg, filepath.Join(sub, exeName("controlsnack", t)), true); err != nil {
			return fmt.Errorf("host %s: %w", t, err)
		}
		if vet && t != native {
			if err := run(targetEnv(t), "go", "vet", "./..."); err != nil {
				return fmt.Errorf("vet %s: %w", t, err)
			}
		}
	}
	for _, t := range agentTargets {
		sub := filepath.Join(dir, t.GOOS+"-"+t.GOARCH)
		if err := os.MkdirAll(sub, 0o750); err != nil {
			return err
		}
		if err := goBuild(t, agentPkg, filepath.Join(sub, "controlsnack-agent"), true); err != nil {
			return fmt.Errorf("agent %s: %w", t, err)
		}
	}
	return nil
}

// cmdAgent builds the guest agents, gzips them deterministically and records their hashes.
//
// Output in internal/vm/agentbin:
//
//	agent-linux-amd64.gz  agent-linux-arm64.gz
//	SHA256SUMS            sha256sum format; for each agent one line for the raw binary (agent-linux-<arch>,
//	                      the hash the guest reports in the handshake) and one for the .gz file.
func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(agentbinDir, 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "controlsnack-agent-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	var sums []string
	for _, t := range agentTargets {
		name := "agent-" + t.GOOS + "-" + t.GOARCH
		bin := filepath.Join(tmp, name)
		if err := goBuild(t, agentPkg, bin, true); err != nil {
			return fmt.Errorf("agent %s: %w", t, err)
		}
		raw, err := os.ReadFile(bin) //nolint:gosec // path is under our own temp directory
		if err != nil {
			return err
		}
		var gz bytes.Buffer
		zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		if err != nil {
			return err
		}
		// The zero gzip header (no name, no mtime) keeps the output reproducible.
		if _, err := zw.Write(raw); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(agentbinDir, name+".gz"), gz.Bytes(), 0o640); err != nil {
			return err
		}
		sums = append(sums, sumLine(raw, name), sumLine(gz.Bytes(), name+".gz"))
		fmt.Printf("%s: %d bytes raw, %d bytes gzip\n", name, len(raw), gz.Len())
	}
	sort.Strings(sums)
	return os.WriteFile(filepath.Join(agentbinDir, "SHA256SUMS"), []byte(strings.Join(sums, "\n")+"\n"), 0o640)
}

func sumLine(data []byte, name string) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]) + "  " + name
}
