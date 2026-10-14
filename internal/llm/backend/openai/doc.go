// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package openai is the client of the OpenAI chat completions API, which LM Studio, llama.cpp's server, vLLM and OpenAI
// itself speak: POST {base}/chat/completions with the base address including /v1 (PLAN.md section 9.1). Tools are sent as
// functions with tool_choice auto; the other ways to get actions are a JSON-schema response format and plain JSON in the
// prompt. Pictures are data URLs, the results of native calls are tool messages, and the arguments of a call arrive as
// JSON text (the parser accepts that form).
package openai
