# TODO
Legend: [ ] todo · [~] in progress · [x] done (date) · [!] blocked · [-] dropped (reason + D-xx)

## Phase 0 — Bootstrap and tracking
- [x] (2026-10-12) P0-T01 Tracking files and identity
- [x] (2026-10-12) P0-T02 Environment check
- [x] (2026-10-12) P0-T03 Skeleton (layout, go.mod, tools/dev, CI) — print tree + go.mod

## Phase 1 — CLI: cmd/controlsnack, internal/cli, internal/config
- [x] (2026-10-12) P1-T01 Root command, logging, build info
- [x] (2026-10-12) P1-T02 Typed config (defaults, viper, validation, redaction; `config show|path`)
- [x] (2026-10-12) P1-T03 Command tree (run, kill, image, vm, audit, doctor --json, llm, schema, version; `image build` takes --accel/--slow)
- [x] (2026-10-12) P1-T04 `run` flags (incl. --accel auto|kvm|whpx|hvf|tcg and --slow) + validation and mutual exclusions
- [x] (2026-10-12) P1-T05 Exit codes and signals

## Phase 2 — Safety: internal/safety (required test suite #1)
- [x] (2026-10-12) P2-T01 Invariants and profiles (§6.1, §6.2)
- [x] (2026-10-12) P2-T02 Exec policy engine (§6.3) with script-analysis hook interface
- [x] (2026-10-12) P2-T03 Default rules + rule-file loader (§6.4)
- [x] (2026-10-12) P2-T04 GUI and browser rules (§6.5), hit-testing against an a11y snapshot
- [x] (2026-10-12) P2-T05 Limits (§6.6): limiter, budgets, loop detector, circuit breaker
- [x] (2026-10-12) P2-T06 Confirmation broker (§6.7)
- [x] (2026-10-12) P2-T07 Audit log (§6.9, §5.5) + `audit verify|ls|show|prune`
- [x] (2026-10-12) P2-T08 Kill switch + session lock + stale reaper (§6.8, §5.6) + `controlsnack kill`
- [x] (2026-10-12) P2-T09 Untrusted fencing (§6.10)
- [x] (2026-10-12) P2-T10 Egress policy (§6.11)
- [x] (2026-10-12) P2-T11 Secrets, tokens and redaction helpers (§6.12)

## Phase 3 — VM: internal/protocol, internal/vm (required test suite #2)
- [x] (2026-10-12) P3-T01 Protocol package (§5.1)
- [x] (2026-10-12) P3-T02 QEMU discovery (§7.1)
- [x] (2026-10-12) P3-T12 Accelerator detector — accel.go + per-OS files (§7.1, D-31, D-32) *(R1)*
- [x] (2026-10-12) P3-T03 Argv builders (§7.2): golden tests per arch × accel; forbidden-flag invariant
- [x] (2026-10-12) P3-T04 Process supervisor (§7.4)
- [x] (2026-10-12) P3-T05 QMP client
- [x] (2026-10-12) P3-T13 Launch with one TCG retry — launch.go (§7.9, D-31) *(R1)*
- [x] (2026-10-12) P3-T14 `controlsnack doctor` (§7.10, D-33) *(R1)*
- [x] (2026-10-12) P3-T06 Image manager
- [x] (2026-10-12) P3-T07 Seed builder + guest templates
- [x] (2026-10-12) P3-T08 `image build` orchestration (§7.5) — real build: 10 min 35 s under tcg, 765 MiB (D-52)
- [x] (2026-10-12) P3-T09 Overlays and kept VMs (§7.6) + `vm ls|rm` (D-53)
- [x] (2026-10-12) P3-T10 Agent installer + client (§7.7) and minimal agent (D-54)
- [x] (2026-10-12) P3-T11 Milestone `run --no-llm --viewer none`: boot with detected accelerator and with `--slow`; doctor verdict matches — verified in WSL with tcg (D-55)

## Phase 4 — Guest agent: internal/agent, cmd/controlsnack-agent
- [x] (2026-10-13) P4-T01 Agent server (dispatch, workers, cancel, logs) — per-category FIFO pools (D-56)
- [x] (2026-10-13) P4-T02 Capture + encoding + change detection; WebP benchmark → D-11 — deepteams/webp chosen, 1080p q70 frame 48–56 KB (D-57)
- [x] (2026-10-13) P4-T03 Windows and monitors (EWMH, RandR) — verified against xfwm4 in the guest (D-58)
- [ ] P4-T04 Input (XTEST, key parser, Unicode typing, drag, scroll)
- [ ] P4-T05 Exec runner
- [ ] P4-T06 Accessibility (AT-SPI) + stubs for other OSes
- [ ] P4-T07 OCR
- [ ] P4-T08 Browser (CDP)
- [ ] P4-T09 wait_for
- [ ] P4-T10 Guest net proxy
- [ ] P4-T11 Host net bridge + egress e2e
- [x] (2026-10-12) P4-T12 Embed pipeline (`tools/dev agent`) — done with P3-T10: both arches, gzip, SHA256SUMS; `agentbin.For` checks the hashes whenever an agent is loaded (every `run`) and refuses a damaged or mismatched set
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
- [x] (2026-10-12) P3-T12: verify on this machine that `WHvGetCapability` is the deciding signal — resolved, see D-44: it is not. DLL present, `HypervisorPresent` TRUE and `WHvCreatePartition` OK while the optional feature reads disabled; the QEMU probe launch decides.
- [ ] P3-T11 / first Windows QEMU: run the whpx probe (`TestProbeAccelRealQEMU`, `-qmp stdio`) against a real QEMU for Windows and record whether WHPX works on this machine with only VirtualMachinePlatform enabled (D-44). If `-qmp stdio` does not work with QEMU for Windows, move the probe to a loopback QMP socket.
- [ ] P3-T14: `doctor` must decide on the probe result, not on the optional-feature state; print feature state, API answers (`HypervisorPresent`, optionally `WHvCreatePartition`) and the probe side by side (D-44).
- [x] (2026-10-12) P2-T01: wire the `internal/safety` ≥ 85 % coverage gate (PLAN §13.2) into `tools/dev check`; tools/dev currently has no coverage step.
- [x] (2026-10-12) P2-T08 / P6-T01: create `internal/platform/listen.go` (the single 127.0.0.1-only listener helper). `tools/dev check` → `repo` already rejects any other file that opens a listener or binds a wildcard address.
- [x] (2026-10-12) P3-T14: `controlsnack doctor --json` step added to the macOS and Windows CI jobs (QEMU installed first; report kept as an artifact). Verify the action versions and the `choco`/`brew` package names at P8-T05.
- [ ] P3-T06 / P5 / P6: register the doctor's extra `Check`s — base image (T06), local model servers (`llm detect`, P5), viewer browsers (P6-T04); PLAN §7.10 item 3.
- [x] (2026-10-12) P3-T10 / P4-T12: `tools/dev build` runs `agent` first (the host embeds the guest agents, D-04); `agentbin` uses `//go:embed *`, so a clean checkout (only PLACEHOLDER.txt) still compiles.
- [ ] P6-T06: add the forbidden front-end API scan (innerHTML, eval, inline handlers…) to `tools/dev check` → `repo`.
- [ ] P8-T02: `tools/dev licenses` only checks; add the `go-licenses report` → THIRD_PARTY_LICENSES.md generation (module path is `github.com/google/go-licenses/v2`).
- [ ] P8-T05: verify the GitHub Actions versions in .github/workflows/ci.yml (checkout@v4, setup-go@v5, cache@v4) against current releases; the workflow could not be run locally.

- [ ] P6-T01: mount `killswitch.ControlHandler` at `/control/kill` and `POST /api/kill` (cookie + CSRF) on the viewer server; bind only through `platform.ListenLoopback`.
- [ ] P7-T06: call `safety.AssertQEMUArgv` before every QEMU launch (runtime and provisioning profiles); build the session's `policy.Engine` with `Options{NetEgress, Scripts: <file_peek reader>}` and feed `Profile.Apply` the engine's verdict; treat any `audit.Log.Append` error as fatal (kill source `audit_failure`).
- [ ] P7-T06 / P4-T11: the net bridge uses `egress.Policy.Dial` and `egress.Tracker`; every decision goes to the audit log as kind `net`.
- [ ] Cleanup: `internal/cli/simple.go` still groups the small commands in one file (D-38); split when convenient.
- [ ] P3-T11 / first hardware: no accelerated boot has been measured (this user cannot open `/dev/kvm` in WSL; Windows has no QEMU). Record "boot → agent ready (accelerated)" (target < 45 s) from the first KVM, WHPX or HVF run, or from the CI doctor artifacts plus a CI `run --no-llm` smoke on a runner with KVM. `sudo usermod -aG kvm $USER` (then restarting WSL) would make the WSL runs accelerated.
- [ ] P3-T11 / aarch64: nothing aarch64 has run for real (no `qemu-system-aarch64` here): the `virt` machine, the UEFI variable store copy (`vars.fd`) and the arm64 agent are covered by goldens and fakes only.
- [ ] P4-T13: rebuild the image once the agent has its features (the recipe hash changes with every recipe edit); the desktop has no wallpaper (black background: fine for screenshots, but decide whether `desktop-base` or a plain colour is wanted); check the first-boot time again.
- [ ] P7-T06: replace the boot-only runner with the full session (model loop, viewer, `--vm`/`--keep-vm` through `overlay.Library` including the generated name for a bare `--keep-vm`, `--allow-net` with the net port); keep what the boot-only runner already does in this order: kill switch → control endpoint → session lock → audit `start` → directory → boot → `ready`; the viewer's server takes over the `/control/kill` handler (P6-T01).
- [ ] P7-T06: `vm.Boot`'s agent heartbeat and timeouts are scaled by the accelerator factor; make the session treat `StateDown` as "agent_down" for the model loop (PLAN §11) and decide how long a tool call waits for a reconnect.
- [ ] P3-T14 / P3-T06: the doctor's extra checks are still unregistered; now that images exist, add the "base image present, matches the recipe, boots" check (`image verify` already does the first two).
- [ ] P4-T06: draw the set-of-marks overlay (D-30) in `internal/imaging` once a11y node bounds exist (`marks_generation` answers `unsupported` until then); `x/image/font/basicfont` is the plan's font.
- [ ] P5 / P6 / P7-T06: every picture the host receives from the guest goes through `imaging.Inspect` (declared format and size, at most 4096 per side) before anything decodes it.
- [ ] P8-T04: if the performance pass wants faster capture, add MIT-SHM (`gen2brain/shm` is pure Go) behind `x11.Conn.Capture`; measure first (1280x800 capture plus PNG took 312 ms under tcg).

## Parking lot (out of scope for v1 — do not implement)
- Multi-machine / Pro features (remote viewer, P2P, tunnels, link sharing, accounts)
- Non-Linux guests; macOS AX / Windows UIA backends
- Global kill hotkey; manual takeover; recording/replay
