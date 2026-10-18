// The viewer's wire protocol (schema/viewer-ws.schema.json), seen from the page.
"use strict";

// A frame is a binary message: 0x01, a big-endian uint32 sequence number, uint16 width, uint16 height, then WebP bytes.
export const FRAME_HEADER = 9;

export function parseFrame(buffer) {
  if (!(buffer instanceof ArrayBuffer) || buffer.byteLength <= FRAME_HEADER) return null;
  const view = new DataView(buffer);
  if (view.getUint8(0) !== 1) return null;
  const w = view.getUint16(5);
  const h = view.getUint16(7);
  if (w === 0 || h === 0) return null;
  return { seq: view.getUint32(1), w, h, data: new Uint8Array(buffer, FRAME_HEADER) };
}

// parseMessage reads a text message of the host. Anything that is not an object with a string type is dropped.
export function parseMessage(text) {
  let m;
  try {
    m = JSON.parse(text);
  } catch {
    return null;
  }
  if (m === null || typeof m !== "object" || Array.isArray(m) || typeof m.type !== "string") return null;
  return m;
}

// The messages the page sends.
export const chat = (text) => ({ type: "chat", text });
export const confirm = (id, approve, note) => (note ? { type: "confirm", id, approve, note } : { type: "confirm", id, approve });
export const control = (op, src) => (src ? { type: "control", op, src } : { type: "control", op });
export const ping = (t) => ({ type: "ping", t });

// The words for the session's states.
const STATE_WORDS = {
  booting: "Starting the VM",
  handshake: "Starting the VM",
  ready: "Ready",
  running: "Working",
  awaiting_confirm: "Needs your approval",
  awaiting_user: "Waiting for you",
  paused: "Paused",
  ending: "Ending",
  ended: "Ended",
  killed: "Killed",
};

export function stateWords(state) {
  return STATE_WORDS[state] ?? state;
}

// A state in which nothing more will happen.
export const isFinal = (state) => state === "ended" || state === "killed" || state === "ending";
