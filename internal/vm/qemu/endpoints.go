// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qemu

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/PacifAIst/controlsnack/internal/platform"
)

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
func AllocateEndpoints(ctx context.Context, goos, dir string, names ...string) ([]Endpoint, error) {
	out := make([]Endpoint, len(names))
	if goos != "windows" {
		for i, n := range names {
			out[i] = Endpoint{Path: dir + "/" + n + ".sock"} // unix sockets: a unix path on every host
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
		out[i] = Endpoint{Port: l.Addr().(*net.TCPAddr).Port}
	}
	return out, nil
}

// NetAddr returns the network and address to dial to reach the endpoint: a unix socket path, or 127.0.0.1 and the
// port.
func (e Endpoint) NetAddr() (network, addr string, err error) {
	switch {
	case e.Path != "" && e.Port == 0:
		return "unix", e.Path, nil
	case e.Path == "" && e.Port >= 1 && e.Port <= 65535:
		return "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.Port)), nil
	}
	return "", "", fmt.Errorf("endpoint %+v is neither a socket path nor a port", e)
}
