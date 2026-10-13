// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func addr(t testing.TB, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("bad test address %q: %v", s, err)
	}
	return a
}

func TestIPv4RangesBoundaries(t *testing.T) {
	// Every non-public range of PLAN.md section 6.11: its first and last address are refused, and the address on
	// each side of it (which is public) is not.
	ranges := []struct{ name, first, last, before, after string }{
		{"0.0.0.0/8", "0.0.0.0", "0.255.255.255", "", "1.0.0.0"},
		{"10.0.0.0/8", "10.0.0.0", "10.255.255.255", "9.255.255.255", "11.0.0.0"},
		{"100.64.0.0/10", "100.64.0.0", "100.127.255.255", "100.63.255.255", "100.128.0.0"},
		{"127.0.0.0/8", "127.0.0.0", "127.255.255.255", "126.255.255.255", "128.0.0.0"},
		{"169.254.0.0/16", "169.254.0.0", "169.254.255.255", "169.253.255.255", "169.255.0.0"},
		{"172.16.0.0/12", "172.16.0.0", "172.31.255.255", "172.15.255.255", "172.32.0.0"},
		{"192.0.0.0/24", "192.0.0.0", "192.0.0.255", "191.255.255.255", "192.0.1.0"},
		{"192.0.2.0/24", "192.0.2.0", "192.0.2.255", "192.0.1.255", "192.0.3.0"},
		{"192.88.99.0/24", "192.88.99.0", "192.88.99.255", "192.88.98.255", "192.88.100.0"},
		{"192.168.0.0/16", "192.168.0.0", "192.168.255.255", "192.167.255.255", "192.169.0.0"},
		{"198.18.0.0/15", "198.18.0.0", "198.19.255.255", "198.17.255.255", "198.20.0.0"},
		{"198.51.100.0/24", "198.51.100.0", "198.51.100.255", "198.51.99.255", "198.51.101.0"},
		{"203.0.113.0/24", "203.0.113.0", "203.0.113.255", "203.0.112.255", "203.0.114.0"},
		{"224.0.0.0/4", "224.0.0.0", "239.255.255.255", "223.255.255.255", ""},
		{"240.0.0.0/4", "240.0.0.0", "255.255.255.255", "239.255.255.255", ""},
	}
	for _, r := range ranges {
		for _, in := range []string{r.first, r.last} {
			if ok, why := IsPublic(addr(t, in)); ok || why == "" {
				t.Errorf("%s (inside %s) must be refused with a reason, got public=%v %q", in, r.name, ok, why)
			}
		}
		for _, out := range []string{r.before, r.after} {
			// the neighbor of 224/4 below is public; the one above 240/4 does not exist
			if out == "" || strings.HasPrefix(out, "239.") {
				continue
			}
			if ok, why := IsPublic(addr(t, out)); !ok {
				t.Errorf("%s (next to %s) is public but was refused: %s", out, r.name, why)
			}
		}
	}
	for _, public := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "142.250.80.46", "151.101.1.69", "223.255.255.254", "100.0.0.1", "172.15.0.1"} {
		if ok, why := IsPublic(addr(t, public)); !ok {
			t.Errorf("%s is public but was refused: %s", public, why)
		}
	}
}

func TestIPv6Ranges(t *testing.T) {
	refused := []string{
		"::", "::1", "::2", "100::", "100::ffff:ffff:ffff:ffff", "2001::", "2001:1ff:ffff:ffff:ffff:ffff:ffff:ffff",
		"2001:db8::", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "2002::", "2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"fc00::", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fe80::", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"fe80::1%eth0", "ff00::", "ff02::1", "3fff::", "3fff:fff:ffff:ffff:ffff:ffff:ffff:ffff", "5f00::1", "fec0::1",
		"64:ff9b:1::1", "1::1", "4000::1", "8000::1", // outside 2000::/3
		"2001:2::1",                            // benchmarking, inside 2001::/23
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo, inside 2001::/23
	}
	for _, s := range refused {
		if ok, why := IsPublic(addr(t, s)); ok || why == "" {
			t.Errorf("%s must be refused with a reason, got public=%v %q", s, ok, why)
		}
	}
	public := []string{"2606:4700:4700::1111", "2001:4860:4860::8888", "2a00:1450:4001:81b::200e", "2001:200::1", "2003::1", "2001:db9::1", "3fff:1000::1", "2620:fe::fe"}
	for _, s := range public {
		if ok, why := IsPublic(addr(t, s)); !ok {
			t.Errorf("%s is public but was refused: %s", s, why)
		}
	}
}

// The reason a model reads says what the address is: ::1 is the loopback, not an "IPv4-compatible" address.
func TestIPv6LoopbackAndUnspecifiedSayWhatTheyAre(t *testing.T) {
	for s, want := range map[string]string{"::1": "loopback", "::": "unspecified", "::2": "IPv4-compatible", "::127.0.0.1": "IPv4-compatible"} {
		ok, why := IsPublic(addr(t, s))
		if ok || !strings.Contains(why, want) {
			t.Errorf("%s: public=%v, reason %q does not mention %q", s, ok, why, want)
		}
	}
}

func TestEmbeddedIPv4IsJudgedByTheIPv4Inside(t *testing.T) {
	refused := []string{
		"::ffff:127.0.0.1", "::ffff:7f00:1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.1.1", "::ffff:0.0.0.0",
		"::127.0.0.1", "::10.0.0.1", "::a00:1", "::8.8.8.8", // IPv4-compatible is deprecated: refused even for a public IPv4
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::192.168.0.1",
	}
	for _, s := range refused {
		if ok, why := IsPublic(addr(t, s)); ok {
			t.Errorf("%s must be refused (embedded private IPv4), why=%q", s, why)
		}
	}
	for _, s := range []string{"::ffff:8.8.8.8", "64:ff9b::808:808", "::ffff:93.184.216.34"} {
		if ok, why := IsPublic(addr(t, s)); !ok {
			t.Errorf("%s embeds a public IPv4 and must be allowed: %s", s, why)
		}
	}
	if ok, _ := IsPublic(netip.Addr{}); ok {
		t.Error("the zero address is not public")
	}
}

func TestParseIPLiteral(t *testing.T) {
	yes := map[string]string{
		"127.0.0.1": "127.0.0.1", "2130706433": "127.0.0.1", "0x7f000001": "127.0.0.1", "017700000001": "127.0.0.1",
		"0177.0.0.1": "127.0.0.1", "127.1": "127.0.0.1", "127.0.1": "127.0.0.1", "0x7f.1": "127.0.0.1", "0x7f.0.0.1": "127.0.0.1",
		"0X7F.0.0.1": "127.0.0.1", "0300.0250.0.1": "192.168.0.1", "3232235777": "192.168.1.1", "1.2.3": "1.2.0.3",
		"4294967295": "255.255.255.255", "0": "0.0.0.0", "8.8.8.8": "8.8.8.8", "0.0.0.0": "0.0.0.0",
		"::1": "::1", "[::1]": "::1", "::ffff:1.2.3.4": "::ffff:1.2.3.4", "2606:4700::1111": "2606:4700::1111",
	}
	for in, want := range yes {
		got, ok := ParseIPLiteral(in)
		if !ok || got.String() != want {
			t.Errorf("ParseIPLiteral(%q) = %v, %v; want %s", in, got, ok, want)
		}
	}
	no := []string{"", "1.2.3.4.5", "256.1.1.1", "4294967296", "0x", "0xg", "08", "09.1", "a.b.c.d", "1..2", ".1", "1.", "-1", "+1", "1_000",
		"example.com", "localhost", "1.2.3.4:80", "0x100000000", "1.2.3.256", "1.65536.1", "16777216.1", "::g", "[1.2.3.4]"}
	for _, in := range no {
		if got, ok := ParseIPLiteral(in); ok {
			t.Errorf("ParseIPLiteral(%q) = %v; it is not an address", in, got)
		}
	}
}

// fakeResolver answers from a table and counts questions.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]string
	err     error
	calls   map[string]int
	// second, if set, is what the name resolves to from the second lookup on (a rebinding attack).
	second map[string][]string
}

func (f *fakeResolver) LookupAddrs(_ context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[host]++
	if f.err != nil {
		return nil, f.err
	}
	src := f.answers[host]
	if f.calls[host] > 1 && f.second != nil {
		src = f.second[host]
	}
	var out []netip.Addr
	for _, s := range src {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

func newPolicy(t *testing.T, c Config, answers map[string][]string) (*Policy, *fakeResolver) {
	t.Helper()
	r := &fakeResolver{answers: answers}
	c.Resolver = r
	p, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func TestCheckPortsAndLiterals(t *testing.T) {
	p, _ := newPolicy(t, Config{ExtraPorts: []int{8080}}, nil)
	ctx := context.Background()
	tests := []struct {
		host string
		port int
		rule string
	}{
		{"8.8.8.8", 443, "ok"},
		{"8.8.8.8", 80, "ok"},
		{"8.8.8.8", 8080, "ok"},
		{"8.8.8.8", 22, "port"},
		{"8.8.8.8", 0, "port"},
		{"8.8.8.8", 65536, "port"},
		{"[2606:4700:4700::1111]", 443, "ok"},
		{"2606:4700:4700::1111", 443, "ok"},
		// everything that means a private address, however it is spelled
		{"127.0.0.1", 80, "private-address"},
		{"2130706433", 80, "private-address"},
		{"0x7f000001", 80, "private-address"},
		{"017700000001", 80, "private-address"},
		{"0177.0.0.1", 80, "private-address"},
		{"127.1", 80, "private-address"},
		{"0x7f.1", 80, "private-address"},
		{"0300.0250.0.1", 80, "private-address"},
		{"3232235777", 80, "private-address"},
		{"10.0.2.2", 80, "private-address"}, // QEMU's gateway: the host's loopback
		{"169.254.169.254", 80, "private-address"},
		{"192.168.1.1", 443, "private-address"},
		{"172.16.5.5", 443, "private-address"},
		{"0.0.0.0", 80, "private-address"},
		{"255.255.255.255", 80, "private-address"},
		{"::1", 80, "private-address"},
		{"[::1]", 80, "private-address"},
		{"::ffff:127.0.0.1", 80, "private-address"},
		{"[::ffff:7f00:1]", 80, "private-address"},
		{"fe80::1", 80, "private-address"},
		{"fe80::1%eth0", 80, "host-syntax"},
		{"fc00::1", 80, "private-address"},
		{"64:ff9b::a00:1", 80, "private-address"},
		// names that are not public names
		{"localhost", 80, "localhost"},
		{"LOCALHOST", 80, "localhost"},
		{"localhost.", 80, "localhost"},
		{"foo.localhost", 80, "localhost"},
		{"intranet", 80, "host-syntax"},
		{"example.123", 80, "host-syntax"},
		{"1.2.3.4.5", 80, "host-syntax"},
		{"", 80, "host-syntax"},
		{" example.com", 80, "host-syntax"},
		{"example.com ", 80, "host-syntax"},
		{"exa mple.com", 80, "host-syntax"},
		{"example..com", 80, "host-syntax"},
		{".example.com", 80, "host-syntax"},
		{"user@example.com", 80, "host-syntax"},
		{"example.com/path", 80, "host-syntax"},
		{"example.com:80", 80, "host-syntax"},
		{"exa_mple.com", 80, "host-syntax"},
		{strings.Repeat("a", 64) + ".com", 80, "host-syntax"},
		{strings.Repeat("a.", 130) + "com", 80, "host-syntax"},
		{"example.com\x00.evil", 80, "host-syntax"},
	}
	for _, tt := range tests {
		d := p.Check(ctx, tt.host, tt.port)
		if d.Rule != tt.rule || d.Allowed != (tt.rule == "ok") {
			t.Errorf("Check(%q, %d) = allowed=%v rule=%q reason=%q; want rule %q", tt.host, tt.port, d.Allowed, d.Rule, d.Reason, tt.rule)
		}
		if d.Reason == "" {
			t.Errorf("Check(%q, %d): the decision needs a reason for the audit log", tt.host, tt.port)
		}
		if d.Allowed && len(d.Addrs) == 0 {
			t.Errorf("Check(%q, %d): an allowed decision carries the addresses to dial", tt.host, tt.port)
		}
	}
}

func TestCheckResolvesAndVetsEveryAnswer(t *testing.T) {
	p, r := newPolicy(t, Config{}, map[string][]string{
		"example.com":              {"93.184.216.34"},
		"dual.example.com":         {"93.184.216.34", "2606:2800:220:1::1"},
		"rebind.example.com":       {"8.8.8.8", "10.0.0.1"},
		"private.example.com":      {"192.168.1.10"},
		"loop.example.com":         {"127.0.0.1"},
		"mapped.example.com":       {"::ffff:127.0.0.1"},
		"metadata.google.internal": {"169.254.169.254"},
		"empty.example.com":        {},
		"v6private.example.com":    {"fe80::1"},
		"xn--bcher-kva.example":    {"93.184.216.34"},
	})
	ctx := context.Background()
	for host, rule := range map[string]string{
		"example.com": "ok", "EXAMPLE.COM": "ok", "example.com.": "ok", "dual.example.com": "ok",
		"rebind.example.com": "private-address", "private.example.com": "private-address", "loop.example.com": "private-address",
		"mapped.example.com": "private-address", "metadata.google.internal": "private-address", "empty.example.com": "no-address",
		"v6private.example.com": "private-address", "unknown.example.com": "no-address",
		"bücher.example": "ok", // internationalized names are looked up in their ASCII form
	} {
		d := p.Check(ctx, host, 443)
		if d.Rule != rule {
			t.Errorf("Check(%q) rule %q (%s), want %q", host, d.Rule, d.Reason, rule)
		}
	}
	d := p.Check(ctx, "dual.example.com", 443)
	if len(d.Addrs) != 2 || d.Addrs[0].String() != "93.184.216.34" || d.Host != "dual.example.com" {
		t.Errorf("decision: %+v", d)
	}
	if d := p.Check(ctx, "bücher.example", 443); d.Host != "xn--bcher-kva.example" {
		t.Errorf("normalized host = %q", d.Host)
	}
	if d := p.Check(ctx, "::ffff:8.8.8.8", 443); len(d.Addrs) != 1 || d.Addrs[0].String() != "8.8.8.8" {
		t.Errorf("a mapped public address is dialed as IPv4: %+v", d)
	}

	// Resolver failures are denials, not allowances.
	r.err = errors.New("SERVFAIL")
	if d := p.Check(ctx, "example.com", 443); d.Allowed || d.Rule != "resolve-failed" || !strings.Contains(d.Reason, "SERVFAIL") {
		t.Errorf("resolver error: %+v", d)
	}
}

func TestDomainLists(t *testing.T) {
	answers := map[string][]string{}
	for _, h := range []string{"example.com", "docs.example.com", "a.b.example.com", "evilexample.com", "example.com.evil.com", "other.org", "bad.example.com", "sub.bad.example.com", "ok.bad.example.com.fine.net"} {
		answers[h] = []string{"93.184.216.34"}
	}
	p, _ := newPolicy(t, Config{AllowDomains: []string{"example.com", "Fine.NET."}, DenyDomains: []string{"bad.example.com"}}, answers)
	ctx := context.Background()
	for host, rule := range map[string]string{
		"example.com": "ok", "docs.example.com": "ok", "a.b.example.com": "ok", "EXAMPLE.com": "ok",
		"evilexample.com": "allow-list", "example.com.evil.com": "allow-list", "other.org": "allow-list",
		"bad.example.com": "deny-list", "sub.bad.example.com": "deny-list", // deny beats allow
		"ok.bad.example.com.fine.net": "ok",
		"93.184.216.34":               "allow-list", // with an allow list, address literals are refused
		"8.8.8.8":                     "allow-list",
	} {
		if d := p.Check(ctx, host, 443); d.Rule != rule {
			t.Errorf("Check(%q) rule %q (%s), want %q", host, d.Rule, d.Reason, rule)
		}
	}
	// A deny list alone does not restrict anything else.
	only, _ := newPolicy(t, Config{DenyDomains: []string{"tracker.example"}}, map[string][]string{"tracker.example": {"1.2.3.4"}, "good.example": {"1.2.3.4"}})
	if d := only.Check(ctx, "good.example", 80); !d.Allowed {
		t.Errorf("%+v", d)
	}
	if d := only.Check(ctx, "x.tracker.example", 80); d.Rule != "deny-list" {
		t.Errorf("%+v", d)
	}
}

func TestNewValidatesItsConfiguration(t *testing.T) {
	for name, c := range map[string]Config{
		"port 0":               {ExtraPorts: []int{0}},
		"port 70000":           {ExtraPorts: []int{70000}},
		"allowed IP":           {AllowDomains: []string{"10.0.0.1"}},
		"allowed with a path":  {AllowDomains: []string{"example.com/x"}},
		"denied is an address": {DenyDomains: []string{"127.0.0.1"}},
		"allowed empty":        {AllowDomains: []string{""}},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestDialConnectsToTheVettedAddressOnly(t *testing.T) {
	p, r := newPolicy(t, Config{}, map[string][]string{"example.com": {"93.184.216.34", "2606:2800:220:1::1"}})
	r.second = map[string][]string{"example.com": {"127.0.0.1"}} // a rebinding attempt: the second answer is private
	var dialed []string
	p.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, network+" "+address)
		if len(dialed) == 1 {
			return nil, errors.New("connection refused")
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	conn, d, err := p.Dial(context.Background(), "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if !d.Allowed || len(dialed) != 2 || dialed[0] != "tcp4 93.184.216.34:443" || dialed[1] != "tcp6 [2606:2800:220:1::1]:443" {
		t.Errorf("dialed %v; it must connect to the vetted addresses in order, never to the name", dialed)
	}
	if r.calls["example.com"] != 1 {
		t.Errorf("the name was resolved %d times; once is what closes the rebinding gap", r.calls["example.com"])
	}
	for _, a := range dialed {
		if strings.Contains(a, "127.0.0.1") || strings.Contains(a, "example.com") {
			t.Errorf("dialed %q", a)
		}
	}
}

func TestDialRefusesWithoutDialing(t *testing.T) {
	p, _ := newPolicy(t, Config{}, map[string][]string{"private.example.com": {"10.0.0.5"}})
	var dials atomic.Int32
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("must not be called")
	}
	for _, tc := range []struct {
		host string
		port int
	}{{"private.example.com", 443}, {"127.0.0.1", 80}, {"8.8.8.8", 22}, {"localhost", 80}} {
		conn, d, err := p.Dial(context.Background(), tc.host, tc.port)
		if err == nil || conn != nil || d.Allowed || !strings.Contains(err.Error(), "refused by the egress policy") {
			t.Errorf("%s:%d: conn=%v err=%v", tc.host, tc.port, conn, err)
		}
	}
	if dials.Load() != 0 {
		t.Error("a refused destination must never reach the network")
	}
}

func TestDialFailureIsReported(t *testing.T) {
	p, _ := newPolicy(t, Config{}, nil)
	p.dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("no route") }
	_, d, err := p.Dial(context.Background(), "8.8.8.8", 443)
	if err == nil || !strings.Contains(err.Error(), "no route") || !d.Allowed {
		t.Errorf("%+v %v", d, err)
	}
}

func TestControlHookChecksTheFinalAddress(t *testing.T) {
	p, _ := newPolicy(t, Config{ExtraPorts: []int{8443}}, nil)
	ok := []string{"8.8.8.8:443", "93.184.216.34:80", "[2606:4700:4700::1111]:443", "8.8.8.8:8443"}
	bad := []string{"127.0.0.1:443", "10.0.0.1:80", "192.168.1.1:443", "[::1]:80", "[fe80::1]:443", "169.254.169.254:80", "8.8.8.8:22",
		"8.8.8.8:0", "example.com:443", "8.8.8.8", "8.8.8.8:http", ":443", "[::ffff:127.0.0.1]:80"}
	for _, a := range ok {
		if err := p.control("tcp", a, nil); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	for _, a := range bad {
		if err := p.control("tcp", a, nil); err == nil {
			t.Errorf("%s must be refused at the last moment", a)
		}
	}
}

func TestRealDialerCannotReachLoopback(t *testing.T) {
	// With the real dialer and its Control hook, even handing the dialer a private address fails before connecting.
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // test server on loopback
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = ln.Close() }()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	p, _ := newPolicy(t, Config{ExtraPorts: []int{ln.Addr().(*net.TCPAddr).Port}}, nil)
	dialer := &net.Dialer{Timeout: time.Second, Control: p.control}
	conn, err := dialer.DialContext(context.Background(), "tcp4", ln.Addr().String())
	if err == nil {
		_ = conn.Close()
		t.Fatal("the Control hook must stop a connection to loopback")
	}
	time.Sleep(50 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Error("the loopback listener saw a connection")
	}
	// And through the public entry point: 127.0.0.1 is refused before any dialing.
	if _, _, err := p.Dial(context.Background(), "127.0.0.1", ln.Addr().(*net.TCPAddr).Port); err == nil {
		t.Error("Dial to loopback must fail")
	}
}

func TestSystemResolverIsTheDefault(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.resolver.(systemResolver); !ok {
		t.Errorf("resolver is %T", p.resolver)
	}
	// An address literal never touches DNS.
	if d := p.Check(context.Background(), "8.8.8.8", 443); !d.Allowed {
		t.Errorf("%+v", d)
	}
	// The system resolver refuses localhost-like names without a lookup too.
	if d := p.Check(context.Background(), "localhost", 80); d.Rule != "localhost" {
		t.Errorf("%+v", d)
	}
}

func TestTrackerLimits(t *testing.T) {
	tr := NewTracker(Limits{MaxStreams: 3, ByteBudget: 1000})
	var releases []func()
	for i := 0; i < 3; i++ {
		rel, err := tr.Open()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, rel)
	}
	if _, err := tr.Open(); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("fourth stream: %v", err)
	}
	releases[0]()
	releases[0]() // idempotent
	if n, _ := tr.Stats(); n != 2 {
		t.Errorf("streams = %d, want 2", n)
	}
	rel, err := tr.Open()
	if err != nil {
		t.Fatalf("a released slot is reusable: %v", err)
	}
	rel()

	if err := tr.AddBytes(600); err != nil {
		t.Fatal(err)
	}
	if err := tr.AddBytes(400); err != nil {
		t.Fatalf("exactly the budget is allowed: %v", err)
	}
	if err := tr.AddBytes(1); !errors.Is(err, ErrByteBudget) {
		t.Fatalf("over budget: %v", err)
	}
	if _, err := tr.Open(); !errors.Is(err, ErrByteBudget) {
		t.Errorf("no new streams once the budget is gone: %v", err)
	}
	if _, b := tr.Stats(); b != 1001 {
		t.Errorf("bytes = %d", b)
	}
}

func TestTrackerDefaultsAndConcurrency(t *testing.T) {
	d := DefaultLimits()
	if d.MaxStreams != 32 || d.IdleTimeout != 5*time.Minute || d.ByteBudget != 2<<30 {
		t.Errorf("%+v does not match PLAN.md section 6.11", d)
	}
	tr := NewTracker(Limits{})
	if tr.IdleTimeout() != 5*time.Minute || tr.limits.MaxStreams != 32 || tr.limits.ByteBudget != 2<<30 {
		t.Errorf("zero limits select the defaults: %+v", tr.limits)
	}
	var wg sync.WaitGroup
	var opened atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tr.Open(); err == nil {
				opened.Add(1)
			}
		}()
	}
	wg.Wait()
	if opened.Load() != 32 {
		t.Errorf("%d streams opened concurrently, want exactly the limit of 32", opened.Load())
	}
}

// constResolver answers every name with the same public address.
type constResolver struct{}

func (constResolver) LookupAddrs(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

// FuzzCheck throws arbitrary host strings and ports at the policy. Whatever it is, an allowed destination must
// consist of public addresses on an allowed port only, and a host that is spelled as a non-public address in any
// form must never be allowed.
func FuzzCheck(f *testing.F) {
	for _, s := range []string{
		"", "example.com", "127.0.0.1", "2130706433", "0x7f.1", "017700000001", "[::1]", "::ffff:127.0.0.1", "fe80::1%25eth0",
		"１２７.０.０.１", "localhost", "a.localhost", "169.254.169.254", "10.0.0.1", "xn--", "bücher.example", "example.com.",
		"..", "a..b", "-a.com", "a-.com", "1.2.3.4.5", "0", "00", "0x", "::", "[::]", "[127.0.0.1]", "%", "\x00", "\u202E127.0.0.1",
		"1e3.com", "٣.٣.٣.٣", "0xffffffff", "0377.0377.0377.0377", "192.168.1.1.", "fc00::1", "[fc00::1]:80", "evil.com@127.0.0.1",
	} {
		f.Add(s, 80)
		f.Add(s, 443)
	}
	p, err := New(Config{Resolver: constResolver{}, ExtraPorts: []int{8080}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, host string, port int) {
		d := p.Check(context.Background(), host, port)
		if d.Reason == "" || d.Rule == "" {
			t.Fatalf("every decision explains itself: %+v", d)
		}
		if !d.Allowed {
			if len(d.Addrs) != 0 {
				t.Fatalf("a refusal must not carry addresses: %+v", d)
			}
			return
		}
		if port != 80 && port != 443 && port != 8080 {
			t.Fatalf("allowed port %d for %q", port, host)
		}
		if len(d.Addrs) == 0 {
			t.Fatalf("allowed without an address: %q", host)
		}
		for _, a := range d.Addrs {
			if ok, why := IsPublic(a); !ok || a.Is4In6() {
				t.Fatalf("%q was allowed but would dial %s (%s)", host, a, why)
			}
		}
		if ip, isIP := ParseIPLiteral(host); isIP {
			if ok, why := IsPublic(ip); !ok {
				t.Fatalf("%q is the non-public address %s (%s) but was allowed", host, ip, why)
			}
		}
	})
}
