// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package actions defines what a model can ask the session to do: the fifteen tools of PLAN.md section 5.3, the typed
// arguments of each, and the validation of a model's call against the tool's schema.
//
// The JSON Schema files under schema/tools are the description of the arguments, for the models and for the validator;
// the Go structs in args.go are what the session works with, and a test compares the two (names, types, required
// arguments, enums, limits). Parse validates one call, fills in the defaults, applies the checks that depend on
// another argument (a drag needs an end, wait_for text_visible needs a text) and returns an Action, or an *Error that
// lists every problem in words a model can act on.
package actions
