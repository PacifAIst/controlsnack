// The WebSocket to the host, with the page's reconnection rules.
"use strict";

import { parseFrame, parseMessage } from "./protocol.js";

const FIRST_DELAY_MS = 500;
const MAX_DELAY_MS = 5000;
const MAX_ATTEMPTS = 30;

// Why a connection will not be tried again.
export const GAVE_UP = {
  replaced: "This window was replaced by another one, or the session is over.",
  bug: "The host refused what this page sent. This is a bug in the viewer.",
  unreachable: "The session cannot be reached. It has probably ended.",
  ended: "The session has ended.",
};

export class Connection {
  // handlers: onOpen(), onMessage(message), onFrame(frame), onClose({ code, retryInMs, gaveUp })
  constructor(url, handlers) {
    this.url = url;
    this.h = handlers;
    this.ws = null;
    this.attempts = 0;
    this.timer = 0;
    this.stopped = false;
    this.ended = false; // the host said the session is over: no more attempts
  }

  start() {
    this.stopped = false;
    this.open();
  }

  open() {
    clearTimeout(this.timer);
    let ws;
    try {
      ws = new WebSocket(this.url);
    } catch {
      this.closed({ code: 1006 });
      return;
    }
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    ws.addEventListener("open", () => {
      this.attempts = 0;
      this.h.onOpen();
    });
    ws.addEventListener("message", (ev) => {
      if (typeof ev.data === "string") {
        const m = parseMessage(ev.data);
        if (m) this.h.onMessage(m);
      } else {
        const f = parseFrame(ev.data);
        if (f) this.h.onFrame(f);
      }
    });
    ws.addEventListener("close", (ev) => {
      if (this.ws === ws) this.ws = null;
      this.closed({ code: ev.code });
    });
  }

  closed({ code }) {
    if (this.stopped) return;
    const give = (why) => this.h.onClose({ code, retryInMs: 0, gaveUp: why });
    if (this.ended) return give("ended");
    if (code === 1003 || code === 1008) return give("bug");
    if (code === 1001) return give("replaced");
    if (this.attempts >= MAX_ATTEMPTS) return give("unreachable");
    const delay = Math.min(MAX_DELAY_MS, FIRST_DELAY_MS * 2 ** Math.min(this.attempts, 4));
    this.attempts += 1;
    this.h.onClose({ code, retryInMs: delay, gaveUp: "" });
    this.timer = setTimeout(() => this.open(), delay);
  }

  get isOpen() {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN;
  }

  // send writes a message and says whether the socket was there to take it.
  send(message) {
    if (!this.isOpen) return false;
    this.ws.send(JSON.stringify(message));
    return true;
  }

  // end is called when the host says the session is over: the socket will close and must not be reopened.
  end() {
    this.ended = true;
  }

  stop() {
    this.stopped = true;
    clearTimeout(this.timer);
    if (this.ws) this.ws.close();
  }
}
