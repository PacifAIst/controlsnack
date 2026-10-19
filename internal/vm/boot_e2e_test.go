// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/platform"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/vm/agentbin"
	"github.com/PacifAIst/controlsnack/internal/vm/image"
	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// TestE2EBoot boots the real image with the real QEMU (P3-T11): the accelerator `run` detects, and software emulation
// when that is not what was detected. It needs a built image (`controlsnack image build`), QEMU, and the embedded
// agent (`go run ./tools/dev agent`); without any of them it skips. It records the boot times in the test log.
func TestE2EBoot(t *testing.T) {
	ctx := context.Background()
	inst, err := qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{Dir: os.Getenv("CONTROLSNACK_QEMU")})
	if err != nil {
		t.Skipf("no usable QEMU: %v", err)
	}
	agent, err := agentbin.For(inst.Arch)
	if err != nil {
		t.Skipf("%v", err)
	}
	dirs, err := platform.DefaultDirs()
	if err != nil {
		t.Skip(err)
	}
	im, err := image.Store{Dir: dirs.ImagesDir()}.Resolve(os.Getenv("CONTROLSNACK_E2E_IMAGE"), inst.Arch)
	if err != nil {
		t.Skipf("no base image: %v", err)
	}

	probe := NewHostProbe(qemu.OSHost{}, inst)
	detected, err := ResolveAccel(ctx, probe, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("detected accelerator: %s", detected.Chosen)
	runs := []struct {
		name string
		slow bool
	}{{"detected", false}}
	if detected.Chosen != AccelTCG {
		runs = append(runs, struct {
			name string
			slow bool
		}{"slow", true})
	} else if forced, ferr := ResolveAccel(ctx, probe, "auto", true); ferr != nil || forced.Chosen != AccelTCG {
		t.Fatalf("--slow must give tcg: %+v %v", forced, ferr)
	}

	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			dir := t.TempDir()
			prepare, err := NewPrepare(PrepareOptions{Install: inst, Base: im.Path, Name: "e2e", Dir: dir,
				CPUs: 2, MemoryMiB: 2048, Width: 1280, Height: 800})
			if err != nil {
				t.Fatal(err)
			}
			begin := time.Now()
			m, err := Boot(ctx, BootOptions{
				Prepare: prepare, Backend: RealBackend(), Agent: agent, HostVersion: "e2e", SessionID: "s_e2e",
				Resolve: func(ctx context.Context) (AccelResult, error) { return ResolveAccel(ctx, probe, "auto", run.slow) },
				Progress: func(format string, args ...any) {
					t.Logf("%6s  "+format, append([]any{time.Since(begin).Round(time.Second)}, args...)...)
				},
			})
			if err != nil {
				t.Fatalf("Boot: %v", err)
			}
			killed := false
			defer func() {
				if !killed {
					_ = m.Kill()
				}
			}()

			rep := m.Report()
			t.Logf("accelerator %s: QEMU running %s, desktop up %s, agent connected %s", rep.Accel.Chosen,
				rep.Timings.Launch.Round(time.Millisecond), rep.Timings.PortOpen.Round(time.Second), rep.Timings.Connected.Round(time.Second))
			if run.slow && rep.Accel.Chosen != AccelTCG {
				t.Errorf("--slow ran with %s", rep.Accel.Chosen)
			}
			if !run.slow && rep.Accel.Chosen != detected.Chosen {
				t.Errorf("the detected accelerator was %s but the run used %s", detected.Chosen, rep.Accel.Chosen)
			}
			if rep.Agent.User != "cs" || rep.Agent.Arch != inst.Arch || rep.Agent.AgentSHA256 != agent.SHA256 {
				t.Errorf("%+v", rep.Agent)
			}
			if _, err := m.Client.Call(ctx, protocol.TypePing, nil, nil, nil); err != nil {
				t.Errorf("ping: %v", err)
			}

			// QMP screendump: a real PNG of the guest's screen. Right after the handshake the desktop is still drawing
			// itself (the stub starts early in the session), so wait until the picture holds more than a blank screen.
			shot := filepath.Join(dir, "screen.png")
			drawn := time.Now()
			var size int64
			for {
				if err := m.Screendump(ctx, shot); err != nil {
					t.Fatalf("screendump: %v", err)
				}
				if fi, err := os.Stat(shot); err == nil {
					size = fi.Size()
				}
				if size > 20<<10 || time.Since(drawn) > 5*time.Minute {
					break
				}
				time.Sleep(5 * time.Second)
			}
			t.Logf("screendump: %d bytes, the desktop was drawn %s after the handshake", size, time.Since(drawn).Round(time.Second))
			if size <= 20<<10 {
				t.Errorf("the screen stayed blank (%d bytes of PNG)", size)
			}
			if keep := os.Getenv("CONTROLSNACK_E2E_SHOT"); keep != "" {
				if data, err := os.ReadFile(shot); err == nil {
					_ = os.WriteFile(keep, data, 0o600)
				}
			}

			// The doctor reaches the same verdict about the accelerator.
			cfg := config.Defaults()
			cfg.VM.Slow = run.slow
			doc, err := (&DoctorService{Host: qemu.OSHost{}}).Build(ctx, cfg)
			if err != nil {
				t.Fatalf("doctor: %v", err)
			}
			if doc.Accel.Chosen != rep.Accel.Chosen {
				t.Errorf("doctor says %s, run used %s", doc.Accel.Chosen, rep.Accel.Chosen)
			}

			// The kill path.
			start := time.Now()
			if err := m.Kill(); err != nil {
				t.Errorf("kill: %v", err)
			}
			killed = true
			took := time.Since(start)
			t.Logf("kill: QEMU gone after %s", took.Round(time.Millisecond))
			if took > 500*time.Millisecond {
				t.Errorf("the kill path took %s, the target is 500 ms", took)
			}
		})
	}
}
