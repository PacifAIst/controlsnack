// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package viewer serves the loopback HTTP and WebSocket viewer and launches its app-mode window.
//
// The server is the door of the program: anything on the machine can connect to a loopback port, so every request is
// held to the rules of PLAN.md section 10.1 before it reaches a handler. Only the front-end embedded in the binary is
// served, only to the window that logged in with the one-time link, and nothing the front-end shows is built as HTML.
package viewer
