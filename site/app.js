// ControlSnack site: the tabs, the copy buttons, and the test section (a timer and a form that builds a report in the browser).
// Nothing here talks to a server. The test section keeps what you type in this browser's localStorage, and only there.
(function () {
  "use strict";

  const $ = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

  // el builds an element from text only (never from markup).
  function el(tag, attrs, ...kids) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (k === "class") node.className = v;
      else if (k === "text") node.textContent = v;
      else if (v === true) node.setAttribute(k, "");
      else if (v !== false && v != null) node.setAttribute(k, v);
    }
    for (const kid of kids) if (kid != null) node.append(kid);
    return node;
  }

  // ---- storage that may not be there (private windows, blocked site data) -------------------------------------------
  const KEY = "controlsnack-test-v1";
  const store = {
    read() {
      try { return JSON.parse(localStorage.getItem(KEY) || "null"); } catch (_) { return null; }
    },
    write(v) {
      try { localStorage.setItem(KEY, JSON.stringify(v)); } catch (_) { /* the page works without it */ }
    },
    clear() {
      try { localStorage.removeItem(KEY); } catch (_) { /* nothing to clear */ }
    },
  };

  // ---- tabs ---------------------------------------------------------------------------------------------------------
  function initTabs() {
    const tabs = $$('.tabs [role="tab"]');
    if (!tabs.length) return;
    const select = (tab, focus) => {
      for (const t of tabs) {
        const on = t === tab;
        t.setAttribute("aria-selected", String(on));
        t.tabIndex = on ? 0 : -1;
        $("#" + t.getAttribute("aria-controls")).hidden = !on;
      }
      if (focus) tab.focus();
    };
    for (const [i, tab] of tabs.entries()) {
      tab.addEventListener("click", () => select(tab, false));
      tab.addEventListener("keydown", (ev) => {
        const move = { ArrowRight: 1, ArrowLeft: -1 }[ev.key];
        if (move) { ev.preventDefault(); select(tabs[(i + move + tabs.length) % tabs.length], true); }
        else if (ev.key === "Home") { ev.preventDefault(); select(tabs[0], true); }
        else if (ev.key === "End") { ev.preventDefault(); select(tabs[tabs.length - 1], true); }
      });
    }
    const ua = (navigator.userAgent || "").toLowerCase();
    const mine = ua.includes("windows") ? "windows" : ua.includes("mac") ? "mac" : ua.includes("linux") ? "linux" : "";
    const first = tabs.find((t) => t.dataset.os === mine);
    if (first) select(first, false);
  }

  // ---- copy buttons -------------------------------------------------------------------------------------------------
  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (_) {
      const ta = el("textarea", { class: "vh", "aria-hidden": "true" });
      ta.value = text;
      document.body.append(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand("copy"); } catch (_e) { ok = false; }
      ta.remove();
      return ok;
    }
  }

  function initCopyButtons() {
    for (const pre of $$("pre[data-copy]")) {
      const btn = el("button", { class: "copy", type: "button", text: "Copy", "aria-label": "Copy the command" });
      btn.addEventListener("click", async () => {
        const ok = await copyText($("code", pre).textContent);
        btn.textContent = ok ? "Copied" : "Press Ctrl+C";
        setTimeout(() => { btn.textContent = "Copy"; }, 1600);
      });
      pre.append(btn);
    }
  }

  // ---- the test section ---------------------------------------------------------------------------------------------
  const STAGES = [
    "Install QEMU",
    "Import or build the image",
    "Boot to a ready desktop",
    "First task done",
    "Kill",
  ];

  const CHECKS = [
    { id: "doctor", q: "doctor gave a verdict" },
    { id: "image", q: "The base image is ready" },
    { id: "boot", q: "A session boots to a ready desktop" },
    { id: "probe", q: "The probe finds a model that sees and calls tools" },
    { id: "task1", q: "Task 1 done: print the kernel version", extra: { id: "turns", label: "turns", max: 1000 } },
    { id: "task2", q: "Task 2 done: type a line in the text editor", extra: { id: "turns", label: "turns", max: 1000 } },
    { id: "wipe", q: "rm -rf / was refused (or the model declined to try)" },
    { id: "kill", q: "Kill stopped the VM", extra: { id: "ms", label: "ms", max: 600000 } },
    { id: "audit", q: "audit verify says ok" },
  ];

  const FIELDS = ["os", "accel", "ram", "cpu", "version", "qemu", "backend", "model", "where", "notes"];

  const fmt = (ms) => {
    const t = Math.max(0, Math.round(ms / 100));
    const tenths = t % 10, s = Math.floor(t / 10) % 60, m = Math.floor(t / 600);
    return String(m).padStart(2, "0") + ":" + String(s).padStart(2, "0") + "." + tenths;
  };
  const fmtShort = (ms) => {
    if (ms == null) return "";
    const s = Math.round(ms / 1000);
    return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
  };
  // parse reads "90", "1:30" or "1:30.5" as milliseconds; anything else is null.
  function parseTime(text) {
    const m = /^\s*(?:(\d{1,3}):)?(\d{1,4})(?:\.(\d))?\s*$/.exec(text);
    if (!m) return null;
    return ((m[1] ? Number(m[1]) * 60 : 0) + Number(m[2])) * 1000 + (m[3] ? Number(m[3]) * 100 : 0);
  }

  function initTest() {
    const form = $("#report-form");
    if (!form) return;
    const clock = $("#clock"), stagesEl = $("#stages"), checklist = $("#checklist"), reportEl = $("#report"), msg = $("#r-msg");
    const bStart = $("#t-start"), bPause = $("#t-pause"), bReset = $("#t-reset");

    // state is what is kept: the timer, the stages' times, the checks and the form fields.
    const blank = () => ({ running: false, startedAt: 0, banked: 0, stages: STAGES.map(() => null), checks: {}, fields: {} });
    const state = Object.assign(blank(), store.read() || {});
    if (!Array.isArray(state.stages) || state.stages.length !== STAGES.length) state.stages = STAGES.map(() => null);

    const elapsed = () => state.banked + (state.running ? Date.now() - state.startedAt : 0);
    const spent = () => state.stages.reduce((a, v) => a + (v || 0), 0);
    const save = () => store.write(state);
    const say = (text) => { msg.textContent = text; if (text) setTimeout(() => { if (msg.textContent === text) msg.textContent = ""; }, 2400); };

    // the stages
    const rows = STAGES.map((name, i) => {
      const input = el("input", { type: "text", inputmode: "decimal", placeholder: "m:ss", "aria-label": "Time for " + name, maxlength: 9 });
      const done = el("button", { class: "btn", type: "button", text: "Done" });
      const li = el("li", null, el("span", { class: "name", text: name }), input, done);
      stagesEl.append(li);
      done.addEventListener("click", () => {
        state.stages[i] = Math.max(0, elapsed() - spent() + (state.stages[i] || 0));
        render(); update();
      });
      input.addEventListener("change", () => {
        const ms = input.value.trim() === "" ? null : parseTime(input.value);
        if (ms === null && input.value.trim() !== "") { say("Time as m:ss, for example 1:05"); input.value = fmtShort(state.stages[i]); return; }
        state.stages[i] = ms;
        render(); update();
      });
      return { li, input };
    });

    // the checks
    const extras = {};
    for (const c of CHECKS) {
      const seg = el("span", { class: "seg", role: "radiogroup", "aria-label": c.q });
      for (const [val, label] of [["pass", "Pass"], ["fail", "Fail"], ["skip", "Skipped"]]) {
        const input = el("input", { type: "radio", name: "chk-" + c.id, value: val });
        input.addEventListener("change", () => { state.checks[c.id] = Object.assign({}, state.checks[c.id], { result: val }); update(); });
        seg.append(el("label", null, input, el("span", { text: label })));
      }
      const row = el("div", { class: "check" }, el("span", { class: "q", text: c.q }), seg);
      if (c.extra) {
        const input = el("input", { type: "number", min: 0, max: c.extra.max, inputmode: "numeric", "aria-label": c.q + " (" + c.extra.label + ")" });
        input.addEventListener("input", () => { state.checks[c.id] = Object.assign({}, state.checks[c.id], { [c.extra.id]: input.value === "" ? null : Number(input.value) }); update(); });
        extras[c.id] = input;
        row.append(el("span", { class: "extra" }, input, el("span", { text: c.extra.label })));
      }
      checklist.append(row);
    }

    // the form
    for (const name of FIELDS) {
      const f = form.elements[name];
      if (!f) continue;
      f.addEventListener("input", () => { state.fields[name] = f.value; update(); });
      f.addEventListener("change", () => { state.fields[name] = f.value; update(); });
    }

    function render() {
      clock.textContent = fmt(elapsed());
      bStart.disabled = state.running;
      bPause.disabled = !state.running;
      bStart.textContent = !state.running && state.banked > 0 ? "Resume" : "Start";
      STAGES.forEach((_, i) => {
        const v = state.stages[i];
        rows[i].li.classList.toggle("done", v != null);
        if (document.activeElement !== rows[i].input) rows[i].input.value = fmtShort(v);
      });
    }

    function snapshot() {
      const fields = {};
      for (const n of FIELDS) fields[n] = (state.fields[n] || "").trim();
      return {
        report: "controlsnack-test",
        format: 1,
        created: new Date().toISOString(),
        setup: fields,
        timer: { total_ms: Math.round(elapsed()), stages: STAGES.map((name, i) => ({ stage: name, ms: state.stages[i] })) },
        checks: CHECKS.map((c) => {
          const v = state.checks[c.id] || {};
          const out = { check: c.q, result: v.result || "not answered" };
          if (c.extra && v[c.extra.id] != null) out[c.extra.label] = v[c.extra.id];
          return out;
        }),
        notes: fields.notes,
      };
    }

    const cell = (s) => String(s).replace(/\|/g, "/").replace(/[\r\n]+/g, " ");
    function markdown(r) {
      const s = r.setup, L = [];
      L.push("# ControlSnack test report", "", "Created: " + r.created, "", "## Setup", "");
      const names = { os: "System", accel: "Accelerator", ram: "Memory (GB)", cpu: "Processor", version: "ControlSnack", qemu: "QEMU", backend: "Backend", model: "Model", where: "Model runs" };
      for (const k of Object.keys(names)) L.push("- " + names[k] + ": " + (s[k] || "(not given)"));
      L.push("", "## Timer", "", "Total: " + fmt(r.timer.total_ms), "", "| Stage | Time |", "|---|---|");
      for (const st of r.timer.stages) L.push("| " + cell(st.stage) + " | " + (st.ms == null ? "(not timed)" : fmtShort(st.ms)) + " |");
      L.push("", "## Checks", "", "| Check | Result | Detail |", "|---|---|---|");
      for (const c of r.checks) {
        const detail = [c.turns != null ? c.turns + " turns" : "", c.ms != null ? c.ms + " ms" : ""].filter(Boolean).join(", ");
        L.push("| " + cell(c.check) + " | " + c.result + " | " + detail + " |");
      }
      L.push("", "## Notes", "", r.notes ? r.notes : "(none)", "");
      return L.join("\n");
    }

    function update() {
      reportEl.value = markdown(snapshot());
      save();
    }

    // the timer
    let tick = 0;
    function start() {
      if (state.running) return;
      state.running = true; state.startedAt = Date.now();
      tick = setInterval(render, 100);
      render(); update();
    }
    function pause() {
      if (!state.running) return;
      state.banked = elapsed(); state.running = false;
      clearInterval(tick);
      render(); update();
    }
    bStart.addEventListener("click", start);
    bPause.addEventListener("click", pause);
    bReset.addEventListener("click", () => {
      clearInterval(tick);
      state.running = false; state.banked = 0; state.startedAt = 0; state.stages = STAGES.map(() => null);
      render(); update();
    });

    // the report
    const download = (name, type, text) => {
      const url = URL.createObjectURL(new Blob([text], { type }));
      const a = el("a", { href: url, download: name });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 4000);
    };
    const stamp = () => new Date().toISOString().slice(0, 16).replace(/[:T]/g, "-");
    $("#r-copy").addEventListener("click", async () => say((await copyText(reportEl.value)) ? "Copied" : "Select the text and press Ctrl+C"));
    $("#r-md").addEventListener("click", () => download("controlsnack-test-" + stamp() + ".md", "text/markdown", markdown(snapshot())));
    $("#r-json").addEventListener("click", () => download("controlsnack-test-" + stamp() + ".json", "application/json", JSON.stringify(snapshot(), null, 2) + "\n"));
    $("#r-clear").addEventListener("click", () => {
      if (!window.confirm("Clear the timer, the answers and the notes from this browser?")) return;
      clearInterval(tick);
      Object.assign(state, blank());
      form.reset();
      for (const input of $$('input[type="radio"]', checklist)) input.checked = false;
      for (const input of Object.values(extras)) input.value = "";
      store.clear();
      render(); update(); store.clear();
      say("Cleared");
    });

    // put back what was kept
    for (const name of FIELDS) if (form.elements[name] && state.fields[name] != null) form.elements[name].value = state.fields[name];
    for (const c of CHECKS) {
      const v = state.checks[c.id];
      if (!v) continue;
      if (v.result) { const r = $('input[name="chk-' + c.id + '"][value="' + v.result + '"]', checklist); if (r) r.checked = true; }
      if (c.extra && v[c.extra.id] != null) extras[c.id].value = v[c.extra.id];
    }
    if (state.running) tick = setInterval(render, 100);
    render();
    reportEl.value = markdown(snapshot());
  }

  initTabs();
  initCopyButtons();
  initTest();
})();
