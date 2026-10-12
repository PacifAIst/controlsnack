// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bufio"
	"io"
	"sync"
	"sync/atomic"
)

// Conn is a framed connection over a byte stream (a virtio-serial port). Writes are serialized, so any number of
// goroutines may Send; Recv is meant for a single reading goroutine.
//
// A protocol violation (oversize or malformed frame) marks the connection bad and closes it: there is no
// recovering the framing of a corrupt stream, and a hostile peer must not get a second try.
type Conn struct {
	r *bufio.Reader
	w io.Writer
	c io.Closer

	wmu sync.Mutex
	bad atomic.Bool
}

// NewConn wraps a stream. br must be the reader any earlier bootstrap lines were read from, so bytes already
// buffered behind them are not lost.
func NewConn(br *bufio.Reader, w io.Writer, c io.Closer) *Conn {
	return &Conn{r: br, w: w, c: c}
}

// Send writes one frame.
func (c *Conn) Send(f Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.bad.Load() {
		return io.ErrClosedPipe
	}
	if err := WriteFrame(c.w, f); err != nil {
		if !IsViolation(err) { // a frame we refused to build is a bug of ours, not a broken stream
			c.markBad()
		}
		return err
	}
	return nil
}

// Recv reads the next frame. On a protocol violation the connection is closed and marked bad.
func (c *Conn) Recv() (Frame, error) {
	f, err := ReadFrame(c.r)
	if err != nil && IsViolation(err) {
		c.markBad()
	}
	return f, err
}

// Bad reports whether the connection was shut down because of a protocol violation or a failed write.
func (c *Conn) Bad() bool { return c.bad.Load() }

func (c *Conn) markBad() {
	if c.bad.CompareAndSwap(false, true) && c.c != nil {
		_ = c.c.Close()
	}
}

// Close closes the underlying stream.
func (c *Conn) Close() error {
	if c.c == nil {
		return nil
	}
	return c.c.Close()
}
