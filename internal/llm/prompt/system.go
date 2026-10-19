// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package prompt

import (
	"bytes"
	"crypto/sha256"
	_ "embed" // system.tmpl
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
)

// SystemVersion is the version of the system prompt. It changes whenever system.tmpl does in a way a model could notice;
// the audit log records it with the hash of the template.
const SystemVersion = 1

//go:embed system.tmpl
var systemTemplate string

var systemTmpl = template.Must(template.New("system").Option("missingkey=error").Parse(systemTemplate))

// TemplateSHA256 is the hash of the embedded template: it names the wording of the prompt, whatever the session's
// parameters, and goes in the audit record of the session's start.
func TemplateSHA256() string {
	sum := sha256.Sum256([]byte(systemTemplate))
	return hex.EncodeToString(sum[:])
}

// Params are what the system prompt says about this session.
type Params struct {
	// Nonce is the session's fence nonce (untrusted.Fencer.Nonce): the prompt names it so that the model can tell a real
	// fence from text that imitates one.
	Nonce string
	// Width and Height are the size of the images the model gets, which is the coordinate space of everything it is told
	// and everything it says; in text-only mode they are the size of the screen.
	Width, Height int
	// MaxActions is the limit on actions in one answer.
	MaxActions int
	// NetAllowed says whether the machine has the filtered path to the internet; NetDomains the sites it is limited to,
	// when it is limited.
	NetAllowed bool
	NetDomains []string
	// Vision is false in text-only mode.
	Vision bool
	// Relative says the model points on a scale of 0 to Width across and 0 to Height down, whatever the picture's size in
	// pixels (the Qwen-VL family does): Width and Height are then the top of that scale, 1000 by 1000.
	Relative bool
	// NoTree says the session cannot read the accessibility tree: there are no ids to click, and the prompt says to point instead.
	NoTree bool
	// Marks says the images carry numbered boxes over the elements of the accessibility tree.
	Marks bool
	// Mode is how the model names its actions; it decides how the prompt asks for the answer.
	Mode backend.Mode
	// Set is the tools of the session. In a mode that does not pass the tools to the backend as definitions, the prompt
	// describes them.
	Set *actions.Set
}

// Rendered is a system prompt with what identifies it.
type Rendered struct {
	Text string
	// Version is SystemVersion; TemplateSHA256 the hash of the template; TextSHA256 the hash of this text.
	Version        int
	TemplateSHA256 string
	TextSHA256     string
}

// maxNetDomains bounds how many sites the prompt lists.
const maxNetDomains = 20

// System renders the system prompt.
func System(p Params) (Rendered, error) {
	if !validNonce(p.Nonce) {
		return Rendered{}, errors.New("the fence nonce must be 4 to 64 letters and digits")
	}
	if p.Width < 1 || p.Height < 1 {
		return Rendered{}, fmt.Errorf("the screen size %dx%d is not a size", p.Width, p.Height)
	}
	if p.MaxActions < 1 {
		return Rendered{}, fmt.Errorf("the limit on actions in one answer is %d, at least 1 is needed", p.MaxActions)
	}
	switch p.Mode {
	case backend.ModeNative, backend.ModeStructured, backend.ModeJSON:
	default:
		return Rendered{}, fmt.Errorf("mode %q is not native, structured or json", p.Mode)
	}
	if p.Set == nil {
		return Rendered{}, errors.New("the prompt needs the tools of the session")
	}
	data := struct {
		Params
		W, H        int
		MidX, MidY  int
		NativeTools bool
		NetDomains  string
		ToolDocs    string
	}{Params: p, W: p.Width, H: p.Height, MidX: p.Width / 2, MidY: p.Height / 2, NativeTools: p.Mode == backend.ModeNative}
	data.NetDomains = joinDomains(p.NetDomains)
	if !data.NativeTools {
		docs, err := ToolDocs(p.Set)
		if err != nil {
			return Rendered{}, err
		}
		data.ToolDocs = strings.TrimRight(docs, "\n")
	}
	var b bytes.Buffer
	if err := systemTmpl.Execute(&b, data); err != nil {
		return Rendered{}, fmt.Errorf("rendering the system prompt: %w", err)
	}
	text := b.String()
	sum := sha256.Sum256([]byte(text))
	return Rendered{Text: text, Version: SystemVersion, TemplateSHA256: TemplateSHA256(), TextSHA256: hex.EncodeToString(sum[:])}, nil
}

func validNonce(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		digit, lower, upper := r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z'
		if !digit && !lower && !upper {
			return false
		}
	}
	return true
}

func joinDomains(domains []string) string {
	var out []string
	for i, d := range domains {
		if i == maxNetDomains {
			out = append(out, fmt.Sprintf("and %d more", len(domains)-maxNetDomains))
			break
		}
		if d = textclean.Line(d, 80); d != "" {
			out = append(out, d)
		}
	}
	return strings.Join(out, ", ")
}
