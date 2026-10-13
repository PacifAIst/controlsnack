// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xtaci/smux"
)

// The net bridge (PLAN.md section 7.8): the guest opens one multiplexed stream for each connection its proxy makes. A
// stream begins with a line of JSON from the guest, naming where it wants to go; the host answers with a line of JSON,
// and after "ok" the stream carries the bytes of the connection both ways.

// MaxNetLine bounds the two lines: a host name is at most 253 characters, and a reason a few hundred.
const MaxNetLine = 1024

// NetRequest asks the host to connect to a host name or an address and a port. The guest never resolves names.
type NetRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// NetReply answers a NetRequest. Error is a sentence a model can read, set when OK is false.
type NetReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ErrNetLine means a line of the net protocol is too long, empty, or not what was expected.
var ErrNetLine = errors.New("bad net bridge line")

// WriteNetRequest writes the first line of a stream.
func WriteNetRequest(w io.Writer, r NetRequest) error { return writeNetLine(w, r) }

// WriteNetReply writes the host's answer.
func WriteNetReply(w io.Writer, r NetReply) error { return writeNetLine(w, r) }

func writeNetLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) >= MaxNetLine {
		return fmt.Errorf("%w: %d bytes", ErrNetLine, len(b))
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadNetRequest reads the first line of a stream. It reads one byte at a time so that nothing past the line is
// consumed: the bytes after it belong to the connection.
func ReadNetRequest(r io.Reader) (NetRequest, error) {
	var req NetRequest
	if err := readNetLine(r, &req); err != nil {
		return req, err
	}
	switch {
	case req.Host == "" || len(req.Host) > 253:
		return req, fmt.Errorf("%w: host is empty or longer than 253 characters", ErrNetLine)
	case req.Port < 1 || req.Port > 65535:
		return req, fmt.Errorf("%w: port %d", ErrNetLine, req.Port)
	case strings.ContainsAny(req.Host, " \t\r\n\x00/\\@"):
		return req, fmt.Errorf("%w: host has characters a host name does not", ErrNetLine)
	}
	return req, nil
}

// ReadNetReply reads the host's answer.
func ReadNetReply(r io.Reader) (NetReply, error) {
	var rep NetReply
	if err := readNetLine(r, &rep); err != nil {
		return rep, err
	}
	if !rep.OK && rep.Error == "" {
		rep.Error = "refused"
	}
	return rep, nil
}

func readNetLine(r io.Reader, v any) error {
	var line bytes.Buffer
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			if line.Len() >= MaxNetLine {
				return fmt.Errorf("%w: longer than %d bytes", ErrNetLine, MaxNetLine)
			}
			line.WriteByte(one[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) && line.Len() > 0 {
				return fmt.Errorf("%w: the line has no end", ErrNetLine)
			}
			return err
		}
	}
	if line.Len() == 0 {
		return fmt.Errorf("%w: empty", ErrNetLine)
	}
	dec := json.NewDecoder(bytes.NewReader(line.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrNetLine, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: more than one value", ErrNetLine)
	}
	return nil
}

// NetMuxConfig is the multiplexer's setting, the same on both ends of the bridge. Version 2 gives each stream a window of
// its own, so one slow connection does not hold the others up. Streams are only ever closed whole: smux's half-close
// loses the last bytes of an answer in a few exchanges of a hundred (D-66).
func NetMuxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	return c
}
