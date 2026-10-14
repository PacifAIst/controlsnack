// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package backend defines what the Ollama, OpenAI-compatible and Anthropic-compatible clients have in common: the
// conversation they are given (Request, Message, Image), the answer they return (Response, with a parse.Reply for
// internal/llm/parse), how the model is asked to name its actions (Mode: native tool calls, a structured JSON answer, or
// JSON in the prompt), and the errors they report (Error and its Kind, which the HTTP client in httpx makes). The prompt
// builder makes a Request; the session parses the Reply of the Response.
package backend
