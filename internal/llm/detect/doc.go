// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package detect finds the model a session will talk to and finds out what it can do (PLAN.md sections 9.2 and 9.3).
//
// Resolve turns the configuration into a Target: with --llm auto it asks the local servers (Ollama, then LM Studio) at
// their loopback addresses, and never picks a server that is anywhere else; with a named backend it takes its default
// address or the one given, checks that it holds no credentials, and finds the API key from the environment. Without
// --model it chooses a model that reports vision, preferring one that is loaded.
//
// Models lists what a backend offers, with what the backend says of each model (vision, tools, context length).
//
// Probe asks the model, with the smallest questions that can tell, whether it sees a picture (PNG, then WebP), calls a
// tool, answers in JSON held to the response schema and answers in JSON asked for in the prompt. The answer is
// Capabilities: the best way the model can name its actions (native tool calls, then structured output, then JSON in
// the prompt) and the image format to send. A question that fails because the server cannot be reached, refuses the key
// or does not know the model ends the probe with that error and records nothing: it says nothing about the model. The
// result is cached per model and build of the model (a hash of backend, address, model and digest; never the key) for
// two weeks, and a model that can do nothing is not cached.
package detect
