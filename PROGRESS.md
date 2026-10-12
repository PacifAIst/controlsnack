# PROGRESS
## Now
- Phase: P0 · Task: P0-T01 → P0-T02 · Updated: 2026-10-12
- Blockers: none for Phases 0–2. Phase 3 needs QEMU (not installed) — see Environment.
- Resume here: —

## Environment (verified)
| Item | Value | Verified on |
|---|---|---|
| Host OS | Windows 10 Pro 10.0.19045 (`EditionID` = Professional), amd64, Intel Core i7-10870H | 2026-10-12 |
| Go | go1.26.5 windows/amd64 (`GOTOOLCHAIN=auto`, `GOPROXY=https://proxy.golang.org,direct`, `CGO_ENABLED=0`). go.dev currently lists go1.27.2 and go1.26.9 → D-35 | 2026-10-12 |
| git | 2.53.0.windows.2; identity PacifAIst (global) | 2026-10-12 |
| C toolchain | none (`gcc`, `cc`, `clang` not found) → no `-race` locally; CI covers it (Linux/macOS) | 2026-10-12 |
| golangci-lint | not installed | 2026-10-12 |
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
- **Go:** installed 1.26.5; latest stable 1.27.2 → D-35 (go directive `1.26`).
- **R1 / Windows:** `Win32_OptionalFeature` and `Win32_ComputerSystem.HypervisorPresent` are readable **without admin**; the Windows edition is readable from `HKLM\...\CurrentVersion\EditionID`; `Win32_Processor.VirtualizationFirmwareEnabled` reads **False while the hypervisor is running** (confirms §7.10: never report "disabled in BIOS" when a hypervisor is present).
- Still open (checked when their phase starts): Debian URLs/signature (P3-T06), WebP libs (P4-T02), kbinani/screenshot cgo-free (P4-T02), go-diskfs ISO9660 (P3-T07), QEMU items (Phase 3), Ollama/LM Studio/OpenAI/Anthropic items (Phase 5), go-licenses paths (P0-T03/P8-T02).

## Phase status
| Phase | Status | Exit criteria | Notes |
|---|---|---|---|
| P0 | in progress | build/test/check green; deviation summary posted | |
| P1 | not started | | |
| P2 | not started | | |
| P3 | not started | | needs QEMU on the dev machine |
| P4 | not started | | |
| P5 | not started | | |
| P6 | not started | | |
| P7 | not started | | |
| P8 | not started | | |

## Log (newest first)
### 2026-10-12 — P0-T01 — in progress
- Changed: new repository `C:\Users\pc\Projects\controlsnack-local` (separate from paranoidchat). Added PLAN.md, CLAUDE.md, TODO.md, PROGRESS.md, DECISIONS.md (D-00…D-34 generated from the PLAN §2 table, plus D-35 Go directive and D-36 repository location), .gitignore, LICENSE (canonical GNU AGPL-3.0 text from gnu.org, sha256 0d96a4ff…abcb0), CONTRIBUTING.md (CLA note).
- D-00 confirmed by the user: module `github.com/PacifAIst/controlsnack-local`, AGPL-3.0-only, copyright holder Dr. Manuel Herrador.
- Tests: n/a (no code yet).
- Follow-ups: P0-T02, P0-T03.

## Metrics
| Metric | Target | Measured | Date |
|---|---|---|---|
| Idle viewer bandwidth | < 50 KB/s | | |
| 1080p WebP q70 frame | < 200 KB | | |
| Kill → QEMU gone | < 500 ms | | |
| Boot → agent ready (accelerated) | < 45 s | | |
| Boot → agent ready (tcg / `--slow`) | record | | |
