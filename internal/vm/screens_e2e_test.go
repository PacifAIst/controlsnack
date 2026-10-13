// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentbin"
	"github.com/PacifAIst/controlsnack-local/internal/vm/image"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qmp"
)

// bootE2E boots the real image at the given resolution, or skips the test when something needed is missing.
func bootE2E(t *testing.T, width, height int) (*Machine, string) {
	t.Helper()
	return bootE2EWith(t, width, height, nil)
}

// bootE2EWith is bootE2E with a change to the options the VM is prepared with (--allow-net, for instance).
func bootE2EWith(t *testing.T, width, height int, change func(*PrepareOptions)) (*Machine, string) {
	t.Helper()
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
	dir := t.TempDir()
	opts := PrepareOptions{Install: inst, Base: im.Path, Name: "e2e", Dir: dir, CPUs: 2, MemoryMiB: 2048, Width: width, Height: height}
	if change != nil {
		change(&opts)
	}
	prepare, err := NewPrepare(opts)
	if err != nil {
		t.Fatal(err)
	}
	probe := NewHostProbe(qemu.OSHost{}, inst)
	begin := time.Now()
	m, err := Boot(ctx, BootOptions{
		Prepare: prepare, Backend: RealBackend(), Agent: agent, HostVersion: "e2e", SessionID: "s_e2e",
		Resolve: func(ctx context.Context) (AccelResult, error) { return ResolveAccel(ctx, probe, "auto", false) },
		Progress: func(format string, args ...any) {
			t.Logf("%6s  "+format, append([]any{time.Since(begin).Round(time.Second)}, args...)...)
		},
	})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	t.Cleanup(func() { _ = m.Kill() })
	return m, dir
}

// keys maps the characters a test types to a QEMU key code and whether shift is held.
var keys = func() map[rune]struct {
	code  string
	shift bool
} {
	k := map[rune]struct {
		code  string
		shift bool
	}{}
	for c := 'a'; c <= 'z'; c++ {
		k[c] = struct {
			code  string
			shift bool
		}{string(c), false}
		k[c-'a'+'A'] = struct {
			code  string
			shift bool
		}{string(c), true}
	}
	for c := '0'; c <= '9'; c++ {
		k[c] = struct {
			code  string
			shift bool
		}{string(c), false}
	}
	plain := map[rune]string{' ': "spc", '\n': "ret", '-': "minus", '=': "equal", '.': "dot", ',': "comma", '/': "slash", '\\': "backslash",
		';': "semicolon", '\'': "apostrophe", '[': "bracket_left", ']': "bracket_right", '`': "grave_accent"}
	for r, code := range plain {
		k[r] = struct {
			code  string
			shift bool
		}{code, false}
	}
	shifted := map[rune]string{'|': "backslash", '>': "dot", '<': "comma", '&': "7", '_': "minus", ':': "semicolon", '"': "apostrophe",
		'*': "8", '(': "9", ')': "0", '~': "grave_accent", '$': "4", '#': "3", '%': "5", '?': "slash", '+': "equal", '!': "1", '@': "2"}
	for r, code := range shifted {
		k[r] = struct {
			code  string
			shift bool
		}{code, true}
	}
	return k
}()

// typeText types s into the guest through QMP key events.
func typeText(t *testing.T, m *Machine, s string) {
	t.Helper()
	ctx := context.Background()
	for _, r := range s {
		k, ok := keys[r]
		if !ok {
			t.Fatalf("no key for %q", r)
		}
		var evs []qmp.InputEvent
		add := func(code string, down bool) {
			e, err := qmp.Key(code, down)
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, e)
		}
		if k.shift {
			add("shift", true)
		}
		add(k.code, true)
		add(k.code, false)
		if k.shift {
			add("shift", false)
		}
		if err := m.QMP.InputSendEvent(ctx, evs); err != nil {
			t.Fatalf("sending %q: %v", r, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func chord(t *testing.T, m *Machine, codes ...string) {
	t.Helper()
	var evs []qmp.InputEvent
	for _, c := range codes {
		e, _ := qmp.Key(c, true)
		evs = append(evs, e)
	}
	for i := len(codes) - 1; i >= 0; i-- {
		e, _ := qmp.Key(codes[i], false)
		evs = append(evs, e)
	}
	if err := m.QMP.InputSendEvent(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// TestE2ECollectScreens saves a few real screenshots of the guest, for benchmarks and for the image tests of the agent:
// CONTROLSNACK_SHOTS_DIR is where they go (the test skips without it), CONTROLSNACK_SHOTS_RES the guest resolution.
func TestE2ECollectScreens(t *testing.T) {
	out := os.Getenv("CONTROLSNACK_SHOTS_DIR")
	if out == "" {
		t.Skip("set CONTROLSNACK_SHOTS_DIR to collect screenshots")
	}
	res := os.Getenv("CONTROLSNACK_SHOTS_RES")
	if res == "" {
		res = "1280x800"
	}
	w, h, err := ParseResolution(res)
	if err != nil {
		t.Fatal(err)
	}
	m, dir := bootE2E(t, w, h)
	ctx := context.Background()
	shot := func(name string, settle time.Duration) {
		t.Helper()
		time.Sleep(settle)
		p := filepath.Join(dir, name+".png")
		if err := m.Screendump(ctx, p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(out, name+"_"+res+".png")
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", dest, len(data))
	}

	shot("desktop", 25*time.Second) // the panel and the icons are drawn by now

	chord(t, m, "ctrl", "alt", "t") // XFCE's shortcut for a terminal
	time.Sleep(25 * time.Second)
	typeText(t, m, strings.Join([]string{
		"cat /etc/os-release",
		"ls -la /usr/bin | head -40",
		"ls /usr/share/doc | head -60 | column -c 120",
	}, "\n")+"\n")
	shot("terminal", 15*time.Second)

	typeText(t, m, "thunar /usr/share/doc &\n")
	shot("files", 45*time.Second)

	typeText(t, m, "mousepad /etc/services &\n")
	shot("editor", 45*time.Second)
}
