// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package netproxy

import (
	"io"
	"net"
	"time"
)

type closeWriter interface{ CloseWrite() error }

// idleConn refreshes a deadline before every read and write, so a connection ends when it has been silent for the idle
// time and not when it has been open that long.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c idleConn) Read(p []byte) (int, error) {
	if c.idle > 0 {
		_ = c.SetReadDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Read(p)
}

func (c idleConn) Write(p []byte) (int, error) {
	if c.idle > 0 {
		_ = c.SetWriteDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Write(p)
}

// splice copies bytes both ways between two connections until both directions have ended, then closes both. When a
// direction ends, its destination is half-closed if it can be (a TCP connection to the browser or the server can; a
// multiplexed stream is not, see fullClose), so the other side sees the end; the other direction then gets a few
// seconds to finish: a server that answers after the request has been sent in full is the normal case.
func splice(a, b net.Conn, idle time.Duration) {
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(idleConn{dst, idle}, idleConn{src, idle})
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(a, b)
	go pipe(b, a)
	<-done
	select {
	case <-done:
	case <-time.After(halfCloseGrace):
	}
	_ = a.Close()
	_ = b.Close()
	// Closing ends the copy that was still running.
	select {
	case <-done:
	case <-time.After(halfCloseGrace):
	}
}
