// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Names of the virtio-serial ports QEMU gives the guest; they appear as /dev/virtio-ports/<name>, and the guest's
// udev rule matches org.controlsnack.* (PLAN.md section 7.5).
const (
	// PortAgent carries the bootstrap and then the framed agent protocol.
	PortAgent = "org.controlsnack.agent.0"
	// PortNet carries the multiplexed network streams of --allow-net (PLAN.md section 7.8).
	PortNet = "org.controlsnack.net.0"
)

// The bootstrap phase (PLAN.md section 5.1): on every boot the guest stub opens the agent's virtio-serial port and
// waits. The host pushes its embedded agent as one text line followed by the raw bytes, the stub checks them and
// answers with one text line, then starts the agent with the port on file descriptor 3.
//
//	host  -> CSBOOT/1 PUSH arch=x86_64 size=1597602 sha256=<hex> nonce=<hex>\n<size raw bytes>
//	guest -> CSBOOT/1 OK sha256=<hex>\n          or          CSBOOT/1 ERR <reason>\n
const (
	// BootMagic starts every bootstrap line and names the bootstrap protocol version.
	BootMagic = "CSBOOT/1"
	// MaxBootLine is the longest bootstrap line, newline included.
	MaxBootLine = 512
	// MaxAgentSize is the largest agent binary the stub accepts.
	MaxAgentSize = 64 << 20
)

var (
	// ErrBootLineTooLong: a bootstrap line exceeded MaxBootLine.
	ErrBootLineTooLong = errors.New("bootstrap line too long")
	// ErrBadBootLine: a bootstrap line could not be understood.
	ErrBadBootLine = errors.New("malformed bootstrap line")

	reHex256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reNonce  = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
	reSize   = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
)

// Architectures a guest agent can be built for, as `uname -m` prints them.
const (
	ArchX86_64  = "x86_64"
	ArchAarch64 = "aarch64"
)

// Push announces the agent binary that follows on the wire.
type Push struct {
	Arch   string
	Size   int64
	SHA256 string // lower-case hex of the raw binary
	Nonce  string // lower-case hex, 8 to 32 random bytes; the agent echoes it in its hello_ack
}

// Validate checks every field against the protocol's limits.
func (p Push) Validate() error {
	switch {
	case p.Arch != ArchX86_64 && p.Arch != ArchAarch64:
		return fmt.Errorf("%w: arch %q is not x86_64 or aarch64", ErrBadBootLine, p.Arch)
	case p.Size < 1 || p.Size > MaxAgentSize:
		return fmt.Errorf("%w: size %d is outside 1..%d", ErrBadBootLine, p.Size, MaxAgentSize)
	case !reHex256.MatchString(p.SHA256):
		return fmt.Errorf("%w: sha256 is not 64 lower-case hex digits", ErrBadBootLine)
	case !reNonce.MatchString(p.Nonce):
		return fmt.Errorf("%w: nonce is not 16 to 64 lower-case hex digits", ErrBadBootLine)
	}
	return nil
}

// Line renders the push line, newline included.
func (p Push) Line() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s PUSH arch=%s size=%d sha256=%s nonce=%s\n", BootMagic, p.Arch, p.Size, p.SHA256, p.Nonce), nil
}

// ParsePush parses a push line with or without its trailing newline.
func ParsePush(line string) (Push, error) {
	line = strings.TrimSuffix(line, "\n")
	if len(line)+1 > MaxBootLine {
		return Push{}, ErrBootLineTooLong
	}
	fields := strings.Split(line, " ")
	if len(fields) != 6 || fields[0] != BootMagic || fields[1] != "PUSH" {
		return Push{}, fmt.Errorf("%w: expected %q PUSH with four fields", ErrBadBootLine, BootMagic)
	}
	kv := map[string]string{}
	for _, f := range fields[2:] {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" || v == "" {
			return Push{}, fmt.Errorf("%w: field %q is not key=value", ErrBadBootLine, f)
		}
		if _, dup := kv[k]; dup {
			return Push{}, fmt.Errorf("%w: field %q repeated", ErrBadBootLine, k)
		}
		kv[k] = v
	}
	for k := range kv {
		switch k {
		case "arch", "size", "sha256", "nonce":
		default:
			return Push{}, fmt.Errorf("%w: unknown field %q", ErrBadBootLine, k)
		}
	}
	// Plain decimal only: ParseInt alone would also take "+10", and a line has one spelling.
	size, err := strconv.ParseInt(kv["size"], 10, 64)
	if err != nil || !reSize.MatchString(kv["size"]) {
		return Push{}, fmt.Errorf("%w: size %q is not a plain decimal number", ErrBadBootLine, kv["size"])
	}
	p := Push{Arch: kv["arch"], Size: size, SHA256: kv["sha256"], Nonce: kv["nonce"]}
	if err := p.Validate(); err != nil {
		return Push{}, err
	}
	return p, nil
}

// Reply is the guest's answer to a push.
type Reply struct {
	OK     bool
	SHA256 string // on OK: the hash the stub computed, which must equal the one pushed
	Reason string // on ERR: why
}

// Line renders the reply line, newline included. Reasons are cut to one short line of printable text.
func (r Reply) Line() string {
	if r.OK {
		return fmt.Sprintf("%s OK sha256=%s\n", BootMagic, r.SHA256)
	}
	return fmt.Sprintf("%s ERR %s\n", BootMagic, cleanReason(r.Reason))
}

func cleanReason(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
		if b.Len() >= 200 {
			break
		}
	}
	if b.Len() == 0 {
		return "unspecified"
	}
	return b.String()
}

// ParseReply parses a reply line with or without its trailing newline.
func ParseReply(line string) (Reply, error) {
	line = strings.TrimSuffix(line, "\n")
	if len(line)+1 > MaxBootLine {
		return Reply{}, ErrBootLineTooLong
	}
	rest, ok := strings.CutPrefix(line, BootMagic+" ")
	if !ok {
		return Reply{}, fmt.Errorf("%w: missing %s", ErrBadBootLine, BootMagic)
	}
	switch {
	case strings.HasPrefix(rest, "OK sha256="):
		sum := strings.TrimPrefix(rest, "OK sha256=")
		if !reHex256.MatchString(sum) {
			return Reply{}, fmt.Errorf("%w: OK without a valid sha256", ErrBadBootLine)
		}
		return Reply{OK: true, SHA256: sum}, nil
	case strings.HasPrefix(rest, "ERR "):
		return Reply{Reason: strings.TrimPrefix(rest, "ERR ")}, nil
	default:
		return Reply{}, fmt.Errorf("%w: neither OK nor ERR", ErrBadBootLine)
	}
}

// ReadBootLine reads one bootstrap line (newline included) without ever buffering more than MaxBootLine bytes of
// an unterminated one. Bytes after the newline stay in br, which matters because the frames that follow the
// bootstrap arrive on the same stream.
func ReadBootLine(br *bufio.Reader) (string, error) {
	var line []byte
	for len(line) < MaxBootLine {
		b, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		line = append(line, b)
		if b == '\n' {
			return string(line), nil
		}
	}
	return "", ErrBootLineTooLong
}
