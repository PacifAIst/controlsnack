// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/exitcode"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/anthropic"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/httpx"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/ollama"
	"github.com/PacifAIst/controlsnack/internal/llm/backend/openai"
)

// Protocol is the API a backend speaks.
type Protocol string

// The three APIs of the plan.
const (
	ProtocolOllama    Protocol = "ollama"
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
)

// Target is the backend and model a session will talk to.
type Target struct {
	// Backend is the --llm value that names it: ollama, lmstudio, llamacpp, openai or anthropic.
	Backend  string
	Protocol Protocol
	// BaseURL is the address, validated (no credentials), with no trailing slash.
	BaseURL string
	Model   string
	// APIKey is the key to send, if there is one. It is a secret: it is never printed, logged or cached.
	APIKey string
	// NumCtx is the context window to ask Ollama for, 0 for its default.
	NumCtx int
	// Local is whether the address is on this machine (a loopback name or address).
	Local bool
}

// Remote reports whether what a session sends goes off this machine.
func (t Target) Remote() bool { return !t.Local }

// Notice is what the terminal and the viewer say when the backend is not on this machine: the audit log records the same
// as remote_llm. It is empty for a local backend.
func (t Target) Notice() string {
	if t.Local {
		return ""
	}
	host := t.BaseURL
	if u, err := url.Parse(t.BaseURL); err == nil {
		host = u.Host
	}
	return fmt.Sprintf("The model runs on %s, not on this machine: screenshots, screen text and command output are sent to it.", textclean.Line(host, 120))
}

// Candidate is a local server that `--llm auto` looks for.
type Candidate struct {
	Backend string
	BaseURL string
	// Path is what is asked of it to find out it is there.
	Path string
}

// DefaultCandidates are tried in order by `--llm auto` (PLAN.md section 9.2): Ollama, then LM Studio.
func DefaultCandidates() []Candidate {
	return []Candidate{
		{Backend: "ollama", BaseURL: ollama.DefaultBaseURL, Path: "/api/tags"},
		{Backend: "lmstudio", BaseURL: openai.LMStudioBaseURL, Path: "/models"},
	}
}

// detectTimeout is how long a local server has to say it is there.
const detectTimeout = 500 * time.Millisecond

// defaultBase is the address of a backend that is named without one.
func defaultBase(backendName string) string {
	switch backendName {
	case "ollama":
		return ollama.DefaultBaseURL
	case "lmstudio":
		return openai.LMStudioBaseURL
	case "llamacpp":
		return openai.LlamaCppBaseURL
	case "openai":
		return openai.DefaultBaseURL
	case "anthropic":
		return anthropic.DefaultBaseURL
	}
	return ""
}

func protocolOf(backendName string) Protocol {
	switch backendName {
	case "ollama":
		return ProtocolOllama
	case "anthropic":
		return ProtocolAnthropic
	}
	return ProtocolOpenAI
}

// Options are what a Detector reads from the outside, so that tests can run it against fake servers.
type Options struct {
	// Candidates are the local servers `--llm auto` tries, in order (DefaultCandidates when nil). They must be loopback.
	Candidates []Candidate
	// NewClient makes the HTTP client of a backend (httpx.New when nil).
	NewClient func(name string) *httpx.Client
	// Getenv reads the environment, for the API key (os.Getenv when nil).
	Getenv func(string) string
	// CacheDir is where probe results are kept; empty means no cache.
	CacheDir string
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// DetectTimeout is how long a local server has to answer (500 ms when 0).
	DetectTimeout time.Duration
	// HomeDir is the user's home directory, where LM Studio leaves the port of its server (os.UserHomeDir when nil).
	HomeDir func() (string, error)
}

// Detector finds the backend, lists its models and probes what a model can do.
type Detector struct {
	opts Options
}

// New makes a Detector.
func New(opts Options) *Detector {
	if opts.HomeDir == nil {
		opts.HomeDir = os.UserHomeDir
	}
	if opts.Candidates == nil {
		home, _ := opts.HomeDir()
		opts.Candidates = candidatesFor(home)
	}
	if opts.NewClient == nil {
		opts.NewClient = httpx.New
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.DetectTimeout <= 0 {
		opts.DetectTimeout = detectTimeout
	}
	return &Detector{opts: opts}
}

// quick is the HTTP client for asking a server what it has: short deadlines and no retries.
func (d *Detector) quick(name string) *httpx.Client {
	c := d.opts.NewClient(name)
	c.Backoff = nil
	c.First, c.Timeout = 15*time.Second, 15*time.Second
	return c
}

// Resolve finds what a session will talk to from the configuration: the backend (looking for a local one when it is
// auto), its address, the key, and the model (the one named, else the first that reports vision). It also returns the
// models found, when it had to look.
func (d *Detector) Resolve(ctx context.Context, cfg config.Config) (Target, []ModelInfo, error) {
	t, err := d.ResolveBackend(ctx, cfg)
	if err != nil {
		return Target{}, nil, err
	}
	if t.Model != "" {
		return t, nil, nil
	}
	models, err := d.Models(ctx, t)
	if err != nil {
		return Target{}, nil, err
	}
	if m, ok := pickVision(models); ok {
		t.Model = m.Name
		return t, models, nil
	}
	return Target{}, models, noVision(t, models)
}

// ResolveBackend finds the backend, its address and the key from the configuration, looking for a local server when the
// backend is auto; the model is the one the configuration names, or empty.
func (d *Detector) ResolveBackend(ctx context.Context, cfg config.Config) (Target, error) {
	name := cfg.LLM.Backend
	if name == "" {
		name = "auto"
	}
	t := Target{Model: strings.TrimSpace(cfg.LLM.Model), NumCtx: cfg.LLM.NumCtx}
	if name == "auto" {
		if cfg.LLM.BaseURL != "" {
			return Target{}, exitcode.Usagef("a base URL needs an explicit --llm so the protocol is known")
		}
		found, err := d.detectLocal(ctx)
		if err != nil {
			return Target{}, err
		}
		t.Backend, t.BaseURL = found.Backend, found.BaseURL
	} else {
		t.Backend, t.BaseURL = name, cfg.LLM.BaseURL
		if t.BaseURL == "" {
			t.BaseURL = defaultBase(name)
		}
	}
	if t.BaseURL == "" {
		return Target{}, exitcode.Usagef("the backend %q is not ollama, lmstudio, llamacpp, openai or anthropic", name)
	}
	u, err := httpx.ValidateBaseURL(t.BaseURL)
	if err != nil {
		return Target{}, exitcode.Usagef("%v", err)
	}
	t.BaseURL, t.Protocol, t.Local = u.String(), protocolOf(t.Backend), httpx.IsLoopback(u)
	if key, ok := config.ResolveAPIKey(&cfg, d.opts.Getenv); ok {
		t.APIKey = key.Value
	}
	return t, nil
}

func noVision(t Target, models []ModelInfo) error {
	if len(models) == 0 {
		return fmt.Errorf("%s at %s offers no models: load or pull one, or name it with --model", t.Backend, t.BaseURL)
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Name)
	}
	if len(names) > 20 {
		names = append(names[:20], fmt.Sprintf("and %d more", len(models)-20))
	}
	return fmt.Errorf("no model of %s reports vision (found: %s): name one with --model; a model without vision runs in text-only mode", t.Backend, strings.Join(names, ", "))
}

// detectLocal asks the candidates, in order, whether they are there. Only loopback addresses are ever tried: a model that
// answers somewhere else is never chosen for the user.
func (d *Detector) detectLocal(ctx context.Context) (Candidate, error) {
	var tried []string
	for _, c := range d.opts.Candidates {
		u, err := httpx.ValidateBaseURL(c.BaseURL)
		if err != nil || !httpx.IsLoopback(u) {
			continue
		}
		client := d.quick(c.Backend)
		_, err = client.Get(ctx, u.String()+c.Path, nil, d.opts.DetectTimeout)
		var he *httpx.Error
		switch {
		case err == nil:
			return c, nil
		case errors.As(err, &he) && he.Kind == httpx.KindAuth:
			// It answered, and wants a key: it is there. The next call says so in words, and where the key goes.
			return c, nil
		case ctx.Err() != nil:
			return Candidate{}, ctx.Err()
		}
		tried = append(tried, fmt.Sprintf("%s at %s", c.Backend, u.Host))
	}
	return Candidate{}, &NoServerError{Tried: tried}
}

// Backend makes the client of a target.
func (d *Detector) Backend(t Target) (backend.Backend, error) {
	return d.backendWith(t, d.opts.NewClient(string(t.Protocol)))
}

func (d *Detector) backendWith(t Target, client *httpx.Client) (backend.Backend, error) {
	switch t.Protocol {
	case ProtocolOllama:
		return ollama.New(ollama.Config{BaseURL: t.BaseURL, Model: t.Model, NumCtx: t.NumCtx}, client)
	case ProtocolAnthropic:
		return anthropic.New(anthropic.Config{BaseURL: t.BaseURL, Model: t.Model, APIKey: t.APIKey}, client)
	case ProtocolOpenAI:
		return openai.New(openai.Config{BaseURL: t.BaseURL, Model: t.Model, APIKey: t.APIKey}, client)
	}
	return nil, fmt.Errorf("no client for the protocol %q", t.Protocol)
}
