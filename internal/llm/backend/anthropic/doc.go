// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package anthropic is the client of Anthropic's messages API, POST {base}/v1/messages, with x-api-key and
// anthropic-version headers (PLAN.md section 9.1). The system message is a field of its own; tools go as name,
// description and input_schema, the model's calls come back as tool_use blocks and their results go in as tool_result
// blocks at the start of the next user message (together with the observation, as one message, since the roles
// alternate); pictures are base64 image blocks. There is no response schema in this API, so structured mode asks in the
// prompt, as JSON mode does.
package anthropic
