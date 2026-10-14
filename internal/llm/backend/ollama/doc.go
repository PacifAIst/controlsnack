// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package ollama is the client of Ollama's native chat API, POST {base}/api/chat (PLAN.md section 9.1): native tools when
// the model has them, otherwise a response schema or plain JSON mode; the images as base64; temperature 0, no streaming,
// and always a context window and a limit on the length of the answer.
package ollama
