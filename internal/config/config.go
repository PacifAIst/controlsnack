// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"strings"
	"time"
)

// EnvPrefix prefixes every environment variable that sets a config key: llm.image_max is CONTROLSNACK_LLM_IMAGE_MAX.
const EnvPrefix = "CONTROLSNACK"

// Duration is a time.Duration that reads and writes as a short string such as "60m" or "90s".
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// MarshalText renders the duration compactly: whole hours as "8h", whole minutes as "15m", whole seconds as "120s".
func (d Duration) MarshalText() ([]byte, error) {
	v := time.Duration(d)
	switch {
	case v != 0 && v%time.Hour == 0:
		return []byte(fmt.Sprintf("%dh", v/time.Hour)), nil
	case v != 0 && v%time.Minute == 0:
		return []byte(fmt.Sprintf("%dm", v/time.Minute)), nil
	case v != 0 && v%time.Second == 0:
		return []byte(fmt.Sprintf("%ds", v/time.Second)), nil
	default:
		return []byte(v.String()), nil
	}
}

// UnmarshalText accepts anything time.ParseDuration does.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 90s, 15m or 8h", string(b))
	}
	*d = Duration(v)
	return nil
}

// Config is the complete effective configuration. Precedence when it is loaded: flags > environment > config
// file > defaults. Every field has a default (see defaultValues) and, where the PLAN.md names one, a run flag
// (see FlagKeys). Fields tagged secret:"true" are masked by Redacted.
type Config struct {
	Log     Log     `mapstructure:"log" yaml:"log" json:"log"`
	VM      VM      `mapstructure:"vm" yaml:"vm" json:"vm"`
	LLM     LLM     `mapstructure:"llm" yaml:"llm" json:"llm"`
	Net     Net     `mapstructure:"net" yaml:"net" json:"net"`
	Safety  Safety  `mapstructure:"safety" yaml:"safety" json:"safety"`
	Viewer  Viewer  `mapstructure:"viewer" yaml:"viewer" json:"viewer"`
	Session Session `mapstructure:"session" yaml:"session" json:"session"`
	Audit   Audit   `mapstructure:"audit" yaml:"audit" json:"audit"`
}

// Log configures diagnostics on stderr.
type Log struct {
	Level  string `mapstructure:"level" yaml:"level" json:"level"`    // debug, info, warn, error
	Format string `mapstructure:"format" yaml:"format" json:"format"` // text, json
}

// VM configures the QEMU guest.
type VM struct {
	QemuDir    string `mapstructure:"qemu_dir" yaml:"qemu_dir" json:"qemu_dir"`       // directory (or binary) of QEMU; empty = PATH and known locations
	Image      string `mapstructure:"image" yaml:"image" json:"image"`                // base image id or path; empty = newest built image
	MemoryMiB  int    `mapstructure:"memory_mib" yaml:"memory_mib" json:"memory_mib"` // guest RAM
	CPUs       int    `mapstructure:"cpus" yaml:"cpus" json:"cpus"`
	Resolution string `mapstructure:"resolution" yaml:"resolution" json:"resolution"` // WxH, up to 1920x1080
	Accel      string `mapstructure:"accel" yaml:"accel" json:"accel"`                // auto, kvm, whpx, hvf, tcg
	Slow       bool   `mapstructure:"slow" yaml:"slow" json:"slow"`                   // shorthand for accel=tcg (D-32)
}

// LLM configures the model backend.
type LLM struct {
	Backend      string   `mapstructure:"backend" yaml:"backend" json:"backend"` // auto, ollama, lmstudio, llamacpp, openai, anthropic
	BaseURL      string   `mapstructure:"base_url" yaml:"base_url" json:"base_url"`
	Model        string   `mapstructure:"model" yaml:"model" json:"model"`
	APIKeyEnv    string   `mapstructure:"api_key_env" yaml:"api_key_env" json:"api_key_env"`
	APIKey       string   `mapstructure:"api_key" yaml:"api_key" json:"api_key" secret:"true"` // prefer the environment over the file
	Tools        string   `mapstructure:"tools" yaml:"tools" json:"tools"`                     // auto, full, compact
	ObsFormat    string   `mapstructure:"obs_format" yaml:"obs_format" json:"obs_format"`      // text, json
	Marks        string   `mapstructure:"marks" yaml:"marks" json:"marks"`                     // auto, on, off (set-of-marks overlay, D-30)
	ImageMax     int      `mapstructure:"image_max" yaml:"image_max" json:"image_max"`         // long edge of images sent to the model
	NumCtx       int      `mapstructure:"num_ctx" yaml:"num_ctx" json:"num_ctx"`
	FirstTimeout Duration `mapstructure:"first_timeout" yaml:"first_timeout" json:"first_timeout"` // first call (model load)
	Timeout      Duration `mapstructure:"timeout" yaml:"timeout" json:"timeout"`                   // later calls
}

// Net configures guest egress. The runtime VM never has a NIC (D-07); these keys only open the policy-checked
// HTTP/HTTPS bridge.
type Net struct {
	Allow        bool     `mapstructure:"allow" yaml:"allow" json:"allow"`
	AllowDomains []string `mapstructure:"allow_domains" yaml:"allow_domains" json:"allow_domains"`
	DenyDomains  []string `mapstructure:"deny_domains" yaml:"deny_domains" json:"deny_domains"`
	AllowPorts   []int    `mapstructure:"allow_ports" yaml:"allow_ports" json:"allow_ports"` // in addition to 80 and 443
}

// Safety configures limits and the policy. Unsafe is deliberately not a config key: it comes only from the
// --unsafe flag (D-38), so no file or environment variable can silently relax the safety layer.
type Safety struct {
	Unsafe             bool     `mapstructure:"-" yaml:"unsafe" json:"unsafe"`
	Step               bool     `mapstructure:"step" yaml:"step" json:"step"`
	Policy             string   `mapstructure:"policy" yaml:"policy" json:"policy"` // rule file merged over the defaults
	ActionsPerSec      float64  `mapstructure:"actions_per_sec" yaml:"actions_per_sec" json:"actions_per_sec"`
	ActionsPerResponse int      `mapstructure:"actions_per_response" yaml:"actions_per_response" json:"actions_per_response"`
	ActionsPerTask     int      `mapstructure:"actions_per_task" yaml:"actions_per_task" json:"actions_per_task"`
	MaxTurns           int      `mapstructure:"max_turns" yaml:"max_turns" json:"max_turns"`
	Timeout            Duration `mapstructure:"timeout" yaml:"timeout" json:"timeout"`                         // whole session
	IdleTimeout        Duration `mapstructure:"idle_timeout" yaml:"idle_timeout" json:"idle_timeout"`          // no user input while Ready or Paused
	ConfirmTimeout     Duration `mapstructure:"confirm_timeout" yaml:"confirm_timeout" json:"confirm_timeout"` // unanswered confirmation = reject
}

// Viewer configures the local viewer window.
type Viewer struct {
	Mode        string `mapstructure:"mode" yaml:"mode" json:"mode"` // window, browser, none, webview
	FPSActive   int    `mapstructure:"fps_active" yaml:"fps_active" json:"fps_active"`
	FPSIdle     int    `mapstructure:"fps_idle" yaml:"fps_idle" json:"fps_idle"`
	WebPQuality int    `mapstructure:"webp_quality" yaml:"webp_quality" json:"webp_quality"`
}

// Session configures what one `run` does.
type Session struct {
	Task   string `mapstructure:"task" yaml:"task" json:"task"`
	Keep   bool   `mapstructure:"keep" yaml:"keep" json:"keep"`          // keep screenshots and the chat transcript
	KeepVM string `mapstructure:"keep_vm" yaml:"keep_vm" json:"keep_vm"` // "" = destroy the VM at the end, "auto" = keep under a generated name
	VM     string `mapstructure:"vm" yaml:"vm" json:"vm"`                // resume this kept VM
	NoLLM  bool   `mapstructure:"no_llm" yaml:"no_llm" json:"no_llm"`    // boot and hand over, no model (P3-T11 milestone)
}

// Audit configures the audit log.
type Audit struct {
	RetentionDays int `mapstructure:"retention_days" yaml:"retention_days" json:"retention_days"`
}

// defaultValues is the single source of the defaults (PLAN.md Appendix C). Its keys must match the
// mapstructure tags of Config one for one; TestDefaultsCoverEveryKey enforces that.
var defaultValues = map[string]any{
	"log.level":  "info",
	"log.format": "text",

	"vm.qemu_dir":   "",
	"vm.image":      "",
	"vm.memory_mib": 2048,
	"vm.cpus":       2,
	"vm.resolution": "1280x800",
	"vm.accel":      "auto",
	"vm.slow":       false,

	"llm.backend":       "auto",
	"llm.base_url":      "",
	"llm.model":         "",
	"llm.api_key_env":   "",
	"llm.api_key":       "",
	"llm.tools":         "auto",
	"llm.obs_format":    "text",
	"llm.marks":         "auto",
	"llm.image_max":     1280,
	"llm.num_ctx":       16384,
	"llm.first_timeout": "600s",
	"llm.timeout":       "180s",

	"net.allow":         false,
	"net.allow_domains": []string{},
	"net.deny_domains":  []string{},
	"net.allow_ports":   []int{},

	"safety.step":                 false,
	"safety.policy":               "",
	"safety.actions_per_sec":      2.0,
	"safety.actions_per_response": 3,
	"safety.actions_per_task":     60,
	"safety.max_turns":            100,
	"safety.timeout":              "60m",
	"safety.idle_timeout":         "15m",
	"safety.confirm_timeout":      "120s",

	"viewer.mode":         "window",
	"viewer.fps_active":   5,
	"viewer.fps_idle":     2,
	"viewer.webp_quality": 70,

	"session.task":    "",
	"session.keep":    false,
	"session.keep_vm": "",
	"session.vm":      "",
	"session.no_llm":  false,

	"audit.retention_days": 30,
}
