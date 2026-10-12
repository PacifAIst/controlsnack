// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// Frame limits (PLAN.md section 5.1). A peer that exceeds them is not slowed down or truncated; the connection is
// reset and the agent is marked bad.
const (
	// Version is the frame format version carried in every header.
	Version = 1
	// MaxHeaderBytes bounds the JSON header.
	MaxHeaderBytes = 1 << 20
	// MaxBlobBytes bounds the binary attachment.
	MaxBlobBytes = 8 << 20
)

// Header is the JSON part of a frame. On the wire a frame is
//
//	[uint32 big-endian header length][header JSON][blob_len bytes of attachment]
//
// Requests carry a non-zero ID that the response echoes; events sent by the agent on its own use ID 0.
type Header struct {
	V       int             `json:"v"`
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	BlobLen int             `json:"blob_len,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

// Frame is a header plus its attachment. Images, file heads and similar binary data travel as the blob, never
// base64 inside the JSON (D-12).
type Frame struct {
	Header Header
	Blob   []byte
}

// Errors returned for protocol violations. All of them mean the peer is misbehaving or the stream is corrupt: the
// caller must drop the connection (Conn does).
var (
	ErrFrameTooLarge = errors.New("frame exceeds the protocol limits")
	ErrBadFrame      = errors.New("malformed frame")
)

var reType = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

// Validate checks a frame before it is sent or after it is received.
func (f Frame) Validate() error {
	h := f.Header
	switch {
	case h.V != Version:
		return fmt.Errorf("%w: unsupported frame version %d", ErrBadFrame, h.V)
	case len(h.Type) == 0 || len(h.Type) > 64 || !reType.MatchString(h.Type):
		return fmt.Errorf("%w: invalid message type %q", ErrBadFrame, h.Type)
	case h.BlobLen < 0 || h.BlobLen > MaxBlobBytes:
		return fmt.Errorf("%w: blob length %d (limit %d)", ErrFrameTooLarge, h.BlobLen, MaxBlobBytes)
	case h.BlobLen != len(f.Blob):
		return fmt.Errorf("%w: header announces %d blob bytes, frame carries %d", ErrBadFrame, h.BlobLen, len(f.Blob))
	}
	return nil
}

// NewFrame builds a frame with the given id and type; body (if not nil) is marshaled into the header and blob is
// attached.
func NewFrame(id uint64, typ string, body any, blob []byte) (Frame, error) {
	f := Frame{Header: Header{V: Version, ID: id, Type: typ, BlobLen: len(blob)}, Blob: blob}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return Frame{}, fmt.Errorf("encoding %s body: %w", typ, err)
		}
		f.Header.Body = data
	}
	return f, nil
}

// WriteFrame writes one frame. The caller serializes writers (Conn does).
func WriteFrame(w io.Writer, f Frame) error {
	f.Header.BlobLen = len(f.Blob)
	if f.Header.V == 0 {
		f.Header.V = Version
	}
	if err := f.Validate(); err != nil {
		return err
	}
	head, err := json.Marshal(f.Header)
	if err != nil {
		return fmt.Errorf("encoding frame header: %w", err)
	}
	if len(head) > MaxHeaderBytes {
		return fmt.Errorf("%w: header is %d bytes (limit %d)", ErrFrameTooLarge, len(head), MaxHeaderBytes)
	}
	buf := make([]byte, 4, 4+len(head)+len(f.Blob))
	binary.BigEndian.PutUint32(buf, uint32(len(head))) //nolint:gosec // bounded by MaxHeaderBytes above
	buf = append(buf, head...)
	buf = append(buf, f.Blob...)
	_, err = w.Write(buf) // one write: frames from different goroutines never interleave on a datagram-like port
	return err
}

// ReadFrame reads and validates one frame. It never allocates more than the limits allow, whatever the peer
// announces, and rejects unknown header fields, trailing data in the header and inconsistent lengths. An error
// that is not io.EOF or a plain read error is a protocol violation.
func ReadFrame(r io.Reader) (Frame, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return Frame{}, err // io.EOF here is a clean end of stream
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 {
		return Frame{}, fmt.Errorf("%w: empty header", ErrBadFrame)
	}
	if n > MaxHeaderBytes {
		return Frame{}, fmt.Errorf("%w: header of %d bytes (limit %d)", ErrFrameTooLarge, n, MaxHeaderBytes)
	}
	head := make([]byte, n)
	if _, err := io.ReadFull(r, head); err != nil {
		return Frame{}, unexpected(err)
	}
	var h Header
	dec := json.NewDecoder(bytes.NewReader(head))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return Frame{}, fmt.Errorf("%w: header: %w", ErrBadFrame, err)
	}
	if dec.More() {
		return Frame{}, fmt.Errorf("%w: data after the header object", ErrBadFrame)
	}
	if h.BlobLen < 0 || h.BlobLen > MaxBlobBytes {
		return Frame{}, fmt.Errorf("%w: blob of %d bytes (limit %d)", ErrFrameTooLarge, h.BlobLen, MaxBlobBytes)
	}
	f := Frame{Header: h}
	if h.BlobLen > 0 {
		f.Blob = make([]byte, h.BlobLen)
		if _, err := io.ReadFull(r, f.Blob); err != nil {
			return Frame{}, unexpected(err)
		}
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// unexpected turns an EOF in the middle of a frame into io.ErrUnexpectedEOF.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// IsViolation reports whether err means the peer broke the protocol (as opposed to the stream simply ending).
func IsViolation(err error) bool {
	return errors.Is(err, ErrBadFrame) || errors.Is(err, ErrFrameTooLarge)
}

// DecodeBody strictly decodes a frame's body into v: unknown fields are errors, and an absent body decodes as
// an empty object.
func DecodeBody(f Frame, v any) error {
	body := f.Header.Body
	if len(body) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %s body: %w", ErrBadFrame, f.Header.Type, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: %s body: data after the object", ErrBadFrame, f.Header.Type)
	}
	return nil
}
