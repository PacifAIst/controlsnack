// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"context"
	"fmt"
	"net"
	"time"
)

// DialOptions tune the connection attempts.
type DialOptions struct {
	// Interval is the pause between attempts; default 50 ms.
	Interval time.Duration
	// AttemptTimeout bounds one attempt, connect and greeting; default 3 s.
	AttemptTimeout time.Duration
}

// Dial connects to QEMU's QMP chardev and keeps trying until ctx ends. QEMU creates its chardevs one after the
// other and waits for each connection (wait=on), so the socket may not exist yet, or may exist and not be served
// yet; both are retried. network is "unix" or "tcp"; a TCP address must be on 127.0.0.1 (D-28).
func Dial(ctx context.Context, network, address string, opt DialOptions) (*Client, error) {
	if err := checkEndpoint(network, address); err != nil {
		return nil, err
	}
	if opt.Interval <= 0 {
		opt.Interval = 50 * time.Millisecond
	}
	if opt.AttemptTimeout <= 0 {
		opt.AttemptTimeout = 3 * time.Second
	}
	var last error
	for {
		actx, cancel := context.WithTimeout(ctx, opt.AttemptTimeout)
		var d net.Dialer
		conn, err := d.DialContext(actx, network, address)
		if err == nil {
			var c *Client
			if c, err = NewClient(actx, conn); err == nil {
				cancel()
				return c, nil
			}
		}
		cancel()
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connecting to QMP at %s %s: %w (last attempt: %w)", network, address, ctx.Err(), last)
		case <-time.After(opt.Interval):
		}
	}
}

func checkEndpoint(network, address string) error {
	switch network {
	case "unix":
		if address == "" {
			return fmt.Errorf("no QMP socket path")
		}
	case "tcp":
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" {
			return fmt.Errorf("QMP over TCP is only reached on 127.0.0.1, not %q", address)
		}
	default:
		return fmt.Errorf("QMP network %q is not unix or tcp", network)
	}
	return nil
}
