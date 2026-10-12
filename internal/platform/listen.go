// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// ListenLoopback opens a TCP listener on 127.0.0.1. Port 0 picks a free port.
//
// This is the only function in the code base that opens a listener (PLAN.md section 6.1, invariant 1;
// `go run ./tools/dev check` rejects any other file that does). The address is IPv4 loopback by construction and
// is verified again after binding, so a platform that rewrites the address cannot silently expose the viewer.
func ListenLoopback(ctx context.Context, port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("port %d is not valid", port)
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listening on 127.0.0.1:%d: %w", port, err)
	}
	if err := AssertLoopbackAddr(l.Addr()); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// AssertLoopbackAddr returns an error unless addr is a TCP address on the IPv4 or IPv6 loopback interface.
func AssertLoopbackAddr(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("listener address %v is not a TCP address", addr)
	}
	if tcp.IP == nil || tcp.IP.IsUnspecified() || !tcp.IP.IsLoopback() {
		return fmt.Errorf("listener address %v is not on the loopback interface; refusing to serve", addr)
	}
	return nil
}
