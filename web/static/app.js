// The viewer: connects to the session, shows its screen and conversation, and carries the person's controls.
// Everything the host sends is put on the page with textContent; no markup is built from it (see panels.js).
"use strict";

import { Confirmations } from "./confirm.js";
import { Connection, GAVE_UP } from "./connection.js";
import { Header } from "./header.js";
import { KillHotkeys } from "./hotkeys.js";
import { Audit, Chat, Notices, Trace } from "./panels.js";
import { chat, confirm, control, isFinal } from "./protocol.js";
import { Screen } from "./screen.js";

const $ = (id) => document.getElementById(id);

const screen = new Screen($("screen"));
const header = new Header($("top"));
const chatPanel = new Chat($("chat-log"));
const trace = new Trace($("trace-log"));
const audit = new Audit($("audit-log"));
const notices = new Notices($("notices"));
const confirmations = new Confirmations({
  root: $("modal-root"),
  main: $("main"),
  killButton: $("kill"),
  answer: (id, approve, note) => conn.send(confirm(id, approve, note)),
  notice: (level, text) => notices.add({ level, text }),
  skew: () => app.skewMs,
});

const app = {
  csrf: "",
  state: "booting",
  stateReason: "",
  connected: false,
  everConnected: false,
  gaveUp: "",
  ended: null, // { reason, summary } once the host says the session is over
  agentOnline: true,
  agentDetail: "",
  killRequested: false,
  planAction: "",
  skewMs: 0, // how far the host's clock is ahead of this page's
};

// ---- what is shown over the screen --------------------------------------------------------------------------------

function setBox(box, title, detail) {
  const [t, d] = box === "overlay" ? [$("overlay-title"), $("overlay-detail")] : [$("banner-title"), $("banner-detail")];
  $(box).hidden = title === "";
  t.textContent = title;
  d.textContent = detail;
}

// The overlay covers the screen for what is final; the banner sits on top of it for what may pass.
function refreshOverlay() {
  let overlay = ["", ""];
  let banner = ["", ""];
  if (app.state === "killed") overlay = ["Session killed", app.stateReason || "The virtual machine was destroyed."];
  else if (app.ended) overlay = ["Session ended", app.ended.summary || app.ended.reason];
  else if (app.gaveUp) overlay = ["Disconnected", app.gaveUp];
  else if (app.killRequested) overlay = ["Killing the session…", ""];
  else if (!app.connected && app.everConnected) banner = ["Connection lost.", "Reconnecting…"];
  else if (!app.agentOnline) banner = ["Agent offline — fallback mode.", app.agentDetail];
  setBox("overlay", ...overlay);
  setBox("banner", ...banner);
}

function refreshControls() {
  const live = app.connected && !isFinal(app.state) && !app.ended && !app.killRequested;
  for (const id of ["pause", "stop", "step", "chat-input", "send"]) $(id).disabled = !live;
  $("pause").textContent = app.state === "paused" ? "Resume" : "Pause";
  $("kill").disabled = app.state === "killed" || app.ended !== null;
}

function refresh() {
  refreshOverlay();
  refreshControls();
}

// ---- the connection -----------------------------------------------------------------------------------------------

function clearPanels() {
  chatPanel.clear();
  trace.clear();
  audit.clear();
  notices.clear();
  confirmations.clear();
  screen.setPlan(null);
  header.setCounts(0, 0);
}

function onMessage(m) {
  switch (m.type) {
    case "hello":
      app.csrf = m.csrf;
      app.connected = true;
      app.everConnected = true;
      app.gaveUp = "";
      app.skewMs = Date.parse(m.now) - Date.now();
      clearPanels(); // what the host replays after this fills them again
      header.setHello(m);
      screen.setGuest(m.resolution.w, m.resolution.h);
      break;
    case "state":
      app.state = m.state;
      app.stateReason = m.reason;
      header.setState(m);
      if (isFinal(m.state)) confirmations.clear();
      break;
    case "agent":
      app.agentOnline = m.online;
      app.agentDetail = m.detail;
      break;
    case "cursor":
      screen.setCursor(m.x, m.y);
      break;
    case "plan":
      app.planAction = m.action_id;
      screen.setPlan(m);
      break;
    case "trace":
      trace.update(m);
      header.setCounts(trace.turn, trace.actions);
      if (m.action_id === app.planAction && !["planned", "running", "pending"].includes(m.status)) {
        app.planAction = "";
        screen.setPlan(null);
      }
      break;
    case "chat":
      chatPanel.add(m);
      break;
    case "audit":
      audit.add(m.line);
      break;
    case "stats":
      header.setStats(m);
      break;
    case "notice":
      notices.add(m);
      break;
    case "ended":
      app.ended = { reason: m.reason, summary: m.summary };
      conn.end();
      break;
    case "confirm_request":
      confirmations.add(m);
      break;
    case "confirm_resolved":
      confirmations.resolve(m.id, m.outcome);
      break;
    default:
      break; // a message of a kind this page does not know
  }
  refresh();
}

const conn = new Connection(`ws://${location.host}/ws`, {
  onOpen() {},
  onMessage,
  onFrame(f) {
    screen.setFrame(f);
  },
  onClose({ gaveUp }) {
    app.connected = false;
    confirmations.clear(); // the host replays what is still open when the page is back
    header.setConnected(false, app.everConnected);
    if (gaveUp) app.gaveUp = GAVE_UP[gaveUp] ?? "";
    refresh();
  },
});

function send(message) {
  if (conn.send(message)) return true;
  notices.add({ level: "warn", text: "Not connected to the session: that was not sent." });
  return false;
}

// ---- the person's controls ----------------------------------------------------------------------------------------

$("compose").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const box = $("chat-input");
  const text = box.value.trim();
  if (text !== "" && send(chat(text))) box.value = "";
});

$("chat-input").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && !ev.shiftKey && !ev.isComposing) {
    ev.preventDefault();
    $("compose").requestSubmit();
  }
});

$("pause").addEventListener("click", () => send(control(app.state === "paused" ? "resume" : "pause")));
$("stop").addEventListener("click", () => send(control("stop")));
$("step").addEventListener("change", (ev) => send(control(ev.target.checked ? "step_on" : "step_off")));

// ---- the kill button ----------------------------------------------------------------------------------------------

async function fetchCSRF() {
  try {
    const r = await fetch("/api/csrf", { credentials: "same-origin" });
    if (r.ok) app.csrf = (await r.json()).csrf;
  } catch {
    // the host may be gone; the kill below says so
  }
}

// kill asks the session to die in two ways at once: the socket, and a plain request that needs no socket. Either is enough.
export async function kill(src = "button") {
  if (app.killRequested) return;
  app.killRequested = true;
  refresh();
  conn.send(control("kill", src));
  if (!app.csrf) await fetchCSRF();
  try {
    await fetch("/api/kill", { method: "POST", credentials: "same-origin", headers: { "X-CS-CSRF": app.csrf } });
  } catch {
    // the session may already be gone
  }
  setTimeout(() => {
    if (app.state === "killed" || app.ended) return;
    app.killRequested = false;
    notices.add({ level: "error", text: "The kill request may not have reached the session. Run “controlsnack kill” in a terminal if the virtual machine is still running." });
    refresh();
  }, 5000);
}

$("kill").addEventListener("click", () => kill("button"));

// ---- start --------------------------------------------------------------------------------------------------------

refresh();
fetchCSRF(); // so that the kill button works even if the socket never comes up
conn.start();

// ---- the keys ----------------------------------------------------------------------------------------------------

const hotkeys = new KillHotkeys({
  kill: (src) => kill(src),
  hint: (text) => {
    $("hotkey-hint").textContent = text;
    $("hotkey-hint").hidden = text === "";
  },
});
window.addEventListener("keydown", (ev) => hotkeys.onKeyDown(ev));
