// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package vision

import (
	"errors"
	"fmt"

	"github.com/PacifAIst/controlsnack/internal/agent/textclean"
	"github.com/PacifAIst/controlsnack/internal/config"
	"github.com/PacifAIst/controlsnack/internal/imaging"
	"github.com/PacifAIst/controlsnack/internal/llm/actions"
	"github.com/PacifAIst/controlsnack/internal/llm/backend"
	"github.com/PacifAIst/controlsnack/internal/llm/detect"
	"github.com/PacifAIst/controlsnack/internal/llm/prompt"
)

// Setup is how a session works with a model, decided once from what the probe found and what the user configured: whether
// the model is shown pictures or only text, how it names its actions, which tools it has, and how pictures are made.
type Setup struct {
	// Vision is whether the model is shown pictures. When it is not, the session is in text-only mode (PLAN.md section 9.6):
	// no picture, the recognized text of the screen and the accessibility tree in its place, and the compact tool profile.
	Vision bool
	// Mode is how the model names its actions: native, structured or json.
	Mode backend.Mode
	// Profile is the tool profile, full or compact.
	Profile string
	// ImageFormat and ImageMax are the format and the longest side of the pictures; Marks says they carry numbered boxes.
	// They mean nothing in text-only mode.
	ImageFormat imaging.Format
	ImageMax    int
	Marks       bool
	// Coords is how the model says where something is on a picture: detect.CoordsPixels or detect.CoordsRelative (0 to 1000
	// across each axis). The Qwen-VL family points on the relative scale however it is asked.
	Coords string
	// Notices are things to tell the person watching, in the terminal and the viewer, before the first turn.
	Notices []string
}

// Relative reports whether the model points on the scale of 0 to 1000.
func (s Setup) Relative() bool { return s.Vision && s.Coords == detect.CoordsRelative }

// CoordSpace is the size of the space the model's coordinates live in: the picture's own, or, for a model that points on the
// relative scale, a square of 1000 by 1000 whatever the picture's size. The observation tells the model positions in that
// space and a click it answers with is read in it.
func (s Setup) CoordSpace(picture prompt.Size) prompt.Size {
	if s.Relative() {
		return prompt.Size{W: detect.RelativeScale, H: detect.RelativeScale}
	}
	return picture
}

// Badge is the word the viewer shows in its header.
func (s Setup) Badge() string {
	if s.Vision {
		return "VISION"
	}
	return "TEXT-ONLY"
}

// Plan decides the setup of a session. A model that cannot name its actions any of the ways a session needs is an error;
// one that cannot see a picture runs in text-only mode and the person is told it is degraded.
func Plan(caps detect.Capabilities, local bool, c config.LLM) (Setup, error) {
	mode, ok := caps.Mode()
	if !ok {
		return Setup{}, errors.New("the model cannot name its actions any way a session can use (tool calls, JSON held to a schema, JSON asked for in the prompt): use another model; `controlsnack llm probe` shows what it did")
	}
	profile, err := actions.ResolveProfile(c.Tools, local, !caps.Vision)
	if err != nil {
		return Setup{}, err
	}
	s := Setup{Vision: caps.Vision, Mode: mode, Profile: profile}
	if caps.Vision {
		s.ImageFormat, s.ImageMax = caps.ImageFormat(), c.ImageMax
		s.Coords = caps.Coords
		if s.Coords == "" {
			s.Coords = detect.CoordsPixels
		}
		s.Marks = MarksEnabled(c.Marks, local, true)
		return s, nil
	}
	s.Notices = append(s.Notices, fmt.Sprintf("%s does not take images: running in TEXT-ONLY mode, where the model reads the screen as text (the accessibility tree and recognized text) and not as a picture. This is degraded: expect it to do less, and worse.", textclean.Line(caps.Model, 120)))
	return s, nil
}
