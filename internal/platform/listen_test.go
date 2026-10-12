// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"context"
	"net"
	"testing"
)

func TestListenLoopbackBindsOnlyLoopback(t *testing.T) {
	l, err := ListenLoopback(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	tcp := l.Addr().(*net.TCPAddr)
	if !tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("bound to %v, want 127.0.0.1", tcp.IP)
	}
	if tcp.Port == 0 {
		t.Fatal("port 0 must resolve to a real port")
	}
	// Reachable from loopback.
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "tcp4", l.Addr().String())
	if err != nil {
		t.Fatalf("loopback dial: %v", err)
	}
	_ = c.Close()
}

func TestListenLoopbackRejectsBadPorts(t *testing.T) {
	for _, p := range []int{-1, 65536, 1 << 20} {
		if l, err := ListenLoopback(context.Background(), p); err == nil {
			_ = l.Close()
			t.Errorf("port %d should be rejected", p)
		}
	}
}

func TestListenLoopbackFailsWhenThePortIsTaken(t *testing.T) {
	l, err := ListenLoopback(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	if l2, err := ListenLoopback(context.Background(), port); err == nil {
		_ = l2.Close()
		t.Fatal("a second listener on the same port must fail, not fall back to another address")
	}
}

func TestAssertLoopbackAddr(t *testing.T) {
	ok := []net.Addr{
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		&net.TCPAddr{IP: net.IPv4(127, 5, 6, 7), Port: 1},
		&net.TCPAddr{IP: net.IPv6loopback, Port: 1},
	}
	for _, a := range ok {
		if err := AssertLoopbackAddr(a); err != nil {
			t.Errorf("%v should be accepted: %v", a, err)
		}
	}
	bad := []net.Addr{
		&net.TCPAddr{IP: net.IPv4zero, Port: 1},
		&net.TCPAddr{IP: net.IPv6unspecified, Port: 1},
		&net.TCPAddr{IP: net.IPv4(192, 168, 1, 10), Port: 1},
		&net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1},
		&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1},
		&net.TCPAddr{Port: 1}, // nil IP means all interfaces
		&net.UnixAddr{Name: "/tmp/x", Net: "unix"},
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
	}
	for _, a := range bad {
		if err := AssertLoopbackAddr(a); err == nil {
			t.Errorf("%v (%T) must be refused", a, a)
		}
	}
}
