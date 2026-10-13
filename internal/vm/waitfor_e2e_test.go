// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
	"github.com/PacifAIst/controlsnack-local/internal/vm/agentclient"
)

// TestE2EWaitFor waits for real things to happen in the real guest: a window that opens, the screen changing and
// settling, an element appearing in the desktop's accessibility tree, and text appearing in a terminal.
func TestE2EWaitFor(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	if !slices.Contains(m.Ack.Capabilities, protocol.CapWaitFor) {
		t.Fatalf("no wait_for capability: %v", m.Ack.Capabilities)
	}
	time.Sleep(20 * time.Second)

	exec := func(cmd string) {
		t.Helper()
		var res protocol.ExecRes
		if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd}, nil, &res); err != nil || res.ExitCode != 0 {
			t.Fatalf("exec %q: %+v %v", cmd, res, err)
		}
	}
	wait := func(req protocol.WaitForReq) protocol.WaitForRes {
		t.Helper()
		var res protocol.WaitForRes
		begin := time.Now()
		if _, err := m.Client.Call(ctx, protocol.TypeWaitFor, req, nil, &res); err != nil {
			t.Fatalf("wait_for %+v: %v", req, err)
		}
		t.Logf("wait_for %s %q: met=%v, waited %d ms (took %s)", req.Condition, req.Value, res.Met, res.WaitedMS, time.Since(begin).Round(time.Millisecond))
		return res
	}

	// seconds
	if res := wait(protocol.WaitForReq{Condition: "seconds", Value: "1.5"}); !res.Met || res.WaitedMS < 1400 || res.WaitedMS > 3000 {
		t.Errorf("%+v", res)
	}

	// screen_idle: a quiet desktop settles.
	if res := wait(protocol.WaitForReq{Condition: "screen_idle", Value: "2000", TimeoutMS: 60000}); !res.Met {
		t.Errorf("a quiet desktop is idle: %+v", res)
	}

	// screen_change and window_title: a window opens a few seconds from now.
	exec(`(sleep 4; setsid xfce4-terminal --title "Late Window" -e "bash -c 'echo waiting; sleep 600'" >/dev/null 2>&1 &) >/dev/null 2>&1 &`)
	if res := wait(protocol.WaitForReq{Condition: "screen_change", TimeoutMS: 60000}); !res.Met || res.WaitedMS < 2000 {
		t.Errorf("the screen changes when the window opens: %+v", res)
	}
	if res := wait(protocol.WaitForReq{Condition: "window_title", Value: "late window", TimeoutMS: 60000}); !res.Met {
		t.Errorf("the window has its title: %+v", res)
	}
	if res := wait(protocol.WaitForReq{Condition: "window_title", Value: "no such window anywhere", TimeoutMS: 1500}); res.Met || res.WaitedMS < 1400 || res.WaitedMS > 6000 {
		t.Errorf("a window that never comes: %+v", res)
	}

	// text_visible: found in the terminal through the accessibility names, with no recognition needed.
	if res := wait(protocol.WaitForReq{Condition: "text_visible", Value: "waiting", TimeoutMS: 60000}); !res.Met {
		t.Errorf("the terminal says 'waiting': %+v", res)
	}

	// element: a menu entry of an application that opens now.
	exec(`(setsid mousepad >/dev/null 2>&1 &) >/dev/null 2>&1 &`)
	if res := wait(protocol.WaitForReq{Condition: "element", Value: "Document", TimeoutMS: 60000}); !res.Met {
		t.Errorf("the editor's menu appears in the desktop tree: %+v", res)
	}
	if res := wait(protocol.WaitForReq{Condition: "element", Value: "an element nobody has", TimeoutMS: 2000}); res.Met {
		t.Errorf("%+v", res)
	}

	// Refusals travel back as errors.
	var re *agentclient.RemoteError
	for name, req := range map[string]protocol.WaitForReq{
		"unknown condition": {Condition: "forever"},
		"timeout too long":  {Condition: "screen_change", TimeoutMS: 120000},
		"no value":          {Condition: "window_title"},
	} {
		_, err := m.Client.Call(ctx, protocol.TypeWaitFor, req, nil, nil)
		if !errors.As(err, &re) || re.Code != protocol.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A wait that the host gives up on stops at once.
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	begin := time.Now()
	_, err := m.Client.Call(cctx, protocol.TypeWaitFor, protocol.WaitForReq{Condition: "seconds", Value: "60"}, nil, nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) > 10*time.Second {
		t.Errorf("%v after %s", err, time.Since(begin))
	}
	// And the worker is free again: another wait gets served.
	if res := wait(protocol.WaitForReq{Condition: "seconds", Value: "0.2"}); !res.Met {
		t.Errorf("%+v", res)
	}
}
