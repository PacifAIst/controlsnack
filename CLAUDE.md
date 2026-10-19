# CLAUDE.md — ControlSnack Local
You are implementing ControlSnack Local as specified in PLAN.md.

## Every session
1. Read PROGRESS.md ("Now"), then the next open task in TODO.md, then the PLAN.md sections it cites.
2. Work on one task ID at a time; mark it [~] in TODO.md.
3. Run `go run ./tools/dev check` before marking anything done.
4. Mark [x] with the date, add a dated PROGRESS.md log entry, add discovered tasks to TODO.md, commit as `type(scope): summary [Pn-Tnn]`.
5. Record any design choice PLAN.md does not cover in DECISIONS.md before coding it.
6. Leave the build green. If stopping mid-task, write "Resume here" in PROGRESS.md.

## Never
- Relax a safety invariant (PLAN.md §6.1), even under --unsafe.
- Add non-loopback listeners, networking between machines, telemetry, accounts or "Pro" features.
- Give the runtime VM a NIC, shared folders or clipboard; add host-side tools for the LLM.
- Add a dependency outside Apache-2.0 / MIT / BSD / ISC without asking.
- Paste code from other projects into the repo.
- Push, create or change anything on GitHub, or open an issue or a pull request, unless the user says so. The repository https://github.com/PacifAIst/controlsnack exists and was first pushed at the user's word (2026-10-19); later pushes need their word each time. Before any push, search the history for keys and personal data (the history is public from the first push).

## Platform
- The accelerator is chosen at runtime (internal/vm/accel.go): kvm, whpx or hvf when usable, otherwise tcg — never a build-time choice.
- Windows Home is supported: WHPX is used on Windows when available; TCG is the universal fallback.
- The host binary is always CGO_ENABLED=0 (Windows APIs via golang.org/x/sys/windows; TCG is just a QEMU flag).

## Source headers
Every Go source file starts with:
```go
// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only
```
Every package has a `doc.go` without build tags (PLAN.md §4.1). Append-only files: DECISIONS.md (never rewrite an entry).

## Ask the user only for
D-00 confirmation, missing prerequisites you cannot install, or a genuinely ambiguous product choice.

## Commands
go run ./tools/dev check · build · cross · agent · licenses · e2e · release
