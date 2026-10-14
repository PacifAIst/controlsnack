// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

// Package httpx is the HTTP plumbing the backends share (PLAN.md section 9.1): a deadline for each attempt (long for the
// first call, which may load the model), retries with a pause for what is worth retrying (a busy or failing server, a slow
// one, a connection that broke; not a refused connection or a 4xx), Retry-After honored up to a limit, a limit on the
// size of an answer, every failure turned into an Error of a Kind the session acts on, and checks of a backend's address.
// The text of an error never holds a request or a key.
package httpx
