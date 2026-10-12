// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vm

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/platform"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qemu"
	"github.com/PacifAIst/controlsnack-local/internal/vm/qmp"
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

func endpointAddr(ep qemu.Endpoint) (network, addr string, err error) {
	switch {
	case ep.Path != "" && ep.Port == 0:
		return "unix", ep.Path, nil
	case ep.Path == "" && ep.Port >= 1 && ep.Port <= 65535:
		return "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port)), nil
	}
	return "", "", fmt.Errorf("endpoint %+v is neither a socket path nor a port", ep)
}

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

// NewSocketDir creates the private directory a VM's unix sockets live in. It is kept short because a unix socket
// path must fit in sun_path (about 104 bytes, D-28), and is readable by this user only.
func NewSocketDir() (dir string, cleanup func(), err error) {
	base := os.TempDir()
	if len(base) > 48 {
		base = "/tmp"
	}
	dir, err = os.MkdirTemp(base, "cs-")
	if err != nil {
		return "", nil, fmt.Errorf("creating the socket directory: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// AllocateEndpoints returns one endpoint per name. On Linux and macOS they are unix sockets in dir; on Windows
// they are free loopback TCP ports (D-28), found by listening on all of them at once so they differ, then
// releasing them. Another process could take a port in between; QEMU then fails to listen and the launch fails
// visibly rather than serving a stranger.
func AllocateEndpoints(ctx context.Context, goos, dir string, names ...string) ([]qemu.Endpoint, error) {
	out := make([]qemu.Endpoint, len(names))
	if goos != "windows" {
		for i, n := range names {
			out[i] = qemu.Endpoint{Path: dir + "/" + n + ".sock"} // unix sockets: a unix path on every host
		}
		return out, nil
	}
	var held []net.Listener
	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()
	for i := range names {
		l, err := platform.ListenLoopback(ctx, 0)
		if err != nil {
			return nil, fmt.Errorf("finding a free port: %w", err)
		}
		held = append(held, l)
		out[i] = qemu.Endpoint{Port: l.Addr().(*net.TCPAddr).Port}
	}
	return out, nil
}
