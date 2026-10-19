// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/PacifAIst/controlsnack/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack/internal/vm/qmp"
)

// RealBackend starts the real QEMU through the supervisor and reaches its chardevs over unix sockets or loopback
// TCP.
func RealBackend() Backend {
	return Backend{
		Start: func(ctx context.Context, argv []string, opt qemu.StartOptions) (Process, error) {
			p, err := qemu.Start(ctx, argv, opt)
			if err != nil {
				return nil, err // not a typed nil inside the interface
			}
			return p, nil
		},
		DialQMP: func(ctx context.Context, ep qemu.Endpoint) (QMP, error) {
			network, addr, err := endpointAddr(ep)
			if err != nil {
				return nil, err
			}
			c, err := qmp.Dial(ctx, network, addr, qmp.DialOptions{})
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		DialStream: func(ctx context.Context, ep qemu.Endpoint) (net.Conn, error) {
			network, addr, err := endpointAddr(ep)
			if err != nil {
				return nil, err
			}
			return dialRetry(ctx, network, addr)
		},
	}
}

func endpointAddr(ep qemu.Endpoint) (network, addr string, err error) { return ep.NetAddr() }

// dialRetry connects to a chardev QEMU has not yet created or is not yet serving.
func dialRetry(ctx context.Context, network, addr string) (net.Conn, error) {
	var last error
	for {
		actx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var d net.Dialer
		conn, err := d.DialContext(actx, network, addr)
		cancel()
		if err == nil {
			return conn, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connecting to %s %s: %w (last attempt: %w)", network, addr, ctx.Err(), last)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
