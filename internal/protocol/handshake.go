// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ProtoVersion is the handshake protocol version. Peers must agree on the major number; minor numbers only add.
const ProtoVersion = "1.0"

// Capabilities an agent announces in its hello_ack.
const (
	CapScreenshot = "screenshot"
	CapInput      = "input"
	CapExec       = "exec"
	CapWindows    = "windows"
	CapA11y       = "a11y"
	CapOCR        = "ocr"
	CapBrowser    = "browser"
	CapWaitFor    = "wait_for"
	CapFilePeek   = "file_peek"
	CapNetProxy   = "netproxy"
)

// Features are what the host asks the agent to switch on.
type Features struct {
	// Net starts the guest proxy that tunnels over the second virtio-serial port (--allow-net).
	Net bool `json:"net"`
}

// Hello is the first frame, host to agent.
type Hello struct {
	Proto       string   `json:"proto"`
	HostVersion string   `json:"host_version"`
	SessionID   string   `json:"session_id"`
	Nonce       string   `json:"nonce"`
	Features    Features `json:"features"`
}

// Display is the guest screen size.
type Display struct {
	W int `json:"w"`
	H int `json:"h"`
}

// HelloAck is the agent's answer.
type HelloAck struct {
	Proto        string   `json:"proto"`
	AgentVersion string   `json:"agent_version"`
	AgentSHA256  string   `json:"agent_sha256"` // of the agent binary (the raw one that was pushed)
	Nonce        string   `json:"nonce"`
	Arch         string   `json:"arch"`
	User         string   `json:"user"`
	Display      Display  `json:"display"`
	Capabilities []string `json:"capabilities"`
}

// ProtoMajor extracts the major version of a "major.minor" string.
func ProtoMajor(proto string) (int, error) {
	major, _, ok := strings.Cut(proto, ".")
	if !ok {
		return 0, fmt.Errorf("protocol version %q is not major.minor", proto)
	}
	n, err := strconv.Atoi(major)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("protocol version %q is not major.minor", proto)
	}
	return n, nil
}

// Expect is what the host requires of a hello_ack.
type Expect struct {
	// Nonce must be echoed (it ties the agent to this very push).
	Nonce string
	// AgentSHA256 is the hash of the agent the host pushed; the agent must report the same binary.
	AgentSHA256 string
	// Arch is the architecture that was pushed.
	Arch string
	// User is the account the agent must run as (cs): an agent running as root would break the isolation model.
	User string
	// Required capabilities that must all be present.
	Required []string
}

// ErrHandshake wraps every handshake validation failure.
var ErrHandshake = errors.New("handshake failed")

// Validate checks an acknowledgement against what the host expects. Every field comes from the guest and so from
// a possibly compromised party; the checks fail closed.
func (a HelloAck) Validate(e Expect) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrHandshake, fmt.Sprintf(format, args...))
	}
	major, err := ProtoMajor(a.Proto)
	if err != nil {
		return fail("%v", err)
	}
	want, _ := ProtoMajor(ProtoVersion)
	if major != want {
		return fail("the agent speaks protocol %s, this host speaks %s", a.Proto, ProtoVersion)
	}
	if a.Nonce != e.Nonce {
		return fail("the agent did not echo the nonce of this session")
	}
	if a.AgentSHA256 != e.AgentSHA256 {
		return fail("the agent reports a different binary than the one pushed")
	}
	if e.Arch != "" && a.Arch != e.Arch {
		return fail("the agent reports architecture %q, expected %q", a.Arch, e.Arch)
	}
	if e.User != "" && a.User != e.User {
		return fail("the agent runs as %q, expected %q", a.User, e.User)
	}
	if a.Display.W < 320 || a.Display.H < 200 || a.Display.W > 7680 || a.Display.H > 4320 {
		return fail("implausible display size %dx%d", a.Display.W, a.Display.H)
	}
	have := map[string]bool{}
	for _, c := range a.Capabilities {
		have[c] = true
	}
	var missing []string
	for _, c := range e.Required {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return fail("the agent lacks required capabilities: %s", strings.Join(missing, ", "))
	}
	if len(a.AgentVersion) == 0 || len(a.AgentVersion) > 128 {
		return fail("agent version is missing or absurdly long")
	}
	return nil
}
