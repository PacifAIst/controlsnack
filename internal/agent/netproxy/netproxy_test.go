// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package netproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtaci/smux"

	"github.com/PacifAIst/controlsnack-local/internal/protocol"
)

// fakeHost plays the host's side of the bridge: it accepts streams, reads the request line, applies a policy, and
// connects every allowed request to one local server (whatever name was asked for).
type fakeHost struct {
	t        *testing.T
	upstream string // host:port every allowed request is connected to
	policy   func(protocol.NetRequest) protocol.NetReply

	mu       sync.Mutex
	requests []protocol.NetRequest
	silent   bool // never answers
}

func (h *fakeHost) seen() []protocol.NetRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]protocol.NetRequest(nil), h.requests...)
}

// serve runs the host's side on conn until it closes.
func (h *fakeHost) serve(conn io.ReadWriteCloser) {
	sess, err := smux.Server(conn, MuxConfig())
	if err != nil {
		h.t.Error(err)
		return
	}
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go h.stream(st)
	}
}

func (h *fakeHost) stream(st *smux.Stream) {
	defer st.Close()
	req, err := protocol.ReadNetRequest(st)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.requests = append(h.requests, req)
	silent := h.silent
	h.mu.Unlock()
	if silent {
		time.Sleep(time.Minute)
		return
	}
	rep := protocol.NetReply{OK: true}
	if h.policy != nil {
		rep = h.policy(req)
	}
	if !rep.OK {
		_ = protocol.WriteNetReply(st, rep)
		return
	}
	up, err := dialTCP(h.upstream)
	if err != nil {
		_ = protocol.WriteNetReply(st, protocol.NetReply{Error: err.Error()})
		return
	}
	defer up.Close()
	if err := protocol.WriteNetReply(st, rep); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, st); _ = up.(*net.TCPConn).CloseWrite(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(st, up); _ = st.CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
}

// bridgeTo returns a Bridge whose port is a loopback connection to the fake host, and the number of times it was opened.
func bridgeTo(t *testing.T, h *fakeHost) (*Bridge, *atomic.Int32) {
	t.Helper()
	l, err := listenTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	opens := &atomic.Int32{}
	b := &Bridge{Open: func() (io.ReadWriteCloser, error) {
		opens.Add(1)
		return dialTCP(l.Addr().String())
	}}
	t.Cleanup(func() { _ = b.Close() })
	return b, opens
}

// echoServer answers every byte it gets with the same byte, until the sender stops, then says goodbye.
func echoServer(t *testing.T) string {
	t.Helper()
	l, err := listenTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
				_, _ = io.WriteString(c, "bye")
			}()
		}
	}()
	return l.Addr().String()
}

func TestBridgeDialsThroughTheHost(t *testing.T) {
	h := &fakeHost{t: t, upstream: echoServer(t)}
	b, opens := bridgeTo(t, h)
	c, err := b.Dial(context.Background(), "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "hello"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
	if seen := h.seen(); len(seen) != 1 || seen[0] != (protocol.NetRequest{Host: "example.com", Port: 443}) {
		t.Errorf("the host was asked %+v", seen)
	}
	// A second connection shares the port.
	c2, err := b.Dial(context.Background(), "other.org", 80)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if opens.Load() != 1 {
		t.Errorf("the port was opened %d times, once is enough", opens.Load())
	}
}

func TestBridgeReportsARefusalWithItsReason(t *testing.T) {
	h := &fakeHost{t: t, policy: func(r protocol.NetRequest) protocol.NetReply {
		return protocol.NetReply{Error: "address 10.0.0.5 of " + r.Host + " is private"}
	}}
	b, _ := bridgeTo(t, h)
	_, err := b.Dial(context.Background(), "intranet.example", 80)
	var denied *DeniedError
	if !errors.As(err, &denied) || denied.Reason != "address 10.0.0.5 of intranet.example is private" {
		t.Errorf("%v", err)
	}
}

func TestBridgeWithoutAPortIsDisabled(t *testing.T) {
	b := &Bridge{Open: func() (io.ReadWriteCloser, error) { return nil, ErrDisabled }}
	if _, err := b.Dial(context.Background(), "example.com", 80); !errors.Is(err, ErrDisabled) {
		t.Errorf("%v", err)
	}
}

func TestBridgeReconnectsAfterTheConnectionDied(t *testing.T) {
	h := &fakeHost{t: t, upstream: echoServer(t)}
	good, _ := bridgeTo(t, h)
	var opens int
	b := &Bridge{Open: func() (io.ReadWriteCloser, error) {
		opens++
		if opens == 1 { // a port whose other end is already gone
			a, z := net.Pipe()
			_ = z.Close()
			return a, nil
		}
		return good.Open()
	}}
	defer b.Close()
	if _, err := b.Dial(context.Background(), "example.com", 80); !errors.Is(err, ErrBridgeDown) {
		t.Fatalf("a dead port: %v", err)
	}
	c, err := b.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatalf("the next try opens the port again: %v", err)
	}
	_ = c.Close()
	if opens != 2 {
		t.Errorf("%d opens", opens)
	}
}

func TestBridgeGivesUpWhenTheHostDoesNotAnswer(t *testing.T) {
	h := &fakeHost{t: t, silent: true}
	b, _ := bridgeTo(t, h)
	b.Handshake = 300 * time.Millisecond
	begin := time.Now()
	_, err := b.Dial(context.Background(), "example.com", 80)
	if !errors.Is(err, ErrBridgeDown) || time.Since(begin) > 5*time.Second {
		t.Errorf("%v after %s", err, time.Since(begin))
	}
}

func TestBridgeStopsWhenTheRequestIsCanceled(t *testing.T) {
	h := &fakeHost{t: t, silent: true}
	b, _ := bridgeTo(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	begin := time.Now()
	if _, err := b.Dial(ctx, "example.com", 80); !errors.Is(err, context.Canceled) || time.Since(begin) > 5*time.Second {
		t.Errorf("%v after %s", err, time.Since(begin))
	}
}

// proxyOn starts a proxy that dials through the fake host and returns its address.
func proxyOn(t *testing.T, h *fakeHost) (*Proxy, string) {
	t.Helper()
	b, _ := bridgeTo(t, h)
	p := New(b.Dial, nil)
	l, err := listenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Serve(l) }()
	t.Cleanup(func() { _ = p.Close() })
	return p, l.Addr().String()
}

func clientVia(addr string) *http.Client {
	u, _ := url.Parse("http://" + addr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}, Timeout: 20 * time.Second}
}

func TestProxyForwardsPlainHTTP(t *testing.T) {
	var got struct {
		sync.Mutex
		method, path, body, host, conn, proxyConn string
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Lock()
		got.method, got.path, got.body, got.host, got.conn, got.proxyConn = r.Method, r.URL.RequestURI(), string(b), r.Host, r.Header.Get("Connection"), r.Header.Get("Proxy-Connection")
		got.Unlock()
		w.Header().Set("X-Seen", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "short and stout")
	}))
	defer up.Close()
	_, addr := proxyOn(t, &fakeHost{t: t, upstream: up.Listener.Addr().String()})

	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://web.example/pot?x=1", strings.NewReader("tea"))
	postReq.Header.Set("Content-Type", "text/plain")
	resp, err := clientVia(addr).Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTeapot || string(body) != "short and stout" || resp.Header.Get("X-Seen") != "yes" {
		t.Errorf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	got.Lock()
	defer got.Unlock()
	if got.method != "POST" || got.path != "/pot?x=1" || got.body != "tea" || got.host != "web.example" {
		t.Errorf("the server saw %s %s %q for %s", got.method, got.path, got.body, got.host)
	}
	if got.proxyConn != "" || !strings.EqualFold(got.conn, "close") {
		t.Errorf("one connection for each request, and no proxy headers: Connection %q, Proxy-Connection %q", got.conn, got.proxyConn)
	}
}

func TestProxyAsksTheHostForTheNameAndPort(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	h := &fakeHost{t: t, upstream: up.Listener.Addr().String()}
	_, addr := proxyOn(t, h)
	for _, u := range []string{"http://plain.example/", "http://plain.example:8080/x", "http://[2001:db8::1]:81/"} {
		resp, err := httpGet(clientVia(addr), u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	want := []protocol.NetRequest{{Host: "plain.example", Port: 80}, {Host: "plain.example", Port: 8080}, {Host: "2001:db8::1", Port: 81}}
	got := h.seen()
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestProxyTunnelsConnect(t *testing.T) {
	h := &fakeHost{t: t, upstream: echoServer(t)}
	_, addr := proxyOn(t, h)
	c, err := dialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	// The first bytes of the tunnel follow the request in one write, as a client in a hurry sends them.
	_, _ = io.WriteString(c, "CONNECT secure.example:443 HTTP/1.1\r\nHost: secure.example:443\r\n\r\nearly")
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("%q %v", line, err)
	}
	for { // the rest of the header
		l, err := r.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "early" {
		t.Fatalf("bytes sent with the request reach the server: %q %v", buf, err)
	}
	_, _ = io.WriteString(c, "later")
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "later" {
		t.Fatalf("%q %v", buf, err)
	}
	// A client that stops sending and waits gets what the server still has to say for a few seconds (a multiplexed stream
	// is closed whole, not half), and then the tunnel ends.
	_ = c.(*net.TCPConn).CloseWrite()
	begin := time.Now()
	rest, err := io.ReadAll(r)
	if err != nil || len(rest) != 0 || time.Since(begin) > 3*halfCloseGrace+2*time.Second {
		t.Errorf("the tunnel ends after the grace: %q %v after %s", rest, err, time.Since(begin))
	}
	if seen := h.seen(); len(seen) != 1 || seen[0] != (protocol.NetRequest{Host: "secure.example", Port: 443}) {
		t.Errorf("%+v", seen)
	}
}

func TestProxyAnswersRefusalsInPlainText(t *testing.T) {
	deny := func(protocol.NetRequest) protocol.NetReply {
		return protocol.NetReply{Error: "address 127.0.0.1 is private\x1b[31m\nsecond line"}
	}
	_, addr := proxyOn(t, &fakeHost{t: t, policy: deny})
	resp, err := httpGet(clientVia(addr), "http://localhost.example/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("%d %v", resp.StatusCode, resp.Header)
	}
	if string(body) != "Denied: address 127.0.0.1 is private[31m second line\n" {
		t.Errorf("a reason the model can read, on one line, without control characters: %q", body)
	}
	if reason := resp.Header.Get("X-ControlSnack-Reason"); !strings.Contains(reason, "address 127.0.0.1 is private") || strings.ContainsAny(reason, "\x1b\n") {
		t.Errorf("the reason header: %q", reason)
	}

	// The same through CONNECT: a status line the browser can count as a refusal.
	c, _ := dialTCP(addr)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(c, "CONNECT localhost.example:443 HTTP/1.1\r\nHost: localhost.example:443\r\n\r\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	if !strings.HasPrefix(line, "HTTP/1.1 403") {
		t.Errorf("%q", line)
	}
}

func TestProxyWithoutANetworkSaysSo(t *testing.T) {
	b := &Bridge{Open: func() (io.ReadWriteCloser, error) { return nil, ErrDisabled }}
	p := New(b.Dial, nil)
	l, _ := listenTCP()
	go func() { _ = p.Serve(l) }()
	defer p.Close()
	resp, err := httpGet(clientVia(l.Addr().String()), "http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || strings.TrimSpace(string(body)) != DisabledMessage {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

func TestProxyWithABridgeThatIsDown(t *testing.T) {
	b := &Bridge{Open: func() (io.ReadWriteCloser, error) { a, z := net.Pipe(); _ = z.Close(); return a, nil }}
	p := New(b.Dial, nil)
	l, _ := listenTCP()
	go func() { _ = p.Serve(l) }()
	defer p.Close()
	resp, err := httpGet(clientVia(l.Addr().String()), "http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(body), "bridge") {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

func TestProxyRefusesWhatIsNotAProxyRequest(t *testing.T) {
	_, addr := proxyOn(t, &fakeHost{t: t, upstream: echoServer(t)})
	for name, req := range map[string]struct {
		raw  string
		want string
	}{
		"origin form":      {"GET /index.html HTTP/1.1\r\nHost: example.com\r\n\r\n", "HTTP/1.1 400"},
		"https absolute":   {"GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n", "HTTP/1.1 400"},
		"connect no port":  {"CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n", "HTTP/1.1 400"},
		"connect bad port": {"CONNECT example.com:99999 HTTP/1.1\r\nHost: example.com:99999\r\n\r\n", "HTTP/1.1 400"},
		"connect port 0":   {"CONNECT example.com:0 HTTP/1.1\r\nHost: example.com:0\r\n\r\n", "HTTP/1.1 400"},
		"ftp":              {"GET ftp://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n", "HTTP/1.1 400"},
	} {
		c, err := dialTCP(addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.WriteString(c, req.raw)
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if !strings.HasPrefix(line, req.want) {
			t.Errorf("%s: %q", name, line)
		}
	}
}

func TestProxyServesLoopbackClientsOnly(t *testing.T) {
	p := New(func(context.Context, string, int) (net.Conn, error) { return nil, errors.New("never") }, nil)
	for _, remote := range []string{"10.0.0.5:4000", "192.168.1.9:1", "[2001:db8::1]:5", "garbage"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), "GET", "http://example.com/", nil)
		req.RemoteAddr = remote
		p.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("%s: %d", remote, rec.Code)
		}
	}
}

func TestProxyLimitsConnections(t *testing.T) {
	h := &fakeHost{t: t, upstream: echoServer(t)}
	p, addr := proxyOn(t, h)
	p.MaxConns = 1
	hold, err := dialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	_ = hold.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(hold, "CONNECT a.example:443 HTTP/1.1\r\nHost: a.example:443\r\n\r\n")
	if line, _ := bufio.NewReader(hold).ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("%q", line)
	}
	if p.Active() != 1 {
		t.Errorf("%d active", p.Active())
	}
	resp, err := httpGet(clientVia(addr), "http://b.example/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("the second connection is refused while the limit is reached: %d", resp.StatusCode)
	}
	hold.Close()
	deadline := time.Now().Add(5 * time.Second)
	for p.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.Active() != 0 {
		t.Errorf("%d still active after the tunnel closed", p.Active())
	}
}

func TestProxyClosesAnIdleTunnel(t *testing.T) {
	h := &fakeHost{t: t, upstream: echoServer(t)}
	p, addr := proxyOn(t, h)
	p.Idle = 300 * time.Millisecond
	c, _ := dialTCP(addr)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	_, _ = io.WriteString(c, "CONNECT a.example:443 HTTP/1.1\r\nHost: a.example:443\r\n\r\n")
	r := bufio.NewReader(c)
	_, _ = r.ReadString('\n')
	_, _ = r.ReadString('\n')
	begin := time.Now()
	_, err := r.ReadByte() // nothing is sent: the proxy ends the tunnel
	if err == nil || time.Since(begin) > 10*time.Second {
		t.Errorf("%v after %s", err, time.Since(begin))
	}
}

func TestSplitHostPort(t *testing.T) {
	type res struct {
		host string
		port int
		ok   bool
	}
	for in, want := range map[string]res{
		"example.com:443":    {"example.com", 443, true},
		"example.com":        {"example.com", 80, true},
		"[2001:db8::1]:8443": {"2001:db8::1", 8443, true},
		"[2001:db8::1]":      {"2001:db8::1", 80, true},
		"1.2.3.4:80":         {"1.2.3.4", 80, true},
		"example.com:0":      {"", 0, false},
		"example.com:70000":  {"", 0, false},
		"example.com:abc":    {"", 0, false},
		":80":                {"", 0, false},
		"a b.com:80":         {"", 0, false},
		"u@a.com:80":         {"", 0, false},
		"":                   {"", 0, false},
	} {
		h, p, err := splitHostPort(in, 80)
		if (err == nil) != want.ok || (want.ok && (h != want.host || p != want.port)) {
			t.Errorf("%q: %q %d %v, want %+v", in, h, p, err, want)
		}
	}
	if _, _, err := splitHostPort("example.com", 0); err == nil {
		t.Error("CONNECT needs a port")
	}
}

func TestCleanReasons(t *testing.T) {
	if got := clean("a\x00b\x1b[0m\n\tc  d\xff"); got != "ab[0m c d" {
		t.Errorf("%q", got)
	}
	if got := clean(strings.Repeat("é", 1000)); len([]rune(got)) != maxReasonRunes {
		t.Errorf("%d", len([]rune(got)))
	}
	if got := headerSafe("blocked: 日本 ok\r\nX: y"); got != "blocked: ok X: y" {
		t.Errorf("%q", got)
	}
}

func TestProxyKeepsProxyHeadersFromTheServer(t *testing.T) {
	seen := make(chan http.Header, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen <- r.Header.Clone() }))
	defer up.Close()
	_, addr := proxyOn(t, &fakeHost{t: t, upstream: up.Listener.Addr().String()})
	c, err := dialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(c, "GET http://web.example/ HTTP/1.1\r\nHost: web.example\r\nProxy-Connection: keep-alive\r\nProxy-Authorization: Basic c2VjcmV0\r\nX-Kept: yes\r\n\r\n")
	_, _ = bufio.NewReader(c).ReadString('\n') // the status line: the server has seen the request
	select {
	case h := <-seen:
		if h.Get("Proxy-Connection") != "" || h.Get("Proxy-Authorization") != "" || h.Get("X-Kept") != "yes" {
			t.Errorf("the credentials for the proxy must not reach the server: %v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server saw no request")
	}
}

// dialTCP, listenTCP and httpGet are the net calls the tests make, with a context.
func dialTCP(addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
}

func listenTCP() (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
}

func httpGet(c *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// greetingServer writes a line and closes at once: the answer and the end arrive together.
func greetingServer(t *testing.T) string {
	t.Helper()
	l, err := listenTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "hello")
			_ = c.Close()
		}
	}()
	return l.Addr().String()
}

// A server that answers and closes straight away: its last bytes must always arrive. smux's half-close does not
// guarantee that (it loses them in a few exchanges of a hundred), which is why a stream is only ever closed whole.
func TestBridgeNeverLosesTheLastBytesOfAnAnswer(t *testing.T) {
	h := &fakeHost{t: t, upstream: greetingServer(t)}
	b, _ := bridgeTo(t, h)
	lost := 0
	const rounds = 400
	for i := 0; i < rounds; i++ {
		c, err := b.Dial(context.Background(), "example.com", 80)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		got, _ := io.ReadAll(c)
		if string(got) != "hello" {
			lost++
		}
		_ = c.Close()
	}
	if lost != 0 {
		t.Errorf("%d of %d answers lost their bytes", lost, rounds)
	}
}

// Big payloads in both directions, then the end: everything arrives before it.
func TestBridgeCarriesBigPayloadsBothWaysBeforeTheEnd(t *testing.T) {
	const size = 600 << 10
	received := make(chan int, 1)
	l, err := listenTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Read the whole request, then send a big answer and close.
		n, _ := io.CopyN(io.Discard, c, size)
		received <- int(n)
		_, _ = c.Write(make([]byte, size))
	}()
	h := &fakeHost{t: t, upstream: l.Addr().String()}
	b, _ := bridgeTo(t, h)
	c, err := b.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	go func() { _, _ = c.Write(make([]byte, size)) }()
	got, err := io.ReadAll(c)
	if err != nil || len(got) != size {
		t.Errorf("the answer: %d of %d bytes, %v", len(got), size, err)
	}
	if n := <-received; n != size {
		t.Errorf("the request: %d of %d bytes", n, size)
	}
}

func TestRunListensOnTheLoopbackAndStopsWithItsContext(t *testing.T) {
	b := &Bridge{Open: func() (io.ReadWriteCloser, error) { return nil, ErrDisabled }}
	p := New(b.Dial, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	// The port is Port on 127.0.0.1, unless something else holds it on this machine; either way Run says so.
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = httpGet(clientVia(net.JoinHostPort("127.0.0.1", "3128")), "http://example.com/")
		if err == nil {
			break
		}
		select {
		case runErr := <-done:
			t.Skipf("port %d is not free here: %v", Port, runErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || strings.TrimSpace(string(body)) != DisabledMessage {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a proxy stopped by its context ends without an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("Run did not return after its context ended")
	}
}
