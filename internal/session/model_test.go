// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PacifAIst/controlsnack-local/internal/config"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend"
	"github.com/PacifAIst/controlsnack-local/internal/llm/detect"
	"github.com/PacifAIst/controlsnack-local/internal/llm/vision"
)

type fakeFinder struct {
	target     detect.Target
	caps       detect.Capabilities
	resolveErr error
	probeErr   error
	backendErr error
	noBackend  bool
	forced     []bool
}

func (f *fakeFinder) Resolve(context.Context, config.Config) (detect.Target, []detect.ModelInfo, error) {
	return f.target, nil, f.resolveErr
}

func (f *fakeFinder) Probe(_ context.Context, _ detect.Target, force bool) (detect.Capabilities, error) {
	f.forced = append(f.forced, force)
	return f.caps, f.probeErr
}

func (f *fakeFinder) Backend(detect.Target) (backend.Backend, error) {
	if f.backendErr != nil {
		return nil, f.backendErr
	}
	if f.noBackend {
		return nil, nil
	}
	return &scriptedModel{}, nil
}

func localFinder() *fakeFinder {
	return &fakeFinder{
		target: detect.Target{Backend: "lmstudio", Protocol: detect.ProtocolOpenAI, BaseURL: "http://127.0.0.1:1234/v1", Model: "qwen", Local: true},
		caps:   detect.Capabilities{Model: "qwen", Vision: true, NativeTools: true, ContextLength: 8192},
	}
}

func TestTheModelIsFoundProbedAndPlannedBeforeTheVmStarts(t *testing.T) {
	f := localFinder()
	m, err := ResolveModel(context.Background(), f, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if m.Target.Model != "qwen" || !m.Setup.Vision || m.Setup.Mode != backend.ModeNative || m.Backend == nil || m.Caps.ContextLength != 8192 || len(m.Notices) != 0 {
		t.Errorf("model %+v", m)
	}
	if len(f.forced) != 1 || f.forced[0] {
		t.Errorf("the probe was forced (%v): a result in the cache is good", f.forced)
	}
}

func TestAModelOnAnotherMachineAndOneThatCannotSeeComeWithNotices(t *testing.T) {
	f := localFinder()
	f.target.Local, f.target.BaseURL = false, "https://api.example.org/v1"
	m, err := ResolveModel(context.Background(), f, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Notices) != 1 || !strings.Contains(m.Notices[0], "api.example.org") || !strings.Contains(m.Notices[0], "not on this machine") {
		t.Errorf("notices %q", m.Notices)
	}
	g := localFinder()
	g.caps.Vision = false
	m, err = ResolveModel(context.Background(), g, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if m.Setup.Vision || len(m.Notices) != 1 || !strings.Contains(m.Notices[0], "TEXT-ONLY") {
		t.Errorf("text-only: %+v", m)
	}
	h := localFinder()
	h.target.Local, h.target.BaseURL = false, "https://api.example.org/v1"
	h.caps.Vision = false
	m, _ = ResolveModel(context.Background(), h, config.Defaults())
	if len(m.Notices) != 2 || !strings.Contains(m.Notices[0], "not on this machine") || !strings.Contains(m.Notices[1], "TEXT-ONLY") {
		t.Errorf("both: %q", m.Notices)
	}
}

func TestWhyAModelCannotBeUsedIsSaidBeforeAnythingElseStarts(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		mod  func(*fakeFinder, *config.Config)
		want string
	}{
		"not found":      {func(f *fakeFinder, _ *config.Config) { f.resolveErr = &detect.NoServerError{} }, ""},
		"probe fails":    {func(f *fakeFinder, _ *config.Config) { f.probeErr = boom }, "finding out what qwen can do: boom"},
		"cannot act":     {func(f *fakeFinder, _ *config.Config) { f.caps = detect.Capabilities{Model: "tiny", Vision: true} }, "tiny cannot name its actions"},
		"no such tools":  {func(_ *fakeFinder, c *config.Config) { c.LLM.Tools = "enormous" }, "enormous"},
		"backend fails":  {func(f *fakeFinder, _ *config.Config) { f.backendErr = boom }, "boom"},
		"no backend":     {func(f *fakeFinder, _ *config.Config) { f.noBackend = true }, "no backend"},
		"resolve passes": {func(f *fakeFinder, _ *config.Config) { f.resolveErr = boom }, "boom"},
	} {
		f := localFinder()
		cfg := config.Defaults()
		tc.mod(f, &cfg)
		_, err := ResolveModel(context.Background(), f, cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The error of a missing server is passed on as it is, for the runner to explain.
	f := localFinder()
	f.resolveErr = &detect.NoServerError{}
	var ns *detect.NoServerError
	if _, err := ResolveModel(context.Background(), f, config.Defaults()); !errors.As(err, &ns) {
		t.Errorf("error %v", err)
	}
}

func TestWhetherTheModelIsLocalDecidesHowTheSessionPlansForIt(t *testing.T) {
	for _, local := range []bool{true, false} {
		f := localFinder()
		f.target.Local = local
		if !local {
			f.target.BaseURL = "https://api.example.org/v1"
		}
		cfg := config.Defaults()
		want, err := vision.Plan(f.caps, local, cfg.LLM)
		if err != nil {
			t.Fatal(err)
		}
		m, err := ResolveModel(context.Background(), f, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if m.Setup.Profile != want.Profile || m.Setup.Marks != want.Marks {
			t.Errorf("local=%v: planned %+v, want %+v", local, m.Setup, want)
		}
	}
	// And the two plans do differ, or this test says nothing.
	cfg := config.Defaults()
	a, _ := vision.Plan(localFinder().caps, true, cfg.LLM)
	b, _ := vision.Plan(localFinder().caps, false, cfg.LLM)
	if a.Profile == b.Profile && a.Marks == b.Marks {
		t.Skip("local and remote plans are the same for this configuration")
	}
}
