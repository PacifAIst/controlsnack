// The approval dialog: an action the safety rules want the person to decide on. It is built to make a wrong approval hard
// and a refusal easy: Reject has the focus, Esc rejects, Approve answers only a real click that comes after a short pause,
// and everything the host says about the action is shown as text.
"use strict";

import { el } from "./panels.js";

// A click on Approve in the first moment is ignored: a dialog that appears under the pointer must not take the click that
// was meant for what was there before.
const APPROVE_DELAY_MS = 700;
// If the host has not closed the request this long after the answer, the buttons come back.
const RESEND_MS = 6000;
const MAX_ARGS = 6000;

function mmss(ms) {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function argsText(args) {
  let s;
  try {
    s = JSON.stringify(args, null, 2);
  } catch {
    s = String(args);
  }
  return s.length > MAX_ARGS ? `${s.slice(0, MAX_ARGS)}\n… (${s.length - MAX_ARGS} more characters)` : s;
}

export class Confirmations {
  // root: where the dialog goes. main: the part of the page made inert while it is open. killButton: stays usable.
  // answer(id, approve, note) sends the answer and says whether it could. notice(level, text) tells the person something.
  // skew() is how far the host's clock is ahead of this page's, in milliseconds.
  constructor({ root, main, killButton, answer, notice, skew, approveDelayMs = APPROVE_DELAY_MS }) {
    this.approveDelayMs = approveDelayMs;
    this.req = null;
    this.root = root;
    this.main = main;
    this.killButton = killButton;
    this.answer = answer;
    this.notice = notice;
    this.skew = skew;
    this.queue = [];
    this.shownId = "";
    this.shownAt = 0;
    this.sent = new Set(); // ids answered by this page
    this.opener = null;
    this.tick = 0;
    this.resend = 0;
    this.nodes = null;
    document.addEventListener("keydown", (ev) => this.keydown(ev), true);
  }

  get pending() {
    return this.queue.length;
  }

  clear() {
    this.queue = [];
    this.sent.clear();
    this.hide();
  }

  add(req) {
    const known = this.queue.findIndex((r) => r.id === req.id);
    if (known >= 0) this.queue[known] = req;
    else this.queue.push(req);
    this.show();
  }

  // resolve closes a request the host has ended: answered by this page, expired, or canceled.
  resolve(id, outcome) {
    const i = this.queue.findIndex((r) => r.id === id);
    if (i < 0) return;
    const [req] = this.queue.splice(i, 1);
    if (!this.sent.delete(id) && (outcome === "expired" || outcome === "canceled")) {
      this.notice("warn", `The request to run ${req.tool} ${outcome === "expired" ? "expired before it was answered" : "was canceled"}.`);
    }
    this.show();
  }

  show() {
    const req = this.queue[0];
    if (!req) {
      this.hide();
      return;
    }
    if (this.shownId === req.id) {
      this.position();
      return;
    }
    if (!this.opener) this.opener = document.activeElement;
    this.build(req);
    this.shownId = req.id;
    this.shownAt = performance.now();
    this.main.inert = true;
    this.root.hidden = false;
    this.nodes.reject.focus();
    this.position();
    this.countdown();
    clearInterval(this.tick);
    this.tick = setInterval(() => this.countdown(), 250);
    if (this.approveDelayMs > 0) setTimeout(() => this.nodes && this.nodes.approve.removeAttribute("aria-disabled"), this.approveDelayMs);
  }

  hide() {
    clearInterval(this.tick);
    clearTimeout(this.resend);
    this.root.replaceChildren();
    this.root.hidden = true;
    this.main.inert = false;
    this.shownId = "";
    this.nodes = null;
    if (this.opener && this.opener.isConnected) this.opener.focus();
    this.opener = null;
  }

  build(req) {
    const dialog = el("div", "modal");
    dialog.setAttribute("role", "alertdialog");
    dialog.setAttribute("aria-modal", "true");
    dialog.setAttribute("aria-labelledby", "confirm-title");
    dialog.setAttribute("aria-describedby", "confirm-why");

    const pos = el("p", "modal-pos");
    const title = el("h2", "modal-title", `The model wants to run: ${req.tool}`);
    title.id = "confirm-title";
    const why = el("p", "modal-why", req.reason || "The model gave no reason.");
    why.id = "confirm-why";

    const rulesBox = el("section", "modal-section");
    rulesBox.append(el("h3", "", "Safety rules that matched"));
    const list = el("ul", "rules");
    for (const r of req.rules) {
      const li = el("li");
      li.append(el("strong", "rule-id", r.id), el("span", "rule-text", ` ${r.explanation}`));
      list.append(li);
    }
    if (req.rules.length === 0) list.append(el("li", "", "none"));
    rulesBox.append(list);

    const actionBox = el("section", "modal-section");
    actionBox.append(el("h3", "", "The action"), el("pre", "modal-args", argsText(req.args)));

    const noteLabel = el("label", "modal-note");
    noteLabel.append(el("span", "", "Note for the log (optional)"));
    const note = el("input");
    note.type = "text";
    note.id = "confirm-note";
    note.maxLength = 500;
    noteLabel.append(note);

    const count = el("p", "modal-count");
    const status = el("p", "modal-status");
    status.setAttribute("role", "status");

    const reject = el("button", "btn-reject", "Reject");
    reject.type = "button";
    reject.id = "confirm-reject";
    const approve = el("button", "btn-approve", "Approve");
    approve.type = "button";
    approve.id = "confirm-approve";
    if (this.approveDelayMs > 0) approve.setAttribute("aria-disabled", "true");
    reject.addEventListener("click", (ev) => ev.isTrusted && this.send(false));
    approve.addEventListener("click", (ev) => {
      if (!ev.isTrusted) return; // only a person's click, not a script's
      if (approve.getAttribute("aria-disabled") === "true") return;
      this.send(true);
    });
    const actions = el("div", "modal-actions");
    actions.append(reject, approve);

    dialog.append(pos, title, why, rulesBox, actionBox, noteLabel, count, status, actions);
    this.root.replaceChildren(el("div", "modal-backdrop"));
    this.root.firstChild.append(dialog);
    this.nodes = { dialog, pos, count, status, reject, approve, note };
    this.req = req;
  }

  position() {
    if (!this.nodes) return;
    const n = this.queue.length;
    this.nodes.pos.textContent = n > 1 ? `Request 1 of ${n} waiting` : "Needs your decision";
  }

  countdown() {
    if (!this.nodes || !this.req) return;
    const left = Date.parse(this.req.expires_at) - (Date.now() + this.skew());
    this.nodes.count.textContent = Number.isNaN(left)
      ? ""
      : left > 0
        ? `Rejected automatically in ${mmss(left)} if you do not answer.`
        : "Time is up. Waiting for the host to close this request.";
  }

  send(approve) {
    if (!this.nodes || this.nodes.reject.disabled) return;
    const { reject, approve: ok, note, status } = this.nodes;
    if (!this.answer(this.req.id, approve, note.value.trim())) {
      status.textContent = "Not connected to the session. Try again when it is back.";
      return;
    }
    this.sent.add(this.req.id);
    reject.disabled = true;
    ok.disabled = true;
    status.textContent = "Sent. Waiting for the host…";
    clearTimeout(this.resend);
    this.resend = setTimeout(() => {
      if (!this.nodes) return;
      this.sent.delete(this.req.id);
      reject.disabled = false;
      ok.disabled = false;
      status.textContent = "The host has not answered yet. You can try again.";
    }, RESEND_MS);
  }

  keydown(ev) {
    if (!this.nodes) return;
    if (ev.key === "Escape") {
      ev.preventDefault();
      ev.stopPropagation(); // an Esc that rejects does not count towards the Esc-three-times kill
      this.send(false);
      return;
    }
    if (ev.key !== "Tab") return;
    // The cycle is made here, in full: the Kill button is early in the page and the dialog late, so the browser's own order
    // would leave the dialog.
    const items = [this.nodes.note, this.nodes.reject, this.nodes.approve, this.killButton].filter((e) => !e.disabled);
    let i = items.indexOf(document.activeElement);
    ev.preventDefault();
    if (ev.shiftKey) i = i <= 0 ? items.length - 1 : i - 1;
    else i = i < 0 || i === items.length - 1 ? 0 : i + 1;
    items[i].focus();
  }
}
