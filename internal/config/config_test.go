// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

// keysOf lists the dotted mapstructure paths of every leaf field in a struct type.
func keysOf(t reflect.Type, prefix string) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("mapstructure")
		if tag == "-" || tag == "" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(Duration(0)) {
			keys = append(keys, keysOf(f.Type, path)...)
			continue
		}
		keys = append(keys, path)
	}
	return keys
}

func TestDefaultsCoverEveryKey(t *testing.T) {
	got := keysOf(reflect.TypeOf(Config{}), "")
	var want []string
	for k := range defaultValues {
		want = append(want, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("struct keys and default table differ\nstruct:   %v\ndefaults: %v", got, want)
	}
}

func TestTagsAgree(t *testing.T) {
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			ms, y, j := f.Tag.Get("mapstructure"), f.Tag.Get("yaml"), f.Tag.Get("json")
			if y != j {
				t.Errorf("%s.%s: yaml tag %q != json tag %q", path, f.Name, y, j)
			}
			if ms != "-" && ms != y {
				t.Errorf("%s.%s: mapstructure tag %q != yaml tag %q", path, f.Name, ms, y)
			}
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(Duration(0)) {
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "Config")
}

func TestDefaultsMatchAppendixC(t *testing.T) {
	d := Defaults()
	checks := []struct {
		name      string
		got, want any
	}{
		{"resolution", d.VM.Resolution, "1280x800"},
		{"memory", d.VM.MemoryMiB, 2048},
		{"cpus", d.VM.CPUs, 2},
		{"accel", d.VM.Accel, "auto"},
		{"actions/s (N)", d.Safety.ActionsPerSec, 2.0},
		{"actions/response (M)", d.Safety.ActionsPerResponse, 3},
		{"actions/task", d.Safety.ActionsPerTask, 60},
		{"turns/task", d.Safety.MaxTurns, 100},
		{"session timeout", d.Safety.Timeout.D(), 60 * time.Minute},
		{"idle timeout", d.Safety.IdleTimeout.D(), 15 * time.Minute},
		{"confirm timeout", d.Safety.ConfirmTimeout.D(), 120 * time.Second},
		{"llm image max", d.LLM.ImageMax, 1280},
		{"ollama num_ctx", d.LLM.NumCtx, 16384},
		{"llm first timeout", d.LLM.FirstTimeout.D(), 600 * time.Second},
		{"llm later timeout", d.LLM.Timeout.D(), 180 * time.Second},
		{"fps active", d.Viewer.FPSActive, 5},
		{"fps idle", d.Viewer.FPSIdle, 2},
		{"webp quality", d.Viewer.WebPQuality, 70},
		{"audit retention", d.Audit.RetentionDays, 30},
		{"unsafe off", d.Safety.Unsafe, false},
		{"network off", d.Net.Allow, false},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v (PLAN.md Appendix C)", c.name, c.got, c.want)
		}
	}
	if err := d.Validate(ValidateOptions{HostOS: runtime.GOOS}); err != nil {
		t.Errorf("the defaults must be valid: %v", err)
	}
}

func TestDurationText(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{8 * time.Hour, "8h"},
		{15 * time.Minute, "15m"},
		{120 * time.Second, "2m"}, // the largest whole unit is preferred
		{90 * time.Second, "90s"},
		{1500 * time.Millisecond, "1.5s"},
		{0, "0s"},
	}
	for _, tt := range tests {
		b, _ := Duration(tt.in).MarshalText()
		if string(b) != tt.want {
			t.Errorf("%v marshals to %q, want %q", tt.in, b, tt.want)
		}
		var back Duration
		if err := back.UnmarshalText(b); err != nil || back.D() != tt.in {
			t.Errorf("round trip of %v: got %v, err %v", tt.in, back.D(), err)
		}
	}
	var d Duration
	if err := d.UnmarshalText([]byte("soon")); err == nil || !strings.Contains(err.Error(), "not a duration") {
		t.Errorf("a bad duration must explain itself, got %v", err)
	}
}

// --- loading -----------------------------------------------------------------------------------------------

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// flagSet builds a flag set that mimics the run command for the keys under test.
func flagSet(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.Int("memory", 0, "")
	fs.String("accel", "", "")
	fs.StringSlice("allow-domain", nil, "")
	fs.IntSlice("allow-port", nil, "")
	fs.Duration("timeout", 0, "")
	fs.Bool("allow-net", false, "")
	fs.Bool("step", false, "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

func noUserConfig() (string, error) {
	return filepath.Join(os.TempDir(), "controlsnack-test-nonexistent"), nil
}

func TestPrecedenceFlagsOverEnvOverFileOverDefaults(t *testing.T) {
	file := writeConfig(t, "vm:\n  memory_mib: 3000\n  cpus: 4\nllm:\n  model: from-file\n")

	l, err := Load(LoadOptions{ConfigFile: file, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.VM.MemoryMiB != 3000 || l.Config.VM.CPUs != 4 || l.Config.LLM.Model != "from-file" {
		t.Fatalf("file values lost: %+v", l.Config.VM)
	}
	if l.Config.VM.Resolution != "1280x800" {
		t.Fatalf("keys absent from the file must keep their default, got %q", l.Config.VM.Resolution)
	}

	t.Setenv("CONTROLSNACK_VM_MEMORY_MIB", "4000")
	l, err = Load(LoadOptions{ConfigFile: file, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.VM.MemoryMiB != 4000 {
		t.Fatalf("env must beat the file: memory = %d", l.Config.VM.MemoryMiB)
	}
	if l.Config.VM.CPUs != 4 {
		t.Fatalf("env for one key must not disturb file values for others: cpus = %d", l.Config.VM.CPUs)
	}

	l, err = Load(LoadOptions{ConfigFile: file, Flags: flagSet(t, "--memory", "5000")})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.VM.MemoryMiB != 5000 {
		t.Fatalf("a flag must beat env and file: memory = %d", l.Config.VM.MemoryMiB)
	}
	if !l.Explicit["memory"] || l.Explicit["accel"] {
		t.Fatalf("Explicit must list exactly the flags that were set: %v", l.Explicit)
	}
}

func TestUnsetFlagDoesNotOverride(t *testing.T) {
	// The flag exists with a zero default; because the user did not type it, the file value must stand.
	file := writeConfig(t, "vm:\n  memory_mib: 3000\n")
	l, err := Load(LoadOptions{ConfigFile: file, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	if l.Config.VM.MemoryMiB != 3000 {
		t.Fatalf("an untouched flag overrode the file: memory = %d", l.Config.VM.MemoryMiB)
	}
}

func TestListsAndDurationsFromEveryLayer(t *testing.T) {
	t.Setenv("CONTROLSNACK_NET_ALLOW_DOMAINS", "a.example.com, b.example.com")
	t.Setenv("CONTROLSNACK_NET_ALLOW_PORTS", "8080,8443")
	l, err := Load(LoadOptions{ConfigFile: writeConfig(t, "safety:\n  timeout: 30m\n"), Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Config.Net.AllowPorts; !reflect.DeepEqual(got, []int{8080, 8443}) {
		t.Errorf("ports from env = %v", got)
	}
	if got := l.Config.Net.AllowDomains; len(got) != 2 || got[0] != "a.example.com" {
		t.Errorf("domains from env = %q", got)
	}
	if l.Config.Safety.Timeout.D() != 30*time.Minute {
		t.Errorf("duration from file = %v", l.Config.Safety.Timeout.D())
	}

	l, err = Load(LoadOptions{ConfigFile: writeConfig(t, ""), Flags: flagSet(t, "--allow-domain", "x.example.org", "--allow-domain", "y.example.org", "--allow-port", "9000", "--timeout", "90m", "--allow-net")})
	if err != nil {
		t.Fatal(err)
	}
	c := l.Config
	if len(c.Net.AllowDomains) != 2 || c.Net.AllowDomains[1] != "y.example.org" || len(c.Net.AllowPorts) != 1 || c.Net.AllowPorts[0] != 9000 {
		t.Errorf("slice flags not applied: %+v", c.Net)
	}
	if c.Safety.Timeout.D() != 90*time.Minute || !c.Net.Allow {
		t.Errorf("duration/bool flags not applied: %v %v", c.Safety.Timeout.D(), c.Net.Allow)
	}
}

func TestStrictFileParsing(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unknown key", "vm:\n  memroy_mib: 3000\n", "memroy_mib"},
		{"unknown section", "network:\n  allow: true\n", "network"},
		{"wrong type", "vm:\n  cpus: lots\n", "cpus"},
		{"bad duration", "safety:\n  timeout: soon\n", "not a duration"},
		{"not yaml", "vm: [unclosed\n", "config file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(LoadOptions{ConfigFile: writeConfig(t, tt.body)})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q should mention %q", err, tt.want)
			}
		})
	}
}

func TestMissingFiles(t *testing.T) {
	if _, err := Load(LoadOptions{ConfigFile: filepath.Join(t.TempDir(), "nope.yaml")}); err == nil {
		t.Error("an explicit --config that does not exist must be an error")
	}
	l, err := Load(LoadOptions{UserConfigDir: noUserConfig})
	if err != nil {
		t.Fatalf("a missing default config file is normal: %v", err)
	}
	if l.File != "" {
		t.Errorf("File = %q, want empty", l.File)
	}
	if !reflect.DeepEqual(l.Config, Defaults()) {
		t.Error("with no file, env or flags the result must equal Defaults()")
	}
}

func TestDefaultPathLocation(t *testing.T) {
	p, err := DefaultPath(func() (string, error) { return filepath.FromSlash("/cfg"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.FromSlash("/cfg"), "controlsnack", "config.yaml"); p != want {
		t.Errorf("DefaultPath = %q, want %q", p, want)
	}
	if _, err := DefaultPath(func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Error("an unresolvable config dir must be reported")
	}
}

func TestUnsafeOnlyFromTheFlag(t *testing.T) {
	// Via the file:
	_, err := Load(LoadOptions{ConfigFile: writeConfig(t, "safety:\n  unsafe: true\n")})
	if err == nil || !strings.Contains(err.Error(), "--unsafe") {
		t.Errorf("safety.unsafe in a file must be refused with a pointer to --unsafe, got %v", err)
	}
	// Via the environment:
	t.Setenv("CONTROLSNACK_SAFETY_UNSAFE", "1")
	_, err = Load(LoadOptions{ConfigFile: writeConfig(t, "")})
	if err == nil || !strings.Contains(err.Error(), "--unsafe") {
		t.Errorf("CONTROLSNACK_SAFETY_UNSAFE must be refused, got %v", err)
	}
	t.Setenv("CONTROLSNACK_SAFETY_UNSAFE", "")
	// Via the flag option:
	l, err := Load(LoadOptions{ConfigFile: writeConfig(t, ""), Unsafe: true})
	if err != nil || !l.Config.Safety.Unsafe {
		t.Errorf("LoadOptions.Unsafe must reach the config: %v %v", l, err)
	}
}

func TestWarnings(t *testing.T) {
	file := writeConfig(t, "net:\n  allow_domains: [example.com]\n")
	l, err := Load(LoadOptions{ConfigFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "--allow-net") {
		t.Errorf("allow_domains without allow must warn: %v", l.Warnings)
	}
	if runtime.GOOS == "windows" {
		return // POSIX permission bits do not exist there
	}
	keyed := writeConfig(t, "llm:\n  api_key: sk-secret\n")
	if err := os.Chmod(keyed, 0o644); err != nil {
		t.Fatal(err)
	}
	l, err = Load(LoadOptions{ConfigFile: keyed})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "chmod 600") || strings.Contains(l.Warnings[0], "sk-secret") {
		t.Errorf("a world-readable file holding an API key must warn without echoing it: %v", l.Warnings)
	}
}

// --- validation --------------------------------------------------------------------------------------------

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		host   string
		flags  []string // flags the user "typed"
		want   string   // substring of the message; empty = valid
	}{
		{"defaults are valid", func(*Config) {}, "linux", nil, ""},
		{"memory too small", func(c *Config) { c.VM.MemoryMiB = 512 }, "linux", nil, "--memory (vm.memory_mib): 512 is out of range"},
		{"memory too large", func(c *Config) { c.VM.MemoryMiB = 65536 }, "linux", nil, "vm.memory_mib"},
		{"cpus zero", func(c *Config) { c.VM.CPUs = 0 }, "linux", nil, "--cpus (vm.cpus)"},
		{"resolution garbage", func(c *Config) { c.VM.Resolution = "big" }, "linux", nil, "WIDTHxHEIGHT"},
		{"resolution above 1080p", func(c *Config) { c.VM.Resolution = "2560x1440" }, "linux", nil, "1920x1080"},
		{"resolution 1080p ok", func(c *Config) { c.VM.Resolution = "1920x1080" }, "linux", nil, ""},
		{"accel unknown", func(c *Config) { c.VM.Accel = "xen" }, "linux", nil, "use one of: auto, kvm, whpx, hvf, tcg"},
		{"kvm on windows", func(c *Config) { c.VM.Accel = "kvm" }, "windows", nil, "only available on linux, not on windows"},
		{"whpx on linux", func(c *Config) { c.VM.Accel = "whpx" }, "linux", nil, "only available on windows"},
		{"hvf on linux", func(c *Config) { c.VM.Accel = "hvf" }, "linux", nil, "only available on darwin"},
		{"hvf on darwin", func(c *Config) { c.VM.Accel = "hvf" }, "darwin", nil, ""},
		{"tcg anywhere", func(c *Config) { c.VM.Accel = "tcg" }, "windows", nil, ""},
		{"slow with auto", func(c *Config) { c.VM.Slow = true }, "linux", nil, ""},
		{"slow with tcg", func(c *Config) { c.VM.Slow = true; c.VM.Accel = "tcg" }, "linux", nil, ""},
		{"slow with whpx", func(c *Config) { c.VM.Slow = true; c.VM.Accel = "whpx" }, "windows", nil, "conflicts with --accel=whpx"},
		{"base url without backend", func(c *Config) { c.LLM.BaseURL = "http://127.0.0.1:9999/v1" }, "linux", nil, "explicit --llm"},
		{"base url with backend", func(c *Config) { c.LLM.Backend = "llamacpp"; c.LLM.BaseURL = "http://127.0.0.1:9999/v1" }, "linux", nil, ""},
		{"base url not http", func(c *Config) { c.LLM.Backend = "openai"; c.LLM.BaseURL = "ftp://x/y" }, "linux", nil, "not an http:// or https:// URL"},
		{"base url with credentials", func(c *Config) { c.LLM.Backend = "openai"; c.LLM.BaseURL = "https://user:pw@api.example.com" }, "linux", nil, "must not contain credentials"},
		{"credentials never echoed", func(c *Config) { c.LLM.Backend = "openai"; c.LLM.BaseURL = "ftp://user:hunter2@x" }, "linux", nil, "(URL with credentials)"},
		{"remote http refused", func(c *Config) { c.LLM.Backend = "openai"; c.LLM.BaseURL = "http://api.example.com/v1" }, "linux", nil, "use https://"},
		{"remote http allowed when unsafe", func(c *Config) {
			c.Safety.Unsafe = true
			c.LLM.Backend = "openai"
			c.LLM.BaseURL = "http://api.example.com/v1"
		}, "linux", nil, ""},
		{"remote https fine", func(c *Config) { c.LLM.Backend = "openai"; c.LLM.BaseURL = "https://api.example.com/v1" }, "linux", nil, ""},
		{"localhost http fine", func(c *Config) { c.LLM.Backend = "ollama"; c.LLM.BaseURL = "http://localhost:11434" }, "linux", nil, ""},
		{"ipv6 loopback fine", func(c *Config) { c.LLM.Backend = "ollama"; c.LLM.BaseURL = "http://[::1]:11434" }, "linux", nil, ""},
		{"lookalike host is remote", func(c *Config) { c.LLM.Backend = "ollama"; c.LLM.BaseURL = "http://localhost.evil.example/" }, "linux", nil, "use https://"},
		{"api key env must be a name", func(c *Config) { c.LLM.APIKeyEnv = "sk-abc123" }, "linux", nil, "NAME of the variable"},
		{"tools unknown", func(c *Config) { c.LLM.Tools = "all" }, "linux", nil, "llm.tools"},
		{"image max too big", func(c *Config) { c.LLM.ImageMax = 8000 }, "linux", nil, "llm.image_max"},
		{"domain with scheme", func(c *Config) { c.Net.Allow = true; c.Net.AllowDomains = []string{"https://example.com"} }, "linux", nil, "not a plain domain name"},
		{"domain is an IP", func(c *Config) { c.Net.Allow = true; c.Net.AllowDomains = []string{"10.0.0.1"} }, "linux", nil, "is an IP address"},
		{"domain fine", func(c *Config) { c.Net.Allow = true; c.Net.AllowDomains = []string{"docs.example.com"} }, "linux", nil, ""},
		{"deny domain checked too", func(c *Config) { c.Net.DenyDomains = []string{"BAD HOST"} }, "linux", nil, "net.deny_domains"},
		{"port out of range", func(c *Config) { c.Net.Allow = true; c.Net.AllowPorts = []int{70000} }, "linux", nil, "not a TCP port"},
		{"allow-domain typed without allow-net", func(c *Config) { c.Net.AllowDomains = []string{"example.com"} }, "linux", []string{"allow-domain"}, "only applies together with --allow-net"},
		{"allow-port typed without allow-net", func(c *Config) { c.Net.AllowPorts = []int{8080} }, "linux", []string{"allow-port"}, "only applies together with --allow-net"},
		{"allow-domain from file without allow-net is only a warning", func(c *Config) { c.Net.AllowDomains = []string{"example.com"} }, "linux", nil, ""},
		{"rate above the safe limit", func(c *Config) { c.Safety.ActionsPerSec = 5 }, "linux", nil, "raising it needs --unsafe"},
		{"rate above the safe limit but unsafe", func(c *Config) { c.Safety.Unsafe = true; c.Safety.ActionsPerSec = 5 }, "linux", nil, ""},
		{"rate above even the unsafe limit", func(c *Config) { c.Safety.Unsafe = true; c.Safety.ActionsPerSec = 11 }, "linux", nil, "up to 10"},
		{"rate zero", func(c *Config) { c.Safety.ActionsPerSec = 0 }, "linux", nil, "above 0"},
		{"lowering the rate is always fine", func(c *Config) { c.Safety.ActionsPerSec = 0.5 }, "linux", nil, ""},
		{"actions per response 4", func(c *Config) { c.Safety.ActionsPerResponse = 4 }, "linux", nil, "--actions-per-response"},
		{"actions per response 10 unsafe", func(c *Config) { c.Safety.Unsafe = true; c.Safety.ActionsPerResponse = 10 }, "linux", nil, ""},
		{"actions per task 500 unsafe", func(c *Config) { c.Safety.Unsafe = true; c.Safety.ActionsPerTask = 500 }, "linux", nil, ""},
		{"actions per task 501 unsafe", func(c *Config) { c.Safety.Unsafe = true; c.Safety.ActionsPerTask = 501 }, "linux", nil, "safety.actions_per_task"},
		{"max turns 101", func(c *Config) { c.Safety.MaxTurns = 101 }, "linux", nil, "--max-turns"},
		{"timeout 2h needs unsafe", func(c *Config) { c.Safety.Timeout = Duration(2 * time.Hour) }, "linux", nil, "--timeout"},
		{"timeout 8h unsafe", func(c *Config) { c.Safety.Unsafe = true; c.Safety.Timeout = Duration(8 * time.Hour) }, "linux", nil, ""},
		{"idle longer than session", func(c *Config) { c.Safety.Timeout = Duration(10 * time.Minute) }, "linux", nil, "safety.idle_timeout"},
		{"confirm timeout too short", func(c *Config) { c.Safety.ConfirmTimeout = Duration(time.Second) }, "linux", nil, "safety.confirm_timeout"},
		{"viewer none with a model", func(c *Config) { c.Viewer.Mode = "none" }, "linux", nil, "needs --unsafe"},
		{"viewer none unsafe", func(c *Config) { c.Viewer.Mode = "none"; c.Safety.Unsafe = true }, "linux", nil, ""},
		{"viewer none without a model", func(c *Config) { c.Viewer.Mode = "none"; c.Session.NoLLM = true }, "linux", nil, ""},
		{"viewer unknown", func(c *Config) { c.Viewer.Mode = "tui" }, "linux", nil, "viewer.mode"},
		{"vm and keep-vm together", func(c *Config) { c.Session.VM = "lab"; c.Session.KeepVM = "other" }, "linux", nil, "use only one of them"},
		{"keep-vm auto", func(c *Config) { c.Session.KeepVM = "auto" }, "linux", nil, ""},
		{"vm name with slash", func(c *Config) { c.Session.VM = "../x" }, "linux", nil, "not a valid VM name"},
		{"no-llm with a task", func(c *Config) { c.Session.NoLLM = true; c.Session.Task = "do it" }, "linux", nil, "nothing to run"},
		{"task too long", func(c *Config) { c.Session.Task = strings.Repeat("x", 8001) }, "linux", nil, "limit is 8000"},
		{"log level", func(c *Config) { c.Log.Level = "trace" }, "linux", nil, "log.level"},
		{"retention zero", func(c *Config) { c.Audit.RetentionDays = 0 }, "linux", nil, "audit.retention_days"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			tt.mutate(&c)
			explicit := map[string]bool{}
			for _, f := range tt.flags {
				explicit[f] = true
			}
			err := c.Validate(ValidateOptions{HostOS: tt.host, Explicit: explicit})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.want)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error must wrap ErrInvalid: %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error does not contain %q:\n%v", tt.want, err)
			}
		})
	}
}

func TestValidateReportsEverythingAtOnce(t *testing.T) {
	c := Defaults()
	c.VM.MemoryMiB = 1
	c.VM.CPUs = 99
	c.Log.Level = "loud"
	err := c.Validate(ValidateOptions{HostOS: "linux"})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Issues) != 3 {
		t.Fatalf("want 3 issues in one error, got %v", err)
	}
}

func TestValidateForRunNeedsAnAPIKey(t *testing.T) {
	c := Defaults()
	c.LLM.Backend = "openai"
	opts := ValidateOptions{HostOS: "linux", ForRun: true, Getenv: func(string) string { return "" }}
	if err := c.Validate(opts); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("openai without a key must say which variable to set: %v", err)
	}
	opts.Getenv = func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-test"
		}
		return ""
	}
	if err := c.Validate(opts); err != nil {
		t.Errorf("key present: %v", err)
	}
	c.LLM.Backend = "ollama"
	opts.Getenv = func(string) string { return "" }
	if err := c.Validate(opts); err != nil {
		t.Errorf("local backends need no key: %v", err)
	}
	c.LLM.APIKeyEnv = "MY_KEY"
	if err := c.Validate(opts); err == nil || !strings.Contains(err.Error(), "MY_KEY") {
		t.Errorf("a named but empty variable must be reported: %v", err)
	}
	// Without ForRun nothing about keys is checked, so `config show` works on a bare machine.
	opts.ForRun = false
	if err := c.Validate(opts); err != nil {
		t.Errorf("key checks must be limited to ForRun: %v", err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	yes := []string{"localhost", "LOCALHOST", "foo.localhost", "127.0.0.1", "127.9.9.9", "::1", "[::1]", "localhost."}
	no := []string{"example.com", "localhost.example.com", "0.0.0.0", "10.0.0.1", "192.168.1.5", "::", "127.0.0.1.evil.com", ""}
	for _, h := range yes {
		if !IsLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range no {
		if IsLoopbackHost(h) {
			t.Errorf("%q must not be treated as loopback", h)
		}
	}
}

// --- secrets -----------------------------------------------------------------------------------------------

func TestRedacted(t *testing.T) {
	c := Defaults()
	c.LLM.APIKey = "sk-very-secret"
	c.LLM.Model = "visible-model"
	r := c.Redacted()
	if r.LLM.APIKey != RedactedMark {
		t.Errorf("api_key = %q, want the redaction mark", r.LLM.APIKey)
	}
	if r.LLM.Model != "visible-model" {
		t.Errorf("non-secret fields must survive: %q", r.LLM.Model)
	}
	if c.LLM.APIKey != "sk-very-secret" {
		t.Error("Redacted must not modify the original")
	}
	empty := Defaults()
	if got := empty.Redacted().LLM.APIKey; got != "" {
		t.Errorf("an unset key stays empty rather than showing the mark: %q", got)
	}
}

func TestEverySecretFieldIsTagged(t *testing.T) {
	// Any new field whose name suggests a secret must carry the tag, or Redacted would leak it.
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(Duration(0)) {
				walk(f.Type, path+"."+f.Name)
				continue
			}
			name := strings.ToLower(f.Name)
			looksSecret := strings.Contains(name, "key") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password")
			if strings.HasSuffix(name, "env") { // the NAME of a variable is not a secret
				continue
			}
			if looksSecret && f.Tag.Get("secret") != "true" {
				t.Errorf("%s.%s looks like a secret but is not tagged secret:\"true\"", path, f.Name)
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "Config")
}

func TestResolveAPIKey(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name       string
		backend    string
		keyEnv     string
		fileKey    string
		env        map[string]string
		wantKey    string
		wantSource string
	}{
		{"explicit variable wins", "openai", "MY_KEY", "file", map[string]string{"MY_KEY": "k1", "OPENAI_API_KEY": "k2", "CONTROLSNACK_LLM_API_KEY": "k3"}, "k1", "environment variable MY_KEY"},
		{"explicit but empty does not fall through", "openai", "MY_KEY", "file", map[string]string{"OPENAI_API_KEY": "k2"}, "", ""},
		{"generic variable before the backend one", "openai", "", "", map[string]string{"OPENAI_API_KEY": "k2", "CONTROLSNACK_LLM_API_KEY": "k3"}, "k3", "environment variable CONTROLSNACK_LLM_API_KEY"},
		{"backend variable", "anthropic", "", "", map[string]string{"ANTHROPIC_API_KEY": "k4"}, "k4", "environment variable ANTHROPIC_API_KEY"},
		{"openai variable is not used for anthropic", "anthropic", "", "", map[string]string{"OPENAI_API_KEY": "k2"}, "", ""},
		{"config file last", "openai", "", "from-file", nil, "from-file", "config file"},
		{"nothing", "openai", "", "", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			c.LLM.Backend, c.LLM.APIKeyEnv, c.LLM.APIKey = tt.backend, tt.keyEnv, tt.fileKey
			got, ok := ResolveAPIKey(&c, env(tt.env))
			if ok != (tt.wantKey != "") || got.Value != tt.wantKey || got.Source != tt.wantSource {
				t.Fatalf("got (%+v, %v), want key %q from %q", got, ok, tt.wantKey, tt.wantSource)
			}
		})
	}
	c := Defaults()
	if _, ok := ResolveAPIKey(&c, nil); ok {
		t.Error("a nil getenv must find nothing instead of reading the real environment")
	}
}

func TestFlagKeysPointAtRealKeys(t *testing.T) {
	valid := map[string]bool{}
	for k := range defaultValues {
		valid[k] = true
	}
	for flag, key := range FlagKeys {
		if !valid[key] {
			t.Errorf("--%s maps to %q, which is not a config key", flag, key)
		}
		if got := flagForKey(key); got == "" {
			t.Errorf("no reverse mapping for %s", key)
		}
	}
	if _, ok := FlagKeys["unsafe"]; ok {
		t.Error("--unsafe must never be bound to a config key (D-38)")
	}
}

func TestEnvironmentLayerIsInjectable(t *testing.T) {
	// The process environment must be ignored when a Getenv is supplied, so tests and embedders stay hermetic.
	t.Setenv("CONTROLSNACK_VM_CPUS", "9")
	env := map[string]string{
		"CONTROLSNACK_VM_MEMORY_MIB":     "4096",
		"CONTROLSNACK_LLM_FIRST_TIMEOUT": "45s",
		"CONTROLSNACK_NET_ALLOW":         "true",
		"CONTROLSNACK_LOG_LEVEL":         "", // empty counts as unset
	}
	l, err := Load(LoadOptions{
		UserConfigDir: noUserConfig,
		Getenv:        func(k string) string { return env[k] },
	})
	if err != nil {
		t.Fatal(err)
	}
	c := l.Config
	if c.VM.CPUs != 2 {
		t.Errorf("the real environment leaked in: cpus = %d", c.VM.CPUs)
	}
	if c.VM.MemoryMiB != 4096 || c.LLM.FirstTimeout.D() != 45*time.Second || !c.Net.Allow || c.Log.Level != "info" {
		t.Errorf("injected environment not applied: %+v", c)
	}
}

func TestEnvNames(t *testing.T) {
	if got := EnvName("llm.image_max"); got != "CONTROLSNACK_LLM_IMAGE_MAX" {
		t.Errorf("EnvName = %q", got)
	}
	for key := range defaultValues {
		if strings.ContainsAny(EnvName(key), ". -") {
			t.Errorf("EnvName(%q) = %q is not a valid variable name", key, EnvName(key))
		}
	}
}
