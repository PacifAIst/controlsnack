// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"slices"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/config"
)

func TestTheReferenceListsTheVisibleCommandsWithTheirOwnFlags(t *testing.T) {
	flags := map[string][]string{}
	for _, ref := range Reference() {
		if _, dup := flags[ref.Path]; dup {
			t.Errorf("%q is listed twice", ref.Path)
		}
		flags[ref.Path] = ref.Flags
	}
	for path, want := range map[string][]string{
		"":                      {"config", "log-format", "log-level", "version"},
		"run":                   {"task", "unsafe", "viewer", "keep-vm", "allow-domain", "resolution"},
		"image build":           {"accel", "force", "slow", "yes"},
		"image import":          {"manifest"},
		"llm":                   {"api-key-env", "base-url", "llm", "model"},
		"audit prune":           {"older-than"},
		"completion bash":       {"no-descriptions"},
		"completion fish":       {"no-descriptions"},
		"completion zsh":        {"no-descriptions"},
		"completion":            nil,
		"doctor":                {"accel", "json", "qemu", "slow"},
		"kill":                  nil,
		"schema":                {"format", "tools"},
		"vm rm":                 nil,
		"config show":           {"json"},
		"image rm":              nil,
		"llm probe":             nil,
		"audit show":            {"raw"},
		"version":               {"json"},
		"completion powershell": {"no-descriptions"},
	} {
		got, ok := flags[path]
		if !ok {
			t.Errorf("the command %q is missing from the reference", path)
			continue
		}
		for _, f := range want {
			if !slices.Contains(got, f) {
				t.Errorf("%q lacks the flag --%s: %v", path, f, got)
			}
		}
	}
	// A hidden command (the reaper that cleans up after a crashed session) and the help command are not for the reader.
	for path := range flags {
		if path == "help" || path == "reaper" || path == "__reap" {
			t.Errorf("%q should not be listed", path)
		}
	}
	// An inherited flag belongs to the command that defines it.
	if slices.Contains(flags["run"], "log-level") || slices.Contains(flags["llm probe"], "model") {
		t.Errorf("an inherited flag is listed under the command that inherits it: run %v, llm probe %v", flags["run"], flags["llm probe"])
	}
}

func TestEveryFlagThatSetsAConfigKeyIsOnACommand(t *testing.T) {
	defined := map[string]bool{}
	for _, ref := range Reference() {
		for _, f := range ref.Flags {
			defined[f] = true
		}
	}
	for name := range config.FlagKeys {
		if !defined[name] {
			t.Errorf("--%s sets a configuration key and no command defines it", name)
		}
	}
	if !defined["unsafe"] {
		t.Error("--unsafe is not on a command")
	}
}
