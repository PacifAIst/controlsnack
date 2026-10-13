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

## D-48 — Launch with the selected accelerator and one TCG retry
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.9 specifies selection, the launch window, the single retry and how the fallback is surfaced; it leaves open how per-attempt files are created, what counts as "fatal", and how a half-started VM is cleaned up.
- Decision:
  1. `vm.Launch(ctx, LaunchOptions)` takes everything environmental as a parameter: `Resolve` (accelerator choice), `Prepare(ctx, attempt)` (fresh socket dir or ports, fresh overlay, UEFI variable copy — the session supplies it), `Backend` (start QEMU, dial QMP, dial a chardev; `RealBackend()` is production) and `Events` (`AccelSelected`, `AccelFallback`). `AuditEvents` writes them as lifecycle audit records (`accel_selected {requested, chosen, tried[]}`, `accel_fallback {requested, from, used, reason, stderr}`) and forwards to a second listener (terminal warning, viewer notice).
  2. The launch window is 30 s, ×4 for tcg; inside it QEMU must be dialed on every chardev (all dials concurrent; when one fails the others are canceled and joined before cleanup), answer QMP, report `running` and stay running for 10 s (not scaled). Failure: process exit, QMP not ready, window expiry (the message says whether the VM ever reached running), or a state of internal-error, shutdown, guest-panicked, io-error, watchdog.
  3. **One retry, hardware accelerators only.** A failed attempt is killed, its output captured, the process waited for (5 s); its files are cleaned up before the next `Prepare`. **No retry** when the failure was tcg, when the caller's context ended, when the failed QEMU would not die (a second QEMU never starts beside a live one), or when the failure was ours (files could not be prepared, the command line was refused by the safety check; exit code 1) or QEMU could not be executed at all.
  4. Both failing ⇒ exit code 3 with both attempts' reasons and stderr excerpts (≤ 1 KiB each, the *end* of the output, with QEMU's `info: QEMU waiting for connection` chatter removed) and the hint to run `controlsnack doctor`.
  5. `VM.Stop` = QMP `quit`, 3 s, kill; `VM.Kill` = immediate; both release the connections and run the cleanup once. `AllocateEndpoints`: unix sockets inside a short private directory (`NewSocketDir`) on Linux/macOS, free loopback ports on Windows.
- Verified for real (WSL, QEMU 8.2.2, TCG, blank disk): the golden runtime argv — including `-sandbox on,…spawn=deny…`, virtio-vga, xhci, virtio-serial ports and `-no-reboot` — is accepted; launch takes 2.15 s with a 2 s hold; `screendump` returns a real PNG; `Stop` 3 ms; `Kill` 2 ms; an explicit `kvm` request is refused by QEMU ("Could not access KVM kernel module: Permission denied") and the retry with tcg takes over with that text recorded. Fake-backend tests cover every branch; `-race` clean.
- Supersedes / superseded by: —

## D-49 — `controlsnack doctor`
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.10 lists what the doctor reports and the cases it distinguishes; D-44 showed that the Windows optional-feature state does not predict whether WHPX works.
- Decision:
  1. **Structure.** Per-OS collectors (`doctor_{linux,windows,darwin,other}.go`) only gather `Facts`; the parsers of what the OS prints (`/proc/cpuinfo`, `/etc/os-release`, the WMI JSON, codesign output) are plain functions tested on every OS; `Diagnose(Facts, QEMUState, AccelResult)` is a pure function that returns notes, remediation steps and the verdict and is table-tested for every case of §7.10 on every OS; `DoctorService` ties them to the same `qemu.Discover` and `ResolveAccel` that `run` uses, so the verdict cannot differ from what a run does (tested for every setting). The probe constructor is injectable for tests.
  2. **Read-only and bounded.** Nothing is changed or installed; Windows facts come from the registry, `IsProcessorFeaturePresent`, `WHvGetCapability` and one non-interactive PowerShell WMI query run via `-EncodedCommand` (30 s); a denied or failing query is reported as a problem and the rest of the report stands.
  3. **The decision follows the probe, not the switches** (D-44): when the hardware accelerator worked, nothing is "fixed" even if an optional feature reads disabled; when it did not, the Windows cases are 1 firmware off and no hypervisor (BIOS, then the feature), 2 feature off (enable it; with a hypervisor running, "do not disable WSL2 or Hyper-V"), 3 feature on but no hypervisor reported (restart; `bcdedit /set hypervisorlaunchtype auto`), 4 everything on but QEMU's launch fails or the build lacks WHPX (install a current QEMU). "Off in the BIOS" is never said while a hypervisor is running.
  4. **Linux looks at `/dev/kvm` first** (found while testing in WSL2, where `/proc/cpuinfo` has no `vmx` yet `/dev/kvm` exists): existing-but-unopenable ⇒ `usermod -aG kvm $USER` (or re-login); CPU flags, module loading and nested-virtualization advice apply only when the device does not exist.
  5. **Exit codes.** 0 when a VM can run (any accelerator, tcg included); 3 when QEMU is missing, too old, lacks a device, or cannot start an empty machine with tcg; 2 when `--accel`/`--slow` are contradictory or name an accelerator the OS does not have (the doctor loads configuration without validating it, so it can diagnose broken setups).
  6. **Extra checks are pluggable** (`Check`, 10 s each): the base image (P3-T06), the viewer's browsers (P6) and local model servers (P5) register theirs when they land; until then the report has none. `--json` prints the whole `Report`.
  7. Long PATHs: `qemu.NotFoundError` counts the directories that simply lack QEMU instead of listing them (the full list stays in `Attempts`).
  8. CI: the macOS and Windows jobs install QEMU (`brew`, `choco`) and run `doctor --json` (must exit 0), keeping the JSON as an artifact — the first real evidence for HVF and WHPX on hardware.
- Verified for real: Windows 10 (edition, build, hypervisor running, firmware virtualization correctly false while a hypervisor runs, the three feature states, `WHvGetCapability` true; QEMU not found ⇒ verdict `cannot run a VM`, exit 3, install step); WSL2 against QEMU 8.2.2 (kvm refused for group membership, tcg chosen, exit 0).
- Supersedes / superseded by: —

## D-50 — Image manager and the Debian upstream
- Status: Accepted (2026-10-12) · Type: Interpretation (one deviation from PLAN §7.5)
- Context: PLAN §7.5 describes the pipeline and the image cache; Appendix B says to re-check Debian's release, URLs and whether a checksum signature exists before relying on them.
- Decision:
  1. **Verified today (2026-10-12):** Debian 13 "trixie" is stable; `https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-{amd64,arm64}.qcow2` exist (341,508,096 bytes for amd64, built 2026-10-01), `SHA512SUMS` lists both, `latest/` redirects (302) to an HTTPS mirror that honors range requests (`206`). **Debian publishes no signature for `SHA512SUMS`** (`.sign`, `.asc` and `.sig` all 404), so there is nothing to verify against a pinned key: the manifest records `signature_verified: false` with the reason, and the image is trusted on HTTPS plus the checksum list fetched from `cloud.debian.org` itself (the origin, not the mirror the image comes from). No PGP dependency is added.
  2. **Layout:** flat files in `<user cache>/controlsnack/images/`: `base-<id>.qcow2` and `base-<id>.json`; the id is the first 12 hex digits of the sealed file's SHA-256. The manifest has the PLAN fields (id, created, controlsnack_version, recipe_sha256, upstream {url, sha512, signature_verified (+ note)}, arch, sha256, size, packages) and is validated strictly on read and on import (nothing in an imported manifest is trusted: ids, URLs, package names are all pattern-checked; unknown fields and trailing data are errors).
  3. **Deviation:** a sealed image is `chmod 0400` (read-only, this user only) instead of the plan's 0444: the image is never written and QEMU runs as this user, so world-readable bought nothing.
  4. **Download:** HTTPS only, redirects only to HTTPS and at most five; resumable through `<file>.part` and `Range` (what is on disk is hashed first, so the SHA-512 comes out right without re-reading); a server that ignores `Range` means starting over, a wrong `Content-Range` is an error, `416` on a complete part finishes the job; an idle timeout (30 s without a byte) and a size cap (4 GiB, checked against the announced size and the bytes actually received); the destination only appears after the checksum matched. A resumed file that fails its checksum is retried once from scratch (the server's file may have changed); a fresh download that fails is final and nothing is kept.
  5. **Store operations:** `List` (newest first; unusable entries are reported, never fatal), `Find` by id prefix (≥ 4 digits, ambiguity is an error), `Newest(arch)`, `Resolve(vm.image, arch)` (empty = newest, an id, or a path to a qcow2 file used as it is), `QuickCheck` (existence and size — what `run` does every start; hashing 2+ GB each time would be absurd), `Verify` (full SHA-256, cancellable), `Remove`, `Import` (manifest valid, qcow2 magic, size and SHA-256 match, copied atomically, id not already present), `Commit` (used by the build to seal).
  6. `image rm|verify|path` with no matching image exit with code 4 (`ImageMissing`); `image import` of a file that does not match its manifest exits 1; `image path` prints the path and nothing else.
- Verified: mutation-checked tests (breaking the resume offset, the checksum comparison and the HTTPS redirect rule each make tests fail); an opt-in test (`CONTROLSNACK_NET_TESTS=1`) against the real server: both architectures' checksums parse, the mirror redirect is followed, an oversize limit stops before any body is read, a real range request returns 206 with the full size.
- Supersedes / superseded by: —

## D-51 — The seed ISO and the guest recipe
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.5 step 4 lists what the seed contains; Appendix B asks to check the ISO library (go-diskfs) before relying on it. The recipe is what the first real image build (P3-T08) will exercise.
- Decision:
  1. **ISO library.** `github.com/diskfs/go-diskfs` v1.9.4, only its `filesystem/iso9660` and `backend/file` packages: **MIT** (the plan assumed Apache-2.0; both are on the allow-list) and a footprint of one more permissive module (`djherbis/times`) plus `x/sys`. Hand-rolling ISO 9660 + Rock Ridge + Joliet was rejected: the point of the seed is that Linux and cloud-init read it. Both extensions are enabled — without them the files would be named `USER_DAT`, which cloud-init does not read.
  2. **Findings while testing it.** go-diskfs's `Open`/`ReadDir` follow `io/fs` rules (unrooted names: `"."`, `"user-data"`; a leading `/` is "invalid argument"). It pads the **primary volume descriptor's text fields with NUL bytes** instead of spaces (ECMA-119 8.4); `blkid` tolerates it, a strict reader may not, so `seed.Build` rewrites those fields with spaces after finalizing. The tests read the ISO with an independent reader written from the specification (primary and Joliet descriptors, UCS-2 names) and, where it exists, with `blkid` (`LABEL="cidata" TYPE="iso9660"`).
  3. **The recipe is data in `guest/files/` plus a table in Go** (destination, mode): LightDM autologin of `cs`, session environment (AT-SPI variables, loopback proxy, no blanking), power manager without blanking or locking, autostart entry and `bootstrap.sh` (the stub of PLAN §5.1, exactly the line format `internal/protocol` writes — tested both ways), the udev rule for `org.controlsnack.*` (checked against `protocol.PortAgent`/`PortNet`), Chromium policy and flags, an nftables ruleset that drops everything except loopback (enabled only at the end of provisioning, because the build needs the network), apt options, a GSettings override for toolkit accessibility. All files go into the cloud-config base64-encoded, so no content can break the YAML; the YAML itself is produced by `yaml.Marshal`.
  4. **Provisioning is one script** (`provision.sh`, `set -Eeuo pipefail`), not a long `runcmd`: installs the packages, configures, purges (openssh-server, unattended-upgrades, light-locker, xfce4-screensaver), quiets GRUB, disables cloud-init for later boots (no seed and no NIC at run time, so nothing may wait for a datasource), self-checks the result (commands, the `cs` user and its groups, no `debian` user), cleans, trims and powers off. It prints `CS-PROVISION:` lines to the kernel's last console (ttyS0 or ttyAMA0) for the host to follow; any failure prints `FAILED at line N: <command>` and powers the VM off instead of hanging. `CS-PROVISION: done` before the power-off is the success marker.
  5. **Accounts.** Only `cs` (uid 1000, bash, locked password, no sudo, no extra groups); no default user, no SSH keys, no password fields anywhere (tested).
  6. **Recipe hash** = SHA-256 over a version string, the user, the sorted package list, and every file's path, mode and content (length-prefixed fields); it goes into the manifest and the cloud-init instance id.
  7. **Package list** validated against the real Debian 13.7 archive with `scripts/aptsim.sh` (private apt state, no root): every name resolves, closure ≤ 399 packages, and none of screensaver, locker, NetworkManager, PulseAudio daemon, OpenSSH, polkit daemon or gvfs is pulled in. Not yet booted: that is P3-T08.
- Supersedes / superseded by: —

## D-52 — Building the image for real (P3-T08)
- Status: Accepted (2026-10-12) · Type: Deviation from D-50 / PLAN §7.5 (the upstream image) plus Interpretation
- Context: the first real build (WSL2 on the Windows dev machine, QEMU 8.2.2, software emulation) failed four times, each time for a reason no fake could have shown. The last one decided the choice of base image.
- Decision:
  1. **The base image is Debian's "generic" cloud image, not "genericcloud".** The genericcloud kernel (`linux-image-cloud-amd64`) is built without graphics and input support: in 6.12.107+deb13-cloud-amd64 `CONFIG_DRM` is not set at all, there is no `usb-xhci`/`usbhid`, and no emulated NIC drivers. The finished desktop image booted to a login prompt and never started X, so the boot check timed out after 8 minutes (the console showed LightDM "started" and nothing after it). `debian-13-generic-{amd64,arm64}.qcow2` carries the standard kernel (435 MB instead of 341 MB for amd64); verified 2026-10-12 that `SHA512SUMS` lists both and that the mirror honors range requests. The reason is written next to `DebianStable` so nobody switches back to save 90 MB.
  2. **The seven steps** (§7.5): preflight (QEMU, accelerator as `run` resolves it, ≥ 8 GB free, confirmation unless `--yes`), upstream lookup (skipped with "up to date" when an image of the same recipe and the same upstream checksum exists, unless `--force`), download, overlay + seed, provisioning boot, flatten (`qemu-img convert -c`), **boot check**, commit. One build at a time per user (`platform.AcquireLock`, pid + start time, stale locks are taken over). Everything the build touches is a field of `Builder` with a real default, so the stage failures (overlay, provisioning, convert, boot check) are tested with fakes and each leaves nothing behind.
  3. **The provisioning VM has what the runtime VM must not:** a display device (a q35 guest without one hangs after GRUB under tcg) and a NIC (`user,model=virtio-net-pci`, the guest's only way to `apt`). The NIC is a single exemption in `safety.AssertQEMUArgv` (`ProvisioningNIC`, provisioning profile only, exact string); the runtime profile still has `-nic none` and the checker still rejects every other NIC in both profiles.
  4. **Failures that real runs showed and tests now pin:** dpkg configuration-file prompts block a non-interactive install (`Dpkg::Options force-confdef/confold` in `apt.conf`; `TestTheBuildNeverStopsToAsk`); the provisioning script wrote every progress line twice (cloud-init also sends stderr to the console), so `say` writes the console only; `glib-compile-schemas` is in `libglib2.0-bin`, which `--no-install-recommends` leaves out; `signal.Notify` re-enables SIGINT and SIGHUP that the parent had ignored (a background job, `nohup`), so `handledSignals` leaves ignored ones ignored; QEMU does not quit when its stdin ends (the sandbox probe now sends `quit`); the VSERPORT_CHANGE event only names a port that has a device `id=` (`agent0`, `net0`).
  5. **A failed boot check keeps its evidence:** the sealed disk as `last-build-failed.qcow2`, a QMP screendump of what the guest shows (`last-build-failed.png`) and the whole console (`last-build-failed-console.log`), all named in the error; the next build removes them. A failed provisioning keeps the console as `last-build-failure.log`. Both exist because a stuck guest cannot be understood from a time-out message.
  6. **Timings:** the boot check waits `BootCheckTimeout` = 180 s (times 4 under tcg), provisioning has an overall and an idle limit (also times 4 under tcg), and an unexpected exit of QEMU during either ends the wait at once.
- Measured (this machine, tcg): provisioning 8 min 12 s, the whole build 10 min 35 s, sealed image 765.3 MiB, the guest opened the agent port after 1 min 12 s in the boot check. (The failed genericcloud attempt: 669.5 MiB.) Accelerated builds are not measured here (no `/dev/kvm` access for this user; `sudo usermod -aG kvm $USER` would give it).
- Supersedes / superseded by: amends D-50 item 1 (file names); D-51 unchanged.

## D-53 — Overlays, kept VMs and locks (P3-T09)
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §7.6 gives one paragraph; the details below decide what can go wrong with a disk that two sessions or a moved base image could damage.
- Decision:
  1. **Overlays always name their base by absolute path** (`qemu-img create -f qcow2 -F qcow2 -b <abs> <overlay>`), because a relative backing file is resolved against the overlay's directory and breaks as soon as the base moves; a session overlay is deleted with its session.
  2. **Kept VMs** live in `<state>/vms/<name>/` with `disk.qcow2`, `vm.json` (name, base image id and path, arch, created, last used, version) and, on aarch64, `vars.fd`. Names are 1 to 40 characters of `a-z0-9-` starting with a letter or digit (they become directory names; the Windows reserved names are refused too). Generating a name for a bare `--keep-vm` belongs to the session (P7-T06).
  3. **Opening a kept VM checks two things:** its recorded base id equals the image `run` resolved (else `BaseMismatchError`, naming both), and the qcow2 header's backing file really is that image (`CheckBacking` reads the header itself, with the same-file test of the platform, so a swapped or deleted base is caught before QEMU fails with an unreadable message). A base given by path (outside the store, no id) cannot be the base of a kept VM.
  4. **One session per kept VM:** `Acquire` takes `<vms>/.<name>.lock` with `platform.AcquireLock` (pid + process start time, so a lock of a dead process, or of a pid that was reused, is taken over; a live one is "in use by process N"). `vm ls` shows name, base image, created, last used, disk size and a status (ok, base image gone, disk does not match its base); `vm rm` refuses a VM that a session is using, and removes the directory without ever following a link out of it.
  5. `platform.FreeSpace` (statfs / `GetDiskFreeSpaceEx`) and `platform.AcquireLock` are shared by the image build and the kept VMs.
- Verified: real `qemu-img` overlays (header parsing against what qemu-img writes), mutation checks on the base-id and backing-file tests.
- Supersedes / superseded by: —

## D-54 — The agent connection: dispatcher, installer and client (P3-T10)
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §5.1 specifies the bootstrap, the frames, the handshake and the heartbeat; §7.7 says "reconnect when the bootstrap reopens the port". Writing the minimal agent and the client raised the points below.
- Decision:
  1. **`ping` is answered by `pong`**, the one request whose response is not `<name>.res` (`protocol.ResponseOf`, §5.1); the registry test pins it and that `ping.res` does not exist.
  2. **The dispatcher answers `ping` inline** on the reading goroutine, and **concurrency limits never stop the reader**: requests beyond `MaxInFlight` (default 8) wait for a slot inside their own goroutine (a cancel releases them), up to 64 running or waiting, after which the answer is `busy`. The first version blocked the reader at the limit; its test hung, which is how it was found: with the reader stuck neither pings nor cancels get through, so a busy agent would have been declared dead exactly when it needed to be cancelled.
  3. **Handler failures stay small:** a handler's own `*HandlerError` carries its protocol code; any other error is `internal` with its text cut to 1000 bytes of valid UTF-8; a panic becomes `internal` without its message (the log has it) and the agent lives on; a request the host cancelled is answered `canceled`; a second request with an id that is still running is `busy`; `id 0` requests are ignored (that id is for agent events); a second `hello` is refused.
  4. **A refused hello ends the session** (`ErrHandshake`; the stub then waits for a new push). An agent started without `--nonce` echoes the one it is given (tests only; the stub always passes it).
  5. **The host connects afresh every time** (`agentclient.Dialer`): the first connection is the one `Launch` already holds, every reconnect dials the chardev again. Closing the host's end is what makes a hung agent in the guest see end-of-file and exit (virtio-console reads return 0 while no host is connected), after which the stub's loop (which already sleeps and retries while the port reads as end-of-file) waits for the next push. Re-pushing on the old stream could not do that, and would have had to guess where the dead agent's last frame ended.
  6. **Every blocking step has a bound:** the push and the stub's verdict (`InstallTimeout`, 60 s), the handshake (`HelloTimeout`, 10 s), each single write to the port (`WriteTimeout`, 20 s: an agent that stops reading cannot block the host, and a failed write tears the connection down because a half-written frame cannot be repaired). `vm.Boot` multiplies them by the accelerator's timeout factor (4 under tcg).
  7. **Heartbeat:** a `ping` every 2 s with 2 s to answer; three misses in a row end the connection (`ErrAgentDown`); a late pong counts as a miss. The connection's context is derived with `context.WithoutCancel` from the `Connect` call, so a short connect deadline does not kill a connection that was made in time.
  8. **Everything the agent sends is checked:** the response type must be `<request>.res` (or `error`), bodies decode strictly (unknown fields are errors), error codes and messages are clipped to 300 runes before they reach a message, answers to unknown ids and malformed events are ignored, and a wrong type, a malformed body or an oversize frame drops the connection (`ErrBadResponse`). A request the host refuses to build (a blob over 8 MiB) is a local error and costs nothing.
  9. **`Supervise`** re-establishes the connection with a pause that doubles from 500 ms to 5 s; `OnState` reports connecting, up and down(err); calls made while down fail at once with `ErrAgentDown` (whether to wait is the session's decision).
  10. **One test hook in the guest stub, `CS_PORT=-`** (file descriptor 3 is already the port), makes the real `bootstrap.sh` testable against the real compiled agent and the real client over a socket pair (Linux only): install, the file's place and mode, the hash check, the handshake, ping, the stub's refusals (`arch-mismatch-<arch>`, `sha256-mismatch`, `bad-line`) and the stub taking the agent's end as its cue to wait for the next push.
- Verified for real: in a booted guest on the real image, the 2.9 MB agent is pushed over virtio-serial, hash-checked by the real stub and shakes hands within about 2 s under tcg (D-55).
- Supersedes / superseded by: —

## D-55 — Booting a session VM, and `run --no-llm --viewer none` (P3-T11)
- Status: Accepted (2026-10-12) · Type: Interpretation
- Context: PLAN §12 asks for a milestone before the viewer and the model exist: the VM side of a session, end to end. `internal/session`'s placeholder runner now does it; every other `run` combination still exits 70.
- Decision:
  1. **`vm.Boot`** = `Launch` + wait for the guest to open `agent0` (the QMP event; QEMU dying first ends the wait at once, with its output and the console tail) + install and handshake + `Supervise`. It returns a `Machine` with the timings of each stage, which become the terminal summary, the audit `ready` record and the metrics. `NewPrepare` (sockets or loopback ports, overlay, aarch64 variable store per attempt) is shared with the image boot check. Waits are multiplied by 4 under tcg, like the launch window.
  2. **The runner's order matters:** configuration checks (QEMU, image, embedded agent, resolution) → the kill switch exists → a loopback-only control endpoint (`/control/kill`, the handler the viewer will mount later, token from the lock) → the session lock (one session per user, D-20; a stale one is reaped) → the audit log (`start`) → the session directory → boot. `controlsnack kill` from another terminal therefore asks the session to die, instead of killing the controller from outside.
  3. **How it ends decides the exit code:** Ctrl+C, SIGTERM or `controlsnack kill` → kill switch → **5**; session timeout → **6**; the guest powering off → 0; QEMU dying by itself → 1; no QEMU or accelerator → 3; no image → **4** with the command to build one; a missing embedded agent → 1 with `go run ./tools/dev agent`. The audit log always ends with an `end` record (an error is recorded in it), so `audit verify` calls every session complete; a log without one means a crash.
  4. **Found by a test, then confirmed as a real risk:** a kill from outside makes QEMU exit before the context is cancelled, and the first version of the wait loop reported that as "QEMU stopped by itself". The loop now asks the kill switch first (it sets its flag before it kills QEMU), and agent-connection messages stop once it has fired.
  5. **Session files** live in `<state>/run/<id>/` (overlay, serial log, screendump `ready.png`, UEFI variables on aarch64), not in `/tmp`: an overlay grows to gigabytes. A `.running` marker says a session owns the directory; the next start sweeps directories that still have it (the controller was killed hard), and `--keep` removes only the marker.
  6. **VM names** may not contain `_`, session ids do (`s_1a2b3c4d`): the QEMU name is the id with hyphens. `NewPrepare` validates it up front. (The first real run failed on this.)
  7. **The screendump** is taken through QMP right after the handshake and its PNG header is read to report size and dimensions; failing is a warning, not an error, because it proves a capability, not the session.
- Measured for real (WSL2 Ubuntu 24.04 on the Windows dev machine, QEMU 8.2.2, **tcg**: this user cannot open `/dev/kvm`, so "detected" and `--slow` are the same accelerator here): QEMU running after 10 s (the stability window), desktop up after 58 to 68 s, agent connected after 61 to 70 s (three runs: CLI, CLI with `--slow`, e2e), kill to QEMU gone 14 to 17 ms (SIGTERM, `controlsnack kill`, `Machine.Kill`), exit code 5, no lock, directory or process left. `doctor` and `run` chose the same accelerator. The desktop screendump (1280x800) shows the XFCE panel, the launcher dock and the desktop icons on a black background (no wallpaper package is installed).
- Not measured: an accelerated boot (KVM, WHPX or HVF): nothing here can use one; CI doctor artifacts and the first hardware run will tell.
- Supersedes / superseded by: —

## D-56 — The agent's worker pools (P4-T01)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 gives the worker counts per category (capture 1, input 1 serialized, a11y 1, OCR 1, exec 2, browser 1, wait_for 1) and says `ping` is always immediate and `cancel` aborts by id. P3-T10's dispatcher had one global limit.
- Decision:
  1. **A pool is a first-in-first-out queue with N workers.** A semaphore would not do: goroutines waiting on it are not served in arrival order, and `input` is only correct if two requests run in the order they were sent. Workers are started when there is work and leave when the queue is empty (an idle agent holds no goroutines); the count drops under the same lock that finds the queue empty, so a request pushed a moment later starts a new worker instead of waiting for one that left. A mutation test (serve the newest first) fails `TestInputIsSerializedInOrder`.
  2. **Categories** (`server.PoolOf`): `capture` = screenshot, screen_info, windows; `input`; `a11y` = a11y_tree, a11y_action; `ocr`; `exec`; `browser`; `wait_for`; and, beyond the plan's list, `file` (file_peek, 2 workers: it is a small read the exec policy needs while a command runs) and a spare pool for types without a category. `Config.Workers` overrides the counts (tests, tuning).
  3. **Cancel is exact:** a request still waiting is taken out of its queue, answered `canceled` at once and never reaches its handler; a running one gets its context cancelled and answers itself. The total of running and waiting requests is capped at 64 (then `busy`), as in D-54.
  4. **The session ending drops the queue:** workers skip jobs once the connection's context is done, so nothing queued runs for a host that has gone, and `Serve` still waits for the workers that are mid-handler.
  5. **Capabilities are derived from the registered handlers** (`server.CapabilityOf`), plus ping and the explicit extras (netproxy is a feature, not a request type), sorted and without duplicates. The agent cannot announce a capability it has no handler for, and cannot forget one it has.
  6. **Logs** go to stderr as JSON (the journal, in the guest); `--log-level` or `CS_LOG_LEVEL` sets debug/info/warn/error. Each request logs type, id, pool, queue time, run time and outcome, at debug when it succeeded and at warn when it did not. **Bodies, results and error text from commands are never logged**: they hold what the model typed and what the screen showed (tested with a command and an output that must not appear).
- Verified: pool isolation (a slow exec does not delay a screenshot; the third exec waits for one of two workers), input order and peak concurrency 1, cancel of a queued request, a session that ends with a queue, capabilities, the log content; green under `-race` (5 runs).
- Supersedes / superseded by: amends D-54 item 2 (the global in-flight limit became per-pool queues; the cap of 64 stays).

## D-57 — Screen capture and the WebP encoder (P4-T02; settles D-11)
- Status: Accepted (2026-10-13) · Type: Interpretation (completes D-11)
- Context: D-11 left the encoder to a benchmark "on real guest screenshots"; PLAN §8 asks to verify that `kbinani/screenshot`'s Linux backend is cgo-free, otherwise to use xgb directly.
- Decision:
  1. **Capture goes through `jezek/xgb` directly** (BSD-3, the maintained fork of BurntSushi/xgb): `GetImage` of the root window, `QueryPointer` for the cursor. Verified 2026-10-13: `kbinani/screenshot`'s Linux backend is cgo-free (only its macOS file imports "C"; it uses jezek/xgb and gen2brain/shm), but it brings Wayland and D-Bus code and a second X connection, and the agent needs xgb itself for EWMH, XTEST and RandR. One connection, one dependency.
  2. **One pixel format is accepted:** 32 bits per pixel, 24-bit true colour with masks 0xff0000/0xff00/0xff, least significant byte first (what Xorg gives). Anything else is `ErrUnsupportedFormat` at start-up, not a silently wrong picture. The unused fourth byte is set opaque (an encoder would take X's leftover for transparency). The capture buffer is reused; no MIT-SHM yet (a 1280x800 capture with the PNG encode took 312 ms under tcg in the real guest; SHM is for the performance pass, P8-T04, if the numbers ask for it). X does not draw the pointer into a capture, so the cursor is a separate field.
  3. **The corpus is real:** eight screenshots of the guest (bare desktop, terminal with text, file manager, text editor) at 1280x800 and 1920x1080, driven through QMP key events (`TestE2ECollectScreens`, `-tags e2e`). Benchmarked on this machine (i7-10870H, Go 1.26.9, five runs, median); PSNR is measured with one neutral decoder (`x/image/webp`, with the limited-range BT.601 conversion VP8 needs; the first run of the benchmark read 23 dB for every lossy WebP because Go's `image.YCbCr` assumes full range, and `gen2brain`'s own decoder returns planes, even for lossless files).
  4. **Results, 1920x1080, "terminal and file manager windows" / "terminal only" scenes:**

     | encoder | bytes | ms | PSNR |
     |---|---|---|---|
     | `deepteams/webp` q70, method 4 | 55,894 / 47,598 | 50 | 39.6 / 41.0 dB |
     | `gen2brain/webp` q70, method 4 | 56,836 / 47,960 | 600 | 41.6 / 44.0 dB |
     | JPEG q70 (stdlib) | 129,193 / 109,397 | 35 | 38.2 / 39.6 dB |
     | PNG (fastest) | 137,823 / 104,687 | 45 | lossless |
     | WebP lossless (`gen2brain`) | 52,288 / 28,214 | 1,200 / 220 | lossless |

     A bare desktop is 8 KB. The budget "< 200 KB per 1080p WebP q70 screenshot" holds with a factor of three.
  5. **Decision: `deepteams/webp` v1.2.8 (MIT), lossy, quality 70, method 4.** It is ten times faster for the same size (what matters in a guest that may run under tcg, where everything is 10 to 50 times slower, and at 5 frames per second); it adds 1.2 MB to the agent instead of 3.7 MB and no dependency; its files are accepted by the reference libwebp (decoded with `gen2brain`'s libwebp-based decoder on all eight images: 38 to 43 dB). `gen2brain/webp` is 2 to 4 dB better at the same size, but its "uses the system libwebp via purego" advantage did not engage in a `CGO_ENABLED=0` Linux build (the same 600 ms with and without the `nodynamic` tag, with libwebp.so.7 installed), so what remains is the transpiled Go at 600 ms. Method 4 is where effort stops paying (0 to 2 are slower and larger, 5 and 6 give the same bytes, 3 is 12 % faster for 3 % more bytes); the default preset beat `text` and `drawing`. Revisit if the model's reading of text turns out to need the extra 2 dB: lossless WebP is an option because screens compress very well (28 to 52 KB) but costs 0.2 to 1.2 s.
  6. **The `screenshot` request** validates everything before it touches the screen (format, quality 0 to 100 with 0 meaning 70, `max_long_edge` 0 or 32 to 7680, `if_changed_since` as 16 lower-case hex digits, unknown fields); hashes the raw capture (maphash over the size and every row, random seed per process, so hashes are only comparable within one agent) and answers `unchanged` with no picture when the host's hash matches, before any scaling or encoding; scales with `x/image/draw` BiLinear (never up, aspect kept, the response carries the factor); refuses an encoding over the 8 MiB blob limit with advice instead of sending a frame the host would reject; `marks_generation` is `unsupported` until the accessibility service exists (P4-T06/T07).
  7. **The agent announces `screenshot` only when it could open the display** (`DISPLAY` and `XAUTHORITY` are inherited from the session); otherwise it logs why, still answers pings, and reports the screen size it found in the handshake (`--screen-size` overrides it, for tests).
- Verified for real: in the guest, the agent's PNG matches QEMU's own screendump (within 1 % of the pixels, for a clock that ticks), `if_changed_since` gives `unchanged` plus the cursor, JPEG and WebP of 640 px and of the whole screen come back; WSL's X server (WSLg's XWayland) accepts `Open` and `QueryPointer` but answers `BadMatch` to reading the root window, as Wayland compositors do, so the capture integration test skips there and runs under Xvfb in CI.
- Supersedes / superseded by: completes D-11.

## D-58 — Windows and monitors: EWMH and RandR (P4-T03)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 says to read EWMH directly with xgb (no xgbutil), to focus and maximize with ClientMessages and to read monitors from RandR. The first run against the real window manager (xfwm4) showed what the plan could not.
- Decision:
  1. **The list is `_NET_CLIENT_LIST_STACKING` reversed**, so the window in front comes first (EWMH is bottom to top); `_NET_CLIENT_LIST` is the fallback; with no window manager the list is empty, not an error. At most 100 windows are read, each with its nine requests (name, old name, class, pid, frame extents, state, type, geometry, position) in flight together.
  2. **Shell windows are left out.** xfwm4's client list also holds the three windows of xfce4-panel and the desktop background (`Xfdesktop`, the size of the screen), and they sit on top of the stacking order. A window whose `_NET_WM_WINDOW_TYPE` is only DESKTOP, DOCK, TOOLTIP, DROPDOWN_MENU, POPUP_MENU, COMBO, DND or NOTIFICATION is `Shell`: not listed, not the active window, and `focus` or `maximize` on it answer `not_found`. A window with no type, or with any other type among its preferences, is a normal one.
  3. **What a window says about itself is cleaned:** the title is `_NET_WM_NAME` (else `WM_NAME`), the class is the class half of `WM_CLASS`; control characters and invalid UTF-8 are dropped, white space runs become one space, 256 and 128 characters at most. It stays untrusted text: a title can say "ignore previous instructions", and the host fences it (§6.10). Bounds are the client rectangle plus `_NET_FRAME_EXTENTS`, in screen pixels, so they match what a screenshot shows.
  4. **Ids** are `0x` and lower-case hex; nothing else parses, and an id must be in the current list, so the host cannot make the agent send a message to an arbitrary X window.
  5. **Focus** is a `_NET_ACTIVE_WINDOW` ClientMessage to the root window with source indication 2 ("a tool", which window managers honor); **maximize** adds `_NET_WM_STATE_MAXIMIZED_VERT` and `_HORZ`. Both then poll for 1.5 s until the window manager shows the effect and answer `timeout` when it does not, instead of reporting success for something that did not happen. A request that the host canceled stays a cancellation.
  6. **Monitors** come from RandR 1.5 `GetMonitors` (active ones); without it, or with none, the screen is one monitor. The guest's single virtual output has no primary flag set, so when none is flagged the first monitor is primary (found in the first real run).
  7. **`screen_info`** adds the pointer and the focused window; no focused window and no readable pointer are facts, not failures.
- Verified for real (xfwm4 in the guest): the terminal appears with title `Terminal - cs@controlsnack: ~`, class `Xfce4-terminal`, its pid and bounds `[0 27 827 519]` (below the 27 px panel); Thunar opened from it comes up in front and active; `focus` puts the terminal in front and active; `maximize` gives Thunar `[0 27 1280 773]`; a window that is not there is `not_found`, a malformed id `invalid_args`.
- Not covered: more than one monitor (the logic is unit-tested, no multi-monitor X server was available), moving, resizing and closing windows (not in the protocol).
- Supersedes / superseded by: —

## D-59 — Input: XTEST, keys and Unicode typing (P4-T04)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 specifies XTEST for pointer and keys, keysyms resolved through the keyboard mapping, Unicode by a borrowed keycode, gestures with timings and the screen hash after the input. Several things the plan leaves open came up while building and testing it.
- Decision:
  1. **A request is validated whole before anything is sent**, so a refused request does nothing at all: at most 64 events, 4000 characters of text in all, scroll at most 20 notches, key repeat 1 to 20, every coordinate inside the *current* screen (the size is read per request), buttons left, middle or right, a key combination that parses, and no control characters in text (only newline, carriage return and tab are keys). A drag is an event type of its own (`drag`: press at x, y, move to `drag_to`, release); `down`, `up`, `click` and `drag` move the pointer to their position first.
  2. **Pointer and buttons through XTEST** (absolute motion, buttons 1 to 7: wheel up and down are 4 and 5, left and right 6 and 7). Each fake event is sent checked, one round trip each, so an error surfaces at the event that caused it. Timings: a click holds 12 ms, a double click is two clicks 60 ms apart (the desktop's interval is 400 ms), 8 ms between wheel notches, a drag goes in ten steps of 20 ms with 30 ms before and after (about 260 ms in all), and keys are pressed and released 10 ms apart (xdotool uses 12). A button that was pressed is always released, also when a step fails or the host cancels.
  3. **Key combinations are xdotool's**: `ctrl+l`, `alt+F4`, `Return`, `ctrl+shift+t`, `ctrl++`. Modifiers are ctrl (control), alt, shift, super (win) and altgr, in any case; one other key at most; named keys in any case (`return`, `Page_Down`, `f12`, `kp_5`), a single character exactly as typed, so `L` is shift and `l` as in xdotool. Keysyms are resolved through the live keyboard mapping, read again for every request that types (that picks up layout changes without watching MappingNotify): the plainest route wins (lowest column, then lowest keycode), AltGr is supported, and the legacy EuroSign keysym stands in for the Unicode one.
  4. **Unicode typing borrows a keycode**, as `xdotool type` does: the highest keycode that has no keysym gets the character's keysym in every column (Unicode keysyms are 0x01000000 plus the code point), the server is synchronized, clients get 15 ms to refresh their keymap, the key is tapped, and the keycode is given back at the end of the request; a repeated character is not remapped again. The plan's "xdotool type as fallback" is not built: the borrowed keycode worked in the guest's GTK applications, and the fallback can be added behind `exec` (P4-T05) if an application ever shows otherwise.
  5. **No key is ever left down.** A modifier that stays down turns the user's next keystrokes into shortcuts, so every request ends by releasing whatever is still held (last pressed first), after success, failure or cancellation. Two bugs of the first version were found by tests: a key that was dropped from the held list *before* its release succeeded would never be released again (a failed release now stays on the list and is retried), and a character's own shift released a shift that the combination itself held (a tap now releases only what it pressed).
  6. **The event queue is drained.** xgb stops reading from the socket when its event queue is full, and every keymap change sends this client a MappingNotify; a goroutine drops the events.
  7. **`screen_hash_after`** is the hash of the screen after the last event, from the screenshot handler's hasher, so it can be compared with a later screenshot's hash (and in the real guest it is: the next screenshot with `if_changed_since` answers `unchanged`). It is taken 120 ms after the last event and, while the screen is still as it was before the input, again every 100 ms for up to 500 ms: the effect of a click shows a moment later, and "nothing changed" is information the host uses (loop detection, PLAN §6.6), not something to report because the screen was looked at too early. An unreadable screen gives an empty hash, not an error.
  8. **A failure says how far it got:** "event 3 (click) failed after 2 earlier ones ran". A key name the keyboard cannot type is the caller's mistake (`invalid_args`); anything else is `internal`.
- Verified for real (xfwm4, xfce4-terminal in the guest): a typed `héllo wörld 日本語 😀 € ñ` reads back exactly from the terminal's window title (set by an escape sequence in the typed command, read through `windows`); BackSpace with repeat 2; `ctrl+alt+t` opens a terminal and `ctrl+c` ends a command; a click on a title bar focuses the window; a drag moves a window by exactly (300, 150); the hash after the drag equals the next screenshot's.
- Not verified: scroll, middle and right clicks and double clicks against an application (the event sequences are unit-tested).
- Finding for the image: the guest has no CJK font, so characters that are typed correctly display as empty boxes in screenshots (`fonts-noto-cjk` is about 60 MB): decide at the image rewrite (P4-T13).
- Supersedes / superseded by: —

## D-60 — The exec runner and `file_peek` (P4-T05)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 says `bash -lc` with `Setpgid`, SIGTERM then SIGKILL after 2 s, output capped at 64 KiB per stream (head 48 KiB, tail 16 KiB) and a quiet environment. The plan does not say how output is collected; that choice decides whether the usual way a model starts a program works.
- Decision:
  1. **Output goes to private files, not pipes.** With pipes, a program the command starts in the background (`thunar &`, `chromium … &`, the idiom a model will use all the time) holds the pipe open: either the exec waits for it, or (with a wait delay) the pipe is closed and the program dies of SIGPIPE at its next write. With files the exec waits only for the shell, the background program keeps writing harmlessly, and the head and the tail of a long output can be read without holding it in memory. The files live in a 0700 scratch directory that is removed afterwards; stdin is a file too (or `/dev/null`).
  2. **A stream is capped at 64 KiB by default (1 to 128 KiB on request): the first three quarters and the last quarter**, with a line saying how many bytes were left out. Because a stream can grow while the command runs, the agent checks its size every 100 ms and stops the command when one stream passes 256 MiB (the notice is added to stderr): a runaway writer must not fill the disk while the caller waits for the timeout.
  3. **Output is cleaned**: terminal escape sequences (colors, cursor movement, window titles, links) are removed, every other control character except newline, tab and carriage return becomes U+FFFD, and invalid UTF-8 does too. A model gains nothing from escape codes, and the cleaning bounds the encoded size: a control character costs six characters of JSON (`\u0001`) and the response header is limited to 1 MiB, so 2 × 128 KiB of control bytes would otherwise make a reply the host cannot receive. The worst case is now three bytes per character.
  4. **Time limits:** 60 s by default, 100 ms to 10 minutes on request. At the end (or when the host cancels) the whole process group gets SIGTERM and SIGKILL 2 s later; once the shell is gone the rest of the group is killed too. A timeout is a result (`timed_out`, exit code 124 as `timeout(1)` does), a cancellation is the dispatcher's `canceled`. A normal exit does **not** touch what the command left in the background.
  5. **Exit codes follow the shell:** the status, or 128 plus the signal that killed the shell. **Environment:** the agent's own (so `DISPLAY` and the session bus reach GUI programs) plus `TERM=dumb PAGER=cat GIT_PAGER=cat DEBIAN_FRONTEND=noninteractive`, run as a login shell. **Directory:** `/home/cs/work` (the home directory if that is missing); a relative `cwd`, a missing one or a file is `invalid_args`.
  6. **The agent applies no policy.** Whether a command may run is the host's decision (§6.3); the agent refuses only what it cannot carry: a command over 16 KiB or with a NUL, stdin over 64 KiB, a timeout or output limit out of range.
  7. **`file_peek`** (the guest half of the script analysis of §6.3, wired in P7-T06): absolute paths only, symbolic links followed (the policy sees what the shell would run), regular files only, decided by `stat` before anything is opened so a pipe can never block the worker; the head (up to 64 KiB, default 64 KiB) is the response's attachment, the size is reported, a missing file is `exists: false`, a directory or device exists with size 0.
  8. **Unix only:** process groups come from `Setpgid`; elsewhere the runner answers `unsupported` (the agent runs in the Linux guest).
- Verified for real (the guest): the command runs as `cs` in `/home/cs/work` with `DISPLAY` set and the quiet environment; stdin and `cwd`; a timeout after 1.5 s with exit code 124; a canceled `sleep` leaves nothing running (`pgrep` finds none); `seq 1 100000` keeps head and tail; `thunar … & echo launched` returns at once and the file manager has a window 40 s later; `file_peek` of `/etc/os-release`, of a missing file and of a directory.
- Not covered: a command that detaches into a new session (`setsid`) survives a timeout (it is no longer in the group); resource exhaustion by a fork bomb is the exec policy's to deny, the VM's limits are the backstop.
- Supersedes / superseded by: —

## D-61 — Accessibility: AT-SPI over D-Bus, generations, actions (P4-T06)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 says to walk the AT-SPI tree of the active window, keep showing and visible nodes that are interactive or carry text, cap nodes, depth and time, number the nodes per generation, and act through `DoAction`, `GrabFocus` and `EditableText`, falling back to the pointer and the keyboard. The plan leaves open how the walk is bounded, what counts as worth reporting, and what an id means once the screen has moved on.
- Decision:
  1. **The walk goes level by level** (`a11y.Walk`), reading each level concurrently (eight bus calls in flight) and assembling the answer in document order (a parent, then its children, siblings in order). A first version walked depth first with free-running goroutines; when a limit cut in, *which* elements were left out depended on which call answered first, so the same window gave different trees. Level by level, what a limit leaves out is the deepest and last elements, always the same ones (tested 25 times with random latencies, and mutation-checked). Limits: 150 nodes by default (1 to 400), depth 30 (at most 60), at most four times as many elements looked at as nodes asked for, and a time budget of 1.5 s by default. The host may pass `budget_ms` (100 to 30000): the same tree takes several times as long without hardware acceleration (the guest under tcg needs about 25 ms per element), so the host scales the budget by the accelerator. Running out of time is not an error: what was found comes back with `truncated`.
  2. **What is worth reporting.** Nothing that is not showing and visible, and nothing below it (the contents of a closed menu are not on screen). Layout roles (filler, viewport, scroll pane, separator...) are skipped but their children are visited. Operable roles (push button, check box, menu, entry, text, slider...) are reported even without a name. Table cells, list items, tree items and icons are reported only with a name or description (a table of folders has four cells per row and one carries the name). Structural roles (panel, tool bar, table...) only with a *name*: toolkits put the tooltip of a button on the panel around it, which listed every toolbar button twice. An element with no area (no Component interface, or zero size) cannot be seen or clicked and is left out. Both rules came from reading real Thunar and Mousepad trees.
  3. **Text from applications is cleaned and bounded** (`internal/agent/textclean`, shared with the window titles): controls and invalid UTF-8 dropped, white space collapsed, names 300 characters, descriptions 200, values 500. **A password field's text is never asked for.** The answer is also cut to 700 KiB of JSON, since JSON writes `<` in six bytes and the protocol header limit is 1 MiB.
  4. **Ids and generations.** Ids are `a1`, `a2`... in document order; every tree request starts a new generation `g<n>`. The agent keeps the last four generations, so a batch of actions planned from one observation is not refused because a tree was read in between. An action first reads the element again: if it is gone, off screen, or has another role or name than it had when shown, the answer is `stale_element` (a new protocol error code); an unknown generation is `stale_element`, an id the generation does not have is `not_found`. Whether an older-but-kept generation is acceptable is the host's call (it knows the latest).
  5. **Scopes.** `active_window`: the application whose process owns the active window (`_NET_WM_PID` matched to the bus peer's process id), and in it the window it calls active, else the one with the window's title, else all its windows; with no match by process, only a window with the title. `desktop`: every application's windows on screen. `browser` is the browser tool's (`invalid_args` here). An empty tree says why in `note` ("no window has the keyboard focus", "the application of the active window does not expose accessibility information; use the screenshot, or OCR").
  6. **Actions.** `click`: for buttons, check boxes, radio buttons, menu items, links, tabs and switches, the element's own action (`click`, `press`, `activate`, `jump`, `toggle`... matched case-insensitively), otherwise a click in the middle of the element *as it is now*, through the input handler (same validation, same release of what was pressed). Lists, trees, tables and text are clicked, not actioned: their actions mean "open" and "edit", not "select". An element that is still busy with its action after two seconds (a button that opened a dialog and waits in it) did it: that is success, and no second click follows. `focus`: `GrabFocus`; if refused, a click only for elements that take text (a click on anything else could press it). `set_text`: only on an element that says it is editable; `EditableText.SetTextContents`, then the text is read back (never for a password field), and if the field did not keep it, select all (`ctrl+a`) and type. Text up to 4000 characters, line breaks and tabs allowed, other controls refused. The text is never put in an error message or a log line. The answer says how it was done (`action:click`, `pointer`, `editable_text`, `keyboard`).
  7. **The bus.** `Dial` asks the session bus for the accessibility bus (`org.a11y.Bus.GetAddress`; `AT_SPI_BUS_ADDRESS` overrides) and speaks AT-SPI with `godbus/dbus` v5.1.0 (BSD-2; v5.2.3 does not build for windows or darwin). An element's role, states and properties are asked as three calls in flight together. D-Bus errors map to "does not have this interface" (`ErrUnsupported`) and "no longer exists" (`ErrGone`). The connection is made when first used and again after it breaks (`LazyBus`: the agent starts with the session, and the accessibility bus may come up later). Outside Linux `Dial` is a stub answering `ErrUnsupported` (D-01).
  8. **Role names are a table of AT-SPI's own numbers** (`atspi-constants.h`, 0 to 130). The first version, written from memory, was off by one from role 21 (`FOCUS_TRAVERSABLE`); the tests now carry anchors written out so a shifted table cannot agree with itself, and the end-to-end test compares the table with `GetRoleName` for every role seen in the guest. The names are ATK's, which current ATK takes from its role enum: `button` (not `push button`), `statusbar`, `accel label`, `editbar`, `embedded`; the first two were found wrong by that test, the others follow from the ATK header.
- Verified for real (Mousepad and Thunar, GTK3, in the guest): the tree of the active window and of the desktop; the editor text read and set through EditableText (Unicode read back exactly) and cleared; a menu opened by its own action; a pointer click on a list row; every refusal; stale after the application closed; the 25 role numbers the bus produced all match `GetRoleName` (two names corrected: `button`, `statusbar`). A tree takes 0.6 to 2 s under tcg (about 25 to 35 ms per element plus 0.5 s).
- Not covered: Qt and Electron applications (none in the image), Chromium's own tree (the browser tool's, P4-T08), applications that expose no Component (their elements are dropped), the `Selection` and `Table` interfaces.
- Supersedes / superseded by: —

## D-62 — The set-of-marks overlay (P4-T06, completing D-30)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: D-30 puts numbered boxes for interactive accessibility elements on the model's copy of the screenshot. `screenshot` already carries `marks_generation`; until now it answered `unsupported`.
- Decision:
  1. **Drawn on the scaled picture, on a copy** (`imaging.DrawMarks`): the viewer's picture is never touched, and boxes are scaled with the image so they stay crisp. A 2-pixel outline just inside the element, a filled label with white text (`basicfont` 7x13) in the top-left corner; on an element too small to hold the label, above it (below it at the top of the picture). Six colors, dark enough for white text. Bigger boxes first so the label of a small element inside a big one stays visible; the result does not depend on the order given. Boxes under 4 pixels or outside the picture are left out; at most 100 marks.
  2. **What is boxed:** the operable elements of the generation (buttons, fields, menus, named list items and cells...) and anything that lists an action, in document order. The label is the id without its letter: `a12` is drawn as `12`, which is how the observation lists it.
  3. **With marks the picture is always sent** (`if_changed_since` is ignored): marks belong to a generation, not to the screen. A generation the agent no longer keeps is `stale_element`; a malformed one `invalid_args`; without the accessibility service `unsupported`. The hash in the answer is the screen's, as always.
- Supersedes / superseded by: —

## D-63 — Text recognition with tesseract (P4-T07)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 says `tesseract <png> stdout -l eng --psm 11 tsv`, words grouped into lines, confidence under 40 dropped, ids `o<n>`, a cache by screen hash, a 10 s timeout, an optional region. It leaves open how the picture is prepared, how words become pointable things, and what is slow.
- Decision:
  1. **The program** (`ocr.Tesseract`) is run as `tesseract stdin stdout -l <lang> --psm 11 tsv` with the picture on its standard input: no temporary files. `OMP_THREAD_LIMIT=1`, because its threads fight over the few CPUs of the virtual machine. Output is capped (8 MiB; 4 KiB of its messages). The limit is a `Write` method on a type that does *not* embed `bytes.Buffer`: `io.Copy` finds the buffer's `ReadFrom` and fills it without asking (the flood test found this). The TSV parser is strict (the header, numeric columns, at most 20000 rows): output of a program that is not tesseract, or that stopped half way, is an error, not text from the screen. Word text is cleaned like every text from the guest. Not installed: `unsupported`, and the capability is not announced; language not installed: `invalid_args`; over the budget: `timeout` ("read a smaller region"); canceled: the program is killed.
  2. **The picture** is the region asked for (at least 16x16, inside the screen) or the whole screen, **enlarged twice** with a smooth filter (`scale` 1 to 3 on request). Measured in the guest under tcg on a terminal with known text: scale 1 takes 6 s and loses text (the title "ControlSnack" and the menu entries disappear); scale 2 reads everything in 8.2 s (727x291 region; about 14 s for the whole 1280x800 screen); scale 3 takes 10 s and reads no more. Words come back to guest pixels, offset by the region.
  3. **Lines.** Words under 40 confidence are dropped. Tesseract's own lines are kept, and one is split where the gap between two words is wider than 1.5 times the taller word's height (at least 12 pixels), so the entries of a menu bar are separate things to point at. The result is in reading order (top to bottom, then left to right, a row being half a line of tolerance). A grouping by row on the screen was tried and rejected: it joined "Tabs" and "Help" of a menu bar and a launcher with a task button of the panel into one box whose middle lies between the two things to point at; for pointing, two boxes for one phrase do less harm than one box for two things. Ids `o1`... in reading order; at most 300 lines (`truncated`).
  4. **Cache.** The last answer is kept, keyed by the screen's hash, the region, the language and the scale: the same screen asked again costs nothing and returns the same ids and generation. Generations are `g<n>` of the OCR's own counter; the host tells an `o` id from an `a` id by its letter.
  5. **Time.** 10 s by default, as in the plan, and the request may carry `budget_ms` (1 to 120 s): the host scales it by the accelerator, since the whole screen takes about 14 s without hardware acceleration.
- Verified for real (a terminal showing known text, in the guest): every known line read exactly (including digits and the colon), the panel's and the terminal's menu entries as separate boxes with correct bounds in screen pixels, the region answer inside the region, the cached answer at once with the same generation, and the refusals (a language that is not installed, a malformed name, regions too small or off the screen).
- Not covered: other languages (the image has English data only), rotated or low-contrast text, text in icons.
- Supersedes / superseded by: —

## D-64 — The browser tool: Chromium over a pipe, ids per tree, places from the page (P4-T08)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 says chromedp against `/usr/bin/chromium`, headful, its own profile, `--force-renderer-accessibility`; `read` is title, address and visible text (16 KiB); `ax_tree` gives `w<n>` ids mapped to backend DOM nodes; a click by id scrolls into view and dispatches mouse events at the center of the box model; typing is a focus and `Input.insertText`; screen bounds are approximate. The plan leaves open how the browser is started and kept, what an id means once the page has moved, and what typing does to what a field holds.
- Decision:
  1. **chromedp v0.21.0** (MIT; its newest API, with generic actions, which the older tutorials do not show). The browser is started by the first `open` (about 20 s under tcg) as `chromium` with the profile `~/.config/cs-chromium`, a visible maximized window, `--no-first-run`, `--no-default-browser-check`, `--force-renderer-accessibility`, `--password-store=basic`, and the guest's proxy `http://127.0.0.1:3128` for every request. chromedp talks to it **through a pipe**: no debugging port is opened in the guest. One browser lives as long as the agent; if it exits, the next `open` starts it again and other operations say `not_found` ("the browser is not open"). The agent grew to 7.9 MB (3.2 MB gzipped).
  2. **Tabs** are numbered from 1 in the order the browser keeps them. `open` opens a new tab in front (the first `open` uses the browser's own first tab). A page that appears by itself (a link that opens a window, a script) is taken to be in front. `switch_tab` brings a tab to the front.
  3. **Addresses** (`browser.CheckURL`, also the policy's rule, here as defense in depth): `http`, `https` and `file` only; `javascript:`, `data:`, `chrome:`, `devtools:`, `about:`, `blob:`, `view-source:` and the rest are `invalid_args`; no user name or password in the address; no white space or control characters; at most 2048 bytes; a `file` address has no host. `open` without an address opens a blank page. Without the network (no proxy yet), a web address fails with `net::ERR_PROXY_CONNECTION_FAILED`, which the model reads as the failure of the navigation.
  4. **`read`**: title, address and the page's visible text (`innerText`), cleaned like all text from the guest, cut to 16 KiB at a character boundary (`truncated`).
  5. **`ax_tree`**: Chromium's accessibility tree, reduced to what a person can operate (buttons, links, fields, options, tabs, switches, sliders, menu items...) and to named headings, images, dialogs and alerts; ids `w1`, `w2`... in document order; role names are the browser's own (`button`, `link`, `textbox`, `heading`...). A password field (found with `input[type=password i]` in the main frame) is reported as `password text`, the same role the desktop tree uses, so one policy rule covers both, and **its value is never read**. Elements that are not rendered are left out; the others carry bounds in screen pixels computed from the box model plus the window's place (`screenX/Y`, the difference between outer and inner size shared as side borders and the rest on top). **In the guest this agrees with the desktop accessibility service to one pixel** (a button at `[127 238 73 24]` against `[127 237 74 25]`). Up to 400 nodes, a `query`, and a note when the page shows nothing. Not covered: frames inside the page, and password fields inside them.
  6. **Ids are those of the last tree.** The request has no generation, so there is one table, replaced by each `ax_tree`, and the generation name only labels it. An id is refused as `stale_element` when the active tab is not the tree's, when the element has left the page (the browser says the node or frame is gone), or when its role or name changed; an id the tree does not have is `not_found`. Navigating, going back and clicking a link all make the old ids stale.
  7. **Click**: by id, scroll into view and press the left button at the middle of the border box through the protocol (not through the display, so a covered window or a missing pointer does not matter); by selector, the first match (`not_found` if none, `invalid_args` for a selector the browser rejects). **Type** *replaces* what the field holds (select all, then insert, or delete when the text is empty), because a person who selects a field and types does that and a model that wants to add to a field says so by reading it first; `submit` then presses Enter; a line break goes into a text area. After a click or typing the service waits 400 ms for what it started (a navigation) before it reports where the page is.
  8. **Time**: a load waits up to 90 s, an action 20 s, the browser's start 3 minutes; the host's own timeouts cancel the request earlier.
- Verified for real (a local site in the guest, `TestE2EBrowser`): cold start 19 to 21 s; read; the tree of eight elements with password and below-the-fold elements; the places against the desktop service (1 pixel); a click that runs the page's script; typing that replaces a field's value; typing into a password field with nothing leaking into the tree; submit with Enter; a text area with a line break; a click on a button 3000 pixels down; a link followed, back, forward, reload, navigate; ids of the page that was left refused as stale; a second tab, `tabs`, `switch_tab`, ids from another tab refused; every refusal; a web address without the network.
- Not covered: JavaScript dialogs (`alert`, `confirm`: a page that raises one blocks until the action times out; to be handled by dismissing and reporting them), frames inside pages, downloads, file pickers, and clicking at coordinates (that is `input`'s).
- Supersedes / superseded by: —

## D-65 — Waiting inside the guest (P4-T09)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 says `wait_for` polls every 250 ms: `screen_change` (hash), `screen_idle` (no change for `value` ms, default 1000), `text_visible` (accessibility names first, recognition at most every 2 s), `window_title`, `element`; at most 60 s. The model-facing tool adds `seconds`.
- Decision:
  1. **Conditions.** `seconds`: the value is a number of seconds, 0.1 to 60, and the wait is always met. `screen_change`: met when the screen's fingerprint differs from the one at the first look. `screen_idle`: met when it has not changed for `value` milliseconds (100 to 60000, default 1000). `window_title`: met when any window's title contains the value, ignoring case. `element`: met when an element with the value in its role, name, description or value exists anywhere on the desktop's accessibility tree. `text_visible`: first the accessibility names of the window that has the focus (cheap), then, at most every 2 s and only if the screen has changed since the last reading, recognition of that window's region (the whole screen when the window is unknown or off the screen). `timeout_ms` is 100 to 60000, default 10000. A wait that ends unmet is an answer (`met: false` and the time waited), not an error; a cancellation stops it at once.
  2. **A probe starts no generation** (`a11y.Service.Probe`): polling a tree four times a second would otherwise use up the four generations the agent keeps and make stale the ids a model holds from a tree it read before the wait. For the same reason recognition goes through a `Contains` that shares the `ocr` handler's cache.
  3. **Every capture is a picture of its own.** The screenshot, input, text recognition and waiting workers all read the screen, in different pools, and `x11.Conn.Capture` handed all of them one shared buffer: one worker could be hashing or encoding while another overwrote it. It allocates a picture per call now (4 MB at 1280x800, a few a second at most).
  4. **Limits.** A screen that is never still (a blinking cursor, an animation, a clock with seconds) is never idle; this is the model's to know and the image's to avoid (see TODO). Recognition under tcg takes 8 to 14 s, so `text_visible` for text the accessibility tree does not have needs a long timeout there; the budget of one reading is the time left (1 to 120 s), and a reading too slow for it ends the wait unmet. The look interval is 250 ms plus the time to look (a capture takes about 50 ms natively, 300 ms under tcg).
- Verified for real (the guest): `seconds`; `screen_idle` met after 2.3 s on a quiet desktop; `screen_change` met 6.9 s after a window was scheduled to open in 4 s (the terminal needs its time to draw); `window_title` met at once for a window already there and unmet after exactly the timeout for one that never comes; `text_visible` found "waiting" in a terminal in 0.4 s through the accessibility names, with no recognition; `element` met 7.8 s after an editor was started and unmet for an element nobody has; refusals; a wait the host cancels stops and the worker serves the next one.
- Not covered in the guest: the recognition branch of `text_visible` (covered by unit tests with a clock that moves only when something waits), and `screen_idle` next to a blinking cursor.
- Supersedes / superseded by: —

## D-66 — The guest's proxy and the stream protocol of the net bridge (P4-T10)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §8 and §7.8: an HTTP proxy on 127.0.0.1:3128 handling `CONNECT` and absolute-form HTTP, each connection one smux stream to the host, which runs the egress policy; a refusal is a 403 with a reason a model can read; with no network, `403 Network is disabled for this session (start with --allow-net)`.
- Decision:
  1. **The proxy is part of the agent and always listens** on `127.0.0.1:3128` (IPv4 loopback, nothing else), whether or not the session has a network, so a program in a session without one gets a sentence it can use and not "connection refused". A client that is not on the loopback gets 403.
  2. **What it serves.** `CONNECT host:port` (a tunnel; bytes the client sent in the same packet as the request are the first bytes of the tunnel) and `GET/POST/... http://host/path` (forwarded through `httputil.ReverseProxy`, one connection for each request: `Connection: close`, no keep-alive, no response compression, `Proxy-*` headers not passed on, two minutes for the response header). Anything else is 400 with a sentence: the origin form, `https://` and other schemes in the request line, `CONNECT` without a port or with a port outside 1 to 65535. At most 128 connections at once (503 above that), 64 KiB of headers, 10 minutes of silence ends a connection.
  3. **The stream protocol** (`protocol.NetRequest`, `NetReply`): the guest opens a stream and writes one line of JSON `{"host":"example.com","port":443}`; the host answers one line, `{"ok":true}` or `{"ok":false,"error":"..."}`; after `ok` the stream carries the connection's bytes both ways. Lines are at most 1 KiB, decoded strictly (no unknown fields, one value), and read a byte at a time so nothing of the connection is consumed. The multiplexer is `xtaci/smux` v1.5.57 (MIT), protocol version 2 (a window for each stream, so one slow stream does not hold the others); both ends use `netproxy.MuxConfig()`. **A stream is only ever closed whole, never half-closed**: with `CloseWrite` on both sides smux v1.5.57 delivered an empty answer in about 3 % of exchanges (96 of 3000, with nothing but smux and a TCP socket pair in the test: the server wrote the answer and sent its end, and the opener, which had sent its own end first, read nothing), while data followed by a full `Close` lost nothing in 3000 exchanges and in 150 exchanges of 640 KB each way. The end of a direction is told over the TCP connection the browser or the server has, which does have a half-close; towards the host, the end of what the browser sent is a stream that is closed after the other direction has had its few seconds. The guest names, the host resolves: the guest never resolves a name.
  4. **Answers.** No port (the session has no network): `403` and the sentence from the plan. The host refused: `403` with `Denied: <its reason>`, cleaned to one line of at most 300 characters, and the same reason in the `X-ControlSnack-Reason` header. The port exists but the host does not answer, or the connection died: `502`, "The network bridge to the host is not connected"; the next request opens the port again (the session is dropped when it is found closed). The host has 30 seconds to answer a request for a connection; the request being canceled ends the wait at once. A tunnel is a `200 Connection Established` after the host accepts; a refusal is the `403` status line (browsers show their own error page for a failed `CONNECT`, not the body: a model learns the reason from `curl`, from the pages of plain http, and from the network status the system prompt states).
  5. **The tunnel** copies both ways; when one direction ends its destination is half-closed if it is a TCP connection (a stream is not, see above) and the other direction gets 3 seconds to finish, then both are closed. A client that half-closes its connection and waits for an answer therefore gets what the server says within those 3 seconds; browsers and curl do not do that. Silence for the idle time (10 minutes) closes it.
- Verified for real (guest without a network): the proxy listens on `127.0.0.1:3128` only; `curl` through it gets `403` with the sentence for http and "CONNECT tunnel failed, response 403" for https, and `400` when it is spoken to as a web server; the browser opens an http address and its page, as `read` returns it, is the sentence.
- Not verified for real: the multiplexing across a real virtio-serial port, which needs the host's bridge (P4-T11: tested with a fake host over a loopback socket until then).
- Supersedes / superseded by: —

## D-67 — The host's side of the net bridge (P4-T11)
- Status: Accepted (2026-10-13) · Type: Interpretation
- Context: PLAN §7.8 and §6.11: with `--allow-net` the guest asks for each connection over the second virtio-serial port; the host decides with the egress policy, resolves, connects and carries the bytes; limits are 32 streams, 5 minutes idle and 2 GiB for the session; every decision goes to the audit log as kind `net`. D-66 fixed the stream protocol.
- Decision:
  1. **`internal/netbridge.Bridge`** serves one multiplexed session (`smux.Server` with `protocol.NetMuxConfig()`, the same setting the guest uses; the setting moved to the protocol package so the two ends cannot drift apart). For each stream: read the request line (10 s), take a slot from `egress.Tracker`, call `Dialer.Dial` (`*egress.Policy` in production: it checks, resolves once, connects to the vetted addresses and checks again at connect), answer `{"ok":true}` or the reason, and carry the bytes. A refusal's reason is the policy's own sentence, cleaned to one line of at most 200 characters (and a generic "the connection was refused" if even that does not fit the 1 KiB line); an allowed destination that does not answer is refused with the dial error, and the audit record says `failed`, not `denied`.
  2. **A connection ends when either direction ends.** A stream has no half-close (D-66) and a stream whose peer has closed cannot be written to, so there is nothing to wait for in the other direction: the server's end of a finished answer is closed after its last byte (the guest sees the end after the last byte), and a guest that closed its stream cannot receive more, so the upstream connection is closed at once. **Idle is for the connection, not the direction**: a pump whose read times out goes on if the other direction moved within the idle time, so a download the guest does not answer is not cut at 5 minutes (a mutation that dropped this fails `TestBridgeDoesNotCallADownloadIdle`).
  3. **The byte budget is the session's**, charged for both directions as they are read (before they are written). When it is used up, the connection ends and new ones are refused with "the session's network byte budget is used up".
  4. **`Run`** keeps the bridge serving for the life of the session: it is given the connection Launch already made (QEMU waits for it before the guest starts) and a function that dials the port again (`Machine.DialNet`), and when a session ends (the guest's agent restarted, or the connection broke) it dials again after 500 ms. A failed dial is retried, not fatal; the context ends it.
  5. **Audit.** `Event` has two phases: `decision` (the policy's verdict, rule and reason; for an allowed destination also whether the server answered) and `closed` (bytes up and down and the duration). `Event.Record()` makes the `net` audit record: verdict `allowed`, `denied` or `failed`, the rule in `rules`, host, port and byte counts in `data`. The session (P7-T06) appends them; an `Append` fsyncs, so the callback blocks for the length of a flush, which is the price of an audit log that is on the disk before the bytes it describes have gone.
- Verified for real (`TestE2ENetBridge`, boot with `--allow-net`, under tcg in WSL): the multiplexer over a real virtio-serial port works, with several streams at once (Chromium opens a dozen) and a 457 KB download; **refused with their reasons** and nothing connected on the host (a listener on the host's loopback, on a port the policy allows, was never reached): `127.0.0.1`, `localhost`, `[::1]`, `10.0.2.2`, `192.168.1.1`, `169.254.169.254`, port 22, and an https `CONNECT` to `127.0.0.1`; a public page over plain http, over a `CONNECT` tunnel (curl, and by hand with the first bytes shown) and in the browser; and **the guest recovers when the host's end of the port is closed and opened again**: the first request after it gets `502 The network bridge to the host is not connected`, the next one goes through (3 s with a 2 s pause between).
- Found: (a) `curl -x` in the guest still skips the proxy for `localhost`, `127.0.0.1` and `::1` because the image sets `NO_PROXY` for them: the guest's own loopback is the guest's, which is right; the e2e test passes `--noproxy ''` to make the host see them. (b) `example.com` is blocked on this machine's network (an HTML page without a status line on port 80, a reset on 443; it is behind Cloudflare), which looked like a bridge defect until the host's side was traced: the e2e test now probes a few small public pages directly from the host and uses the first that answers as expected, and skips the public part if none does. `CS_E2E_TRACE=1` logs the first bytes of every connection the bridge makes. (c) the policy's reason for `::1` said "IPv4-compatible address" (it lies inside `::/96`); it now says "IPv6 loopback" (and "unspecified" for `::`).
- Not verified: the failure of a read or write on the guest's port while the host is not connected was seen only through its effect (the session closes, the next request opens the port again); deadlines on the port's `os.File` are not used (the multiplexer's keep-alive, 10 s and 30 s, ends a session whose host has vanished without an end of file).
- Supersedes / superseded by: —
