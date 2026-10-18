// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/llm/fakellm"
)

// The end-to-end tests of the program as a person runs it: the binary is built, then started as a process with flags, an
// environment and an exit code, against the fake model. They check what the session tests cannot: the command line, the
// configuration rules (they skip the validation), the control endpoint across two processes, and the exit codes.
//
//	MSYS_NO_PATHCONV=1 wsl -e bash scripts/wsl-go.sh env CGO_ENABLED=0 go test -tags e2e -v -timeout 20m ./cmd/controlsnack/
//
// A test that boots a VM skips without QEMU and a built image.

// program is the built binary and the environment it runs in: its own config and state, the real image cache.
type program struct {
	t   *testing.T
	bin string
	env []string
}

func build(t *testing.T) *program {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	name := "controlsnack"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/controlsnack")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the program: %v\n%s", err, out)
	}
	cfg, state := t.TempDir(), t.TempDir()
	env := append(os.Environ(), "XDG_CONFIG_HOME="+cfg, "XDG_STATE_HOME="+state, "APPDATA="+cfg, "LOCALAPPDATA="+state)
	return &program{t: t, bin: bin, env: env}
}

// result is how a run of the program ended.
type result struct {
	out  string
	code int
}

func (p *program) start(ctx context.Context, args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.CommandContext(ctx, p.bin, args...)
	cmd.Env = p.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	return cmd, &out
}

func (p *program) run(timeout time.Duration, args ...string) result {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd, out := p.start(ctx, args...)
	err := cmd.Run()
	return result{out: out.String(), code: exitOf(p.t, err, ctx)}
}

func exitOf(t *testing.T, err error, ctx context.Context) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if ctx.Err() != nil {
		t.Fatalf("the program did not end in time")
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("starting the program: %v", err)
	return -1
}

// needsVM skips a test that boots a guest when there is no QEMU or no image to boot.
func (p *program) needsVM() {
	p.t.Helper()
	r := p.run(time.Minute, "doctor", "--json")
	if r.code != 0 {
		p.t.Skipf("no VM can run here (doctor exits %d)", r.code)
	}
	if im := p.run(time.Minute, "image", "path"); im.code != 0 {
		p.t.Skipf("no base image: %s", strings.TrimSpace(im.out))
	}
}

func modelArgs(fake *fakellm.Server) []string {
	return []string{"--llm", "lmstudio", "--base-url", fake.URL(), "--model", fake.Model()}
}

func TestBinaryRefusesWhatTheConfigurationForbidsBeforeAnythingStarts(t *testing.T) {
	p := build(t)
	fake := fakellm.Start(t, fakellm.Options{})
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"a model with nobody watching needs --unsafe", append([]string{"run", "--viewer", "none", "--task", "x"}, modelArgs(fake)...), "--unsafe"},
		{"no chat, so a task is needed", append([]string{"run", "--viewer", "none", "--unsafe"}, modelArgs(fake)...), "--task"},
		{"a remote model over plain http", []string{"run", "--llm", "openai", "--base-url", "http://192.0.2.7:1234/v1", "--task", "x"}, "https"},
		{"a policy file that is not there", append([]string{"run", "--policy", "/no/such/rules.yaml", "--viewer", "none", "--unsafe", "--task", "x"}, modelArgs(fake)...), "policy"},
		{"software emulation and a hardware accelerator at once", []string{"run", "--slow", "--accel", "kvm", "--task", "x"}, "slow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := p.run(time.Minute, tt.args...)
			if r.code != 2 || !strings.Contains(strings.ToLower(r.out), strings.ToLower(tt.want)) {
				t.Errorf("exit %d, want 2 and a word about %q:\n%s", r.code, tt.want, r.out)
			}
		})
	}
}

func TestBinaryAsksTheModelServerWhatItOffersAndWhatItCanDo(t *testing.T) {
	p := build(t)
	fake := fakellm.Start(t, fakellm.Options{})
	args := []string{"--llm", "lmstudio", "--base-url", fake.URL()}
	if r := p.run(time.Minute, append([]string{"llm", "models"}, args...)...); r.code != 0 || !strings.Contains(r.out, fake.Model()) {
		t.Errorf("llm models: exit %d\n%s", r.code, r.out)
	}
	r := p.run(2*time.Minute, append([]string{"llm", "probe", "--model", fake.Model()}, args...)...)
	if r.code != 0 || !strings.Contains(r.out, "Model") {
		t.Errorf("llm probe: exit %d\n%s", r.code, r.out)
	}
	t.Logf("llm probe:\n%s", r.out)
}

func TestBinaryDoctorGivesAVerdictAsJSON(t *testing.T) {
	p := build(t)
	r := p.run(time.Minute, "doctor", "--json")
	if r.code != 0 && r.code != 3 {
		t.Fatalf("doctor exits %d\n%s", r.code, r.out)
	}
	if !json.Valid([]byte(r.out)) && !strings.Contains(r.out, "{") {
		t.Fatalf("not JSON:\n%s", r.out)
	}
	if !strings.Contains(r.out, "verdict") {
		t.Errorf("no verdict:\n%s", r.out)
	}
	if v := p.run(time.Minute, "version"); v.code != 0 || !strings.Contains(v.out, "controlsnack") {
		t.Errorf("version: exit %d\n%s", v.code, v.out)
	}
	if k := p.run(time.Minute, "kill"); k.code != 0 {
		t.Errorf("kill with nothing running: exit %d\n%s", k.code, k.out)
	}
}

func TestBinaryRunsATaskWithNobodyWatchingAndLeavesALogThatVerifies(t *testing.T) {
	p := build(t)
	p.needsVM()
	step := 0
	fake := fakellm.Start(t, fakellm.Options{Then: func(r *fakellm.Request) fakellm.Reply {
		defer func() { step++ }()
		if step == 0 {
			return fakellm.Reply{Calls: []fakellm.Call{fakellm.Tool("exec", map[string]any{"command": "uname -r", "timeout_s": 30})}}
		}
		return fakellm.Reply{Calls: []fakellm.Call{fakellm.Tool("done", map[string]any{"success": true, "summary": "the kernel was printed"})}}
	}})
	args := append([]string{"run", "--viewer", "none", "--unsafe", "--task", "Print the kernel version"}, modelArgs(fake)...)
	r := p.run(10*time.Minute, args...)
	t.Logf("run:\n%s", r.out)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	for _, want := range []string{"Task finished", "the kernel was printed", "ended (task done)", "Audit log:"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("the output lacks %q", want)
		}
	}
	ls := p.run(time.Minute, "audit", "ls")
	if ls.code != 0 || !strings.Contains(ls.out, "complete") {
		t.Fatalf("audit ls: exit %d\n%s", ls.code, ls.out)
	}
	var id string
	for _, f := range strings.Fields(ls.out) {
		if strings.HasPrefix(f, "s_") {
			id = f
		}
	}
	if v := p.run(time.Minute, "audit", "verify", id); v.code != 0 || !strings.Contains(v.out, "ok") {
		t.Errorf("audit verify %q: exit %d\n%s", id, v.code, v.out)
	}
	if show := p.run(time.Minute, "audit", "show", id); show.code != 0 || !strings.Contains(show.out, "exec") {
		t.Errorf("audit show: exit %d\n%s", show.code, show.out)
	}
}

func TestBinaryIsKilledFromAnotherTerminalWhateverTheModelIsDoing(t *testing.T) {
	p := build(t)
	p.needsVM()
	var asked atomic.Bool
	fake := fakellm.Start(t, fakellm.Options{Then: func(*fakellm.Request) fakellm.Reply {
		asked.Store(true)
		return fakellm.Reply{Hang: true}
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	args := append([]string{"run", "--viewer", "none", "--unsafe", "--task", "Wait for a model that never answers"}, modelArgs(fake)...)
	cmd, out := p.start(ctx, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.After(8 * time.Minute)
	for !asked.Load() {
		select {
		case err := <-done:
			t.Fatalf("the program ended before the model was asked: %v\n%s", err, out.String())
		case <-deadline:
			t.Fatalf("the model was never asked:\n%s", out.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	began := time.Now()
	k := p.run(time.Minute, "kill")
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the session did not end after kill:\n%s", out.String())
	}
	took := time.Since(began)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 5 {
		t.Errorf("the session ended with %v (want exit 5)\n%s", err, out.String())
	}
	t.Logf("kill: exit %d; the session was gone after %s\n%s", k.code, took.Round(time.Millisecond), out.String())
	if k.code != 0 {
		t.Errorf("kill exits %d:\n%s", k.code, k.out)
	}
	if took > 5*time.Second {
		t.Errorf("kill took %s", took)
	}
	if runtime.GOOS != "windows" {
		if pg, _ := exec.Command("pgrep", "-f", "-c", "controlsnack-s-").Output(); strings.TrimSpace(string(pg)) != "0" && len(pg) > 0 {
			t.Errorf("QEMU is still running: %s", pg)
		}
	}
}
