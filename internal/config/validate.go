// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is wrapped by every *ValidationError.
var ErrInvalid = errors.New("invalid configuration")

// Issue is one problem found by validation.
type Issue struct {
	Key string // config key, e.g. vm.memory_mib
	Msg string
}

// ValidationError lists every problem at once so the user can fix them in one pass.
type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, is := range e.Issues {
		b.WriteString("\n  ")
		if flag := flagForKey(is.Key); flag != "" {
			fmt.Fprintf(&b, "%s (%s): %s", flag, is.Key, is.Msg)
		} else {
			fmt.Fprintf(&b, "%s: %s", is.Key, is.Msg)
		}
	}
	return b.String()
}

// Unwrap makes errors.Is(err, ErrInvalid) true.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// ValidateOptions carries what validation needs to know about the machine and the command line.
type ValidateOptions struct {
	// HostOS is runtime.GOOS; injectable so every OS is testable everywhere.
	HostOS string
	// Explicit are the flags the user set on the command line (Loaded.Explicit).
	Explicit map[string]bool
	// Getenv is used to look for API keys when ForRun is set; defaults to "no variables".
	Getenv func(string) string
	// ForRun adds the checks that only matter when a session is about to start (API key present).
	ForRun bool
}

// Validate checks the loaded configuration and returns a *ValidationError listing every problem.
func (l *Loaded) Validate(o ValidateOptions) error {
	if o.Explicit == nil {
		o.Explicit = l.Explicit
	}
	return l.Config.Validate(o)
}

var (
	reResolution = regexp.MustCompile(`^([0-9]{3,4})x([0-9]{3,4})$`)
	reEnvName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reVMName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	reDomain     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// hostAccel maps hardware accelerators to the only OS that offers them (D-32).
var hostAccel = map[string]string{"kvm": "linux", "whpx": "windows", "hvf": "darwin"}

// Validate checks c. See ValidateOptions.
func (c *Config) Validate(o ValidateOptions) error {
	v := &validator{c: c, o: o}
	v.logging()
	v.vm()
	v.llm()
	v.network()
	v.safety()
	v.viewer()
	v.session()
	v.audit()
	if o.ForRun {
		v.apiKey()
	}
	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues}
}

type validator struct {
	c      *Config
	o      ValidateOptions
	issues []Issue
}

func (v *validator) add(key, format string, args ...any) {
	v.issues = append(v.issues, Issue{Key: key, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) oneOf(key, val string, allowed ...string) {
	for _, a := range allowed {
		if val == a {
			return
		}
	}
	v.add(key, "%q is not valid; use one of: %s", val, strings.Join(allowed, ", "))
}

func (v *validator) intRange(key string, val, lo, hi int, unit string) {
	if val < lo || val > hi {
		v.add(key, "%d is out of range; allowed %d to %d%s", val, lo, hi, unit)
	}
}

func (v *validator) logging() {
	v.oneOf("log.level", v.c.Log.Level, "debug", "info", "warn", "error")
	v.oneOf("log.format", v.c.Log.Format, "text", "json")
}

func (v *validator) vm() {
	c := v.c.VM
	v.intRange("vm.memory_mib", c.MemoryMiB, 1024, 16384, " MiB")
	v.intRange("vm.cpus", c.CPUs, 1, 16, "")

	if m := reResolution.FindStringSubmatch(c.Resolution); m == nil {
		v.add("vm.resolution", "%q is not WIDTHxHEIGHT, e.g. 1280x800", c.Resolution)
	} else {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		if w < 640 || w > 1920 || h < 480 || h > 1080 {
			v.add("vm.resolution", "%dx%d is out of range; allowed 640x480 up to 1920x1080", w, h)
		}
	}

	v.oneOf("vm.accel", c.Accel, "auto", "kvm", "whpx", "hvf", "tcg")
	if want, hardware := hostAccel[c.Accel]; hardware {
		if v.o.HostOS != "" && v.o.HostOS != want {
			v.add("vm.accel", "%s is only available on %s, not on %s (use auto, or tcg to force software emulation)", c.Accel, want, v.o.HostOS)
		}
		if c.Slow {
			v.add("vm.slow", "--slow means software emulation (--accel=tcg) and conflicts with --accel=%s", c.Accel)
		}
	}
}

func (v *validator) llm() {
	c := v.c.LLM
	v.oneOf("llm.backend", c.Backend, "auto", "ollama", "lmstudio", "llamacpp", "openai", "anthropic")
	v.oneOf("llm.tools", c.Tools, "auto", "full", "compact")
	v.oneOf("llm.obs_format", c.ObsFormat, "text", "json")
	v.oneOf("llm.marks", c.Marks, "auto", "on", "off")
	v.intRange("llm.image_max", c.ImageMax, 256, 2048, " px")
	v.intRange("llm.num_ctx", c.NumCtx, 2048, 1048576, " tokens")
	if c.FirstTimeout.D() < 10*time.Second || c.FirstTimeout.D() > 60*time.Minute {
		v.add("llm.first_timeout", "%s is out of range; allowed 10s to 60m", c.FirstTimeout.D())
	}
	if c.Timeout.D() < 5*time.Second || c.Timeout.D() > 30*time.Minute {
		v.add("llm.timeout", "%s is out of range; allowed 5s to 30m", c.Timeout.D())
	}
	if c.APIKeyEnv != "" && !reEnvName.MatchString(c.APIKeyEnv) {
		v.add("llm.api_key_env", "%q is not an environment variable name; give the NAME of the variable, not the key itself", c.APIKeyEnv)
	}
	if c.BaseURL == "" {
		return
	}
	if c.Backend == "auto" {
		v.add("llm.base_url", "a base URL needs an explicit --llm (ollama, lmstudio, llamacpp, openai or anthropic) so the protocol is known")
	}
	u, err := url.Parse(c.BaseURL)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		v.add("llm.base_url", "%q is not an http:// or https:// URL", redactURL(c.BaseURL))
	case u.User != nil:
		v.add("llm.base_url", "the URL must not contain credentials; pass the key through --api-key-env")
	case u.Scheme == "http" && !IsLoopbackHost(u.Hostname()) && !v.c.Safety.Unsafe:
		v.add("llm.base_url", "plain http:// to the non-local host %s would send screenshots unencrypted; use https:// (or --unsafe)", u.Hostname())
	}
}

// redactURL keeps a malformed URL out of error messages if it might carry credentials.
func redactURL(raw string) string {
	if strings.Contains(raw, "@") {
		return "(URL with credentials)"
	}
	return raw
}

// IsLoopbackHost reports whether host names this machine: localhost, *.localhost or a loopback IP literal.
// Other host names count as remote; no DNS lookup is ever made here.
func IsLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (v *validator) network() {
	c := v.c.Net
	for _, d := range append(append([]string{}, c.AllowDomains...), c.DenyDomains...) {
		key := "net.allow_domains"
		if contains(c.DenyDomains, d) && !contains(c.AllowDomains, d) {
			key = "net.deny_domains"
		}
		switch {
		case net.ParseIP(d) != nil:
			v.add(key, "%q is an IP address; list domain names (IP destinations are always checked against the private-range blocklist)", d)
		case !reDomain.MatchString(d):
			v.add(key, "%q is not a plain domain name such as example.com (no scheme, port, path or wildcard)", d)
		}
	}
	for _, p := range c.AllowPorts {
		if p < 1 || p > 65535 {
			v.add("net.allow_ports", "%d is not a TCP port (1 to 65535)", p)
		}
	}
	if !c.Allow {
		if v.o.Explicit["allow-domain"] {
			v.add("net.allow_domains", "--allow-domain only applies together with --allow-net")
		}
		if v.o.Explicit["allow-port"] {
			v.add("net.allow_ports", "--allow-port only applies together with --allow-net")
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (v *validator) safety() {
	c := v.c.Safety
	b := BoundsFor(c.Unsafe)
	hint := ""
	if !c.Unsafe {
		hint = "; raising it needs --unsafe"
	}
	if c.ActionsPerSec <= 0 || c.ActionsPerSec > b.ActionsPerSec {
		v.add("safety.actions_per_sec", "%g is out of range; allowed above 0 up to %g%s", c.ActionsPerSec, b.ActionsPerSec, hint)
	}
	v.intRange("safety.actions_per_response", c.ActionsPerResponse, 1, b.ActionsPerResponse, hint)
	v.intRange("safety.actions_per_task", c.ActionsPerTask, 1, b.ActionsPerTask, hint)
	v.intRange("safety.max_turns", c.MaxTurns, 1, b.MaxTurns, hint)
	if c.Timeout.D() < time.Minute || c.Timeout.D() > b.SessionTimeout {
		v.add("safety.timeout", "%s is out of range; allowed 1m up to %s%s", c.Timeout.D(), b.SessionTimeout, hint)
	}
	if c.IdleTimeout.D() < time.Minute || c.IdleTimeout.D() > c.Timeout.D() {
		v.add("safety.idle_timeout", "%s is out of range; allowed 1m up to the session timeout (%s)", c.IdleTimeout.D(), c.Timeout.D())
	}
	if c.ConfirmTimeout.D() < 5*time.Second || c.ConfirmTimeout.D() > 10*time.Minute {
		v.add("safety.confirm_timeout", "%s is out of range; allowed 5s to 10m (an unanswered confirmation is rejected)", c.ConfirmTimeout.D())
	}
}

func (v *validator) viewer() {
	c := v.c.Viewer
	v.oneOf("viewer.mode", c.Mode, "window", "browser", "none", "webview")
	v.intRange("viewer.fps_active", c.FPSActive, 1, 10, " fps")
	v.intRange("viewer.fps_idle", c.FPSIdle, 1, 10, " fps")
	v.intRange("viewer.webp_quality", c.WebPQuality, 30, 95, "")
	if c.Mode == "none" && !v.c.Session.NoLLM && !v.c.Safety.Unsafe {
		v.add("viewer.mode", "--viewer none lets the model act with nobody watching and needs --unsafe (or --no-llm)")
	}
}

func (v *validator) session() {
	c := v.c.Session
	if len(c.Task) > 8000 {
		v.add("session.task", "the task is %d characters; the limit is 8000", len(c.Task))
	}
	if c.KeepVM != "" && c.KeepVM != "auto" && !reVMName.MatchString(c.KeepVM) {
		v.add("session.keep_vm", "%q is not a valid VM name; use lowercase letters, digits, - and _ (at most 32)", c.KeepVM)
	}
	if c.VM != "" && !reVMName.MatchString(c.VM) {
		v.add("session.vm", "%q is not a valid VM name; use lowercase letters, digits, - and _ (at most 32)", c.VM)
	}
	if c.VM != "" && c.KeepVM != "" {
		v.add("session.vm", "--vm resumes an existing kept VM and --keep-vm creates a new one; use only one of them")
	}
	if c.NoLLM && c.Task != "" {
		v.add("session.no_llm", "--no-llm boots the VM without a model, so --task has nothing to run")
	}
}

func (v *validator) audit() {
	v.intRange("audit.retention_days", v.c.Audit.RetentionDays, 1, 3650, " days")
}

// apiKey is the run-time check: hosted backends cannot work without a key.
func (v *validator) apiKey() {
	c := v.c.LLM
	if _, ok := ResolveAPIKey(v.c, v.o.Getenv); ok {
		return
	}
	switch {
	case c.APIKeyEnv != "":
		v.add("llm.api_key_env", "the environment variable %s is empty or not set", c.APIKeyEnv)
	case c.Backend == "openai":
		v.add("llm.api_key_env", "no API key found for the openai backend: set OPENAI_API_KEY or CONTROLSNACK_LLM_API_KEY, or point --api-key-env at another variable")
	case c.Backend == "anthropic":
		v.add("llm.api_key_env", "no API key found for the anthropic backend: set ANTHROPIC_API_KEY or CONTROLSNACK_LLM_API_KEY, or point --api-key-env at another variable")
	}
}
