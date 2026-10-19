// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PacifAIst/controlsnack/internal/llm/backend/openai"
)

// NoServerError is what `--llm auto` returns when no local model server answers. It carries what was tried and the steps
// that start a server, in words a person who has never run one can follow; the terminal prints them, and the viewer shows
// the same text as a notice.
type NoServerError struct {
	// Tried names the servers asked, as "ollama at 127.0.0.1:11434".
	Tried []string
}

func (e *NoServerError) Error() string {
	return "no local model server answered (" + strings.Join(e.Tried, ", ") + ")\n\n" + e.Instructions()
}

// Instructions says how to start a model server.
func (e *NoServerError) Instructions() string {
	return `To start one:
  LM Studio  Load a model that sees images (a "vision" model, for the screenshots), open the Developer tab and set the
             server to Running; or run: lms server start. It listens on port 1234 unless you changed it there.
             If you turned on "Require Authentication", give the key with --api-key-env.
  Ollama     Run: ollama serve    and, once: ollama pull qwen2.5vl
  Another    llama.cpp, vLLM, OpenRouter, OpenAI...: name it, for example
             --llm openai --base-url https://openrouter.ai/api/v1 --model <name> --api-key-env OPENROUTER_API_KEY`
}

// lmstudioStatusFiles are where LM Studio writes the port its server last ran on, relative to the user's home directory.
var lmstudioStatusFiles = []string{
	filepath.Join(".cache", "lm-studio", ".internal", "http-server-last-status.json"),
	filepath.Join(".lmstudio", ".internal", "http-server-last-status.json"),
}

// lmstudioPorts reads the ports LM Studio's server last ran on. People change the port in its settings; the default,
// 1234, is tried anyway. The file holds only the port, and what is read from it is used only as an address on this machine.
func lmstudioPorts(home string) []int {
	var ports []int
	for _, rel := range lmstudioStatusFiles {
		data, err := os.ReadFile(filepath.Join(home, rel)) //nolint:gosec // a file of LM Studio in the user's own home
		if err != nil || len(data) > 4096 {
			continue
		}
		var st struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(data, &st) == nil && st.Port > 0 && st.Port < 65536 {
			ports = append(ports, st.Port)
		}
	}
	return ports
}

// candidatesFor adds to the default candidates an LM Studio at the port its own status file names, when that is not the
// default one.
func candidatesFor(home string) []Candidate {
	cands := DefaultCandidates()
	if home == "" {
		return cands
	}
	seen := map[string]bool{}
	for _, c := range cands {
		seen[c.BaseURL] = true
	}
	for _, p := range lmstudioPorts(home) {
		base := "http://127.0.0.1:" + strconv.Itoa(p) + "/v1"
		if !seen[base] && base != openai.LMStudioBaseURL {
			cands = append(cands, Candidate{Backend: "lmstudio", BaseURL: base, Path: "/models"})
			seen[base] = true
		}
	}
	return cands
}
