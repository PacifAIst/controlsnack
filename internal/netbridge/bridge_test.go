// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package netbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xtaci/smux"

	"github.com/PacifAIst/controlsnack/internal/agent/netproxy"
	"github.com/PacifAIst/controlsnack/internal/protocol"
	"github.com/PacifAIst/controlsnack/internal/safety/audit"
	"github.com/PacifAIst/controlsnack/internal/safety/egress"
)

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func dialTCP(addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
}

// tcpPair is a connected pair of loopback sockets: the host's end and the guest's end of the port.
func tcpPair(t *testing.T) (host, guest net.Conn) {
	t.Helper()
	l := listenTCP(t)
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := l.Accept(); ch <- res{c, err} }()
	g, err := dialTCP(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { _ = r.c.Close(); _ = g.Close() })
	return r.c, g
}

// recorder collects the bridge's events.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// wait returns the events once there are n of them.
func (r *recorder) wait(t *testing.T, n int) []Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ev := r.all(); len(ev) >= n {
			return ev
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d events, got %+v", n, r.all())
	return nil
}

// fakeDialer connects every allowed request to one local server, whatever name was asked for, as the egress policy would
// connect to the name.
type fakeDialer struct {
	upstream string
	deny     func(host string, port int) string // a reason, or "" to allow
	failWith error                              // allowed, but the server does not answer

	mu   sync.Mutex
	seen []string
}

func (d *fakeDialer) Dial(ctx context.Context, host string, port int) (net.Conn, egress.Decision, error) {
	d.mu.Lock()
	d.seen = append(d.seen, fmt.Sprintf("%s:%d", host, port))
	d.mu.Unlock()
	dec := egress.Decision{Allowed: true, Rule: "ok", Reason: "public address", Host: host, Port: port}
	if d.deny != nil {
		if why := d.deny(host, port); why != "" {
			dec.Allowed, dec.Rule, dec.Reason = false, "private-address", why
			return nil, dec, errors.New("connection refused by the egress policy: " + why)
		}
	}
	if d.failWith != nil {
		return nil, dec, d.failWith
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.upstream)
	return c, dec, err
}

func (d *fakeDialer) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

// rig is a host bridge serving a guest bridge over a loopback socket.
type rig struct {
	host    *Bridge
	guest   *netproxy.Bridge
	events  *recorder
	tracker *egress.Tracker
}

func newRig(t *testing.T, d Dialer, l egress.Limits) *rig {
	t.Helper()
	r := &rig{events: &recorder{}, tracker: egress.NewTracker(l)}
	r.host = &Bridge{Dialer: d, Tracker: r.tracker, Audit: r.events.add, RequestTimeout: 500 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := listenTCP(t)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = r.host.Serve(ctx, c) }()
		}
	}()
	r.guest = &netproxy.Bridge{Open: func() (io.ReadWriteCloser, error) { return dialTCP(listener.Addr().String()) }}
	t.Cleanup(func() { _ = r.guest.Close() })
	return r
}

// echoServer answers each byte it gets with the same byte.
func echoServer(t *testing.T) string {
	t.Helper()
	l := listenTCP(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}

func readN(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("reading %d bytes: %v", n, err)
	}
	return string(buf)
}

func TestBridgeCarriesAConnectionAndTellsTheAuditLog(t *testing.T) {
	d := &fakeDialer{upstream: echoServer(t)}
	r := newRig(t, d, egress.Limits{})
	c, err := r.guest.Dial(context.Background(), "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, c, 4); got != "ping" {
		t.Fatalf("echo %q", got)
	}
	// Windows reads its clock in ticks of 0.5 to 15.6 ms: a connection must outlast one to be measured at all.
	time.Sleep(40 * time.Millisecond)
	_ = c.Close()

	ev := r.events.wait(t, 2)
	if ev[0].Phase != PhaseDecision || !ev[0].Decision.Allowed || ev[0].Decision.Host != "example.com" || ev[0].Decision.Port != 443 {
		t.Errorf("decision event: %+v", ev[0])
	}
	if ev[1].Phase != PhaseClosed || ev[1].Up != 4 || ev[1].Down != 4 || ev[1].Duration < 10*time.Millisecond {
		t.Errorf("closed event: %+v", ev[1])
	}
	if got := d.requests(); len(got) != 1 || got[0] != "example.com:443" {
		t.Errorf("the dialer was asked for %v", got)
	}
	if streams, bytes := r.tracker.Stats(); bytes != 8 {
		t.Errorf("bytes charged to the session: %d (streams %d)", bytes, streams)
	}
	waitFor(t, "the stream slot to be freed", func() bool { s, _ := r.tracker.Stats(); return s == 0 })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// greetingServer writes a line and closes at once: the answer and the end arrive together.
func greetingServer(t *testing.T, text string) string {
	t.Helper()
	l := listenTCP(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, text)
			_ = c.Close()
		}
	}()
	return l.Addr().String()
}

// The host closes its end of a stream whole after the last byte; the guest must always get all of it.
func TestBridgeNeverLosesTheLastBytesOfAnAnswer(t *testing.T) {
	r := newRig(t, &fakeDialer{upstream: greetingServer(t, "hello")}, egress.Limits{MaxStreams: 64})
	lost := 0
	const rounds = 300
	for i := 0; i < rounds; i++ {
		c, err := r.guest.Dial(context.Background(), "example.com", 80)
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
func TestBridgeCarriesBigPayloadsBothWays(t *testing.T) {
	const size = 3 << 20
	received := make(chan int, 1)
	l := listenTCP(t)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		n, _ := io.CopyN(io.Discard, c, size)
		received <- int(n)
		_, _ = c.Write(make([]byte, size))
	}()
	r := newRig(t, &fakeDialer{upstream: l.Addr().String()}, egress.Limits{})
	c, err := r.guest.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))
	go func() { _, _ = c.Write(make([]byte, size)) }()
	got, err := io.ReadAll(c)
	if err != nil || len(got) != size {
		t.Errorf("the answer: %d of %d bytes, %v", len(got), size, err)
	}
	if n := <-received; n != size {
		t.Errorf("the request: %d of %d bytes", n, size)
	}
	ev := r.events.wait(t, 2)
	if ev[1].Up != size || ev[1].Down != size {
		t.Errorf("bytes in the closed event: up %d down %d", ev[1].Up, ev[1].Down)
	}
}

func TestBridgeRefusesWithTheReasonAndRecordsIt(t *testing.T) {
	d := &fakeDialer{upstream: "unused", deny: func(host string, port int) string {
		if host == "intranet.example" {
			return "the address 10.1.2.3 is private"
		}
		return ""
	}}
	r := newRig(t, d, egress.Limits{})
	_, err := r.guest.Dial(context.Background(), "intranet.example", 80)
	var denied *netproxy.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "10.1.2.3 is private") {
		t.Fatalf("the refusal: %v", err)
	}
	ev := r.events.wait(t, 1)
	if len(ev) != 1 || ev[0].Phase != PhaseDecision || ev[0].Decision.Allowed || ev[0].Decision.Rule != "private-address" {
		t.Errorf("events: %+v", ev)
	}
	if s, _ := r.tracker.Stats(); s != 0 {
		t.Errorf("a refused request holds %d slots", s)
	}
}

func TestBridgeReportsAServerThatDoesNotAnswer(t *testing.T) {
	d := &fakeDialer{failWith: errors.New("could not connect to example.com:80: i/o timeout")}
	r := newRig(t, d, egress.Limits{})
	_, err := r.guest.Dial(context.Background(), "example.com", 80)
	var denied *netproxy.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "i/o timeout") {
		t.Fatalf("the refusal: %v", err)
	}
	ev := r.events.wait(t, 1)
	if !ev[0].Decision.Allowed || ev[0].Error == "" {
		t.Errorf("an allowed destination that failed to connect: %+v", ev[0])
	}
	if got := ev[0].Record().Verdict; got != "failed" {
		t.Errorf("verdict %q", got)
	}
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupAddrs(_ context.Context, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

// The real policy behind the bridge: the destinations a guest must never reach are refused with a sentence that says
// why, and nothing is connected.
func TestBridgeAppliesTheEgressPolicy(t *testing.T) {
	policy, err := egress.New(egress.Config{Resolver: fakeResolver{
		"internal.example": {netip.MustParseAddr("10.1.1.1")},
		"rebind.example":   {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, policy, egress.Limits{})
	for _, tc := range []struct {
		host   string
		port   int
		reason string
	}{
		{"10.0.2.2", 80, "not a public"},
		{"127.0.0.1", 80, ""},
		{"localhost", 80, ""},
		{"192.168.1.10", 443, "not a public"},
		{"169.254.169.254", 80, ""},
		{"internal.example", 80, "not a public"},
		{"rebind.example", 80, "not a public"},
		{"[::1]", 80, ""},
		{"0x7f.1", 80, ""},
		{"example.com", 22, "port"},
		{"no-such.example", 80, ""},
	} {
		_, err := r.guest.Dial(context.Background(), tc.host, tc.port)
		var denied *netproxy.DeniedError
		if !errors.As(err, &denied) {
			t.Errorf("%s:%d was not refused: %v", tc.host, tc.port, err)
			continue
		}
		if denied.Reason == "" || strings.ContainsAny(denied.Reason, "\r\n") {
			t.Errorf("%s:%d: reason %q", tc.host, tc.port, denied.Reason)
		}
		if tc.reason != "" && !strings.Contains(strings.ToLower(denied.Reason), tc.reason) {
			t.Errorf("%s:%d: reason %q does not mention %q", tc.host, tc.port, denied.Reason, tc.reason)
		}
	}
	for _, e := range r.events.wait(t, 11) {
		if e.Decision.Allowed || e.Phase != PhaseDecision {
			t.Errorf("an event for a refused destination: %+v", e)
		}
		if rec := e.Record(); rec.Verdict != "denied" || len(rec.Rules) != 1 {
			t.Errorf("record: %+v", rec)
		}
	}
	if s, b := r.tracker.Stats(); s != 0 || b != 0 {
		t.Errorf("refused requests held %d slots and carried %d bytes", s, b)
	}
}

func TestBridgeLimitsConcurrentConnections(t *testing.T) {
	r := newRig(t, &fakeDialer{upstream: echoServer(t)}, egress.Limits{MaxStreams: 2})
	var open []net.Conn
	for i := 0; i < 2; i++ {
		c, err := r.guest.Dial(context.Background(), "example.com", 80)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
	}
	_, err := r.guest.Dial(context.Background(), "example.com", 80)
	var denied *netproxy.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "too many") {
		t.Fatalf("the third connection: %v", err)
	}
	_ = open[0].Close()
	waitFor(t, "a slot to be freed", func() bool { s, _ := r.tracker.Stats(); return s == 1 })
	c, err := r.guest.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatalf("after a connection ended: %v", err)
	}
	_ = c.Close()
	_ = open[1].Close()
}

func TestBridgeStopsAtTheByteBudget(t *testing.T) {
	l := listenTCP(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = c.Write(make([]byte, 4<<20)) }()
		}
	}()
	r := newRig(t, &fakeDialer{upstream: l.Addr().String()}, egress.Limits{ByteBudget: 100 << 10})
	c, err := r.guest.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	got, _ := io.ReadAll(c)
	_ = c.Close()
	if len(got) > 100<<10 || len(got) == 0 {
		t.Errorf("carried %d bytes of a %d byte budget", len(got), 100<<10)
	}
	// The budget is the session's: the next connection is refused with a sentence that says so.
	waitFor(t, "the first connection to end", func() bool { s, _ := r.tracker.Stats(); return s == 0 })
	_, err = r.guest.Dial(context.Background(), "example.com", 80)
	var denied *netproxy.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "byte budget") {
		t.Errorf("after the budget: %v", err)
	}
}

func TestBridgeClosesAnIdleConnection(t *testing.T) {
	r := newRig(t, &fakeDialer{upstream: echoServer(t)}, egress.Limits{IdleTimeout: 300 * time.Millisecond})
	c, err := r.guest.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, _ := c.Read(make([]byte, 1)) // nothing is sent: the end is the bridge closing the connection
	if n != 0 || time.Since(start) > 5*time.Second {
		t.Errorf("read %d bytes after %v", n, time.Since(start))
	}
	waitFor(t, "the slot", func() bool { s, _ := r.tracker.Stats(); return s == 0 })
}

// One direction can be silent for as long as it likes while the other moves: a download the guest does not answer is
// not idle.
func TestBridgeDoesNotCallADownloadIdle(t *testing.T) {
	l := listenTCP(t)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 12; i++ {
			_, _ = c.Write([]byte("x"))
			time.Sleep(100 * time.Millisecond)
		}
	}()
	r := newRig(t, &fakeDialer{upstream: l.Addr().String()}, egress.Limits{IdleTimeout: 400 * time.Millisecond})
	c, err := r.guest.Dial(context.Background(), "example.com", 80)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	got, _ := io.ReadAll(c)
	if len(got) != 12 {
		t.Errorf("received %d of 12 bytes: the connection was closed as idle while data moved", len(got))
	}
}

// rawClient speaks to the host bridge directly, without the guest's code.
func rawClient(t *testing.T, r *rig) *smux.Session {
	t.Helper()
	l := listenTCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		c, err := l.Accept()
		if err == nil {
			_ = r.host.Serve(ctx, c)
		}
	}()
	c, err := dialTCP(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s, err := smux.Client(c, protocol.NetMuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBridgeAnswersARequestThatIsNotValid(t *testing.T) {
	r := newRig(t, &fakeDialer{upstream: echoServer(t)}, egress.Limits{})
	s := rawClient(t, r)
	for _, line := range []string{"hello\n", "{\"host\":\"\",\"port\":80}\n", "{\"host\":\"a\",\"port\":80,\"extra\":1}\n", "\x00\x01\x02\n"} {
		st, err := s.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = st.Write([]byte(line))
		rep, err := protocol.ReadNetReply(st)
		if err != nil || rep.OK || rep.Error == "" {
			t.Errorf("%q: reply %+v, %v", line, rep, err)
		}
		_ = st.Close()
	}
	ev := r.events.wait(t, 4)
	for _, e := range ev {
		if e.Decision.Allowed || e.Decision.Rule != "bad-request" || e.Error == "" {
			t.Errorf("event: %+v", e)
		}
	}
}

func TestBridgeGivesUpOnARequestThatNeverComes(t *testing.T) {
	r := newRig(t, &fakeDialer{upstream: echoServer(t)}, egress.Limits{})
	s := rawClient(t, r)
	st, err := s.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.Write([]byte("{\"host\":")) // and nothing more
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	rep, err := protocol.ReadNetReply(st)
	if err != nil || rep.OK {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	r.events.wait(t, 1)
}

func TestBridgeRunServesAgainAfterTheGuestWentAway(t *testing.T) {
	d := &fakeDialer{upstream: echoServer(t)}
	rec := &recorder{}
	host := &Bridge{Dialer: d, Tracker: egress.NewTracker(egress.Limits{}), Audit: rec.add}

	// Each time the guest opens the port, the host side of the new connection is handed to the bridge's dial function.
	hosts := make(chan net.Conn, 4)
	var opens atomic.Int32
	var failures atomic.Int32
	guest := &netproxy.Bridge{Open: func() (io.ReadWriteCloser, error) {
		opens.Add(1)
		h, g := tcpPair(t)
		hosts <- h
		return g, nil
	}}
	t.Cleanup(func() { _ = guest.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- host.Run(ctx, nil, func(ctx context.Context) (net.Conn, error) {
			if failures.Add(1) <= 2 {
				return nil, errors.New("the port is not there yet") // the bridge keeps trying
			}
			select {
			case c := <-hosts:
				return c, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}()

	use := func() {
		t.Helper()
		var c net.Conn
		var err error
		// A guest whose connection just died learns it on the next request; it then opens the port again.
		for i := 0; i < 20; i++ {
			if c, err = guest.Dial(context.Background(), "example.com", 80); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("ok"))
		if got := readN(t, c, 2); got != "ok" {
			t.Fatalf("echo %q", got)
		}
		_ = c.Close()
	}
	use()
	_ = guest.Close() // the guest's agent restarted: the port is closed and opened again
	use()
	if opens.Load() < 2 {
		t.Errorf("the port was opened %d times", opens.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestBridgeRunStopsWhileWaitingForThePort(t *testing.T) {
	host := &Bridge{Dialer: &fakeDialer{}, Tracker: egress.NewTracker(egress.Limits{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- host.Run(ctx, nil, func(ctx context.Context) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// A reason that is long, multi-line or made of characters JSON writes at length still reaches the guest as one short
// line, and a refusal is never lost to the line limit.
func TestBridgeSendsOnlyReasonsThatFit(t *testing.T) {
	for name, why := range map[string]string{
		"long":      strings.Repeat("word ", 1000),
		"escaped":   strings.Repeat("<&>", 400),
		"multiline": "first line\r\nsecond line\x00\x07",
		"wide":      strings.Repeat("é", 2000),
	} {
		r := newRig(t, &fakeDialer{failWith: errors.New(why)}, egress.Limits{})
		_, err := r.guest.Dial(context.Background(), "example.com", 80)
		var denied *netproxy.DeniedError
		if !errors.As(err, &denied) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if denied.Reason == "" || strings.ContainsAny(denied.Reason, "\r\n\x00\x07") || len([]rune(denied.Reason)) > maxReasonRunes || !utf8.ValidString(denied.Reason) {
			t.Errorf("%s: reason %q", name, denied.Reason)
		}
	}
}

func TestEventRecordsAreAcceptedByTheAuditLog(t *testing.T) {
	dir := t.TempDir()
	log, err := audit.Open(dir, "netbridge-test", audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{Phase: PhaseDecision, Decision: egress.Decision{Allowed: true, Rule: "ok", Reason: "public", Host: "example.com", Port: 443}},
		{Phase: PhaseClosed, Decision: egress.Decision{Allowed: true, Rule: "ok", Host: "example.com", Port: 443}, Up: 10, Down: 2048, Duration: 1500 * time.Millisecond},
		{Phase: PhaseDecision, Decision: egress.Decision{Rule: "localhost", Reason: "localhost is not reachable", Host: "localhost", Port: 80}},
		{Phase: PhaseDecision, Decision: egress.Decision{Rule: "limit", Host: "example.com", Port: 80}, Error: "too many connections"},
	}
	wantVerdict := []string{"allowed", "allowed", "denied", "denied"}
	for i, e := range events {
		rec := e.Record()
		if rec.Kind != audit.KindNet || rec.Verdict != wantVerdict[i] || rec.Event != e.Phase {
			t.Errorf("record %d: %+v", i, rec)
		}
		if _, err := log.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err := audit.Verify(log.Path())
	if err != nil || !rep.OK() {
		t.Fatalf("verify: %v %+v", err, rep)
	}
	if got := string(events[1].Record().Data); !strings.Contains(got, `"up":10`) || !strings.Contains(got, `"down":2048`) || !strings.Contains(got, `"ms":1500`) {
		t.Errorf("data of a closed event: %s", got)
	}
}

// holdOnly keeps a stream the way `defer st.Close()` does: through the stream inside the wrapper, not the wrapper.
func holdOnly(st *smux.Stream, release <-chan struct{}) {
	defer st.Close()
	<-release
}

// smux closes an accepted stream when its wrapper is garbage collected; the bridge must not let that happen to a stream
// that is still waiting (a guest's connection ended with nothing said, rarely, when a collection came at the wrong time).
func TestAcceptedStreamsAreNotClosedByTheGarbageCollector(t *testing.T) {
	host, guest := tcpPair(t)
	ready := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		sess, err := smux.Server(host, protocol.NetMuxConfig())
		if err != nil {
			return
		}
		st, err := acceptStream(sess)
		if err != nil {
			return
		}
		close(ready)
		holdOnly(st, release)
	}()
	sess, err := smux.Client(guest, protocol.NetMuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	st, err := sess.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream was not accepted")
	}
	for i := 0; i < 30; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	_ = st.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := st.Read(make([]byte, 1)); !errors.Is(err, smux.ErrTimeout) {
		t.Errorf("the guest's read ends with %v: the host's stream was closed while it was waiting", err)
	}
}
