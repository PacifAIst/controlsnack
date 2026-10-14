// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package fakellm is a scripted OpenAI-compatible model server for deterministic tests and end-to-end runs.
//
// It listens on a loopback port and serves what LM Studio and llama.cpp serve: the list of models (and LM Studio's own,
// which says the model sees), and /v1/chat/completions. The conversation is answered from a script, step by step: in
// words, with native tool calls, with an error, late, or never. A step can read the request, so a script can click the
// element whose name the observation shows (Find, FindText) without knowing its id. A step can also say what the request
// must be (Expect); a request that is not that fails the test.
//
// The capability probe is answered from a Profile, as a model with those capabilities would answer it, and is not part of
// the script; a request for what the profile lacks (a picture for a model without vision, tools for one without them) is
// refused with a 400 as a real server does it, so a session that picks the wrong mode fails in the test and not in front of
// a user. Every conversation request is kept for the test to look at (Requests).
package fakellm
