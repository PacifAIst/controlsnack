// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package live holds the tests that put the whole LLM layer in front of a real model (go test -tags live). They are not run
// by the gate: they need a model server (LM Studio, llama.cpp, Ollama, OpenRouter...) named by CS_LIVE_BACKEND,
// CS_LIVE_BASE_URL, CS_LIVE_MODEL and, for a remote one, the variable CS_LIVE_API_KEY_ENV. What they check is whether the
// model does what the prompt asks, with a small simulated desktop that answers its actions, and what they log is what the
// model said, for a person to read.
package live
