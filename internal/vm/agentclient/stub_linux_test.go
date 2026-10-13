// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package agentclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	guestrecipe "github.com/PacifAIst/controlsnack-local/guest"
	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
)

// These tests run the real guest stub (guest/files/bootstrap.sh) and a really compiled controlsnack-agent against the
// real client, with a socket pair standing in for the virtio-serial port.

var (
	agentOnce sync.Once
	agentBin  agentbin.Agent
	agentErr  error
)

func builtAgent(t *testing.T) agentbin.Agent {
	t.Helper()
	agentOnce.Do(func() {
		arch := map[string]string{"amd64": protocol.ArchX86_64, "arm64": protocol.ArchAarch64}[runtime.GOARCH]
		if arch == "" {
			agentErr = errors.New("no guest agent for " + runtime.GOARCH)
			return
		}
		dir, err := os.MkdirTemp("", "cs-agent-")
		if err != nil {
			agentErr = err
			return
		}
		out := filepath.Join(dir, "agent")
		cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", out, "github.com/PacifAIst/controlsnack-local/cmd/controlsnack-agent")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if msg, err := cmd.CombinedOutput(); err != nil {
			agentErr = errors.New("building the agent: " + err.Error() + "\n" + string(msg))
			return
		}
		bin, err := os.ReadFile(out)
		if err != nil {
			agentErr = err
			return
		}
		sum := sha256.Sum256(bin)
		agentBin = agentbin.Agent{Arch: arch, Binary: bin, SHA256: hex.EncodeToString(sum[:])}
	})
	if agentErr != nil {
		t.Skipf("cannot build the agent here: %v", agentErr)
	}
	return agentBin
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stub is a running bootstrap.sh with the guest end of a socket pair on its file descriptor 3.
type stub struct {
	host net.Conn
	logs *syncBuf
	run  string // its XDG_RUNTIME_DIR
}

func startStub(t *testing.T) *stub {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	var script []byte
	for _, f := range guestrecipe.Files() {
		if f.Path == "/usr/local/lib/controlsnack/bootstrap.sh" {
			script = f.Content
		}
	}
	if script == nil {
		t.Fatal("the recipe has no bootstrap.sh")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil { //nolint:gosec // a test script in a temp dir
		t.Fatal(err)
	}
	run := filepath.Join(dir, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no socket pair: %v", err)
	}
	hostFile, guestFile := os.NewFile(uintptr(fds[0]), "host"), os.NewFile(uintptr(fds[1]), "guest")
	host, err := net.FileConn(hostFile)
	_ = hostFile.Close()
	if err != nil {
		t.Fatal(err)
	}

	logs := &syncBuf{}
	cmd := exec.CommandContext(t.Context(), bash, path) //nolint:gosec // the script written above
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "XDG_RUNTIME_DIR=" + run, "CS_PORT=-", "CS_READ_TIMEOUT=20"}
	cmd.ExtraFiles = []*os.File{guestFile} // becomes file descriptor 3
	cmd.Stderr, cmd.Stdout = logs, logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = guestFile.Close()
	t.Cleanup(func() {
		_ = host.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the stub and the agent it started
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("stub output:\n%s", logs.String())
		}
	})
	return &stub{host: host, logs: logs, run: run}
}

func (s *stub) options(agent agentbin.Agent) Options {
	u, _ := user.Current()
	used := false
	return Options{
		Dial: func(context.Context) (net.Conn, error) {
			if used {
				return nil, errors.New("this port has one connection")
			}
			used = true
			return s.host, nil
		},
		Agent: agent, HostVersion: "test", SessionID: "s_test", User: u.Username, Required: []string{protocol.CapPing},
		Interval: 200 * time.Millisecond, InstallTimeout: 30 * time.Second, HelloTimeout: 30 * time.Second,
	}
}

func TestRealStubInstallsAndStartsTheRealAgent(t *testing.T) {
	agent := builtAgent(t)
	s := startStub(t)
	c, err := New(s.options(agent))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v\nstub output:\n%s", err, s.logs.String())
	}
	ack, ok := c.Ack()
	if !ok || ack.AgentSHA256 != agent.SHA256 || ack.Arch != agent.Arch || ack.Display.W != 1280 || len(ack.Capabilities) == 0 {
		t.Errorf("%v %+v", ok, ack)
	}
	if _, err := c.Call(ctx, protocol.TypePing, nil, nil, nil); err != nil {
		t.Errorf("ping: %v", err)
	}
	_, err = c.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: "id"}, nil, nil)
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != protocol.CodeUnknownType {
		t.Errorf("the minimal agent serves nothing else yet: %v", err)
	}

	// Where the stub put it, and who may touch it.
	dir := filepath.Join(s.run, "controlsnack")
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("%v %v", st, err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "agent"))
	if err != nil || !bytes.Equal(stored, agent.Binary) {
		t.Errorf("the stored agent differs from the pushed one: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "agent")); st == nil || st.Mode().Perm() != 0o700 {
		t.Errorf("agent mode: %v", st)
	}

	// The host goes away: the agent ends and the stub waits for the next push.
	_ = c.Close()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(s.logs.String(), "waiting for the next push") {
		if time.Now().After(deadline) {
			t.Fatalf("the stub did not take the agent's end as its cue\n%s", s.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRealStubRefusals(t *testing.T) {
	agent := builtAgent(t)
	other := protocol.ArchAarch64
	if agent.Arch == other {
		other = protocol.ArchX86_64
	}
	tests := []struct {
		name   string
		mutate func(a *agentbin.Agent)
		reason string
	}{
		{"another architecture", func(a *agentbin.Agent) { a.Arch = other }, "arch-mismatch-" + agent.Arch},
		{"bytes that do not match the hash", func(a *agentbin.Agent) { a.SHA256 = strings.Repeat("ab", 32) }, "sha256-mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startStub(t)
			a := agent
			tt.mutate(&a)
			c, err := New(s.options(a))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var ie *InstallError
			if err := c.Connect(ctx); !errors.As(err, &ie) || ie.Reason != tt.reason {
				t.Errorf("%v (want %q)", err, tt.reason)
			}
			if _, err := os.Stat(filepath.Join(s.run, "controlsnack", "agent")); err == nil {
				t.Error("a refused agent must not be left in place")
			}
		})
	}

	t.Run("a line that is not a push", func(t *testing.T) {
		s := startStub(t)
		_ = s.host.SetDeadline(time.Now().Add(30 * time.Second))
		if _, err := io.WriteString(s.host, "GET / HTTP/1.1\n"); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(s.host).ReadString('\n')
		if err != nil || line != "CSBOOT/1 ERR bad-line\n" {
			t.Errorf("%q %v", line, err)
		}
	})
}
