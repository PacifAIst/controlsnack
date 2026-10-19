// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PacifAIst/controlsnack/internal/netbridge"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/safety/egress"
)

// TestE2ENetBridge runs a session started with --allow-net: the guest reaches the public internet through the host's
// bridge, and the destinations it must never reach are refused with the reason, with nothing connected on the host. It
// also checks the part nothing else can: the multiplexer over a real virtio-serial port, and what the guest does when
// the host's end of that port goes away and comes back.
func TestE2ENetBridge(t *testing.T) {
	// A listener on the host's loopback that must never be reached, on a port the policy would allow.
	trap, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer trap.Close()
	var reached atomic.Int32
	go func() {
		for {
			c, err := trap.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			_ = c.Close()
		}
	}()
	trapPort := trap.Addr().(*net.TCPAddr).Port

	m, _ := bootE2EWith(t, 1280, 800, func(o *PrepareOptions) { o.Net = true })
	if m.Net == nil {
		t.Fatal("the machine has no network port")
	}
	policy, err := egress.New(egress.Config{ExtraPorts: []int{trapPort}})
	if err != nil {
		t.Fatal(err)
	}
	events := &eventLog{}
	var dialer netbridge.Dialer = policy
	if os.Getenv("CS_E2E_TRACE") != "" { // the first bytes of every connection, for finding out what a server really sent
		dialer = tracedDialer{policy, t}
	}
	bridge := &netbridge.Bridge{Dialer: dialer, Tracker: egress.NewTracker(egress.Limits{}), Audit: events.add}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bridge.Run(ctx, m.Net, m.DialNet) }()
	time.Sleep(20 * time.Second)

	exec := func(cmd string) protocol.ExecRes {
		t.Helper()
		var res protocol.ExecRes
		if _, err := m.Client.Call(ctx, protocol.TypeExec, protocol.ExecReq{Command: cmd, TimeoutMS: 90000}, nil, &res); err != nil {
			t.Fatalf("exec %q: %v", cmd, err)
		}
		return res
	}
	curl := func(url string) string {
		t.Helper()
		// --noproxy '': the guest's own loopback is reached directly (NO_PROXY), but a destination the host would see is not.
		return exec(`curl -sS -i --max-time 60 --noproxy '' -x http://127.0.0.1:3128 -w '\nHTTP=%{http_code}\n' '` + url + `' 2>&1`).Stdout
	}

	if res := exec(`ls /dev/virtio-ports/; ip -o link 2>/dev/null | grep -vc ' lo:'`); !strings.Contains(res.Stdout, "org.controlsnack.net.0") {
		t.Errorf("the network port is there: %q", res.Stdout)
	}

	// What must never be reached: refused with a sentence, and nothing connected on the host.
	for _, u := range []string{
		fmt.Sprintf("http://127.0.0.1:%d/", trapPort),
		fmt.Sprintf("http://localhost:%d/", trapPort),
		fmt.Sprintf("http://[::1]:%d/", trapPort),
		fmt.Sprintf("http://10.0.2.2:%d/", trapPort),
		fmt.Sprintf("http://192.168.1.1:%d/", trapPort),
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.2.2/",
		"http://example.com:22/",
	} {
		out := curl(u)
		t.Logf("%s\n%s", u, out)
		hasReason := strings.Contains(out, "not a public address") || strings.Contains(out, "localhost") || strings.Contains(out, "not allowed")
		if !strings.Contains(out, "HTTP=403") || !hasReason {
			t.Errorf("%s was not refused with a reason: %q", u, out)
		}
	}
	if out := exec(`curl -sS --max-time 60 --noproxy '' -x http://127.0.0.1:3128 https://127.0.0.1/ 2>&1; echo "exit=$?"`).Stdout; !strings.Contains(out, "403") {
		t.Errorf("a tunnel to the loopback: %q", out)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the host's loopback listener was connected to %d times", n)
	}
	denied := 0
	for _, e := range events.all() {
		if !e.Decision.Allowed {
			denied++
		}
	}
	if denied < 9 {
		t.Errorf("%d refusals in the audit events: %+v", denied, events.all())
	}

	// The public internet, when this machine has it.
	// Which public addresses answer from this machine: example.com, for one, is blocked by some providers (an HTML page
	// without a status line on port 80, a reset on 443), which says nothing about the bridge.
	plain := firstReachable(ctx, []publicPage{
		{"http://detectportal.firefox.com/success.txt", 200, "success"},
		{"http://connectivitycheck.gstatic.com/generate_204", 204, ""},
		{"http://www.msftconnecttest.com/connecttest.txt", 200, "Microsoft Connect Test"},
	})
	secure := firstReachable(ctx, []publicPage{
		{"https://detectportal.firefox.com/success.txt", 200, "success"},
		{"https://www.google.com/robots.txt", 200, "User-agent"},
		{"https://www.mozilla.org/robots.txt", 200, "User-agent"},
	})
	if plain == nil || secure == nil {
		t.Logf("this machine reaches no public page (plain %v, https %v): the public part is skipped", plain, secure)
		return
	}
	check := func(what string, p *publicPage) string {
		t.Helper()
		out := curl(p.url)
		t.Logf("%s: %s\n%.500s", what, p.url, out)
		if !strings.Contains(out, fmt.Sprintf("HTTP=%d", p.status)) || !strings.Contains(out, p.contains) {
			t.Errorf("%s %s: %q", what, p.url, out)
		}
		return out
	}
	check("a plain request through the bridge", plain)
	check("a tunnel through the bridge", secure)

	// The tunnel by hand: CONNECT, then a request over it, and the status line comes back right after the tunnel's own.
	u, _ := url.Parse(plain.url)
	raw := exec(fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/3128
printf 'CONNECT %[1]s:80 HTTP/1.1\r\nHost: %[1]s:80\r\n\r\n' >&3
sleep 2
printf 'GET %[2]s HTTP/1.1\r\nHost: %[1]s\r\nConnection: close\r\n\r\n' >&3
timeout 20 cat <&3 | head -c 400 | tr -d '\r'`, u.Hostname(), u.Path))
	t.Logf("by hand:\n%s", raw.Stdout)
	if !strings.Contains(raw.Stdout, "200 Connection Established\n\nHTTP/1.1 "+fmt.Sprint(plain.status)) {
		t.Errorf("the bytes of a tunnel: %q", raw.Stdout)
	}

	closed := false
	for _, e := range events.all() {
		if e.Phase == netbridge.PhaseClosed && e.Decision.Host == u.Hostname() && e.Down > 100 && e.Up > 20 {
			closed = true
		}
	}
	if !closed {
		t.Errorf("no closed event with bytes for %s: %+v", u.Hostname(), events.all())
	}

	// The host's end of the port goes away and comes back (the host's side was restarted): the guest opens the port
	// again by itself, and the next request goes through.
	_ = m.Net.Close()
	begin := time.Now()
	recovered := false
	for i := 1; i <= 20 && !recovered; i++ {
		out := curl(plain.url)
		if recovered = strings.Contains(out, fmt.Sprintf("HTTP=%d", plain.status)); !recovered {
			t.Logf("attempt %d after the port was closed: %.200q", i, out)
			time.Sleep(2 * time.Second)
		}
	}
	t.Logf("recovered=%v after %s", recovered, time.Since(begin).Round(time.Second))
	if !recovered {
		t.Error("the guest did not recover when the host's end of the port came back")
	}

	// The browser goes through the same proxy.
	if secure.contains != "" && secure.status == 200 {
		var page protocol.BrowserRes
		if _, err := m.Client.Call(ctx, protocol.TypeBrowser, protocol.BrowserReq{Op: "open", URL: secure.url}, nil, &page); err != nil {
			t.Fatalf("opening a web address: %v", err)
		}
		if _, err := m.Client.Call(ctx, protocol.TypeBrowser, protocol.BrowserReq{Op: "read"}, nil, &page); err != nil {
			t.Fatal(err)
		}
		t.Logf("the browser shows: %.200q", page.Text)
		if !strings.Contains(page.Text, secure.contains) {
			t.Errorf("the page the browser shows: %q", page.Text)
		}
	}
}

// publicPage is a small page on the public internet and what it answers.
type publicPage struct {
	url      string
	status   int
	contains string
}

// firstReachable returns the first page that answers as expected when this machine asks it directly, or nil.
func firstReachable(ctx context.Context, pages []publicPage) *publicPage {
	client := &http.Client{
		Timeout:       8 * time.Second,
		Transport:     &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for i := range pages {
		p := pages[i]
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if resp.StatusCode == p.status && strings.Contains(string(body), p.contains) {
			return &p
		}
	}
	return nil
}

// tracedDialer shows the first bytes that cross each connection the bridge makes, in both directions.
type tracedDialer struct {
	netbridge.Dialer
	t *testing.T
}

func (d tracedDialer) Dial(ctx context.Context, host string, port int) (net.Conn, egress.Decision, error) {
	c, dec, err := d.Dialer.Dial(ctx, host, port)
	if err != nil {
		return c, dec, err
	}
	return &tracedConn{Conn: c, t: d.t, name: fmt.Sprintf("%s:%d", host, port)}, dec, nil
}

type tracedConn struct {
	net.Conn
	t         *testing.T
	name      string
	nr, nw    atomic.Int32
	rbytes    atomic.Int64
	wbytes    atomic.Int64
	closeOnce sync.Once
}

func (c *tracedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.rbytes.Add(int64(n))
		if c.nr.Add(1) <= 3 {
			c.t.Logf("[%s] server -> host: %d bytes: %.80q", c.name, n, p[:n])
		}
	}
	if err != nil {
		c.t.Logf("[%s] server -> host ended after %d bytes: %v", c.name, c.rbytes.Load(), err)
	}
	return n, err
}

func (c *tracedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.wbytes.Add(int64(n))
	if c.nw.Add(1) <= 3 {
		c.t.Logf("[%s] host -> server: %d bytes: %.80q (err %v)", c.name, len(p), p, err)
	}
	return n, err
}

func (c *tracedConn) Close() error {
	c.closeOnce.Do(func() {
		c.t.Logf("[%s] closed: %d bytes from the server, %d to it", c.name, c.rbytes.Load(), c.wbytes.Load())
	})
	return c.Conn.Close()
}

// eventLog collects the bridge's events for the test.
type eventLog struct {
	mu     sync.Mutex
	events []netbridge.Event
}

func (l *eventLog) add(e netbridge.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) all() []netbridge.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]netbridge.Event(nil), l.events...)
}
