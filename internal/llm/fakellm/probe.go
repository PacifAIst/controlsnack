// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package fakellm

import (
	"net/http"
	"strings"

	"github.com/PacifAIst/controlsnack/internal/llm/detect"
)

const doneJSON = `{"thought":"checking","actions":[{"tool":"done","args":{"success":true,"summary":"ok"}}]}`

// probe answers a question of the capability probe from the profile, the way a model with those capabilities would; it
// reports false for any other request. A probe is one user message that holds one of the probe's questions.
func (s *Server) probe(r *Request) (Reply, bool) {
	if len(r.Messages) != 1 || r.Messages[0].Role != "user" {
		return Reply{}, false
	}
	m := r.Messages[0]
	switch {
	case strings.Contains(m.Text, detect.ProbePointQuestion):
		if !s.profile.Vision {
			return Reply{Status: http.StatusBadRequest, Body: errorBody("this model does not accept images")}, true
		}
		if s.profile.Relative {
			return Reply{Text: `{"x":718,"y":350}`}, true // the middle of the square on the scale of 0 to 1000
		}
		return Reply{Text: `{"x":460,"y":140}`}, true // and in pixels
	case strings.Contains(m.Text, detect.ProbeVisionQuestion):
		if !s.profile.Vision {
			return Reply{Status: http.StatusBadRequest, Body: errorBody("this model does not accept images")}, true
		}
		if len(m.Images) == 1 && (m.Images[0] != "webp" || s.profile.WebP) && m.Images[0] != "unknown" {
			return Reply{Text: `{"color":"red"}`}, true
		}
		return Reply{Text: `{"color":"white"}`}, true
	case strings.Contains(m.Text, detect.ProbeDoneAsk):
		if !s.profile.Native {
			return Reply{Status: http.StatusBadRequest, Body: errorBody(s.opts.Model + " does not support tools")}, true
		}
		return Reply{Calls: []Call{Tool("done", map[string]any{"success": true, "summary": "ok"})}}, true
	case strings.Contains(m.Text, detect.ProbeDoneAnswer):
		if r.Format == "json_schema" {
			if s.profile.Structured {
				return Reply{Text: doneJSON}, true
			}
			return Reply{Text: "I would rather explain it in words."}, true
		}
		if s.profile.JSONPrompt {
			return Reply{Text: "```json\n" + doneJSON + "\n```"}, true
		}
		return Reply{Text: "Sure! I have done it."}, true
	}
	return Reply{}, false
}
