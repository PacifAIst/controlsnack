// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package qmp

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// FuzzServerStream feeds arbitrary bytes to the client as if QEMU had sent them after the handshake. Whatever
// arrives, the client must not panic or hang: it either keeps going or ends the connection and releases callers.
func FuzzServerStream(f *testing.F) {
	f.Add([]byte(`{"return": {}, "id": 1}` + "\r\n"))
	f.Add([]byte(`{"event": "VSERPORT_CHANGE", "data": {"id": "agent0", "open": true}, "timestamp": {"seconds": 1, "microseconds": 2}}` + "\n"))
	f.Add([]byte(`{"event": "VSERPORT_CHANGE", "data": "not an object"}` + "\n"))
	f.Add([]byte(`{"error": {"class": 5}, "id": 2}` + "\n"))
	f.Add([]byte(`{"return": {}, "id": 18446744073709551616}` + "\n"))
	f.Add([]byte("\x00\xff\xfe{\n[[[[[[[[\n"))
	f.Add([]byte(`{"event": ""}` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			_, _ = io.WriteString(b, greeting+"\r\n")
			r := bufio.NewReader(b)
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = io.WriteString(b, `{"return": {}, "id": 1}`+"\r\n")
			_ = line
			_, _ = b.Write(data)
			// Keep the pipe open a moment so the client reads everything, then end it.
			time.Sleep(5 * time.Millisecond)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c, err := NewClient(ctx, a)
		if err != nil {
			return
		}
		defer c.Close()
		select {
		case <-c.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the client did not notice the end of the stream")
		}
		_ = c.Events()
		_ = c.PortOpen("agent0")
		if _, err := c.QueryStatus(ctx); err == nil {
			t.Fatal("a command on a finished connection cannot succeed")
		}
	})
}
