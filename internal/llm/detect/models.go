// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PacifAIst/controlsnack-local/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/anthropic"
	"github.com/PacifAIst/controlsnack-local/internal/llm/backend/httpx"
)

// ModelInfo is a model a backend offers, with what the backend says about it.
type ModelInfo struct {
	Name   string
	Digest string
	// Vision and Tools are what the backend reports; a backend that says nothing about it (most OpenAI-compatible servers)
	// leaves them false with the Known field false: the probe finds out.
	Vision, VisionKnown bool
	Tools, ToolsKnown   bool
	// ContextLength is the model's context window in tokens, 0 when not reported.
	ContextLength int
	// Loaded is whether the model is in memory now.
	Loaded bool
}

// maxModels bounds how many models are asked about one by one.
const maxModels = 50

// Models lists the models a backend offers, with what it says of them.
func (d *Detector) Models(ctx context.Context, t Target) ([]ModelInfo, error) {
	var (
		models []ModelInfo
		err    error
	)
	switch t.Protocol {
	case ProtocolOllama:
		models, err = d.ollamaModels(ctx, t)
	case ProtocolAnthropic:
		models, err = d.anthropicModels(ctx, t)
	default:
		if t.Backend == "lmstudio" {
			if models, err = d.lmstudioModels(ctx, t); err == nil {
				break
			}
		}
		models, err = d.openaiModels(ctx, t)
	}
	if err != nil {
		return nil, fmt.Errorf("listing the models of %s at %s: %w", t.Backend, t.BaseURL, err)
	}
	return models, nil
}

// pickVision chooses a model that reports vision, preferring one that is already loaded.
func pickVision(models []ModelInfo) (ModelInfo, bool) {
	var first ModelInfo
	found := false
	for _, m := range models {
		if !m.Vision {
			continue
		}
		if m.Loaded {
			return m, true
		}
		if !found {
			first, found = m, true
		}
	}
	return first, found
}

func (d *Detector) get(ctx context.Context, t Target, rawURL string, headers map[string]string) ([]byte, error) {
	return d.quick(string(t.Protocol)).Get(ctx, rawURL, headers, 15*time.Second)
}

func authHeaders(t Target) map[string]string {
	switch {
	case t.APIKey == "":
		return nil
	case t.Protocol == ProtocolAnthropic:
		return map[string]string{"x-api-key": t.APIKey, "anthropic-version": anthropic.APIVersion}
	}
	return map[string]string{"Authorization": "Bearer " + t.APIKey}
}

// ollamaModels asks /api/tags for the models and /api/show for what each can do.
func (d *Detector) ollamaModels(ctx context.Context, t Target) ([]ModelInfo, error) {
	data, err := d.get(ctx, t, t.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, &httpx.Error{Kind: httpx.KindProtocol, Backend: "ollama", Message: "the list of models is not JSON"}
	}
	var out []ModelInfo
	for i, m := range tags.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		info := ModelInfo{Name: cleanName(name), Digest: m.Digest}
		if i < maxModels {
			d.ollamaShow(ctx, t, name, &info)
		}
		out = append(out, info)
	}
	return out, nil
}

// ollamaShow fills in what /api/show says about a model: its capabilities (vision, tools) and its context length. A model
// it cannot say anything about is left as it is.
func (d *Detector) ollamaShow(ctx context.Context, t Target, name string, info *ModelInfo) {
	body, _ := json.Marshal(map[string]string{"model": name})
	data, err := d.quick("ollama").Post(ctx, t.BaseURL+"/api/show", nil, body)
	if err != nil {
		return
	}
	var show struct {
		Capabilities []string                   `json:"capabilities"`
		ModelInfo    map[string]json.RawMessage `json:"model_info"`
	}
	if json.Unmarshal(data, &show) != nil {
		return
	}
	if show.Capabilities != nil {
		info.VisionKnown, info.ToolsKnown = true, true
		for _, c := range show.Capabilities {
			switch c {
			case "vision":
				info.Vision = true
			case "tools":
				info.Tools = true
			}
		}
	}
	for k, v := range show.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			var n int
			if json.Unmarshal(v, &n) == nil && n > info.ContextLength {
				info.ContextLength = n
			}
		}
	}
}

// lmstudioModels asks LM Studio's own list, which says which models see images, call tools and are loaded.
func (d *Detector) lmstudioModels(ctx context.Context, t Target) ([]ModelInfo, error) {
	u, err := url.Parse(t.BaseURL)
	if err != nil {
		return nil, err
	}
	root := *u
	root.Path = strings.TrimSuffix(root.Path, "/v1")
	data, err := d.get(ctx, t, root.String()+"/api/v0/models", authHeaders(t))
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			ID                  string   `json:"id"`
			Type                string   `json:"type"`
			State               string   `json:"state"`
			MaxContextLength    int      `json:"max_context_length"`
			LoadedContextLength int      `json:"loaded_context_length"`
			Capabilities        []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, &httpx.Error{Kind: httpx.KindProtocol, Backend: "lmstudio", Message: "the list of models is not JSON"}
	}
	var out []ModelInfo
	for _, m := range list.Data {
		if m.Type == "embeddings" || m.Type == "embedding" {
			continue
		}
		info := ModelInfo{Name: cleanName(m.ID), Vision: m.Type == "vlm", VisionKnown: m.Type != "", Loaded: m.State == "loaded", ContextLength: m.MaxContextLength}
		if m.LoadedContextLength > 0 && info.Loaded {
			info.ContextLength = m.LoadedContextLength
		}
		if m.Capabilities != nil {
			info.ToolsKnown = true
			for _, c := range m.Capabilities {
				if c == "tool_use" {
					info.Tools = true
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// openaiModels asks the plain /models list: names only.
func (d *Detector) openaiModels(ctx context.Context, t Target) ([]ModelInfo, error) {
	data, err := d.get(ctx, t, t.BaseURL+"/models", authHeaders(t))
	if err != nil {
		return nil, err
	}
	return namesOnly(data, "openai", false)
}

// anthropicModels asks /v1/models; every Claude model takes pictures and tools.
func (d *Detector) anthropicModels(ctx context.Context, t Target) ([]ModelInfo, error) {
	data, err := d.get(ctx, t, t.BaseURL+"/v1/models", authHeaders(t))
	if err != nil {
		return nil, err
	}
	return namesOnly(data, "anthropic", true)
}

// namesOnly reads {"data":[{"id":"..."}]}.
func namesOnly(data []byte, backendName string, capable bool) ([]ModelInfo, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, &httpx.Error{Kind: httpx.KindProtocol, Backend: backendName, Message: "the list of models is not JSON"}
	}
	var out []ModelInfo
	for _, m := range list.Data {
		if m.ID != "" {
			out = append(out, ModelInfo{Name: cleanName(m.ID), Vision: capable, VisionKnown: capable, Tools: capable, ToolsKnown: capable})
		}
	}
	return out, nil
}

// cleanName makes a model name fit to print: what a server says is not trusted to be one clean line.
func cleanName(s string) string { return textclean.Line(s, 120) }
