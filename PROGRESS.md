# PROGRESS
## Now
- Phase: P3 in progress (P3-T01 done) → next P3-T02 (QEMU discovery) · Updated: 2026-10-12
- Blockers: none for the unit-testable parts of Phase 3 (T02–T07, T09, T10, T12–T14 use fakes). No QEMU on the Windows host: P3-T08 (real image build) and P3-T11 (real boot) run in WSL with TCG (x86_64 only); the first WHPX measurement needs an admin to enable Windows Hypervisor Platform.
- Resume here: P3-T02 (`internal/vm/qemu` discovery), then P3-T12 (accelerator detector) → P3-T03 as §12 orders them.

## Environment (verified)
| Item | Value | Verified on |
|---|---|---|
| Host OS | Windows 10 Pro 10.0.19045 (`EditionID` = Professional), amd64, Intel Core i7-10870H | 2026-10-12 |
| Go | system Go is go1.26.5 windows/amd64 (`GOTOOLCHAIN=auto`, `GOPROXY=https://proxy.golang.org,direct`, `CGO_ENABLED=0`). go.dev lists go1.27.2 and go1.26.9 as current. The module pins `toolchain go1.26.9`, so builds here use 1.26.9 (D-35, D-37); govulncheck: 0 vulnerabilities on 1.26.9, 21 stdlib advisories on 1.26.5 | 2026-10-12 |
| git | 2.53.0.windows.2; identity PacifAIst (global) | 2026-10-12 |
| C toolchain | none (`gcc`, `cc`, `clang` not found) → no `-race` locally; CI covers it (Linux/macOS) | 2026-10-12 |
| golangci-lint | v2.14.0 installed to `C:\Users\pc\go\bin` (`go install`, GOTOOLCHAIN=local); that directory must be on PATH for `tools/dev check` to run the lint step (otherwise it is skipped locally and fails in CI) | 2026-10-12 |
| QEMU / qemu-img | **Windows host: not installed** (not on PATH, not in `%ProgramFiles%\qemu`). **WSL: qemu-system-x86_64 and qemu-img 8.2.2** (Ubuntu build); no `qemu-system-aarch64`. Needed from Phase 3 (P3-T08, P3-T11) | 2026-10-12 |
| Hypervisor | `Win32_ComputerSystem.HypervisorPresent` = True (WSL2 is the default distro: Ubuntu, v2) | 2026-10-12 |
| Windows features (CIM, non-elevated) | `HypervisorPlatform` InstallState 2 (**disabled**), `VirtualMachinePlatform` 1 (enabled), `Microsoft-Hyper-V-All` 2 (disabled) → this machine is PLAN §7.10 case 2: WHPX unavailable, `doctor` verdict today would be `will use tcg (slow)`; fix = enable Windows Hypervisor Platform (admin + reboot), never disable WSL2 | 2026-10-12 |
| WinHvPlatform.dll | present in System32 even though the feature is disabled (see TODO → Discovered) | 2026-10-12 |
| Process elevation | not administrator (so nothing here can enable features) | 2026-10-12 |
| Local LLM | none: 127.0.0.1:11434, :1234, :8080 closed; `ollama` not on PATH. Needed from Phase 5 (fakellm covers tests) | 2026-10-12 |
| Browsers (viewer window) | Google Chrome and Microsoft Edge installed | 2026-10-12 |
| Disk | 304.7 GB free on C: | 2026-10-12 |
| WSL | WSL2 Ubuntu (default distro), gcc present, QEMU 8.2.2 x86_64 present. `/dev/kvm` exists (`root:kvm 0660`) but the user is not in group `kvm`, so QEMU there runs TCG. `scripts/wsl-go.sh` installs a private, SHA-256-verified Go 1.26.9 under `~/.cache/controlsnack-dev` on first use; with it `go test -race` and the Linux-only code run on real Linux (D-42) | 2026-10-12 |
| Other | winget available; Node and Python 3.14 present (not used by the product) | 2026-10-12 |

### Appendix B items resolved so far
- **Go:** installed 1.26.5; latest stable 1.27.2 (1.26.9 is the latest 1.26.x) → D-35 (go directive floor), D-37 (`toolchain go1.26.9`).
- **go-licenses:** the current module path is `github.com/google/go-licenses/v2` (v2.0.1); `check` takes `--allowed_licenses` and `--ignore`. It must be built for the host (`go tool -n go-licenses`) and then run with the target GOOS/GOARCH in the environment — `GOOS=linux go tool go-licenses` would build a Linux tool that cannot run on Windows.
- **R1 / Windows:** `Win32_OptionalFeature` and `Win32_ComputerSystem.HypervisorPresent` are readable **without admin**; the Windows edition is readable from `HKLM\...\CurrentVersion\EditionID`; `Win32_Processor.VirtualizationFirmwareEnabled` reads **False while the hypervisor is running** (confirms §7.10: never report "disabled in BIOS" when a hypervisor is present).
- Still open (checked when their phase starts): Debian URLs/signature (P3-T06), WebP libs (P4-T02), kbinani/screenshot cgo-free (P4-T02), go-diskfs ISO9660 (P3-T07), QEMU items (Phase 3), Ollama/LM Studio/OpenAI/Anthropic items (Phase 5), go-licenses paths (P0-T03/P8-T02).

## Phase status
| Phase | Status | Exit criteria | Notes |
|---|---|---|---|
| P0 | **done** 2026-10-12 | `go build ./...`, `go test ./...`, `go run ./tools/dev check` green; deviation/interpretation summary posted | gate steps: gofmt, mod-tidy, vet, lint, test (no -race locally: no C compiler), vuln, licenses, cross (5 host + 2 agent, CGO_ENABLED=0), repo |
| P1 | **done** 2026-10-12 | `controlsnack --help` and `run --help` list everything; `config show` works | 17 third-party deps, all MIT/BSD-3/Apache-2.0 (go-licenses verified with a negative control) |
| P2 | **done** 2026-10-12 | `internal/safety` coverage ≥ 85 %: **93.0 %** · fuzzing clean (exec policy 60 s / 364 k execs, fence 30 s / 500 k, egress 40 s / 5.4 M) · tests green with `-race` (13 packages, Linux/WSL) | 4 gaps found by the table/fuzz are listed in the log; D-39…D-42 |
| P3 | in progress (T01 done) | | real boot/build milestones (T08, T11) run in WSL with TCG |
| P4 | not started | | |
| P5 | not started | | |
| P6 | not started | | |
| P7 | not started | | |
| P8 | not started | | |

## Log (newest first)
### 2026-10-12 — P3-T01 — done
- Changed: `internal/protocol` — `bootstrap.go` (`CSBOOT/1 PUSH|OK|ERR` line codec, byte-wise `ReadBootLine` that never reads past the newline, 512-byte line cap, plain-decimal size, lowercase-hex hash/nonce), `frame.go` (`[uint32 BE header_len][header JSON][blob]`, header ≤ 1 MiB, blob ≤ 8 MiB, strict decoding, limits checked before any allocation, single-write `WriteFrame`), `messages.go` (type registry + every request/response body), `handshake.go` (`HelloAck.Validate` against nonce, pushed-binary hash, arch, user, display sanity, required capabilities, same major version), `conn.go` (serialized `Send`, violation ⇒ connection marked bad and closed).
- Tests: 20 test functions + 2 fuzz targets — codec round trips, 17 malformed push lines plus 11 field-validation mutations and 8 bad replies, 17 frame violations, truncation at every byte offset, limit boundaries, concurrent senders never interleave, buffered bytes behind the bootstrap survive into `Conn`, every body type survives a strict JSON round trip. `-race` green on Linux (WSL). Fuzz: `FuzzReadFrame` 25 s / 4.2 M execs, `FuzzParseBootstrap` 20 s / 3.7 M execs, clean.
- Real bug found by the table test: `ParsePush` accepted `size=+10` (strconv.ParseInt) — a line must have one spelling, so size is now matched against `^(0|[1-9][0-9]{0,18})$` as well.
- Follow-ups: none.
### 2026-10-12 — P2-T01…P2-T11 — done
- Changed (all under `internal/safety/` unless noted):
  - `safety` root: Verdict/Class, the nine invariants, `Profile` (safe vs unsafe, budgets clamped to the mode's bounds, `Apply` with `--step` winning over `--unsafe`), `AssertQEMUArgv` (closed allowlist of QEMU options and device models; refuses NICs, host sharing, non-loopback chardevs, paths outside the session dirs; `-nic user` only for provisioning). `platform.ListenLoopback` is the only function allowed to listen (enforced by `tools/dev check` → repo).
  - `policy`: the exec engine (bash parsed with mvdan.cc/sh, whole-AST walk, wrapper peeling, `bash -c`/here-doc/`trap`/`find -exec`/script-file following through a ScriptReader), `default.yaml` with all §6.4 rules, rule-file loader (user rules, overrides, strict mode, invariant protection, content hash), GUI/browser rules (§6.5) with a11y hit-testing.
  - `limits` (token bucket with a 2 s wait cap, budgets, check-in, session/idle timers, circuit breaker, loop detector, one-LLM gate, FakeClock), `confirm` (broker that cannot approve by itself), `audit` (hash chain, Verify, ls/show/prune, `schema/audit-record.schema.json`), `killswitch` (Killer, session lock + stale reaper, `controlsnack kill` client, `ControlHandler`), `untrusted` (fences), `secret` (tokens, redactor, password mask), `egress` (destination policy, dial-the-vetted-IP, limits).
  - `platform`: per-OS state/config/cache dirs, process identity (PID + start time) for Linux/macOS/Windows.
  - CLI: `audit show --raw`, `kill` now real, `audit` now real; `tools/dev check` gained the `coverage` step.
- Tests: ~1,700 test cases across the safety packages, including 550+ exec-policy cases (every bypass form in the plan), tamper cases for the audit log, a real-process kill, boundary tables for every IPv4/IPv6 range. Fuzz targets: exec policy, fence, egress.
- Real bugs found by the tests and fuzzers (each now has a regression case): `/dev/st*` matched `/dev/stderr` as a block device; `flock FILE -c 'cmd'` was not followed; `find /usr -exec rm -f {} +` escaped the root-wipe rule; `curl -fsSLo x.sh` (bundled `-o`) and `wget URL` (saves under the URL's name) defeated the script-written-in-the-same-action check; `chmod +x` wrongly erased a script's known content; `[` (the test command) looked like a glob; `nice $X` leaked a NUL marker into a message (fuzz); `[1.2.3.4]` parsed as an IPv4 literal; a `{a,b}{a,b}…` brace bomb would have exhausted memory; `isProtectedRoot("/.")` looped forever; a flaky confirm-timer test.
- Decisions: D-39 (exec policy scope and invariant rules), D-40 (GUI rule IDs, argv allowlist, egress details), D-41 (audit/kill/lock details), D-42 (WSL validation).
- Measured: trigger → process gone 1.1 ms (Windows, real child process standing in for QEMU).
- Follow-ups: see TODO → Discovered (viewer mounts the control handler; session wires AssertQEMUArgv/ScriptReader/audit-failure kill).
### 2026-10-12 — P1-T01…P1-T05 — done
- Changed: `internal/exitcode` (codes 0/1/2/3/4/5/6/70, `ErrNotImplemented`, `Pending(task)`); `internal/config` (typed `Config` with a single defaults table, viper-free injectable env layer, strict file parsing, flag↔key table, validation with flag-naming messages, redaction, API-key resolution); `internal/cli` (cobra tree: run, kill, image build|ls|rm|verify|import|path, vm ls|rm, audit verify|ls|show|prune, doctor, llm probe|models, schema, version, config show|path; zap logging; signal forwarding with force-exit on the second signal); service interfaces + exit-70 placeholders in session, killswitch, audit, image, overlay, vm, detect, schema; `cmd/controlsnack` now calls `cli.Main`.
- Decisions: D-38 (flag-only `--unsafe`, loosening limits needs `--unsafe`, `--vm`/`--keep-vm` exclusive, `kill` never reads the config, etc.).
- Tests: config (defaults==struct keys, tags agree, Appendix C values, 4-layer precedence, strict parsing, unsafe not from file/env, ~65 validation cases, redaction, API key order, hermetic env), exitcode, cli (usage errors→2, every placeholder→70, run flags reach the config, flag defaults equal config defaults, broken config doesn't block `kill`/`version`/`--help`, runner errors→exit codes, `config show` never prints the API key, signal forwarding, logger formats). `go run ./tools/dev check` green. Built binary smoke-tested: `--version`, `run` (70), `run --memory 100` (2, names `--memory (vm.memory_mib)`), unknown command (2), `doctor` (70).
- Verified: `go-licenses` really enforces the policy (allowing only MIT fails on cobra/pflag/fsnotify).
- Follow-ups: small commands share `internal/cli/simple.go` instead of one file each (D-38); `-race` still unavailable locally.
### 2026-10-12 — P0-T03 — done
- Changed: go.mod (`go 1.26.0`, `toolchain go1.26.9`, tools govulncheck + go-licenses/v2 via `go get -tool`); the full §4.1 package layout with an untagged `doc.go` per package (49 packages, SPDX headers); `internal/buildinfo` (+ tests); `cmd/controlsnack` (identifies the build, exits 70) and `cmd/controlsnack-agent` (linux main + non-linux stub); `tools/dev` with `check build cross agent licenses fuzz e2e release` (+ unit tests); `.golangci.yml` (v2); `.github/workflows/ci.yml` (§13.5); `internal/vm/agentbin/PLACEHOLDER.txt`.
- Tests: `go build ./...`, `go vet ./...`, `go test ./...`, `go run ./tools/dev check` all green (9 steps). Agent builds are byte-identical across runs (amd64 1.6 MB raw / 725 KB gz, arm64 1.6 MB / 667 KB gz). `build` produced a working windows/amd64 binary (exit 70, as designed).
- Gate design notes: gofmt runs in-process (go/format); `-race` is used only when a C compiler exists (none here); the repo step enforces the SPDX header and that only `internal/platform/listen.go` may open listeners and nothing may bind a wildcard address.
- Follow-ups: see TODO → Discovered.
### 2026-10-12 — P0-T02 — done
- Changed: Environment table above; Appendix B items for Go, go-licenses and R1/Windows resolved (see below the table).
- Findings that matter later: no QEMU, no C compiler, no local LLM on the dev machine; Windows Hypervisor Platform is **off** while WSL2's hypervisor runs (PLAN §7.10 case 2) — real WHPX runs here need an admin to enable it and a reboot, otherwise this machine runs TCG; `WinHvPlatform.dll` exists although the feature is off.
### 2026-10-12 — P0-T01 — done
- Changed: new repository `C:\Users\pc\Projects\controlsnack-local` (separate from paranoidchat). Added PLAN.md, CLAUDE.md, TODO.md, PROGRESS.md, DECISIONS.md (D-00…D-34 generated from the PLAN §2 table, plus D-35 Go directive and D-36 repository location), .gitignore, LICENSE (canonical GNU AGPL-3.0 text from gnu.org, sha256 0d96a4ff…abcb0), CONTRIBUTING.md (CLA note).
- D-00 confirmed by the user: module `github.com/PacifAIst/controlsnack-local`, AGPL-3.0-only, copyright holder Dr. Manuel Herrador.
- Tests: n/a (no code yet).
- Follow-ups: none.

## Metrics
| Metric | Target | Measured | Date |
|---|---|---|---|
| Idle viewer bandwidth | < 50 KB/s | | |
| 1080p WebP q70 frame | < 200 KB | | |
| Kill → QEMU gone | < 500 ms | 1.1 ms with a real child process as stand-in (real QEMU pending, P3-T11) | 2026-10-12 |
| Boot → agent ready (accelerated) | < 45 s | | |
| Boot → agent ready (tcg / `--slow`) | record | | |
