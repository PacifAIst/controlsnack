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

## D-39 — Exec policy: scope, invariant rules and what is deliberately not analyzed
- Status: Accepted (2026-10-12) · Type: Interpretation (PLAN.md §6.3, §6.4, P2-T02, P2-T03)
- Context: the plan fixes the engine's behavior but leaves open which default rules a user policy may relax, how far wrappers and scripts are followed, and what happens with inputs that could make the analyzer itself a target.
- Decision:
  1. **Invariant rules** (a user policy can neither change nor disable them): DENY-UNPARSEABLE, DENY-DYNAMIC-CMD, DENY-NOT-ALLOWLISTED, DENY-PIPE-TO-SHELL (they keep the analysis sound: what cannot be read is not run) and DENY-PRIV-ESC, DENY-FIREWALL, DENY-KERNEL, DENY-RAW-DEV-WRITE (they defend the guest's own isolation layers, D-21). The rules that only protect a disposable guest (ROOT-WIPE, DISK, POWER, ACCOUNTS, MASS-KILL, FORK-BOMB, and every CONFIRM rule) can be relaxed with `override`.
  2. **Every command is found by walking the whole AST** (syntax.Walk), so nesting cannot hide one; context (pipe position, background, redirects) comes from the ancestor stack. Wrappers are peeled and each layer is also judged (`sudo rm -rf /` hits both). Followed: `bash -c`, here-documents and here-strings, `trap`, `find -exec` (with `{}` replaced by the start paths and `rm` treated as recursive), `watch`, `flock -c`, `source`, script files via the ScriptReader hook, and scripts written earlier in the same action when their content is known (`cat > f` with a literal here-document, plain `echo`).
  3. **Refused rather than guessed:** unparseable input, a command name from an expansion or containing a glob, nesting beyond 3, a script written and run in one action with unknown content (printf, echo -e, curl -o, cp, tee ...), more than 4000 commands or 12 scripts in one action, brace expansions above 256 words (a `{a,b}{a,b}...` memory bomb), a script the ScriptReader cannot read or that exceeds 64 KiB. Any panic inside the analysis is recovered and becomes DENY-UNPARSEABLE.
  4. **Not analyzed (documented limitation):** the code of other interpreters (python, node, perl, awk's system(), vim...), compiled programs, and arguments whose value is only known at run time (they are allowed; a recursive delete of one is a confirmation). The exec policy is a guardrail; the VM is the boundary (D-16).
  5. Rules are data (`default.yaml`, a small match DSL plus named Go detectors); user policies get strict parsing, IDs prefixed USER-/CONFIRM-REGEX-/DENY-REGEX-, RE2 regexes only, and a content hash for the audit log.
- Consequences: the rule list in §6.4 gained engine-raised IDs (DENY-UNPARSEABLE, DENY-NOT-ALLOWLISTED) and the GUI rules of D-40.
- Supersedes / superseded by: —

## D-40 — GUI rule IDs, QEMU argv as a closed allowlist, egress details
- Status: Accepted (2026-10-12) · Type: Interpretation (PLAN.md §6.5, §7.2, §6.11)
- Decision:
  1. §6.5 names no rule IDs; they are CONFIRM-GUI-DESTRUCTIVE (delete-like labels and shift+Delete), CONFIRM-GUI-NET-ACTION (send/pay/log-in-like labels, only with --allow-net), CONFIRM-PASSWORD-FIELD (typing into a password field, text redacted) and DENY-BROWSER-URL (anything but http, https, file; schemes are compared after dropping the characters browsers drop). Mouse clicks and drags are hit-tested against the innermost interactive element of the latest accessibility snapshot.
  2. `safety.AssertQEMUArgv` is a **closed allowlist** of QEMU options and device models (not just the §7.2 deny-list), plus required `-nic none` and `-display none`, chardevs only on a local path or 127.0.0.1, file paths only inside the session, socket, VM and image directories, and `-nic user` only in the provisioning profile. New QEMU features therefore need an explicit entry and test. Both launch paths must call it before executing QEMU.
  3. Egress: names are resolved once and refused if **any** answer is non-public; the vetted address is dialed (never the name) and re-checked in the dialer's Control hook; every numeric spelling of an address (inet_aton forms, mapped, compatible, NAT64) is judged as that address; single-label names, `localhost` and names ending in a numeric label are refused; with a domain allow list, address literals are refused too. IPv4-compatible IPv6 addresses are refused even when the embedded IPv4 is public (deprecated, never routed).
- Supersedes / superseded by: —

## D-41 — Audit chain, kill switch and session lock details
- Status: Accepted (2026-10-12) · Type: Interpretation (PLAN.md §5.5, §5.6, §6.8, §6.9, P2-T07, P2-T08)
- Decision:
  1. **Audit.** One file per session, created O_EXCL, mode 0600, fsync per record, sticky write error (the session must stop when the trail cannot be written). A line must equal the canonical marshaling of the record it decodes to, so adding, reordering or re-spacing fields is detected. Truncation at a record boundary leaves a valid chain; it shows as "no end record", and `Report.LastHash` can be stored elsewhere to anchor the log. The chain is tamper-evident against edits, not authenticated against an attacker who recomputes it (no key is kept from the same-user adversary).
  2. **Kill switch.** Order: flag, SIGKILL QEMU, cancel context, wait (bounded) for QEMU to exit, then teardown hooks (each bounded, panic-safe). A VM registered after the kill is killed at once; a hook added after teardown runs immediately. Measured trigger-to-gone against a real child process: about 1 ms.
  3. **Lock.** `active.json` is created with full content by hard-linking a temp file (atomic, exclusive); a process is recognized by PID **and** start time (/proc stat on Linux, sysctl on macOS, GetProcessTimes on Windows), so PID reuse never gets an innocent process killed. A corrupt lock is reported, never deleted. `controlsnack kill` sends the bearer token only to a loopback IP literal over http; without an acknowledgement within 1 s it ends QEMU and then the controller directly (verified by start time) and removes the lock.
  4. Interfaces changed from the Phase 1 placeholders: `Killer.Kill(ctx, w)` and `audit.Admin.Show(..., raw bool)` (`audit show --raw`).
- Supersedes / superseded by: —

## D-42 — Linux and race-detector validation through WSL
- Status: Accepted (2026-10-12) · Type: Addition
- Context: the Windows dev machine has no C compiler, so `go test -race` cannot run natively, and Linux-only code (the Phase 3/4 guest agent, /proc handling) would otherwise be untested until CI.
- Decision: `scripts/wsl-go.sh` runs any command in WSL with a private Go toolchain installed under `~/.cache/controlsnack-dev` (version from go.dev's release feed, SHA-256 verified, no sudo, nothing outside that directory). `tools/dev check` stays Windows-native; the race run is `wsl -e bash scripts/wsl-go.sh env CGO_ENABLED=1 go test -race ./...`.
- Supersedes / superseded by: —

## D-43 — QEMU discovery rules
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.1 gives the search order and "qemu-img must sit alongside"; it leaves open what an explicit choice that fails does, how PATH is read, and how features are probed.
- Decision:
  1. **An explicit `--qemu` / `vm.qemu_dir` is never replaced.** If it is missing or lacks either program, discovery fails naming that one place; it does not fall back to PATH. A path to one of the programs means its directory.
  2. **Only absolute PATH entries are searched.** `.`, `bin` and empty entries are skipped: the result must not depend on the directory `controlsnack` is started in, and a planted `qemu-system-*` in a working directory must not be picked up. Duplicates are tried once.
  3. **Both programs in one directory, regular files, executable** (the `.exe` extension decides on Windows; symlinks are followed, as Homebrew needs). Each place tried is kept with its reason, so `doctor` and the error message can say why.
  4. **Features are asked, not inferred** (D-02): minimum major version 8, then `-accel help`, `-machine help` and `-device help` are parsed (aliases count as device names) and every machine and device the runtime argv names must exist; the `Install` is returned together with a `*VersionError` / `*FeatureError` so `doctor` can still show it. A too-old QEMU is not asked further questions.
  5. **`-sandbox` is probed once, on Linux only**, by starting an empty machine (`-machine none -S -qmp stdio`) under `SandboxArg` and sending `qmp_capabilities` and `quit` on stdin. The quit is required: measured with QEMU 8.2.2, a QMP session whose stdin ends does not exit. Exit 0 means supported; the argv builder adds the option only then.
  6. **aarch64 firmware** is searched next to the installation (`../share/qemu`, `../share`, `share`, also behind symlinks) and then in the standard system directories, for four known file-name pairs (QEMU's own, Debian AAVMF, Fedora, Arch). Both files of a pair must exist. x86_64 uses the BIOS inside QEMU and needs none. Missing firmware is not a discovery error; `Install.RequireFirmware` explains what to install when an aarch64 run needs it.
  7. Probe programs run with a minimal environment (`PATH`, `HOME`, Windows system variables, `LC_ALL=C`), output capped at 512 KiB, 30 s each. The guest architecture defaults to the host's: the guest is never emulated across architectures.
- Supersedes / superseded by: —

## D-44 — Accelerator detection: what the Windows API answers, and what decides
- Status: Accepted (2026-10-12) · Type: Interpretation (corrects an assumption in PLAN §7.1 and in PROGRESS)
- Context: PLAN §7.1 reads `WinHvPlatform.dll` missing as "feature not enabled" and treats a TRUE `HypervisorPresent` as the first gate for whpx. Measured on the dev machine (Windows 10 19045, WSL2 running, `Win32_OptionalFeature` HypervisorPlatform InstallState 2 = disabled, VirtualMachinePlatform enabled): the DLL is present, `WHvGetCapability(HypervisorPresent)` returns TRUE, and `WHvCreatePartition` succeeds. So neither the DLL nor `HypervisorPresent` says anything about the optional feature; it says a hypervisor is running (here WSL2's). The earlier note that this machine "cannot use WHPX without an admin enabling the feature" was inferred from the feature state alone and is withdrawn until a Windows QEMU has run here.
- Decision:
  1. The **QEMU probe launch is the authority** for whpx and hvf, exactly as PLAN §7.1 has it; the API call only rules out machines with no hypervisor at all. kvm needs no probe launch (opening `/dev/kvm` is the test; a KVM that still fails at launch falls back to TCG once, §7.9).
  2. The probe uses `-qmp stdio` with `qmp_capabilities` + `quit` on stdin instead of a socket endpoint (no endpoint to create, same result): exit 0 and no accelerator complaint on stderr ⇒ usable; non-zero exit, a complaint naming the accelerator, or 10 s without finishing ⇒ not usable, with a ≤ 1 KiB stderr excerpt as the reason. QEMU does not exit when QMP stdin ends, hence the explicit quit. **Verified with a real QEMU 8.2.2 for tcg (27 ms) and kvm (fails in 13 ms with "Could not access KVM kernel module: Permission denied"); not yet verified on Windows (no QEMU there).** If `-qmp stdio` turns out not to work with QEMU for Windows, `--accel whpx` skips detection (D-32) and the probe moves to a loopback QMP socket.
  3. `-cpu` per accelerator is one table (`qemu.CPUModel`): `host` for kvm and hvf, `max` for tcg, none for whpx. The detector never passes `kernel-irqchip=` or `ssd=off`.
  4. Detection is cached in a `Detector` for the life of the process; a canceled context is an error, never a quiet fall back to tcg. `ErrNoAccelerator` when the QEMU build has neither the hardware accelerator nor tcg.
- Consequences: `doctor` (P3-T14) must not claim "Windows Hypervisor Platform is off" from the feature state alone; it reports the feature state, the API answers and the probe result side by side, and decides on the probe.
- Supersedes / superseded by: amends PLAN §7.1 wording only (not D-31)

## D-45 — QEMU command-line builders
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.2 shows the runtime argv for x86_64 + KVM and says "builders are pure functions with golden tests per arch × accel"; the provisioning profile (§7.5 step 5) is only described in prose.
- Decision:
  1. `qemu.RuntimeArgv` and `qemu.ProvisionArgv` are pure functions of a spec (no file system, clock or environment) and **return nothing that has not passed `safety.AssertQEMUArgv`** with the matching profile, so no caller can skip the check. The error comes with a nil command line.
  2. Inputs are validated before building: accelerator is one of kvm/whpx/hvf/tcg (never `auto`; whpx only for x86_64), VM name `[a-z0-9-]{1,40}`, 1-64 CPUs, ≥ 256 MiB, resolution 320x200-7680x4320, absolute paths without commas or control characters (a comma would let a path add QEMU options; QEMU's `,,` escape is deliberately not used), unix socket paths ≤ 100 bytes (`sun_path`), exactly one of socket path / 127.0.0.1 port per endpoint.
  3. Added to PLAN §7.2: **`-no-reboot`** in both profiles. A guest that reboots would otherwise loop unseen; with the flag QEMU exits and the supervisor / launch window (§7.9: status `shutdown` = failure) see it.
  4. **Chardev order is agent, qmp, then net** (QEMU waits for each connection in declaration order); the net port exists only when `Net` is set (`--allow-net`). Port names are `protocol.PortAgent` / `protocol.PortNet`.
  5. **aarch64**: UEFI code is attached read-only from the discovered firmware, the variable store is the session's own copy (the builder never attaches the system template); the firmware's directory is added to the allowed directories automatically because it comes from discovery, not from the user. `-device virtio-gpu-pci` instead of `virtio-vga`.
  6. **Provisioning** differs from runtime on purpose: `-nic user` (the only place), the NoCloud seed as a read-only `virtio` cdrom (works on q35 and virt alike), `virtio-rng-pci`, a QMP endpoint (clean quit; required), and **no** display device, agent port or sandbox. Progress is read from `-serial file:` (portable, same as runtime) rather than `-serial stdio`.
  7. `-sandbox` is appended last and only when discovery found it works (`Install.Sandbox`).
  8. Golden files live in `internal/vm/qemu/testdata/argv`; regenerate with `go test ./internal/vm/qemu -run Golden -update` and review the diff by hand — they are generated by the code under test.
- Not yet verified against a real QEMU: the command lines are syntax-checked by the safety allowlist only; the first real launch is in P3-T13/P3-T11 (WSL, TCG).
- Supersedes / superseded by: extends PLAN §7.2

## D-46 — QEMU process supervisor
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.4 lists the per-OS mechanisms for tying QEMU to the controller; details such as which pid is killed under the macOS reaper, and the Go runtime's thread behavior with `Pdeathsig`, are not specified.
- Decision:
  1. `qemu.Start(ctx, argv, opts)` returns a `*Process` that satisfies `killswitch.QEMU` (`Kill`, `Exited`). `argv[0]` must be an absolute path (a name resolved through PATH is refused). Minimal environment (`PATH`, `HOME`, Windows system variables, `LC_ALL=C`), cwd = the session directory, stdout and stderr merged into a 64 KiB `Ring`. Cancelling `ctx` kills the whole process group; the kill switch kills first and cancels afterwards, so this is a second line.
  2. **Linux:** own process group (`Setpgid`, so Ctrl+C aimed at the controller does not reach QEMU, which the controller then stops through the kill switch) and `Pdeathsig: SIGKILL`. The kernel delivers `Pdeathsig` when the *thread* that created the child exits, and Go may retire threads, so the goroutine that starts QEMU **locks its OS thread and also waits for the process**; the thread ends together with that goroutine, after the child is gone. `Kill` signals the group (`kill(-pid)`), falling back to `Process.Kill`.
  3. **Windows:** own console process group, no console window, and a **Job Object with `KILL_ON_JOB_CLOSE`** that only the controller holds a handle to, so the OS ends QEMU whenever the controller dies. (The process is assigned to the job right after it starts; QEMU starts no children in that window.)
  4. **macOS (and any Unix with `ForceReaper`, which is how it is tested on Linux):** QEMU runs under the hidden `controlsnack __reaper --parent <pid> -- <qemu> <args>`. The reaper starts QEMU in its own process group, reports QEMU's pid on file descriptor 3 (`CSREAPER/1 pid=N`) so the controller can kill **QEMU itself** rather than the wrapper, polls `getppid` every 200 ms (kqueue is not needed) and kills QEMU's group when the controller is gone or it gets SIGTERM/SIGINT/SIGHUP; it exits with QEMU's status. The reaper is not given `Pdeathsig` on Linux, because a wrapper killed without a chance to clean up would orphan QEMU.
  5. `Stop(ctx, quit, grace)` calls the QMP-quit callback, waits up to the grace period (3 s by caller), then kills; it returns only when the process is gone. `Kill` is idempotent and a no-op after exit.
- Verified: unit tests on Windows (real Job Object: controller killed ⇒ child gone) and on Linux with `-race` (Pdeathsig and the reaper path both: controller killed ⇒ child gone; kill under the reaper ends QEMU itself). Measured kill switch → supervised process gone: 0.5 ms (Windows), 0.3 ms (Linux). **Not run on macOS** (no machine); the reaper logic is OS-neutral Unix code and only `Setpgid` differs.
- Supersedes / superseded by: —

## D-47 — QMP client
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN P3-T05 lists greeting, `qmp_capabilities`, commands, events, `screendump`, `input-send-event` and `quit`, tested against a fake server. QMP can also run arbitrary monitor commands, add backends and devices, and start migrations.
- Decision:
  1. **The client sends only a closed allowlist of commands**: `qmp_capabilities`, `query-status`, `query-version`, `query-chardev`, `quit`, `system_powerdown`, `stop`, `cont`, `screendump`, `input-send-event`. Anything else is `ErrCommandNotAllowed` and nothing is written. Adding a command means adding it here with a test, the same rule as the QEMU argv allowlist.
  2. **Events are remembered** (last 256) so that waiting for one cannot lose a race with it: `WaitEvent` looks at the past first, and `WaitPortOpen(id)` follows the latest `VSERPORT_CHANGE` per port (it keeps waiting after a close, because the guest stub reopens the port when the agent is re-pushed). Waiters are released with the connection's error when it ends.
  3. Messages are newline-delimited JSON, at most 4 MiB each; a longer or non-JSON message ends the connection. Replies are matched by a numeric `id`; a reply nobody waits for is dropped; a caller whose context ends stops waiting but the connection stays up.
  4. `Quit` counts the connection closing as success (QEMU may exit before it answers). It is the graceful callback of `qemu.Process.Stop`.
  5. `Dial` retries until its context ends (QEMU creates the chardevs one by one and `wait=on` serves them in turn; a socket that exists but is not served yet is retried too). TCP endpoints must be `127.0.0.1`.
  6. `screendump` takes an absolute plain path and `png` or `ppm`; `input-send-event` takes at most 256 events built by `MoveTo` (absolute pointer scaled to 0..0x7fff over the screen, clamped), `Button` (left/middle/right/wheel/...) and `Key` (QEMU qcode names).
- Verified: fake-server tests (handshake failures, out-of-order replies, errors, loss, oversize and garbage input, event history), a fuzz target over the byte stream (355k executions, clean), and against a real QEMU 8.2.2 in WSL under `-race`: greeting and version, `prelaunch` state, QEMU's own `GenericError` reply to a screendump without a console, `Stop` via `Quit` ends the process in 2 ms with `SHUTDOWN host-qmp-quit`.
- Supersedes / superseded by: —
