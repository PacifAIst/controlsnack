// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package config

import "time"

// FlagKeys maps a command-line flag (without the leading dashes) to the config key it overrides. It is the single
// place where the two vocabularies meet: Load binds every flag found in it, and validation messages use the
// reverse mapping to name the flag the user actually typed.
//
// --unsafe is intentionally absent: it never travels through viper, so it cannot come from a file or the
// environment (D-38). --config only locates the file and is not a config key either.
var FlagKeys = map[string]string{
	"log-level":            "log.level",
	"log-format":           "log.format",
	"qemu":                 "vm.qemu_dir",
	"image":                "vm.image",
	"memory":               "vm.memory_mib",
	"cpus":                 "vm.cpus",
	"resolution":           "vm.resolution",
	"accel":                "vm.accel",
	"slow":                 "vm.slow",
	"llm":                  "llm.backend",
	"base-url":             "llm.base_url",
	"model":                "llm.model",
	"api-key-env":          "llm.api_key_env",
	"tools":                "llm.tools",
	"obs-format":           "llm.obs_format",
	"marks":                "llm.marks",
	"allow-net":            "net.allow",
	"allow-domain":         "net.allow_domains",
	"allow-port":           "net.allow_ports",
	"step":                 "safety.step",
	"policy":               "safety.policy",
	"actions-per-sec":      "safety.actions_per_sec",
	"actions-per-response": "safety.actions_per_response",
	"actions-per-task":     "safety.actions_per_task",
	"max-turns":            "safety.max_turns",
	"timeout":              "safety.timeout",
	"viewer":               "viewer.mode",
	"task":                 "session.task",
	"keep":                 "session.keep",
	"keep-vm":              "session.keep_vm",
	"vm":                   "session.vm",
	"no-llm":               "session.no_llm",
}

// flagForKey is the reverse of FlagKeys.
func flagForKey(key string) string {
	for name, k := range FlagKeys {
		if k == key {
			return "--" + name
		}
	}
	return ""
}

// Bounds are the limits validation enforces on the budgets. The defaults (PLAN.md Appendix C) equal SafeBounds,
// so without --unsafe a user can only tighten them; --unsafe widens them to UnsafeBounds (PLAN.md section 6.2).
type Bounds struct {
	ActionsPerSec      float64
	ActionsPerResponse int
	ActionsPerTask     int
	MaxTurns           int
	SessionTimeout     time.Duration
}

// SafeBounds are the limits of a normal run.
var SafeBounds = Bounds{ActionsPerSec: 2, ActionsPerResponse: 3, ActionsPerTask: 60, MaxTurns: 100, SessionTimeout: 60 * time.Minute}

// UnsafeBounds are the limits under --unsafe: rate x5, ten actions per response, 500 per task, 8 h sessions.
var UnsafeBounds = Bounds{ActionsPerSec: 10, ActionsPerResponse: 10, ActionsPerTask: 500, MaxTurns: 500, SessionTimeout: 8 * time.Hour}

// BoundsFor returns the limits that apply for the given safety mode.
func BoundsFor(unsafe bool) Bounds {
	if unsafe {
		return UnsafeBounds
	}
	return SafeBounds
}
