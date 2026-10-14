// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package vision is what stands between the guest screen and what the model is shown of it (PLAN.md sections 9.3 and 9.6).
//
// The model is given the screen scaled down to llm.image_max, and says where to click in pixels of that picture. A Mapper
// knows both sizes: it converts what the model reads (bounds, cursor, text positions) from the screen to the picture, and
// what it says (mouse, scroll, regions) back, rounding the same way both times. A coordinate a little beyond the edge is
// the edge (2 % of the size); further out is an invalid_args problem that names the coordinate and the valid range.
//
// A Pipeline asks the agent for the picture (in the format the probe found, at the size configured, with the numbered
// boxes of the set-of-marks overlay when the model is local and an accessibility generation is at hand), and checks what
// comes back as the host checks anything from the guest: the format, the size against the header and against the
// screen, the ids of the boxes. It remembers the last plain picture so that a screen that has not changed costs the
// guest nothing to send again.
//
// Plan decides, once per session, from what the probe found and what the user configured, whether the model is shown
// pictures or only text (text-only mode, the fallback for a model that cannot see), how it names its actions, which tools
// it has and how the pictures are made. An Observer then looks at the screen each turn: the active window, the
// accessibility tree and, with vision, the picture with its boxes, or, in text-only mode, the text recognized on the
// screen instead, all in the coordinates the model works in, ready to be put into a prompt.Observation. What the tree or
// the text could not be read is a note for the model, not the end of the turn; a text-only model that has neither is
// blind (ErrBlind) and the session has to stop.
package vision
