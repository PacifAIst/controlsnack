// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/exitcode"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
)

type fakeRunner struct {
	called int
	got    config.Config
	err    error
}

func (f *fakeRunner) Run(_ context.Context, cfg config.Config) error {
	f.called++
	f.got = cfg
	return f.err
}

type fakeKiller struct{ called int }

func (f *fakeKiller) Kill(context.Context, io.Writer) error { f.called++; return nil }

// result is what one CLI invocation produced.
type result struct {
	code        int
	out, errOut string
}

// run executes the CLI hermetically: an empty config dir, the given environment, a chosen host OS.
func runCLI(t *testing.T, env map[string]string, hostOS string, svc Services, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	e := Env{
		Stdout:        &out,
		Stderr:        &errOut,
		Services:      svc,
		Getenv:        func(k string) string { return env[k] },
		UserConfigDir: func() (string, error) { return t.TempDir(), nil },
		HostOS:        hostOS,
	}
	code := Run(context.Background(), e, args)
	return result{code: code, out: out.String(), errOut: errOut.String()}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersion(t *testing.T) {
	r := runCLI(t, nil, "linux", Services{}, "version")
	if r.code != 0 || !strings.Contains(r.out, "controlsnack") || !strings.Contains(r.out, "os/arch:") {
		t.Fatalf("version: %+v", r)
	}
	r = runCLI(t, nil, "linux", Services{}, "version", "--json")
	var m map[string]string
	if r.code != 0 || json.Unmarshal([]byte(r.out), &m) != nil || m["version"] == "" || m["go_version"] == "" {
		t.Fatalf("version --json: %+v", r)
	}
	r = runCLI(t, nil, "linux", Services{}, "--version")
	if r.code != 0 || !strings.HasPrefix(r.out, "controlsnack ") {
		t.Fatalf("--version: %+v", r)
	}
}

func TestBareInvocationShowsHelp(t *testing.T) {
	r := runCLI(t, nil, "linux", Services{})
	if r.code != 0 || !strings.Contains(r.out, "Available Commands") {
		t.Fatalf("bare invocation: %+v", r)
	}
	for _, name := range []string{"run", "kill", "image", "vm", "audit", "doctor", "llm", "schema", "version", "config"} {
		if !strings.Contains(r.out, "  "+name+" ") {
			t.Errorf("help does not list the %q command", name)
		}
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"frobnicate"}, "unknown command"},
		{"unknown subcommand", []string{"image", "frobnicate"}, "unknown command"},
		{"unknown flag", []string{"run", "--nope"}, "unknown flag"},
		{"flag needs value", []string{"run", "--memory"}, "needs an argument"},
		{"bad flag value", []string{"run", "--memory", "lots"}, "invalid argument"},
		{"stray argument", []string{"run", "extra"}, "takes no arguments"},
		{"missing argument", []string{"image", "rm"}, "needs an image id"},
		{"too many arguments", []string{"image", "path", "a", "b"}, "at most 1"},
		{"import needs manifest", []string{"image", "import", "x.qcow2"}, "--manifest"},
		{"schema format", []string{"schema", "--format", "xml"}, "unknown schema format"},
		{"age", []string{"audit", "prune", "--older-than", "soonish"}, "not an age"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCLI(t, nil, "linux", Services{}, tt.args...)
			if r.code != exitcode.Usage {
				t.Fatalf("exit code %d, want 2\n%s", r.code, r.errOut)
			}
			if !strings.Contains(r.errOut, tt.want) {
				t.Errorf("stderr %q should contain %q", r.errOut, tt.want)
			}
			if !strings.Contains(r.errOut, "--help") {
				t.Errorf("a usage error should point to --help: %q", r.errOut)
			}
		})
	}
}

func TestPlaceholdersExitSeventy(t *testing.T) {
	// Every command that is not built yet must say so and exit 70 rather than pretend.
	cmds := [][]string{
		{"run", "--task", "x"},
		{"image", "build", "--yes"},
		{"vm", "ls"},
		{"vm", "rm", "lab"},
		{"llm", "probe"},
		{"llm", "models"},
		{"schema"},
		{"schema", "--format", "openai"},
	}
	for _, args := range cmds {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := runCLI(t, nil, "linux", Services{}, args...)
			if r.code != exitcode.NotImplemented {
				t.Fatalf("exit code %d, want 70\n%s", r.code, r.errOut)
			}
			if !strings.Contains(r.errOut, "not implemented yet (planned for P") {
				t.Errorf("the message must name the task that delivers it: %q", r.errOut)
			}
			if strings.Contains(r.errOut, "--help") {
				t.Errorf("exit 70 is not a usage error: %q", r.errOut)
			}
		})
	}
}

func TestRunDefaultsEqualConfigDefaults(t *testing.T) {
	// Guards against a flag default drifting away from the config default (they are separate declarations).
	fr := &fakeRunner{}
	r := runCLI(t, nil, "linux", Services{Runner: fr}, "run")
	if r.code != 0 || fr.called != 1 {
		t.Fatalf("run: %+v called=%d", r, fr.called)
	}
	if !reflect.DeepEqual(fr.got, config.Defaults()) {
		t.Fatalf("flag defaults differ from config defaults:\n got %+v\nwant %+v", fr.got, config.Defaults())
	}
}

func TestRunHasEveryPlannedFlag(t *testing.T) {
	// PLAN.md P1-T04.
	want := []string{"task", "llm", "base-url", "model", "api-key-env", "tools", "obs-format", "marks", "allow-net",
		"allow-domain", "allow-port", "keep", "keep-vm", "vm", "unsafe", "step", "viewer", "resolution", "memory", "cpus",
		"accel", "slow", "qemu", "image", "policy", "timeout", "max-turns", "actions-per-sec", "actions-per-response",
		"actions-per-task", "no-llm"}
	a := newApp(Env{})
	runCmd, _, err := a.root.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		if runCmd.Flags().Lookup(name) == nil {
			t.Errorf("run has no --%s", name)
		}
	}
	// Every flag with a config key must be defined where users can type it.
	for name := range config.FlagKeys {
		if runCmd.Flags().Lookup(name) == nil && runCmd.InheritedFlags().Lookup(name) == nil {
			t.Errorf("config.FlagKeys lists --%s but run does not define it", name)
		}
	}
	for _, name := range []string{"accel", "slow", "qemu"} {
		for _, path := range [][]string{{"image", "build"}, {"doctor"}} {
			c, _, _ := a.root.Find(path)
			if c.Flags().Lookup(name) == nil {
				t.Errorf("%s has no --%s (D-32)", strings.Join(path, " "), name)
			}
		}
	}
}

func TestRunFlagsReachTheConfig(t *testing.T) {
	fr := &fakeRunner{}
	r := runCLI(t, map[string]string{"MY_KEY": "k"}, "linux", Services{Runner: fr}, "run",
		"--task", "print the kernel version", "--llm", "ollama", "--model", "qwen", "--base-url", "http://127.0.0.1:11434",
		"--api-key-env", "MY_KEY", "--tools", "compact", "--obs-format", "json", "--marks", "off",
		"--allow-net", "--allow-domain", "a.example.com", "--allow-domain", "b.example.com", "--allow-port", "8080",
		"--keep", "--keep-vm=lab", "--step", "--policy", "rules.yaml", "--timeout", "30m", "--max-turns", "50",
		"--actions-per-sec", "1.5", "--actions-per-response", "2", "--actions-per-task", "20", "--viewer", "browser",
		"--resolution", "1920x1080", "--memory", "4096", "--cpus", "4", "--accel", "tcg", "--qemu", "/opt/qemu",
		"--image", "base-1", "--log-level", "debug")
	if r.code != 0 {
		t.Fatalf("run failed: %+v", r)
	}
	c := fr.got
	checks := map[string][2]any{
		"task":        {c.Session.Task, "print the kernel version"},
		"backend":     {c.LLM.Backend, "ollama"},
		"model":       {c.LLM.Model, "qwen"},
		"base url":    {c.LLM.BaseURL, "http://127.0.0.1:11434"},
		"key env":     {c.LLM.APIKeyEnv, "MY_KEY"},
		"tools":       {c.LLM.Tools, "compact"},
		"obs":         {c.LLM.ObsFormat, "json"},
		"marks":       {c.LLM.Marks, "off"},
		"allow net":   {c.Net.Allow, true},
		"domains":     {c.Net.AllowDomains, []string{"a.example.com", "b.example.com"}},
		"ports":       {c.Net.AllowPorts, []int{8080}},
		"keep":        {c.Session.Keep, true},
		"keep-vm":     {c.Session.KeepVM, "lab"},
		"step":        {c.Safety.Step, true},
		"policy":      {c.Safety.Policy, "rules.yaml"},
		"timeout":     {c.Safety.Timeout.D(), 30 * time.Minute},
		"max turns":   {c.Safety.MaxTurns, 50},
		"rate":        {c.Safety.ActionsPerSec, 1.5},
		"per resp":    {c.Safety.ActionsPerResponse, 2},
		"per task":    {c.Safety.ActionsPerTask, 20},
		"viewer":      {c.Viewer.Mode, "browser"},
		"resolution":  {c.VM.Resolution, "1920x1080"},
		"memory":      {c.VM.MemoryMiB, 4096},
		"cpus":        {c.VM.CPUs, 4},
		"accel":       {c.VM.Accel, "tcg"},
		"qemu":        {c.VM.QemuDir, "/opt/qemu"},
		"image":       {c.VM.Image, "base-1"},
		"log level":   {c.Log.Level, "debug"},
		"unsafe off":  {c.Safety.Unsafe, false},
		"no-llm off":  {c.Session.NoLLM, false},
		"slow off":    {c.VM.Slow, false},
		"vm (resume)": {c.Session.VM, ""},
	}
	for name, p := range checks {
		if !reflect.DeepEqual(p[0], p[1]) {
			t.Errorf("%s = %v, want %v", name, p[0], p[1])
		}
	}
}

func TestKeepVMWithoutAName(t *testing.T) {
	fr := &fakeRunner{}
	if r := runCLI(t, nil, "linux", Services{Runner: fr}, "run", "--keep-vm"); r.code != 0 || fr.got.Session.KeepVM != "auto" {
		t.Fatalf("--keep-vm alone should mean a generated name: %+v keep=%q", r, fr.got.Session.KeepVM)
	}
}

func TestConfigPrecedenceThroughTheCLI(t *testing.T) {
	file := writeFile(t, "vm:\n  cpus: 3\n  memory_mib: 3000\n")
	fr := &fakeRunner{}
	svc := Services{Runner: fr}

	runCLI(t, nil, "linux", svc, "--config", file, "run")
	if fr.got.VM.CPUs != 3 || fr.got.VM.MemoryMiB != 3000 {
		t.Fatalf("file values: %+v", fr.got.VM)
	}
	env := map[string]string{"CONTROLSNACK_VM_CPUS": "5"}
	runCLI(t, env, "linux", svc, "--config", file, "run")
	if fr.got.VM.CPUs != 5 || fr.got.VM.MemoryMiB != 3000 {
		t.Fatalf("env must beat the file for its own key only: %+v", fr.got.VM)
	}
	runCLI(t, nil, "linux", svc, "--config", file, "run", "--cpus", "6")
	if fr.got.VM.CPUs != 6 {
		t.Fatalf("a flag must beat env: %+v", fr.got.VM)
	}
}

func TestUnsafeIsFlagOnlyAndWidensLimits(t *testing.T) {
	fr := &fakeRunner{}
	svc := Services{Runner: fr}

	r := runCLI(t, nil, "linux", svc, "run", "--actions-per-response", "10")
	if r.code != exitcode.Usage || !strings.Contains(r.errOut, "--actions-per-response") || !strings.Contains(r.errOut, "--unsafe") {
		t.Fatalf("raising a limit without --unsafe must be refused and say why: %+v", r)
	}
	r = runCLI(t, nil, "linux", svc, "run", "--unsafe", "--actions-per-response", "10", "--actions-per-sec", "10")
	if r.code != 0 || !fr.got.Safety.Unsafe || fr.got.Safety.ActionsPerResponse != 10 {
		t.Fatalf("--unsafe should allow it: %+v", r)
	}
	// A config file cannot switch it on.
	r = runCLI(t, nil, "linux", svc, "--config", writeFile(t, "safety:\n  unsafe: true\n"), "run")
	if r.code != exitcode.Usage || !strings.Contains(r.errOut, "--unsafe") {
		t.Fatalf("unsafe from a file must be refused: %+v", r)
	}
	// Neither can the environment.
	r = runCLI(t, map[string]string{"CONTROLSNACK_SAFETY_UNSAFE": "1"}, "linux", svc, "run")
	if r.code != exitcode.Usage {
		t.Fatalf("unsafe from the environment must be refused: %+v", r)
	}
}

func TestValidationMessages(t *testing.T) {
	tests := []struct {
		name string
		host string
		env  map[string]string
		args []string
		want string
	}{
		{"memory", "linux", nil, []string{"--memory", "100"}, "--memory (vm.memory_mib)"},
		{"kvm on windows", "windows", nil, []string{"--accel", "kvm"}, "only available on linux"},
		{"whpx on linux", "linux", nil, []string{"--accel", "whpx"}, "only available on windows"},
		{"slow vs whpx", "windows", nil, []string{"--slow", "--accel", "whpx"}, "conflicts with --accel=whpx"},
		{"viewer none", "linux", nil, []string{"--viewer", "none"}, "needs --unsafe"},
		{"vm and keep-vm", "linux", nil, []string{"--vm", "lab", "--keep-vm=other"}, "use only one"},
		{"no-llm with task", "linux", nil, []string{"--no-llm", "--task", "x"}, "nothing to run"},
		{"allow-domain without allow-net", "linux", nil, []string{"--allow-domain", "example.com"}, "--allow-net"},
		{"base url without backend", "linux", nil, []string{"--base-url", "http://127.0.0.1:9"}, "explicit --llm"},
		{"remote http", "linux", nil, []string{"--llm", "openai", "--base-url", "http://api.example.com/v1"}, "https://"},
		{"openai without key", "linux", nil, []string{"--llm", "openai"}, "OPENAI_API_KEY"},
		{"bad log level", "linux", nil, []string{"--log-level", "loud"}, "log.level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{}
			r := runCLI(t, tt.env, tt.host, Services{Runner: fr}, append([]string{"run"}, tt.args...)...)
			if r.code != exitcode.Usage || !strings.Contains(r.errOut, tt.want) {
				t.Fatalf("want exit 2 mentioning %q, got %d:\n%s", tt.want, r.code, r.errOut)
			}
			if fr.called != 0 {
				t.Error("the session must not start with an invalid configuration")
			}
		})
	}
}

func TestAPIKeyCheckOnlyWhenAModelIsInvolved(t *testing.T) {
	fr := &fakeRunner{}
	svc := Services{Runner: fr}
	env := map[string]string{"OPENAI_API_KEY": "sk-test"}
	if r := runCLI(t, env, "linux", svc, "run", "--llm", "openai"); r.code != 0 {
		t.Fatalf("key present: %+v", r)
	}
	if r := runCLI(t, nil, "linux", svc, "run", "--llm", "openai", "--no-llm"); r.code != 0 {
		t.Fatalf("--no-llm needs no key: %+v", r)
	}
}

func TestKillAndVersionWorkWithABrokenConfig(t *testing.T) {
	// The emergency stop must not depend on a parseable config file.
	broken := writeFile(t, "vm: [this is: not valid\n")
	fk := &fakeKiller{}
	r := runCLI(t, nil, "linux", Services{Killer: fk}, "--config", broken, "kill")
	if r.code != 0 || fk.called != 1 {
		t.Fatalf("kill with a broken config: %+v called=%d", r, fk.called)
	}
	if r := runCLI(t, nil, "linux", Services{}, "--config", broken, "version"); r.code != 0 {
		t.Fatalf("version with a broken config: %+v", r)
	}
	if r := runCLI(t, nil, "linux", Services{}, "--config", broken, "--help"); r.code != 0 {
		t.Fatalf("--help with a broken config: %+v", r)
	}
	fr := &fakeRunner{}
	if r := runCLI(t, nil, "linux", Services{Runner: fr}, "--config", broken, "run"); r.code != exitcode.Usage || fr.called != 0 {
		t.Fatalf("run with a broken config must fail with a usage error: %+v", r)
	}
}

func TestRunnerErrorsMapToExitCodes(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("boom"), exitcode.Failure},
		{exitcode.New(exitcode.Environment, errors.New("qemu missing")), exitcode.Environment},
		{exitcode.New(exitcode.ImageMissing, errors.New("run image build")), exitcode.ImageMissing},
		{context.Canceled, exitcode.Killed},
		{fmt.Errorf("session: %w", context.DeadlineExceeded), exitcode.Timeout},
		{exitcode.Pending("P7-T06"), exitcode.NotImplemented},
	}
	for _, tt := range tests {
		fr := &fakeRunner{err: tt.err}
		r := runCLI(t, nil, "linux", Services{Runner: fr}, "run")
		if r.code != tt.want {
			t.Errorf("runner error %v -> exit %d, want %d", tt.err, r.code, tt.want)
		}
		if tt.err != nil && !strings.HasPrefix(r.errOut, "controlsnack: ") {
			t.Errorf("errors are reported as 'controlsnack: ...': %q", r.errOut)
		}
	}
}

func TestConfigShowRedactsSecrets(t *testing.T) {
	file := writeFile(t, "llm:\n  api_key: sk-very-secret\n  model: m1\n")
	r := runCLI(t, nil, "linux", Services{}, "--config", file, "config", "show")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.out, "sk-very-secret") || strings.Contains(r.errOut, "sk-very-secret") {
		t.Fatal("config show leaked the API key")
	}
	if !strings.Contains(r.out, config.RedactedMark) || !strings.Contains(r.out, "model: m1") || !strings.Contains(r.out, "# config file: "+file) {
		t.Errorf("unexpected output:\n%s", r.out)
	}
	r = runCLI(t, nil, "linux", Services{}, "--config", file, "config", "show", "--json")
	var c config.Config
	if r.code != 0 || json.Unmarshal([]byte(r.out), &c) != nil || c.LLM.APIKey != config.RedactedMark || c.LLM.Model != "m1" {
		t.Fatalf("config show --json: %+v", r)
	}
	if strings.Contains(r.out, "sk-very-secret") {
		t.Fatal("config show --json leaked the API key")
	}
}

func TestConfigShowWorksWithoutAFileAndReportsInvalidValuesAfterShowing(t *testing.T) {
	r := runCLI(t, nil, "linux", Services{}, "config", "show")
	if r.code != 0 || !strings.Contains(r.out, "config file: none") || !strings.Contains(r.out, "memory_mib: 2048") {
		t.Fatalf("%+v", r)
	}
	r = runCLI(t, nil, "linux", Services{}, "--config", writeFile(t, "vm:\n  memory_mib: 1\n"), "config", "show")
	if r.code != exitcode.Usage || !strings.Contains(r.out, "memory_mib: 1") || !strings.Contains(r.errOut, "vm.memory_mib") {
		t.Fatalf("an invalid value should be shown and then reported: %+v", r)
	}
}

func TestConfigPath(t *testing.T) {
	r := runCLI(t, nil, "linux", Services{}, "config", "path")
	if r.code != 0 || !strings.Contains(r.out, filepath.Join("controlsnack", "config.yaml")) || !strings.Contains(r.out, "does not exist yet") {
		t.Fatalf("%+v", r)
	}
	file := writeFile(t, "")
	r = runCLI(t, nil, "linux", Services{}, "--config", file, "config", "path")
	if r.code != 0 || !strings.Contains(r.out, file) || !strings.Contains(r.out, "(exists)") {
		t.Fatalf("%+v", r)
	}
}

type fakeAudit struct {
	pruned time.Duration
	file   string
}

func (f *fakeAudit) Verify(_ context.Context, _ io.Writer, file string) error {
	f.file = file
	return nil
}
func (f *fakeAudit) List(context.Context, io.Writer) error               { return nil }
func (f *fakeAudit) Show(context.Context, io.Writer, string, bool) error { return nil }
func (f *fakeAudit) Prune(_ context.Context, _ io.Writer, age time.Duration) error {
	f.pruned = age
	return nil
}

func TestAuditCommandsReachTheService(t *testing.T) {
	fa := &fakeAudit{}
	svc := Services{Audit: fa}
	if r := runCLI(t, nil, "linux", svc, "audit", "prune", "--older-than", "2w"); r.code != 0 || fa.pruned != 14*24*time.Hour {
		t.Fatalf("--older-than 2w: %+v pruned=%v", r, fa.pruned)
	}
	if r := runCLI(t, nil, "linux", svc, "audit", "prune"); r.code != 0 || fa.pruned != 30*24*time.Hour {
		t.Fatalf("the default is audit.retention_days (30): %+v pruned=%v", r, fa.pruned)
	}
	file := writeFile(t, "audit:\n  retention_days: 7\n")
	if r := runCLI(t, nil, "linux", svc, "--config", file, "audit", "prune"); r.code != 0 || fa.pruned != 7*24*time.Hour {
		t.Fatalf("a configured retention applies: %+v pruned=%v", r, fa.pruned)
	}
	if r := runCLI(t, nil, "linux", svc, "audit", "verify", "x.jsonl"); r.code != 0 || fa.file != "x.jsonl" {
		t.Fatalf("verify: %+v file=%q", r, fa.file)
	}
}

func TestParseAge(t *testing.T) {
	good := map[string]time.Duration{
		"30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "36h": 36 * time.Hour, "90m": 90 * time.Minute, " 7d ": 7 * 24 * time.Hour,
	}
	for in, want := range good {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "0d", "-3d", "1.5d", "soon", "10", "5y"} {
		if _, err := parseAge(in); err == nil {
			t.Errorf("parseAge(%q) should fail", in)
		}
	}
}

func TestLoggerFormatsAndLevels(t *testing.T) {
	var buf bytes.Buffer
	log, err := newLogger(&buf, "warn", "json")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hidden")
	log.Warn("shown")
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil || line["msg"] != "shown" || line["level"] != "warn" {
		t.Fatalf("json log line: %q err=%v", buf.String(), err)
	}
	buf.Reset()
	log, _ = newLogger(&buf, "debug", "text")
	log.Debug("hello")
	if !strings.Contains(buf.String(), "DEBUG") || !strings.Contains(buf.String(), "hello") {
		t.Fatalf("text log line: %q", buf.String())
	}
	if _, err := newLogger(&buf, "loud", "text"); err == nil {
		t.Error("unknown level must be rejected")
	}
	if _, err := newLogger(&buf, "info", "xml"); err == nil {
		t.Error("unknown format must be rejected")
	}
}

func TestWarningsAreLoggedInTheChosenFormat(t *testing.T) {
	file := writeFile(t, "net:\n  allow_domains: [example.com]\n")
	r := runCLI(t, nil, "linux", Services{Runner: &fakeRunner{}}, "--config", file, "--log-format", "json", "run")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.errOut)), &line); err != nil || line["level"] != "warn" {
		t.Fatalf("stderr should hold one JSON warning, got %q (%v)", r.errOut, err)
	}
	if strings.Contains(r.out, "allow_domains") {
		t.Error("diagnostics belong on stderr, not stdout")
	}
}

func TestSignalForwarding(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var forced atomic.Bool
	done := make(chan struct{})
	go func() {
		forwardSignals(sigs, cancel, func() { forced.Store(true) })
		close(done)
	}()

	sigs <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the first signal must cancel the context")
	}
	if forced.Load() {
		t.Fatal("the first signal must not force an exit")
	}
	sigs <- os.Interrupt
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the second signal must force an exit")
	}
	if !forced.Load() {
		t.Fatal("force was not called")
	}
}

func TestDefaultServicesAreAllPresent(t *testing.T) {
	s := DefaultServices()
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("DefaultServices leaves %s nil", v.Type().Field(i).Name)
		}
	}
}

type fakeDoctor struct {
	called int
	json   bool
	cfg    config.Config
	err    error
}

func (f *fakeDoctor) Run(_ context.Context, w io.Writer, cfg config.Config, asJSON bool) error {
	f.called++
	f.json, f.cfg = asJSON, cfg
	_, _ = io.WriteString(w, "will use tcg (slow)\n")
	return f.err
}

func TestDoctorCommandReachesTheServiceWithTheVMSettings(t *testing.T) {
	fd := &fakeDoctor{}
	r := runCLI(t, nil, "linux", Services{Doctor: fd}, "doctor", "--json", "--accel", "tcg", "--qemu", "/opt/qemu/bin")
	if r.code != 0 || fd.called != 1 || !fd.json || !strings.Contains(r.out, "will use") {
		t.Fatalf("%+v %+v", r, fd)
	}
	if fd.cfg.VM.Accel != "tcg" || fd.cfg.VM.QemuDir != "/opt/qemu/bin" {
		t.Errorf("doctor sees the same settings run would: %+v", fd.cfg.VM)
	}

	fd = &fakeDoctor{}
	if r := runCLI(t, nil, "linux", Services{Doctor: fd}, "doctor", "--slow"); r.code != 0 || fd.json || !fd.cfg.VM.Slow {
		t.Errorf("%+v %+v", r, fd.cfg.VM)
	}

	// No VM can run: exit code 3 comes through unchanged.
	fd = &fakeDoctor{err: exitcode.New(exitcode.Environment, errors.New("no VM can run"))}
	if r := runCLI(t, nil, "linux", Services{Doctor: fd}, "doctor"); r.code != exitcode.Environment {
		t.Errorf("%+v", r)
	}
}

type fakeImages struct{ calls []string }

func (f *fakeImages) rec(s string) error { f.calls = append(f.calls, s); return nil }
func (f *fakeImages) Build(_ context.Context, _ io.Writer, _ config.Config, o image.BuildOptions) error {
	return f.rec(fmt.Sprintf("build yes=%v force=%v", o.Yes, o.Force))
}
func (f *fakeImages) List(context.Context, io.Writer) error { return f.rec("ls") }
func (f *fakeImages) Remove(_ context.Context, _ io.Writer, id string) error {
	return f.rec("rm " + id)
}
func (f *fakeImages) Verify(_ context.Context, _ io.Writer, id string) error {
	return f.rec("verify " + id)
}
func (f *fakeImages) Import(_ context.Context, _ io.Writer, q, m string) error {
	return f.rec("import " + q + " " + m)
}
func (f *fakeImages) Path(_ context.Context, _ io.Writer, id string) error {
	return f.rec("path " + id)
}

func TestImageCommandsReachTheManager(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"image", "ls"}, "ls"},
		{[]string{"image", "rm", "abcd1234"}, "rm abcd1234"},
		{[]string{"image", "verify"}, "verify "},
		{[]string{"image", "verify", "abcd"}, "verify abcd"},
		{[]string{"image", "path"}, "path "},
		{[]string{"image", "path", "abcd"}, "path abcd"},
		{[]string{"image", "import", "x.qcow2", "--manifest", "m.json"}, "import x.qcow2 m.json"},
		{[]string{"image", "build", "--yes", "--force"}, "build yes=true force=true"},
	} {
		f := &fakeImages{}
		if r := runCLI(t, nil, "linux", Services{Images: f}, tt.args...); r.code != 0 || len(f.calls) != 1 || f.calls[0] != tt.want {
			t.Errorf("%v: code %d calls %v (%s)", tt.args, r.code, f.calls, r.errOut)
		}
	}
}
