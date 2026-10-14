// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package prompt renders the system prompt and per-turn observations and keeps them inside the context budget
// (PLAN.md sections 9.4 and 5.2).
//
// System renders the embedded system.tmpl for a session: the machine and its network, the screen (or its absence), how a
// turn works and how to answer in the mode of the backend, how to work, and what to trust, naming the session's fence
// nonce. In a mode that does not pass the tools to the backend as definitions it describes them (ToolDocs, made from the
// same schemas that check the calls). The template's hash and SystemVersion go in the audit record of the session.
//
// Renderer turns an Observation into the compact text of the plan or into JSON (schema/observation.schema.json). Every
// string that came from the guest is cleaned and fenced with the session's nonce; what is not fenced is a number, a token
// of a restricted alphabet, or text the host or the user wrote, and text of several lines that is not the model's own is
// indented, so that nothing a program in the machine writes can look like a line of the observation or a fence marker.
// The tests hold that as a property, with a fuzz target.
//
// Conversation is the history the model is shown: the system prompt, the last turns in full (DefaultKeepTurns), one line for
// each older turn, and the current observation with the only picture. Request trims it to 85 % of the model's context,
// giving up in the plan's order: the oldest turns, then nodes of the accessibility tree (those with actions, a state
// that matters or inside the active window stay), then lines of screen text, then the text of results; when even that is
// not enough it returns a ContextError. Tokens are estimated, not counted (EstimateTokens, ImageTokens).
//
// With native tool calls the results go back as tool messages, one for each call, which the session must supply even for
// actions that were invalid, not run or dropped (Request refuses a conversation where one is missing); with any other
// mode they are shown in the next observation.
package prompt
