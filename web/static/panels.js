// The panels on the right of the page and the notices under the screen. Every string that comes from the host (and so
// from the machine or the model) is put in with textContent: no markup is ever built from it.
"use strict";

// el makes an element with a class and, if given, a text.
export function el(tag, className, text) {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

// A scroller follows the bottom unless the person has scrolled up to read.
function stickToBottom(box, add) {
  const atEnd = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  add();
  if (atEnd) box.scrollTop = box.scrollHeight;
}

const ROLE_WORDS = { user: "You", assistant: "Model", system: "System" };

function clock(ts) {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export class Chat {
  constructor(log) {
    this.log = log;
    this.seen = new Set();
  }

  clear() {
    this.log.replaceChildren();
    this.seen.clear();
  }

  add(m) {
    if (this.seen.has(m.id)) return; // a replay after a reconnect repeats what the page already shows
    this.seen.add(m.id);
    const role = ROLE_WORDS[m.role] ? m.role : "system";
    const item = el("div", `msg msg-${role}`);
    const head = el("div", "msg-head");
    head.append(el("span", "msg-role", ROLE_WORDS[role]), el("span", "msg-time", clock(m.ts)));
    item.append(head, el("div", "msg-body", m.text));
    stickToBottom(this.log, () => this.log.append(item));
  }
}

const MAX_TRACE = 500;
const MAX_ARGS = 4000;

function argsText(args) {
  let s;
  try {
    s = JSON.stringify(args, null, 2);
  } catch {
    s = String(args);
  }
  return s.length > MAX_ARGS ? `${s.slice(0, MAX_ARGS)}\n… (${s.length - MAX_ARGS} more characters)` : s;
}

export class Trace {
  constructor(log) {
    this.log = log;
    this.items = new Map(); // action id -> its elements
    this.turn = 0;
  }

  clear() {
    this.log.replaceChildren();
    this.items.clear();
    this.turn = 0;
  }

  get actions() {
    return this.items.size;
  }

  update(t) {
    this.turn = Math.max(this.turn, t.turn);
    let it = this.items.get(t.action_id);
    if (!it) {
      it = this.make();
      this.items.set(t.action_id, it);
      stickToBottom(this.log, () => this.log.append(it.root));
      if (this.items.size > MAX_TRACE) this.dropOldest();
    }
    it.root.dataset.status = t.status;
    it.tool.textContent = t.tool;
    it.verdict.textContent = t.verdict;
    it.verdict.className = `chip verdict-${t.verdict}`;
    it.status.textContent = t.status;
    it.status.className = `t-status status-${t.status}`;
    it.ms.textContent = t.duration_ms > 0 ? `${t.duration_ms} ms` : "";
    it.summary.textContent = t.summary || t.reason;
    it.reason.textContent = t.reason || "—";
    it.args.textContent = argsText(t.args);
    it.rules.textContent = t.rules.length > 0 ? t.rules.join(", ") : "none";
    it.result.textContent = t.summary || "—";
  }

  make() {
    const root = el("details", "trace-item");
    const sum = el("summary");
    const tool = el("span", "t-tool");
    const verdict = el("span", "chip");
    const status = el("span", "t-status");
    const ms = el("span", "t-ms");
    const summary = el("span", "t-summary");
    sum.append(tool, verdict, status, ms, summary);
    const body = el("dl", "trace-body");
    const row = (label) => {
      const dd = el("dd");
      body.append(el("dt", "", label), dd);
      return dd;
    };
    const reason = row("Why");
    const argsBox = el("pre");
    body.append(el("dt", "", "Arguments"), argsBox);
    const rules = row("Rules");
    const result = row("Result");
    root.append(sum, body);
    return { root, tool, verdict, status, ms, summary, reason, args: argsBox, rules, result };
  }

  dropOldest() {
    const [id, it] = this.items.entries().next().value;
    it.root.remove();
    this.items.delete(id);
  }
}

const MAX_AUDIT = 200;

export class Audit {
  constructor(log) {
    this.log = log;
  }

  clear() {
    this.log.replaceChildren();
  }

  add(line) {
    stickToBottom(this.log, () => {
      this.log.append(el("p", "audit-line", line));
      while (this.log.childElementCount > MAX_AUDIT) this.log.firstElementChild.remove();
    });
  }
}

const MAX_NOTICES = 5;
const INFO_MS = 15000;

export class Notices {
  constructor(box) {
    this.box = box;
  }

  add(n) {
    const level = ["info", "warn", "error"].includes(n.level) ? n.level : "info";
    const item = el("div", `notice notice-${level}`);
    const close = el("button", "", "Dismiss");
    close.type = "button";
    close.addEventListener("click", () => item.remove());
    item.append(el("span", "notice-text", n.text), close);
    this.box.append(item);
    while (this.box.childElementCount > MAX_NOTICES) this.box.firstElementChild.remove();
    if (level === "info") setTimeout(() => item.remove(), INFO_MS);
  }

  clear() {
    this.box.replaceChildren();
  }
}
