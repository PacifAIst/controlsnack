# PROGRESS
## Now
- Phase: P0 complete → next P1-T01 · Updated: 2026-10-12
- Blockers: none for Phases 1–2. Phase 3 needs QEMU (not installed on the dev machine) — see Environment.
- Resume here: start P1-T01 (cobra root command, zap logging, `version`); first `go get` of cobra/viper/zap, then re-run `go run ./tools/dev check`.

## Environment (verified)
| Item | Value | Verified on |
|---|---|---|
| Host OS | Windows 10 Pro 10.0.19045 (`EditionID` = Professional), amd64, Intel Core i7-10870H | 2026-10-12 |
| Go | system Go is go1.26.5 windows/amd64 (`GOTOOLCHAIN=auto`, `GOPROXY=https://proxy.golang.org,direct`, `CGO_ENABLED=0`). go.dev lists go1.27.2 and go1.26.9 as current. The module pins `toolchain go1.26.9`, so builds here use 1.26.9 (D-35, D-37); govulncheck: 0 vulnerabilities on 1.26.9, 21 stdlib advisories on 1.26.5 | 2026-10-12 |
| git | 2.53.0.windows.2; identity PacifAIst (global) | 2026-10-12 |
| C toolchain | none (`gcc`, `cc`, `clang` not found) → no `-race` locally; CI covers it (Linux/macOS) | 2026-10-12 |
| golangci-lint | v2.14.0 installed to `C:\Users\pc\go\bin` (`go install`, GOTOOLCHAIN=local); that directory must be on PATH for `tools/dev check` to run the lint step (otherwise it is skipped locally and fails in CI) | 2026-10-12 |
| QEMU / qemu-img | **not installed** (not on PATH, not in `%ProgramFiles%\qemu`). Needed from Phase 3 (P3-T08, P3-T11) | 2026-10-12 |
| Hypervisor | `Win32_ComputerSystem.HypervisorPresent` = True (WSL2 is the default distro: Ubuntu, v2) | 2026-10-12 |
| Windows features (CIM, non-elevated) | `HypervisorPlatform` InstallState 2 (**disabled**), `VirtualMachinePlatform` 1 (enabled), `Microsoft-Hyper-V-All` 2 (disabled) → this machine is PLAN §7.10 case 2: WHPX unavailable, `doctor` verdict today would be `will use tcg (slow)`; fix = enable Windows Hypervisor Platform (admin + reboot), never disable WSL2 | 2026-10-12 |
| WinHvPlatform.dll | present in System32 even though the feature is disabled (see TODO → Discovered) | 2026-10-12 |
| Process elevation | not administrator (so nothing here can enable features) | 2026-10-12 |
| Local LLM | none: 127.0.0.1:11434, :1234, :8080 closed; `ollama` not on PATH. Needed from Phase 5 (fakellm covers tests) | 2026-10-12 |
| Browsers (viewer window) | Google Chrome and Microsoft Edge installed | 2026-10-12 |
| Disk | 304.7 GB free on C: | 2026-10-12 |
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
| P1 | not started | | |
| P2 | not started | | |
| P3 | not started | | needs QEMU on the dev machine |
| P4 | not started | | |
| P5 | not started | | |
| P6 | not started | | |
| P7 | not started | | |
| P8 | not started | | |

## Log (newest first)
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
| Kill → QEMU gone | < 500 ms | | |
| Boot → agent ready (accelerated) | < 45 s | | |
| Boot → agent ready (tcg / `--slow`) | record | | |
