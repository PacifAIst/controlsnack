# Contributing to ControlSnack Local

Thank you for your interest. Please read this before opening a pull request.

## Contributor License Agreement (CLA)

**Outside contributions are not accepted without a signed Contributor License Agreement.**
ControlSnack Local is licensed under AGPL-3.0-only (see `LICENSE`), and the author keeps all copyright so that
dual-licensing, a commercial edition, or a transfer of the project remain possible. To keep that possible, every
contributor must grant the project owner the right to relicense their contribution. Until a CLA process is in place,
pull requests from outside contributors will not be merged; issues and bug reports are welcome.

## Ground rules

- Write original code. Third-party code enters only as a Go module that passes the license policy
  (Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC — see `PLAN.md` D-25). Never paste code from other projects.
- If a snippet is adapted from elsewhere, its origin and license must be recorded in `THIRD_PARTY_LICENSES.md`.
- Every Go source file starts with the SPDX header:
  ```go
  // SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
  // SPDX-License-Identifier: AGPL-3.0-only
  ```
- Safety invariants (`PLAN.md` §6.1) are never relaxed, and the project's hard fences (`PLAN.md` §1.2) are never crossed
  — no telemetry, no non-loopback listeners, no multi-machine features, no host-side tools for the model.
- Run `go run ./tools/dev check` before submitting; it must be green.
- Commits follow Conventional Commits: `type(scope): summary [Pn-Tnn]`.
