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
- [x] (2026-10-13) P4-T04 Input (XTEST, key parser, Unicode typing, drag, scroll) — verified in the guest (D-59)
- [x] (2026-10-13) P4-T05 Exec runner (plus the guest `file_peek`) — verified in the guest (D-60)
- [x] (2026-10-13) P4-T06 Accessibility (AT-SPI) + stubs for other OSes + set-of-marks overlay — verified against Mousepad and Thunar in the guest (D-61, D-62)
- [x] (2026-10-13) P4-T07 OCR — tesseract over stdin, lines with ids, cache by screen hash; verified in the guest (D-63)
- [x] (2026-10-13) P4-T08 Browser (CDP) — chromedp over a pipe, ids per tree, places agree with the desktop service to 1 px; verified in the guest (D-64)
- [x] (2026-10-13) P4-T09 wait_for — six conditions, polling every 250 ms; verified in the guest (D-65)
- [x] (2026-10-13) P4-T10 Guest net proxy — loopback HTTP proxy over smux streams; the no-network answers verified in the guest (D-66)
- [x] (2026-10-13) P4-T11 Host net bridge + egress e2e — `internal/netbridge`; verified in the guest with `--allow-net`: refusals with reasons, public pages over http, `CONNECT` and the browser, recovery after the port's host end was reopened (D-67)
- [x] (2026-10-12) P4-T12 Embed pipeline (`tools/dev agent`) — done with P3-T10: both arches, gzip, SHA256SUMS; `agentbin.For` checks the hashes whenever an agent is loaded (every `run`) and refuses a damaged or mismatched set
- [x] (2026-10-14) P4-T13 Image recipe finalisation + smoke — `TestE2EImageSmoke` passes in the real VM; payload sizes in PROGRESS.md (D-68, D-69, D-70)

## Phase 5 — LLM: internal/llm (required test suite #3)
- [x] (2026-10-14) P5-T01 Actions + schemas (§5.3) — `schema/tools/*.json`, `internal/llm/actions` (typed arguments, registry, `Parse`), `controlsnack schema --format jsonmode`; the other formats are T02 (D-72)
- [x] (2026-10-14) P5-T02 Renderings + profiles — `actions.Set` (full and compact), `Render` for openai, ollama and anthropic, the plain and strict envelopes, `controlsnack schema --format ... --tools ...` (D-73)
- [x] (2026-10-14) P5-T03 Parser + repair (§9.5) — `internal/llm/parse`: native calls, JSON answers (whole, fenced, first object), reasoning blocks, trailing commas, the limit on actions, `RepairMessage` and `Feedback` (D-74)
- [x] (2026-10-14) P5-T04 Ollama backend — `internal/llm/backend` (the contract), `backend/httpx` (timeouts, retries, errors, address checks), `backend/ollama`; fake-server tests and fixtures; not yet run against a real Ollama (D-75)
- [x] (2026-10-14) P5-T05 OpenAI-compatible backend — `backend/openai` (LM Studio, llama.cpp, vLLM, OpenAI); fake-server tests and fixtures; not yet run against a real server (D-76)
- [x] (2026-10-14) P5-T06 Anthropic-compatible backend — `backend/anthropic`; fake-server tests and fixtures; `Request.Validate` now checks that every native call is answered; not yet run against the real API (D-77)
- [x] (2026-10-14) P5-T07 Detection + probe + cache + `llm probe|models` — `internal/llm/detect`: `Resolve`/`ResolveBackend`, `Models`, `Probe` (vision, WebP, native tools, structured, JSON), `Capabilities.Mode/ImageFormat`, the cache, the two commands; fake-server tests, four golden reports; not yet run against a real model server (D-78)
- [x] (2026-10-14) P5-T08 Prompt + observation rendering + context budget — `internal/llm/prompt`: `System` (embedded `system.tmpl`, tool docs for the prompt modes, hash for the audit), `Renderer` (text and JSON observations, every guest string fenced; `schema/observation.schema.json`), `Conversation` (last 6 turns in full, one line for older ones, the only picture, trimmed to 85 % of the context in the plan order), `EstimateTokens`/`ImageTokens`; golden files, fence property tests and a fuzz target; not yet used with a real model (D-79)
- [x] (2026-10-14) P5-T09 LLM image pipeline + coordinate mapping + marks — `internal/llm/vision`: `Mapper` (picture ↔ screen, the 2 % rule, `Guest(action)` for mouse/scroll/region), `Pipeline.Capture` (format of the probe, `llm.image_max`, marks, unchanged screens, hostile-answer checks), `MarksEnabled`, protocol→observation conversions; the agent now says which boxes it drew (`ScreenshotRes.Marked`); tested against a fake agent and the agent's own handler (D-80)
- [x] (2026-10-14) P5-T10 Text-only mode — `vision.Plan` (vision or text-only from the probe, tool profile, marks, a notice and the TEXT-ONLY badge), `vision.Observer` (the tree, the active window and, in text-only mode, the recognized text each turn; vision mode the picture with its boxes), `Seen.Fill`; tested against a fake guest and the real one (D-81)
- [x] (2026-10-14) P5-T11 fakellm — `internal/llm/fakellm`: a scripted OpenAI-compatible server (LM Studio list included) that answers the probe from a profile, refuses what the profile lacks, records every request, and has `Find` to click what an observation shows; the first whole-layer test found and fixed the loss of the arguments of replayed calls (D-82)

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
- [ ] P7-T06 (net bridge, from P4-T11): start `netbridge.Bridge.Run(ctx, m.Net, m.DialNet)` for a session with `--allow-net`, with the session's `egress.Policy` (`--allow-port`, `--allow-domain`, `--deny-domain`) and `egress.Tracker`; `Audit` appends `Event.Record()` to the audit log (an `Append` fsyncs and a failure is fatal like any other); without `--allow-net` the net port does not exist and nothing is started.
- [ ] Cleanup: `internal/cli/simple.go` still groups the small commands in one file (D-38); split when convenient.
- [ ] P3-T11 / first hardware: no accelerated boot has been measured (this user cannot open `/dev/kvm` in WSL; Windows has no QEMU). Record "boot → agent ready (accelerated)" (target < 45 s) from the first KVM, WHPX or HVF run, or from the CI doctor artifacts plus a CI `run --no-llm` smoke on a runner with KVM. `sudo usermod -aG kvm $USER` (then restarting WSL) would make the WSL runs accelerated.
- [ ] P3-T11 / aarch64: nothing aarch64 has run for real (no `qemu-system-aarch64` here): the `virt` machine, the UEFI variable store copy (`vars.fd`) and the arm64 agent are covered by goldens and fakes only.
- [x] (2026-10-14, D-70: rebuilt twice, black background kept, first boot 1 min 2 s to 1 min 10 s under tcg) P4-T13: rebuild the image once the agent has its features (the recipe hash changes with every recipe edit); the desktop has no wallpaper (black background: fine for screenshots, but decide whether `desktop-base` or a plain colour is wanted); check the first-boot time again.
- [ ] P7-T06: replace the boot-only runner with the full session (model loop, viewer, `--vm`/`--keep-vm` through `overlay.Library` including the generated name for a bare `--keep-vm`, `--allow-net` with the net port); keep what the boot-only runner already does in this order: kill switch → control endpoint → session lock → audit `start` → directory → boot → `ready`; the viewer's server takes over the `/control/kill` handler (P6-T01).
- [ ] P7-T06: `vm.Boot`'s agent heartbeat and timeouts are scaled by the accelerator factor; make the session treat `StateDown` as "agent_down" for the model loop (PLAN §11) and decide how long a tool call waits for a reconnect.
- [ ] P3-T14 / P3-T06: the doctor's extra checks are still unregistered; now that images exist, add the "base image present, matches the recipe, boots" check (`image verify` already does the first two).
- [x] (2026-10-13) P4-T06: draw the set-of-marks overlay (D-30) in `internal/imaging` once a11y node bounds exist (`marks_generation` answers `unsupported` until then); `x/image/font/basicfont` is the plan's font.
- [ ] P5 / P6 / P7-T06: every picture the host receives from the guest goes through `imaging.Inspect` (declared format and size, at most 4096 per side) before anything decodes it.
- [ ] P8-T04: if the performance pass wants faster capture, add MIT-SHM (`gen2brain/shm` is pure Go) behind `x11.Conn.Capture`; measure first (1280x800 capture plus PNG took 312 ms under tcg).
- [x] (2026-10-14, D-70: `fonts-wqy-microhei`, the build checks zh, ja and ko) P4-T13: the guest has no CJK font, so correctly typed CJK characters show as empty boxes in screenshots (Latin, Greek, Cyrillic, emoji and the euro sign render); `fonts-noto-cjk` is about 60 MB: decide at the image rewrite whether screenshots of CJK text matter enough.
- [x] (2026-10-14: scroll, right click and double click work against Thunar, D-70; middle click is still unchecked) P4-T04: scroll, middle and right clicks and double clicks are unit-tested but not yet checked against a real application; cover them in the P4-T13 smoke (scroll a Thunar folder, double-click to open a file). An `xdotool type` fallback for Unicode is only worth building if an application ever ignores the borrowed keycode.
- [ ] P7-T06 / P7 host: send `budget_ms` with `a11y_tree` and `ocr` scaled by the accelerator factor (D-61): under tcg an element costs about 25 to 35 ms and a tree has a fixed 0.5 s, so the default 1.5 s budget truncates a desktop tree; keep `marks_generation` on for local backends in vision mode (D-62); the host decides whether an older kept generation is acceptable for an action (the agent keeps four and re-checks the element).
- [ ] P8-T04: the accessibility walk is latency bound (two round trips per level: the element, then its children). Ask for the children in the same round trip as the element's states (one wasted call on leaves) and keep the application list for a second; measure with acceleration first, tcg is the slow path.
- [ ] P4-T08 / P4-T13: accessibility of Qt, Electron and Chromium is untested (the image has only GTK3 applications besides the browser); Chromium needs `--force-renderer-accessibility` and is read by the browser tool, not this one. Elements without a Component interface are left out (no area), which loses some custom widgets.
- [ ] P7: recognition of the whole screen takes 14 s under tcg (not yet measured with acceleration); `wait_for text_visible` already tries the accessibility names first and reads only the focused window, at most every 2 s (D-65); the host sends `budget_ms` for `ocr` as for `a11y_tree`.
- [ ] P4-T13: the image has English recognition data only (`tesseract-ocr-eng`); add languages only if a task needs them (each is a few MB).
- [ ] P4-T08 / P4-T13: JavaScript dialogs (`alert`, `confirm`, `prompt`, `beforeunload`) block the page until an action times out; handle `Page.javascriptDialogOpening` by dismissing the dialog and telling the model what it said (a `note` on the next answer). Also not handled: frames inside pages (their elements and password fields), downloads, file pickers.
- [x] (2026-10-13) P4-T10: with no network the browser and curl now get the readable 403 sentence from the guest proxy (D-66); with a network, a refusal of an https `CONNECT` shows no body in a browser (P4-T11 checks what the model sees).
- [ ] P8: the agent is 7.9 MB (3.2 MB gzipped, twice in the host binary for the two guest architectures) because of chromedp and its protocol types; if the release size matters, look at what the protocol packages pull in (the agent is built without debug information already).
- [x] (2026-10-14, D-70: `screen_idle` is met in 1.1 to 1.8 s with a new terminal or the editor in front) P4-T13: `wait_for screen_idle` never settles next to a blinking cursor: set the terminal's cursor not to blink in the image (xfce4-terminal `MiscCursorBlinks=FALSE` in its xfconf/terminalrc, GTK `gtk-cursor-blink=false` in settings.ini for Mousepad and Thunar) and check `screen_idle` with a terminal in front; also decide whether the panel's clock should show seconds (it does not by default).
- [x] (2026-10-13) P4-T11: the guest side of the multiplexer on a real virtio-serial port works, with a dozen streams at once and a 457 KB download; a host that closes and reopens its end makes the guest open the port again (D-67). Deadlines on the port's `os.File` are not used (the multiplexer's keep-alive ends a dead session).
- [ ] P4-T11 / P8: the first request after the host's end of the port was reopened gets `502 The network bridge to the host is not connected` and the next one works. The guest could retry once, after a short pause, when a request finds the session closed; decide with the browser in mind (a failed `CONNECT` shows no reason there).
- [ ] P4-T11 / P5: the model has to learn that a provider can block a destination (`example.com` is blocked on the developer's network: a page without a status line on port 80, a reset on 443). A tunnel whose server resets the connection shows the guest `200 Connection Established` and then an empty stream; only the host knows (the audit record has the bytes), so the system prompt's network status should say that a failing site may be blocked, not broken.
- [ ] P8-T04: the guest proxy uses `net/http` (server, reverse proxy, TLS and x509 come with it), which made the agent 3.6 MB larger (7.9 to 11.5 MB raw, 3.2 to 4.7 MB gzipped, twice in the host binary); a proxy on raw TCP with a small request-head parser would take that back out. Measure whether the release size matters before trading `net/http`'s parsing for our own.

- [x] (2026-10-14, D-79: in system.tmpl) P5-T08 / P8: tell the model, in the system prompt or the tool descriptions, how the file manager appears in the accessibility tree: a `directory pane` named "Details view" whose `table cell` elements are the file's name, size, type and date (one cell each, in rows), where a click selects a row and a double click opens it (D-69); an application's icon view, or any custom widget, may be one element with nothing inside, and then the screenshot and `ocr` are the way to read it.
- [ ] P5-T07 / first real model: the probe has only run against fakes (D-78). When a local server is available, run `controlsnack llm probe` on two or three models and check that (a) a model that sees answers the colour question in the JSON shape asked for (a model that answers "The square is red." passes, one that refuses to see passes nothing), (b) the 128×128 picture is not too small for the model's image encoder, (c) a model that is slow to load fits the 10-minute deadline of the first question, (d) Ollama does not reload the model between the probe and the session (the probe and the session must ask for the same `num_ctx`: the target carries `--num-ctx`).
- [ ] P5-T07 / P7: the session must take the cached `Capabilities` (`Probe(ctx, target, false)`), refuse a model whose `Usable()` is false, send screenshots in `ImageFormat()`, run in text-only mode when `Vision` is false (P5-T10), and print `Target.Notice()` and audit `remote_llm` when the target is not local.
- [ ] P5-T07 / P8: `llm probe` could also measure the time of the first answer and the size of the context actually in use (`/api/ps` on Ollama gives `context_length` of the loaded model) to warn when `num_ctx` is less than the prompt budget; and `llm models` could show the size on disk.
- [ ] P5-T08 / P7: the session builds each `prompt.Observation` from the guest's answers with everything in image space (T09 gives the mapping), gives the conversation a `Result` for **every** native call of an answer (also `CodeNotRun` for the valid actions after a mistake, `CodeDropped` for those beyond the limit, and the error code with the problems for an invalid one), queues `parse.Feedback` with `Conversation.Note`, speaks `parse.RepairMessage` with `Conversation.Notice`, fills `Result.Action` with `prompt.Describe(action)` for the summaries, records `prompt.TemplateSHA256()` and `SystemVersion` in the audit of `lifecycle.start`, and sends `Report` (what was cut) to the viewer and the log. Guest text in `Result.Detail` (an agent's error message) is cleaned and sanitized by the renderer, but the session should still keep the agent's words out of it where it can.
- [ ] P5-T08 / first real model: nothing here has been read by a model. Check with a real one that it (a) answers in the shape asked in each mode, (b) uses ids and `wait_for` as the prompt says, (c) does not obey text in a fence (try a page or a file with an instruction in it), (d) copes with a trimmed tree (`2/40 nodes`), and tune the wording of `system.tmpl` (bump `SystemVersion`); and compare `EstimateTokens` with the tokens the backend reports in `Usage` (a ratio far from 1 means the 85 % is too tight or too loose for that model).
- [ ] P5-T08 / P8: the system prompt is only in English; the assistant messages of the history are kept as the model wrote them (cleaned, 3000 characters), not rewritten in a canonical form; the one-line summaries cut an argument at 120 characters.
- [ ] P5-T09 / P7: the session builds one `vision.Pipeline` for the screen size of the handshake (`Ack.Display`) with `Format = Capabilities.ImageFormat()`, `MaxLongEdge = llm.image_max`, `Marks = vision.MarksEnabled(llm.marks, target.Local, caps.Vision)`; in text-only mode no pipeline is made and `vision.Identity(guest)` is the mapper. Each turn: read the tree (`read_accessibility_tree` or the observation's own read), `Capture(ctx, tree.Generation)`, build the observation with `shot.Mapper.UITree/OCR/Window`, `shot.Note` and `shot.Cursor`; run each action through `Mapper.Guest` **before** the policy sees it (the policy decides on guest coordinates) and record the model's own coordinates in the audit (`Raw` keeps them). A change of the screen's size means a new pipeline.
- [ ] P5-T09 / first real model: check how well a model aims in the picture (does 2 % of overshoot help or hide mistakes; are the boxes' numbers read right at `llm.image_max` 1280 and below; do small VLMs do better with marks on `auto`), and the picture size/quality (`image_max`, WebP 70) against the model's own limits (e.g. Ollama models resize to 336 or 896 pixels).
- [ ] P5-T09 / P8: `Marked` could also carry the label color/position so that the prompt can say where numbers sit (top-left of each box); marks of the browser tree (`w` ids) are not drawn (the agent draws the desktop tree's); a screenshot of a region (zoom) is not offered to the model.
- [ ] P5-T10 / P7: the session calls `vision.Plan(caps, target.Local, cfg.LLM)` once (print `Setup.Notices` in the terminal and the viewer, show `Setup.Badge()`, add `text_only`/`vision` to the audit of `lifecycle.start`), builds `vision.NewObserver(client, ObserverConfig{Setup, Guest: ack.Display, TreeBudget, OCRBudget})` with the budgets scaled by the speed of the VM (tcg is several times slower), and each turn calls `Look` and `Seen.Fill`; `vision.ErrBlind` and an error of the connection pause the session with a notice.
- [ ] P5-T10 / first real model: text-only operation has never been tried with a model. Measure how well a model without vision does the smoke tasks from the tree and the recognized text alone, what an `ocr` call costs on each turn under tcg and with hardware acceleration (one look at the screen, tree and `ocr` together, took 15.6 s under tcg in the e2e test), and whether the OCR should be asked only when the tree is thin (a tree with few named elements) rather than every turn.
- [ ] P5-T10 / P8: there is no flag to force text-only mode with a model that can see (PLAN §9.6 only has it as the fallback). It would also keep screenshots from leaving the machine with a remote model; decide with the README (`--no-images`?).
- [ ] P5-T11 / P7-T07: the e2e scenarios start `fakellm.Start(t, Options{Script: ...})` and run `controlsnack run --llm lmstudio --base-url <URL()> --task ...` (the probe is answered by the fake, so `run` starts as against LM Studio; choose `Profile` to test text-only or JSON mode). The fake serves only the OpenAI-compatible API and does not stream; if a scenario needs Ollama's or Anthropic's wire format, add it then.
- [ ] Phase 5 exit / **ask the user**: the exit asks for `llm probe` against whatever local backend is installed. **LM Studio is installed on this machine** (`%LOCALAPPDATA%\Programs\LM Studio`, `~/.cache/lm-studio/bin/lms`), with two downloaded models in `~/.cache/lm-studio/models/Publisher/Repository` (8.1 GB): `hugging-quants/Llama-3.2-3B-Instruct-Q8_0-GGUF` (text only) and `sunil-pathak/gemma-4-E4B-it-IQ4_NL`. Its server was not running (nothing answered on 127.0.0.1:1234), and starting it and loading a model uses several GB of memory and the CPU/GPU, so it was not done without asking. With the user's agreement: start the server (`lms server start`), load a model, run `controlsnack llm models` and `llm probe`, record the report in PROGRESS and the README matrix, and try a real two-turn run (the first real test of everything under P5).
- [ ] P4-T06 / P8-T04: a long list is read from its first row (D-69): a tree view with thousands of rows is truncated, and the rows in view of a list scrolled far down are found only if the limits reach them. Read a table through the `Table` interface (the rows in view) if file lists or spreadsheets matter. Also: with the default 150 nodes the desktop tree stopped at its limit on what a walk looks at (`truncated`) although 126 nodes were found; the limit counts cells that are scrolled out of view.
- [ ] P4-T13 / P8: only an English recognition language, no middle-click check, no Qt or Electron application in the image: the smoke list covers GTK3 applications and Chromium (D-70).
- [ ] P4-T04 / P8-T04: borrowed keycodes (D-68) are reused after 2 s of not being used; a text of more than 64 distinct characters that the keyboard lacks (a long Chinese paragraph) types the 65th and later at one every 2 s once the first 64 have been used within those 2 s. Measure with a real CJK text under tcg before deciding whether a larger pool, or a keymap with several keysyms for each keycode, is worth it.

## Parking lot (out of scope for v1 — do not implement)
- Multi-machine / Pro features (remote viewer, P2P, tunnels, link sharing, accounts)
- Non-Linux guests; macOS AX / Windows UIA backends
- Global kill hotkey; manual takeover; recording/replay
