// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package parse turns a model's reply into validated actions, with a repair message when nothing in it can be run
// (PLAN.md section 9.5).
//
// Parse takes a Reply (the words the model wrote and the native tool calls the backend reported) and the tools the
// session offers. Native tool calls come first; without any, the text is read as a JSON answer: <think> blocks are taken
// out, then the whole text, a fenced block or the first object that holds actions is tried, and commas before a closing
// bracket are forgiven (nothing else is). Every action is checked against its tool's schema by internal/llm/actions.
//
// The result says which actions to run (the valid ones before the first one that is not: an action may depend on the ones
// before it), which were not valid and why, how many were skipped after a mistake and how many were beyond the limit on
// actions in one answer. RepairMessage and Feedback turn that into what the model is told next.
package parse
