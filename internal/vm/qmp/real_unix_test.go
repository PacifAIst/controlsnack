// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package qmp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
)

// TestRealQEMU talks to the real program over a unix socket, where there is one: the greeting, the version, the
// run state, an error reply, and a polite stop through the supervisor with Quit as the graceful step.
func TestRealQEMU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inst, err := qemu.Discover(ctx, qemu.OSHost{}, qemu.Options{})
	if errors.Is(err, qemu.ErrNotFound) {
		t.Skip("no QEMU installed here")
	}
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "qmp.sock")
	argv := []string{inst.System, "-machine", "none", "-nodefaults", "-display", "none", "-S", "-qmp", "unix:" + sock + ",server=on,wait=off"}
	p, err := qemu.Start(ctx, argv, qemu.StartOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })

	c, err := Dial(ctx, "unix", sock, DialOptions{Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("%v\nQEMU said: %s", err, p.Output())
	}
	defer c.Close()
	g := c.Greeting().QMP.Version.QEMU
	t.Logf("QEMU %d.%d.%d (%s)", g.Major, g.Minor, g.Micro, c.Greeting().QMP.Version.Package)
	if g.Major < qemu.MinMajor {
		t.Errorf("QEMU %d is older than the minimum", g.Major)
	}

	st, err := c.QueryStatus(ctx)
	if err != nil || st.Running || st.Status != "prelaunch" {
		t.Errorf("a machine started with -S has not been launched: %+v %v", st, err)
	}
	// QEMU's own error reply comes through as an *Error (there is no display to dump).
	err = c.Screendump(ctx, filepath.Join(dir, "shot.png"), "png")
	var qe *Error
	if !errors.As(err, &qe) {
		t.Errorf("screendump of a machine without a display: %v", err)
	} else {
		t.Logf("screendump: %v", qe)
	}

	start := time.Now()
	if err := p.Stop(ctx, c.Quit, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("quit -> process gone: %v", time.Since(start).Round(time.Millisecond))
	if code, err := p.Result(); code != 0 || err != nil {
		t.Errorf("QEMU should exit cleanly after quit: %d %v\n%s", code, err, p.Output())
	}
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Error("the connection ends when QEMU does")
	}
	if ev, err := c.WaitEvent(ctx, func(e Event) bool { return e.Name == EventShutdown }); err != nil {
		t.Logf("no SHUTDOWN event was seen before the connection closed: %v", err)
	} else {
		var sd Shutdown
		_ = ev.Decode(&sd)
		t.Logf("SHUTDOWN: guest=%v reason=%s", sd.Guest, sd.Reason)
	}
}
