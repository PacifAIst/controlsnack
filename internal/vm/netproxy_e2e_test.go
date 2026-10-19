// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/protocol"
)

// TestE2ENetProxyWithoutANetwork checks the guest's proxy in a session started without --allow-net: it listens on the
// loopback only, and it answers every request with the sentence that says why there is no network, to curl as well as
// to the browser, so a model can read it.
func TestE2ENetProxyWithoutANetwork(t *testing.T) {
	m, _ := bootE2E(t, 1280, 800)
	ctx := context.Background()
	time.Sleep(20 * time.Second)

	exec := func(cmd string) protocol.ExecRes {
		t.Helper()
		var res protocol.ExecRes
		if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd, TimeoutMS: 60000}, nil, &res); err != nil {
			t.Fatalf("exec %q: %v", cmd, err)
		}
		return res
	}

	// There is no port to the host, and no network device.
	if res := exec(`ls /dev/virtio-ports/; ip -o link 2>/dev/null | grep -vc ' lo:'`); !strings.Contains(res.Stdout, "org.controlsnack.agent.0") || strings.Contains(res.Stdout, "org.controlsnack.net.0") {
		t.Errorf("only the agent's port is there: %q", res.Stdout)
	}

	// The proxy listens on the loopback and nowhere else.
	res := exec(`ss -ltn 2>/dev/null | grep ':3128'`)
	t.Logf("listening: %q", res.Stdout)
	if !strings.Contains(res.Stdout, "127.0.0.1:3128") || strings.Contains(res.Stdout, "0.0.0.0:3128") || strings.Contains(res.Stdout, "*:3128") {
		t.Errorf("the proxy listens on 127.0.0.1 only: %q", res.Stdout)
	}

	// A plain request gets the sentence, as a 403.
	res = exec(`curl -sS -i --max-time 20 -x http://127.0.0.1:3128 http://example.com/ 2>&1`)
	t.Logf("curl http:\n%s", res.Stdout)
	if !strings.Contains(res.Stdout, "403") || !strings.Contains(res.Stdout, "Network is disabled for this session (start with --allow-net)") {
		t.Errorf("%q", res.Stdout)
	}
	// A tunnel for https is refused with the status curl reports.
	res = exec(`curl -sS --max-time 20 -x http://127.0.0.1:3128 https://example.com/ 2>&1; echo "exit=$?"`)
	t.Logf("curl https:\n%s", res.Stdout)
	if !strings.Contains(res.Stdout, "403") {
		t.Errorf("%q", res.Stdout)
	}
	// Not a proxy request.
	res = exec(`curl -sS -i --max-time 20 http://127.0.0.1:3128/ 2>&1 | head -1`)
	if !strings.Contains(res.Stdout, "400") {
		t.Errorf("%q", res.Stdout)
	}

	// The browser reaches the proxy and shows the sentence on its page, which `read` returns.
	var out protocol.BrowserRes
	if _, err := m.Client.Call(ctx, protocol.TypeBrowser, protocol.BrowserReq{Op: "open", URL: "http://example.com/"}, nil, &out); err != nil {
		t.Fatalf("opening a web address: %v", err)
	}
	if _, err := m.Client.Call(ctx, protocol.TypeBrowser, protocol.BrowserReq{Op: "read"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	t.Logf("the browser shows: %q", out.Text)
	if !strings.Contains(out.Text, "Network is disabled for this session") {
		t.Errorf("the page the browser shows says why: %q", out.Text)
	}
}
