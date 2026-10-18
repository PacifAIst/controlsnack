// The keys that kill the session (PLAN.md section 10.5, D-19): Ctrl+Shift+Esc, where the system hands it to the page, and
// Esc pressed three times within 1.2 seconds. The second is deliberately awkward to do by accident, and an Esc that was
// meant for something else never counts: one that closes an approval dialog, one that cancels an IME composition, and one
// that is a held key repeating.
"use strict";

export const SEQUENCE = 3;
export const WINDOW_MS = 1200;

export class KillHotkeys {
  // kill(src) ends the session. hint(text) tells the person how far they are (text "" clears it).
  constructor({ kill, hint }) {
    this.kill = kill;
    this.hint = hint;
    this.presses = []; // the times (event timeStamp) of the latest Esc presses
    this.clearTimer = 0;
  }

  // onKeyDown looks at a keydown event and says whether it was a kill.
  onKeyDown(ev) {
    if (!ev.isTrusted || ev.defaultPrevented || ev.isComposing || ev.repeat || ev.key !== "Escape") return false;
    if (ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey) {
      this.fire(ev);
      return true;
    }
    if (ev.ctrlKey || ev.shiftKey || ev.altKey || ev.metaKey) return false;
    this.presses.push(ev.timeStamp);
    if (this.presses.length > SEQUENCE) this.presses.shift();
    if (this.presses.length === SEQUENCE && this.presses[SEQUENCE - 1] - this.presses[0] <= WINDOW_MS) {
      this.fire(ev);
      return true;
    }
    const left = SEQUENCE - this.countRecent(ev.timeStamp);
    if (left < SEQUENCE) this.say(`Press Esc ${left} more time${left === 1 ? "" : "s"} to kill the session.`);
    return false;
  }

  // countRecent is how many of the latest presses are inside the window that ends now.
  countRecent(now) {
    return this.presses.filter((t) => now - t <= WINDOW_MS).length;
  }

  fire(ev) {
    ev.preventDefault();
    this.presses = [];
    this.say("");
    this.kill("hotkey");
  }

  say(text) {
    this.hint(text);
    clearTimeout(this.clearTimer);
    if (text !== "") this.clearTimer = setTimeout(() => this.hint(""), WINDOW_MS);
  }
}
