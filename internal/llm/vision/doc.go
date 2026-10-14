// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package vision is what stands between the guest screen and the picture the model sees (PLAN.md section 9.3).
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
package vision
