// The header: what the session is (badges), what state it is in, and its running figures.
"use strict";

import { el } from "./panels.js";
import { stateWords } from "./protocol.js";

function badge(kind, text, title) {
  const b = el("span", `badge badge-${kind}`, text);
  if (title) b.title = title;
  return b;
}

// accelBadge says how the VM runs. Software emulation is slow and is shown as a warning, with what was asked for when the
// session had to fall back from it.
export function accelBadge(accel) {
  const used = String(accel.used || "").toLowerCase();
  if (used === "tcg") {
    const asked = String(accel.requested || "").toLowerCase();
    const fell = accel.fallback_reason ? ` · fallback from ${asked.toUpperCase()}` : "";
    const why = accel.fallback_reason
      ? `Hardware acceleration (${asked.toUpperCase()}) did not work: ${accel.fallback_reason}. The VM runs in software and is much slower.`
      : "The VM runs in software emulation (TCG) and is much slower than with hardware acceleration.";
    return badge("warn", `TCG (slow)${fell}`, why);
  }
  return badge("ok", used.toUpperCase() || "?", `Hardware acceleration: ${used.toUpperCase()}`);
}

export function badgesFor(h) {
  const out = [accelBadge(h.accel)];
  out.push(
    h.mode === "text_only"
      ? badge("warn", "TEXT-ONLY", "This model cannot see pictures: it works from the accessibility tree and recognized text.")
      : badge("ok", "VISION", "The model sees the screen."),
  );
  out.push(h.net ? badge("warn", "NET ON", "The VM can reach the internet through the filtered path.") : badge("ok", "NET OFF", "The VM has no network."));
  if (h.unsafe) out.push(badge("danger", "UNSAFE", "Safety limits are raised (--unsafe)."));
  if (h.remote_llm) out.push(badge("warn", "REMOTE LLM", "Screenshots and text go to a model server that is not on this machine."));
  return out;
}

function clockText(totalSeconds) {
  const s = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const two = (n) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${two(m)}:${two(sec)}` : `${m}:${two(sec)}`;
}

export class Header {
  constructor(root) {
    this.session = root.querySelector("#session");
    this.backend = root.querySelector("#backend");
    this.badges = root.querySelector("#badges");
    this.state = root.querySelector("#state");
    this.timer = root.querySelector("#timer");
    this.counters = root.querySelector("#counters");
    this.traffic = root.querySelector("#traffic");
    this.startedAt = 0;
    this.frozenAt = 0; // when the session ended, so the timer stops
    this.limits = { max_turns: 0, max_actions: 0, timeout_s: 0 };
    this.turn = 0;
    this.actions = 0;
    setInterval(() => this.tick(), 1000);
  }

  setHello(h) {
    this.session.textContent = h.session;
    this.session.title = `Session ${h.session}`;
    this.backend.textContent = `${h.backend} · ${h.model}`;
    this.badges.replaceChildren(...badgesFor(h));
    this.startedAt = Date.parse(h.started_at) || Date.now();
    this.frozenAt = 0;
    this.limits = h.limits;
    this.counts();
    this.tick();
  }

  setState(s) {
    this.state.dataset.state = s.state;
    this.state.textContent = stateWords(s.state);
    this.state.title = s.reason || "";
    if ((s.state === "ended" || s.state === "killed") && !this.frozenAt) this.frozenAt = Date.now();
  }

  setConnected(connected, everConnected) {
    if (!connected) {
      this.state.dataset.state = "booting";
      this.state.textContent = everConnected ? "Reconnecting" : "Connecting";
      this.state.title = "";
    }
  }

  setCounts(turn, actions) {
    this.turn = turn;
    this.actions = actions;
    this.counts();
  }

  counts() {
    const of = (n, max) => (max > 0 ? `${n}/${max}` : String(n));
    this.counters.textContent = `turns ${of(this.turn, this.limits.max_turns)} · actions ${of(this.actions, this.limits.max_actions)}`;
  }

  setStats(s) {
    const llm = s.llm_ms > 0 ? ` · model ${(s.llm_ms / 1000).toFixed(1)} s` : "";
    this.traffic.textContent = `${s.kbps.toFixed(1)} KB/s · ${s.fps.toFixed(1)} fps${llm}`;
  }

  tick() {
    if (!this.startedAt) return;
    const now = this.frozenAt || Date.now();
    const limit = this.limits.timeout_s > 0 ? ` / ${clockText(this.limits.timeout_s)}` : "";
    this.timer.textContent = `${clockText((now - this.startedAt) / 1000)}${limit}`;
  }
}
