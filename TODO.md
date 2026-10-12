# TODO
Legend: [ ] todo · [~] in progress · [x] done (date) · [!] blocked · [-] dropped (reason + D-xx)

## Phase 0 — Bootstrap and tracking
- [x] (2026-10-12) P0-T01 Tracking files and identity
- [x] (2026-10-12) P0-T02 Environment check
- [x] (2026-10-12) P0-T03 Skeleton (layout, go.mod, tools/dev, CI) — print tree + go.mod

## Phase 1 — CLI: cmd/controlsnack, internal/cli, internal/config
- [ ] P1-T01 Root command, logging, build info
- [ ] P1-T02 Typed config (defaults, viper, validation, redaction; `config show|path`)
- [ ] P1-T03 Command tree (run, kill, image, vm, audit, doctor --json, llm, schema, version; `image build` takes --accel/--slow)
- [ ] P1-T04 `run` flags (incl. --accel auto|kvm|whpx|hvf|tcg and --slow) + validation and mutual exclusions
- [ ] P1-T05 Exit codes and signals

## Phase 2 — Safety: internal/safety (required test suite #1)
- [ ] P2-T01 Invariants and profiles (§6.1, §6.2)
- [ ] P2-T02 Exec policy engine (§6.3) with script-analysis hook interface
- [ ] P2-T03 Default rules + rule-file loader (§6.4)
- [ ] P2-T04 GUI and browser rules (§6.5), hit-testing against an a11y snapshot
- [ ] P2-T05 Limits (§6.6): limiter, budgets, loop detector, circuit breaker
- [ ] P2-T06 Confirmation broker (§6.7)
- [ ] P2-T07 Audit log (§6.9, §5.5) + `audit verify|ls|show|prune`
- [ ] P2-T08 Kill switch + session lock + stale reaper (§6.8, §5.6) + `controlsnack kill`
- [ ] P2-T09 Untrusted fencing (§6.10)
- [ ] P2-T10 Egress policy (§6.11)
- [ ] P2-T11 Secrets, tokens and redaction helpers (§6.12)

## Phase 3 — VM: internal/protocol, internal/vm (required test suite #2)
- [ ] P3-T01 Protocol package (§5.1)
- [ ] P3-T02 QEMU discovery (§7.1)
- [ ] P3-T12 Accelerator detector — accel.go + per-OS files (§7.1, D-31, D-32) *(R1)*
- [ ] P3-T03 Argv builders (§7.2): golden tests per arch × accel; forbidden-flag invariant
- [ ] P3-T04 Process supervisor (§7.4)
- [ ] P3-T05 QMP client
- [ ] P3-T13 Launch with one TCG retry — launch.go (§7.9, D-31) *(R1)*
- [ ] P3-T14 `controlsnack doctor` (§7.10, D-33) *(R1)*
- [ ] P3-T06 Image manager
- [ ] P3-T07 Seed builder + guest templates
- [ ] P3-T08 `image build` orchestration (§7.5)
- [ ] P3-T09 Overlays and kept VMs (§7.6) + `vm ls|rm`
- [ ] P3-T10 Agent installer + client (§7.7) and minimal agent
- [ ] P3-T11 Milestone `run --no-llm --viewer none`: boot with detected accelerator and with `--slow`; doctor verdict matches

## Phase 4 — Guest agent: internal/agent, cmd/controlsnack-agent
- [ ] P4-T01 Agent server (dispatch, workers, cancel, logs)
- [ ] P4-T02 Capture + encoding + change detection; WebP benchmark → D-11
- [ ] P4-T03 Windows and monitors (EWMH, RandR)
- [ ] P4-T04 Input (XTEST, key parser, Unicode typing, drag, scroll)
- [ ] P4-T05 Exec runner
- [ ] P4-T06 Accessibility (AT-SPI) + stubs for other OSes
- [ ] P4-T07 OCR
- [ ] P4-T08 Browser (CDP)
- [ ] P4-T09 wait_for
- [ ] P4-T10 Guest net proxy
- [ ] P4-T11 Host net bridge + egress e2e
- [ ] P4-T12 Embed pipeline (`tools/dev agent`)
- [ ] P4-T13 Image recipe finalisation + smoke

## Phase 5 — LLM: internal/llm (required test suite #3)
- [ ] P5-T01 Actions + schemas (§5.3)
- [ ] P5-T02 Renderings + profiles
- [ ] P5-T03 Parser + repair (§9.5)
- [ ] P5-T04 Ollama backend
- [ ] P5-T05 OpenAI-compatible backend
- [ ] P5-T06 Anthropic-compatible backend
- [ ] P5-T07 Detection + probe + cache + `llm probe|models`
- [ ] P5-T08 Prompt + observation rendering + context budget
- [ ] P5-T09 LLM image pipeline + coordinate mapping + marks
- [ ] P5-T10 Text-only mode
- [ ] P5-T11 fakellm

## Phase 6 — Viewer: internal/viewer, web/
- [ ] P6-T01 Server + auth + headers + tripwire (§10.1)
- [ ] P6-T02 WebSocket protocol + backpressure + client validation (§5.4)
- [ ] P6-T03 Frame pump + stats + budget tests (§10.3)
- [ ] P6-T04 UI layout (§10.2), incl. accelerator badge and TCG-fallback notice (§7.9)
- [ ] P6-T05 Confirmation modal
- [ ] P6-T06 Rendering-safety checks
- [ ] P6-T07 Hotkeys
- [ ] P6-T08 Window launcher + dead-man hooks (§10.4)

## Phase 7 — Integration: internal/session, run
- [ ] P7-T01 State machine (§3.4)
- [ ] P7-T02 Turn loop + batching rules (§11)
- [ ] P7-T03 Chat, ask_user, Stop, Pause, Step mode
- [ ] P7-T04 Budgets, loop detection, circuit breaker, dead-man switch
- [ ] P7-T05 Agent-down fallback + LLM error handling
- [ ] P7-T06 `run` wiring + cleanup + end summary; script analysis via file_peek; accelerator result and TCG fallback in hello, audit and summary
- [ ] P7-T07 e2e scenarios (-tags e2e)

## Phase 8 — Docs, hardening, release
- [ ] P8-T01 README (§14), incl. "Windows Home" section
- [ ] P8-T02 THIRD_PARTY_LICENSES.md
- [ ] P8-T03 Security review
- [ ] P8-T04 Performance pass (boot-to-ready per accelerator, always incl. tcg)
- [ ] P8-T05 Release (`tools/dev release`; CI smoke: --version and `doctor --json` exits 0 with a verdict)
- [ ] P8-T06 Final tracking cleanup

## Phase 9 — Optional (only if the user asks)
- [ ] Native viewer window (-tags webview), tile-diff streaming, manual takeover, system-wide kill hotkey, robotgo backend, MessagePack, recording/replay, policy editor UI

## Discovered
- [ ] P3-T12: verify on this machine that `WHvGetCapability` is the deciding signal, not the DLL — `WinHvPlatform.dll` exists in System32 although the "Windows Hypervisor Platform" feature is disabled (found in P0-T02; contradicts the PLAN §7.1 note "DLL missing = feature not enabled").
- [ ] P2-T01: wire the `internal/safety` ≥ 85 % coverage gate (PLAN §13.2) into `tools/dev check`; tools/dev currently has no coverage step.
- [ ] P2-T08 / P6-T01: create `internal/platform/listen.go` (the single 127.0.0.1-only listener helper). `tools/dev check` → `repo` already rejects any other file that opens a listener or binds a wildcard address.
- [ ] P3-T14: add the `controlsnack doctor --json` step (must exit 0 and print a verdict) to the Windows and macOS CI jobs (PLAN §13.5); not added in P0 because the command does not exist yet.
- [ ] P3-T10 / P4-T12: make `tools/dev build` run `agent` first once the host embeds the guest agents (D-04); `agentbin` will use `//go:embed *` so a clean checkout (only PLACEHOLDER.txt) still compiles.
- [ ] P6-T06: add the forbidden front-end API scan (innerHTML, eval, inline handlers…) to `tools/dev check` → `repo`.
- [ ] P8-T02: `tools/dev licenses` only checks; add the `go-licenses report` → THIRD_PARTY_LICENSES.md generation (module path is `github.com/google/go-licenses/v2`).
- [ ] P8-T05: verify the GitHub Actions versions in .github/workflows/ci.yml (checkout@v4, setup-go@v5, cache@v4) against current releases; the workflow could not be run locally.

## Parking lot (out of scope for v1 — do not implement)
- Multi-machine / Pro features (remote viewer, P2P, tunnels, link sharing, accounts)
- Non-Linux guests; macOS AX / Windows UIA backends
- Global kill hotkey; manual takeover; recording/replay
