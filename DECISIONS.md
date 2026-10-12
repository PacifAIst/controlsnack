# DECISIONS

ADR-lite log seeded from PLAN.md §2 (D-00…D-34). **Append-only:** new decisions are added as new entries;
existing entries are never rewritten. Add an entry *before* implementing any choice PLAN.md does not already make.

## D-00 — Module path and license
- Status: Accepted (2026-10-12) — confirmed by the user in P0-T01: module path, AGPL-3.0-only, copyright holder "Dr. Manuel Herrador" (source headers: `SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador` + `SPDX-License-Identifier: AGPL-3.0-only`) · Type: **Confirm with user in P0-T01**
- Context: Keeps dual-licensing, a Pro edition or a project transfer possible.
- Decision: Module path `github.com/PacifAIst/controlsnack-local`. Own code licensed **AGPL-3.0-only** (the license chosen earlier for ControlSnack); the author keeps all copyright. Dependencies stay permissive (D-25).
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-01 — Guest is always Linux; only the AT-SPI accessibility backend
- Status: Accepted (2026-10-12) · Type: **Deviation**
- Context: AX/UIA matter only for macOS/Windows guests, which are out of scope (macOS guests are also restricted by Apple's license).
- Decision: The guest is always Linux (Debian stable, XFCE, Xorg); guest arch = host arch. Only the AT-SPI accessibility backend is built; macOS AX and Windows UIA get interface + `ErrUnsupported` stubs only.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-02 — QEMU is an external process on every host
- Status: Accepted (2026-10-12) · Type: Spec-aligned (amended by D-31)
- Context: GPL stays out of the binary; one hypervisor codepath.
- Decision: QEMU is an external process on every host — never bundled, never linked. Accelerator per OS (§7.1); TCG fallback with a loud warning. Probe features at runtime instead of trusting version numbers; minimum QEMU 8.0.
- Consequences: — (see Decision)
- Supersedes / superseded by: amended by D-31

## D-03 — Two control planes: virtio-serial agent and QMP
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: vsock needs vhost-vsock (Linux hosts only); virtio-serial also works under HVF and WHPX. QMP keeps the session observable when the agent is down.
- Decision: Two control planes: the guest agent over **virtio-serial** (primary) and **QMP** on the host (lifecycle, kill, fallback screenshots via `screendump`, fallback input via `input-send-event`). No vsock.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-04 — Host pushes its embedded agent over virtio-serial at first boot
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: Satisfies "installed over virtio-serial at first boot"; agent always matches the host version; no image rebuild when the agent changes; one downloadable file.
- Decision: The base image contains only a tiny bootstrap (bash + coreutils) started from XFCE autostart. At the first boot of every session the host **pushes its embedded agent** over virtio-serial (size + SHA-256 checked). The host binary embeds gzip'd agents for linux/amd64 and linux/arm64.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-05 — Pure-Go guest agent; robotgo is not used
- Status: Accepted (2026-10-12) · Type: **Deviation**
- Context: robotgo requires cgo (cross-compiling linux/arm64 then needs sysroots or Docker) and its Linux code imports `robotn/xgbutil`, a fork of `BurntSushi/xgbutil`, whose WTFPL license is outside the policy (verify). The guest is X11-only, so xgb covers everything needed. Revisit behind a build tag if ever required.
- Decision: Guest agent is **pure Go (`CGO_ENABLED=0`)**: X11 via `jezek/xgb` (XTEST, MIT-SHM, RandR), capture via `kbinani/screenshot`. **robotgo is not used.**
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-06 — Pure-Go host binary; viewer in an app-mode browser window
- Status: Accepted (2026-10-12) · Type: Spec-allowed
- Context: The brief allows "webview or system browser"; this keeps the single-binary promise while still giving a standalone window.
- Decision: Host binary is **pure Go (`CGO_ENABLED=0`)**, so all five targets cross-compile from one machine. Viewer = loopback HTTP/WebSocket server shown in an **app-mode window** of an installed Chromium-family browser (Edge/Chrome/Chromium `--app=` with a throwaway profile); fallback: default browser tab. Optional native window behind `-tags webview` (evaluate the purego-based `abemedia/go-webview` first, else cgo `webview/webview_go`), not in default releases.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-07 — Runtime VM has no NIC; --allow-net via proxy over virtio-serial
- Status: Accepted (2026-10-12) · Type: **Deviation** (stricter)
- Context: QEMU user-mode networking maps 10.0.2.2 to the host's loopback (exposing the viewer, Ollama, etc.) and NATs into the LAN, and a guest firewall can be undone by guest root. No NIC is a structural guarantee and behaves the same on every OS. Cost: non-proxy-aware tools and raw TCP/UDP do not work.
- Decision: **The runtime VM never has a network device.** `--allow-net` = HTTP/HTTPS egress only: guest apps use a guest-local proxy (127.0.0.1:3128, inside the agent) that tunnels each connection over a dedicated virtio-serial port to the host, where the egress policy (§6.11) decides and dials.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-08 — What "no networking outside localhost" means
- Status: Accepted (2026-10-12) · Type: **Interpretation**
- Context: Remote APIs and the one-time image build need outbound traffic; this makes the boundary explicit.
- Decision: "No networking outside localhost" means: no non-loopback listeners, no peers/relays/tunnels, no telemetry. The only outbound connections are (1) the user-configured LLM endpoint, (2) `controlsnack image build` downloads, (3) `--allow-net` guest egress under policy. Auto-detection only probes 127.0.0.1. Remote LLM endpoints need explicit flags, show a "screenshots leave this machine" notice, and must use HTTPS (plain HTTP to a remote host only with `--unsafe`).
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-09 — Base image is built locally from the Debian genericcloud image
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: No hosted backend; reproducible; no surprise downloads.
- Decision: The base image is **built locally** by `controlsnack image build` from the official Debian stable *genericcloud* image (checksum-verified) plus a cloud-init NoCloud seed generated in pure Go. Sealed read-only base + per-session qcow2 overlay. `run` never downloads anything; it tells the user to run `image build`. `image import` covers offline machines.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-10 — Default guest resolution 1280x800; client-side downscale
- Status: Accepted (2026-10-12) · Type: **Interpretation**
- Context: Better click accuracy, smaller payloads, and no silent provider-side resizing that would skew coordinates.
- Decision: Default guest resolution **1280×800** (`--resolution` up to 1920×1080). Images for the LLM are downscaled client-side to `llm.image_max` (default 1280 px long edge) and model coordinates are mapped back.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-11 — WebP q70 with JPEG fallback; encoder chosen by benchmark
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: Meets the payload budgets without cgo.
- Decision: WebP q70 for viewer frames and for LLM backends that accept WebP; JPEG (stdlib) otherwise. Encoder chosen by benchmark in P4-T02 between `gen2brain/webp` (MIT; libwebp compiled to WASM on wazero, cgo-free, and it uses the system libwebp via purego when installed) and `deepteams/webp` (pure Go, MIT). Frames are sent only on change and only while a viewer is connected.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-12 — Internal wire format: JSON control messages plus raw binary blobs
- Status: Accepted (2026-10-12) · Type: Spec-allowed
- Context: Meets the budgets; one dependency fewer.
- Decision: Internal wire format: JSON control messages + raw binary attachments (no base64 on internal channels). MessagePack deferred.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-13 — Thin stdlib HTTP LLM clients, no SDKs
- Status: Accepted (2026-10-12) · Type: **Deviation**
- Context: "Compatible" servers deviate from what official SDKs expect; smaller binary; one fixture-tested surface. An SDK can be slotted in later behind the same interface.
- Decision: LLM clients are **thin stdlib HTTP clients** per wire protocol (Ollama native, OpenAI chat-completions, Anthropic messages). No SDKs in v1.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-14 — Native tool calling with JSON-mode fallback
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: Many local vision models lack native tool calling.
- Decision: Native tool calling when backend + model support it; otherwise JSON mode (structured-output schema when the server accepts one, else lenient extraction). One canonical `Action` type; strict schema validation; at most 2 repair attempts per turn. Tool profiles `full` and `compact`.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-15 — Action budgets: N=2/s, M=3 per response, 60 per task
- Status: Accepted (2026-10-12) · Type: **Interpretation**
- Context: "Turn" is ambiguous; both readings are enforced.
- Decision: Budgets: **N = 2 actions/s**; **M = 3 actions per model response** (the brief's "turn"); plus **60 actions per user task** before a mandatory check-in and at most 100 model turns per task.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-16 — Exec policy parses shell with mvdan.cc/sh
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: Regex-only denylists are trivially bypassed.
- Decision: The exec policy parses commands with a real shell parser (`mvdan.cc/sh/v3`) and applies deny/confirm/allow rules to every extracted invocation; constructs that cannot be analysed are denied. Documented as a guardrail, not the security boundary (the VM is).
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-17 — What --unsafe never disables
- Status: Accepted (2026-10-12) · Type: **Interpretation**
- Context: "Safety beats convenience."
- Decision: `--unsafe` never disables: kill switch, audit log, loopback-only viewer + auth, no-NIC / egress private-range blocking, no host FS/clipboard/devices, untrusted-content labelling, schema validation. It relaxes only the items in §6.2.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-18 — Audit always persists; screenshots only with --keep
- Status: Accepted (2026-10-12) · Type: **Interpretation**
- Context: An audit log deleted on exit is not an audit log.
- Decision: Audit logs always persist (state dir, 30-day retention, `controlsnack audit prune`) and store screenshot **hashes**, never images. Screenshots and chat transcripts persist only with `--keep`; the VM disk only with `--keep-vm`.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-19 — Kill triggers
- Status: Accepted (2026-10-12) · Type: **Deviation** (addition)
- Context: Ctrl+Shift+Esc cannot be relied on inside a browser window on Windows.
- Decision: Kill triggers: viewer button; **Ctrl+Shift+Esc** where the OS delivers it to the page (Windows reserves it for Task Manager); **Esc pressed 3× within 1.2 s**; `controlsnack kill`; Ctrl+C / SIGTERM in the terminal. A system-wide global hotkey is deferred.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-20 — One session per user; dead-man switch
- Status: Accepted (2026-10-12) · Type: **Interpretation**
- Context: The brief assumes the user is watching.
- Decision: One active session per user (lock file); one viewer connection at a time. **Dead-man switch:** if the viewer disconnects the agent pauses at once; after 60 s the session ends. LLM-driven runs without a viewer (`--viewer none`) require `--unsafe`.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-21 — Guest user cs has no sudo
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: Shrinks the guest-to-host escape surface (device access needs root) and protects in-guest configuration.
- Decision: Guest user `cs` has **no sudo**; the agent and every LLM-driven process run as `cs` inside the autologin XFCE session. cloud-init's default sudo user is not created.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-22 — Browser automation via CDP
- Status: Accepted (2026-10-12) · Type: Spec-allowed
- Context: Pure Go, offline.
- Decision: Browser automation via **CDP** (`chromedp`, MIT) against the guest's Chromium. No Playwright (it needs a Node driver and downloads).
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-23 — OCR via the tesseract CLI in the guest
- Status: Accepted (2026-10-12) · Type: Spec-aligned
- Context: No cgo; gives word boxes for clicking.
- Decision: OCR via the `tesseract` CLI inside the guest (Apache-2.0, external process).
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-24 — JSON Schema files are the source of truth for tools
- Status: Accepted (2026-10-12) · Type: —
- Context: Keeps deliverable 3 authoritative.
- Decision: JSON Schema files in `schema/` are the source of truth for tools; validated with `santhosh-tekuri/jsonschema/v6` (Apache-2.0); tests keep Go types and schemas in sync.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-25 — Dependency license policy
- Status: Accepted (2026-10-12) · Type: —
- Context: The brief's package rules.
- Decision: License policy: allowed = Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC. Anything else (MPL, LGPL, GPL, AGPL, WTFPL, unknown) needs explicit user approval. Enforced in CI with `go-licenses`. Note: `spf13/viper` is **MIT**, not Apache-2.0 (allowed). External programs (QEMU, tesseract, Chromium, Debian) are never linked.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-26 — --keep-vm named VMs
- Status: Accepted (2026-10-12) · Type: Addition
- Context: Makes `--keep-vm` useful.
- Decision: `--keep-vm[=name]` stores the overlay as a named VM; `run --vm <name>` resumes it; `vm ls`, `vm rm`. Kept VMs never get a NIC either.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-27 — Vanilla front-end with a strict CSP
- Status: Accepted (2026-10-12) · Type: —
- Context: Offline, auditable, no supply chain.
- Decision: Viewer front-end is vanilla HTML/CSS/JS: no framework, no build step, no CDN, strict CSP.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-28 — Host to QEMU endpoints
- Status: Accepted (2026-10-12) · Type: —
- Context: Cross-platform with minimal exposure.
- Decision: Host↔QEMU endpoints: Unix sockets in a short 0700 temp dir on Linux/macOS (paths must fit `sun_path`); on Windows, TCP 127.0.0.1 with `server=on,wait=on` unless AF_UNIX chardevs are verified to work (log the result). `wait=on` makes QEMU block until the controller connects, so the controller is the first and only client.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-29 — Dev tasks run through go run ./tools/dev
- Status: Accepted (2026-10-12) · Type: —
- Context: The developer may be on any OS.
- Decision: Dev tasks run through `go run ./tools/dev <cmd>` (works on Windows); a Makefile is an optional thin wrapper. Dev tools pinned with `go get -tool` (govulncheck, go-licenses); golangci-lint installed as a binary.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-30 — Set-of-marks overlay
- Status: Accepted (2026-10-12) · Type: Addition
- Context: Small local VLMs ground far better on IDs than on raw coordinates.
- Decision: Set-of-marks overlay: numbered boxes for interactive accessibility elements drawn on the LLM's copy of the screenshot (never the viewer's). Default `auto` = on for local backends in vision mode.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-31 — Accelerator selection is a runtime concern (amends D-02)
- Status: Accepted (2026-10-12) · Type: Revision R1
- Context: The same binary has to run everywhere — KVM workstations, Macs, Windows laptops (Home included), with or without hardware acceleration.
- Decision: **Accelerator selection is a runtime concern, never a build-time one** (amends D-02). `internal/vm/accel.go` checks the hardware option for the host OS and returns the first usable one: Linux — `/dev/kvm` exists and opens read-write → `kvm`; Windows — WHPX available (`WHvGetCapability(WHvCapabilityCodeHypervisorPresent)` from `WinHvPlatform.dll`, confirmed by a short QEMU probe launch whose stderr is inspected on failure) → `whpx`; macOS — `kern.hv_support` = 1 → `hvf`; otherwise `tcg` (pure software emulation, always works). QEMU gets `-enable-kvm`, `-accel whpx`, `-accel hvf` or `-accel tcg`. If a hardware accelerator fails at launch, QEMU is relaunched **once** with `tcg` and the fallback is shown in the terminal, the viewer and the audit log (§7.9). The host binary stays `CGO_ENABLED=0`: Windows APIs go through `golang.org/x/sys/windows` lazy DLL calls, and TCG is only a QEMU flag.
- Consequences: — (see Decision)
- Supersedes / superseded by: amends D-02

## D-32 — --accel and --slow flags
- Status: Accepted (2026-10-12) · Type: Revision R1 · **Interpretation**
- Context: The brief named `--slow` without defining it; this reading makes it the explicit "no hardware acceleration" switch.
- Decision: Flags: `--accel` = `auto` (default), `kvm`, `whpx`, `hvf` or `tcg`, plus `--slow`. An explicit value must match the host OS (kvm on Linux, whpx on Windows, hvf on macOS; otherwise a usage error), skips detection, and still gets the single TCG retry. `--slow` is shorthand for `--accel=tcg` — deliberately run without hardware acceleration (troubleshooting, broken WHPX); combining it with `kvm`, `whpx` or `hvf` is a usage error. Whenever the effective accelerator is `tcg` (chosen, forced or fallback), VM-layer timeouts (launch window, boot-to-agent wait, agent push and handshake) are multiplied by 4; safety budgets, LLM timeouts and agent tool timeouts do not change.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-33 — controlsnack doctor is read-only
- Status: Accepted (2026-10-12) · Type: Revision R1
- Context: Users should learn before a run why they would get software emulation and exactly how to fix it.
- Decision: `controlsnack doctor` (§7.10) is read-only — it never elevates and never changes settings. It prints OS, edition, arch and CPU virtualization capability; runs per-OS checks (Windows: the "Windows Hypervisor Platform" and "Virtual Machine Platform" features, a hypervisor already holding VT-x because of Hyper-V, WSL2, Windows Sandbox or Memory Integrity, firmware virtualization; Linux: `/dev/kvm` permissions and `kvm_intel`/`kvm_amd`; macOS: `kern.hv_support` and QEMU's hypervisor entitlement); runs the same detector as `run`; and ends with one verdict — `will use kvm`, `will use whpx`, `will use hvf` or `will use tcg (slow)` — followed by numbered remediation steps.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-34 — Windows Home is supported
- Status: Accepted (2026-10-12) · Type: Revision R1
- Context: Correct framing, and no advice that would break users' WSL2 setups.
- Decision: **Windows Home is supported.** WHPX is used on Windows when available; TCG is the universal fallback. WHPX needs only the "Windows Hypervisor Platform" optional feature, not the Hyper-V role that Home lacks. When the Windows hypervisor is already running (Hyper-V, WSL2's Virtual Machine Platform, Windows Sandbox, Memory Integrity) it holds VT-x, and QEMU can then use hardware acceleration only through WHPX — so the fix is enabling Windows Hypervisor Platform, never disabling WSL2.
- Consequences: — (see Decision)
- Supersedes / superseded by: —

## D-35 — go directive follows the installed toolchain (Go 1.26), not a pinned 1.27
- Status: Accepted (2026-10-12) · Type: Interpretation (PLAN.md §4.2, Appendix B)
- Context: PLAN.md §4.2 writes `go 1.27` but says "use the installed latest stable". The dev machine has Go 1.26.5; go.dev lists 1.27.2 and 1.26.9 as current. `GOTOOLCHAIN=auto` would silently download a newer toolchain the moment go.mod asks for one.
- Decision: go.mod uses `go 1.26` (language and std-lib floor that the installed toolchain satisfies). No language feature newer than 1.26 is used. CI installs Go from go.mod's version, so it tracks the latest 1.26.x patch. Moving to 1.27 later is a one-line go.mod change recorded as a new decision.
- Consequences: the local toolchain is four patch releases behind 1.26.9, so `govulncheck` may report standard-library findings that are fixed in later patches; those are an upgrade-the-toolchain item, not a code finding.
- Supersedes / superseded by: —

## D-36 — Repository location and source-header format
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: the session started inside `C:\Users\pc\Projects\paranoidchat`, an unrelated Rust/Buildroot repository with a proprietary LICENSE. PLAN.md assumes a fresh `controlsnack-local/` repository.
- Decision: ControlSnack Local lives in its own repository at `C:\Users\pc\Projects\controlsnack-local` (its own `git init`, `main` branch). Nothing in `paranoidchat` is touched. Every Go source file starts with `// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador` and `// SPDX-License-Identifier: AGPL-3.0-only` (D-00).
- Consequences: none for the design; the module path in D-00 still names the future GitHub location.
- Supersedes / superseded by: —

## D-37 — go.mod pins `toolchain go1.26.9`
- Status: Accepted (2026-10-12) · Type: Interpretation · Refines the Consequences of D-35
- Context: the dev machine's Go 1.26.5 carries 21 known standard-library advisories (govulncheck: fixed in 1.26.6 and 1.26.9; none reachable from our code yet, but `net/http`, `crypto/tls` and `archive/*` will make them reachable from Phase 1 on). Upgrading the system Go is the user's machine, not ours to change; the `go` command can use a newer toolchain for one module on its own.
- Decision: go.mod keeps `go 1.26.0` as the language floor (D-35) and adds `toolchain go1.26.9`. With the default `GOTOOLCHAIN=auto` the go command fetches that official, checksum-verified toolchain into the module cache and uses it for this module only; nothing is installed system-wide. CI's `setup-go` reads the same line. Bump the `toolchain` line (new decision) when a newer patch fixes a reachable advisory.
- Consequences: first `go` invocation in a fresh checkout downloads the toolchain (~70 MB, once). `govulncheck` reports 0 vulnerabilities on go1.26.9 as of 2026-10-12.
- Supersedes / superseded by: refines D-35

## D-38 — Phase 1 CLI and configuration rules the plan left open
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN.md P1-T02…T05 list flags, precedence and exit codes but not several edge rules that a safety tool has to settle one way or the other.
- Decision:
  1. **`--unsafe` is flag-only.** It is not a config key and is rejected in the config file and in `CONTROLSNACK_SAFETY_UNSAFE`, so nothing outside the command line can silently relax the safety layer for every later run (extends D-17).
  2. **Limits can be tightened freely; loosening them needs `--unsafe`.** The defaults of Appendix C are the safe maxima (2 actions/s, 3 per response, 60 per task, 100 turns, 60 min). Under `--unsafe` the maxima become the §6.2 values (10, 10, 500, 500, 8 h). Values outside the applicable range are usage errors that name the flag.
  3. **`--viewer none` needs `--unsafe` unless `--no-llm`** (D-20). A non-loopback `--base-url` must be `https` unless `--unsafe` (D-08). A `--base-url` needs an explicit `--llm` so the protocol is known; URLs with credentials are refused.
  4. **`--vm` (resume a kept VM) and `--keep-vm` (create one) are mutually exclusive.** `--keep-vm` without a value means a generated name (`auto`); a name must be written `--keep-vm=NAME`. `--no-llm` conflicts with `--task`.
  5. **`--allow-domain` / `--allow-port` typed without `--allow-net` is a usage error;** the same keys coming from a file or the environment only produce a warning. `net.allow_ports` are ports *in addition to* 80 and 443. `net.deny_domains` is config-file only.
  6. **The environment layer is read through an injectable `Getenv`** and merged above the file and below the flags; an empty variable counts as unset. Unknown keys in the config file are errors (typos must not be silently ignored).
  7. **`kill`, `version`, `schema`, `--help` and the bare command never read the config file** so a broken file cannot block the emergency stop or the help. `config show` prints first and validates after, so the values that are wrong can be seen. `doctor` accepts `--accel`, `--slow` and `--qemu` so the verdict can be checked for a forced setting (extends D-32).
  8. **Exit code 5 covers interruption** (Ctrl+C/SIGTERM/SIGHUP, a canceled context) as well as the kill switch. A second signal ends the process immediately.
  9. A tiny leaf package `internal/exitcode` holds the codes and `ErrNotImplemented` so every layer can return a coded error without importing the CLI; each package that owns a command's behavior defines the service interface the command calls, with a placeholder that exits 70 and names the task that replaces it.
- Consequences: PLAN.md §4.1 gains `internal/exitcode`; `internal/cli` groups some small commands per file instead of strictly one file per command.
- Supersedes / superseded by: extends D-17, D-20, D-32
