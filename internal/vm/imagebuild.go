// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/buildinfo"
	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
)

// BootCheckTimeout is how long a freshly built image has to open the agent port: the whole boot, the desktop
// session start and the bootstrap stub (PLAN.md section 7.5, step 6); times four under tcg.
const BootCheckTimeout = 180 * time.Second

// NewImageManager returns the image manager with everything `image build` takes from this package: the accelerator
// resolution `run` uses, and the boot check.
func NewImageManager() image.Manager {
	return &image.FileManager{
		Version: buildinfo.Get().Version,
		Accel: func(ctx context.Context, cfg config.Config, inst qemu.Install) (string, error) {
			res, err := ResolveAccel(ctx, NewHostProbe(qemu.OSHost{}, inst), cfg.VM.Accel, cfg.VM.Slow)
			if err != nil {
				return "", err
			}
			return res.Chosen, nil
		},
		BootCheck: VerifyBootable,
	}
}

// VerifyBootable boots disk with the runtime profile (no network device) on a throwaway overlay and waits until the
// guest has opened the agent port, which means the kernel, the desktop session and the bootstrap stub all came up.
// The overlay and everything else is removed afterwards; the disk is not touched.
func VerifyBootable(ctx context.Context, inst qemu.Install, accel, disk string, out io.Writer) error {
	absDisk, err := filepath.Abs(disk)
	if err != nil {
		return err
	}
	session, err := os.MkdirTemp("", "css-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(session) }()
	prepare, err := NewPrepare(PrepareOptions{Install: inst, Base: absDisk, Name: "verify", Dir: session,
		CPUs: 2, MemoryMiB: 2048, Width: 1280, Height: 800})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "      booting the image with %s, no network\n", accel)
	begin := time.Now()
	v, err := Launch(ctx, LaunchOptions{
		Resolve: func(context.Context) (AccelResult, error) {
			return AccelResult{Requested: accel, Chosen: accel, Slow: accel == AccelTCG, Tried: []AccelTry{{Accel: accel, Usable: true}}}, nil
		},
		Prepare: prepare, Backend: RealBackend(), Stable: 3 * time.Second,
	})
	if err != nil {
		return err
	}
	defer func() { _ = v.Kill() }()
	if v.Fallback != nil {
		fmt.Fprintf(out, "      %s did not start, so the check ran with software emulation: %s\n", v.Fallback.From, v.Fallback.Reason)
	}

	wait := BootCheckTimeout * time.Duration(v.Accel.TimeoutFactor())
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := v.QMP.WaitPortOpen(wctx, "agent0"); err != nil {
		keepEvidence(ctx, v, filepath.Dir(absDisk))
		hint := tailFile(filepath.Join(filepath.Dir(v.Spec.Serial), "serial.log"), 12)
		return fmt.Errorf("the guest did not open the agent port within %s: %w\nlast lines of the serial console:\n%s", wait, err, hint)
	}
	fmt.Fprintf(out, "      the guest opened the agent port after %s\n", time.Since(begin).Round(time.Second))
	return nil
}

// tailFile returns the last n lines of a text file, indented, for error messages.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path) //nolint:gosec // a session file
	if err != nil || len(data) == 0 {
		return "  (no console output)"
	}
	if len(data) > 64<<10 {
		data = data[len(data)-64<<10:]
	}
	lines := splitLines(string(data))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := ""
	for _, l := range lines {
		out += "  | " + l + "\n"
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, trimCR(s[start:i]))
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, trimCR(s[start:]))
	}
	return lines
}

func trimCR(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\r' {
		s = s[:len(s)-1]
	}
	return s
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // a firmware file found by discovery
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600) //nolint:gosec // dst is in a session directory this package just created
}

// keepEvidence saves what the stuck guest shows on its screen and the whole serial console, best effort: whatever
// cannot be saved is simply missing.
func keepEvidence(ctx context.Context, v *VM, dir string) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = v.QMP.Screendump(sctx, filepath.Join(dir, image.BootCheckScreenshot), "png")
	if data, err := os.ReadFile(v.Spec.Serial); err == nil { //nolint:gosec // the verification VM's own console file
		_ = os.WriteFile(filepath.Join(dir, image.BootCheckConsole), data, 0o600) //nolint:gosec // inside the build directory
	}
}
