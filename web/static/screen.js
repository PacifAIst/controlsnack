// The canvas that shows the guest's screen, with the cursor and the marker of the action about to run.
"use strict";

const PLAN_LIGHT = "#ffffff";
const PLAN_DARK = "#d9480f";

export class Screen {
  constructor(canvas) {
    this.canvas = canvas;
    this.ctx = canvas.getContext("2d");
    this.bitmap = null;
    this.guest = { w: 1280, h: 800 }; // the guest's screen in its own pixels
    this.cursor = null;
    this.plan = null;
    this.token = 0; // the newest frame being decoded; an older one that finishes later is dropped
    this.frames = 0;
    this.lastSeq = 0;
    this.dropped = 0;
    new ResizeObserver(() => this.draw()).observe(canvas);
  }

  setGuest(w, h) {
    if (w > 0 && h > 0) this.guest = { w, h };
    this.draw();
  }

  // setFrame decodes a WebP and shows it. Only the newest picture counts: one that is still decoding when a newer one
  // arrives is dropped.
  async setFrame(frame) {
    const token = ++this.token;
    let bitmap;
    try {
      bitmap = await createImageBitmap(new Blob([frame.data], { type: "image/webp" }));
    } catch {
      this.dropped += 1;
      return;
    }
    if (token !== this.token) {
      bitmap.close();
      this.dropped += 1;
      return;
    }
    if (this.bitmap) this.bitmap.close();
    this.bitmap = bitmap;
    this.guest = { w: bitmap.width, h: bitmap.height };
    this.frames += 1;
    this.lastSeq = frame.seq;
    this.draw();
  }

  setCursor(x, y) {
    this.cursor = { x, y };
    this.draw();
  }

  setPlan(plan) {
    this.plan = plan;
    this.draw();
  }

  // fit is where the guest screen sits in the canvas, in CSS pixels: as large as it goes without distortion, centred.
  fit(cssW, cssH) {
    const s = Math.min(cssW / this.guest.w, cssH / this.guest.h);
    const w = this.guest.w * s;
    const h = this.guest.h * s;
    return { s, x: (cssW - w) / 2, y: (cssH - h) / 2, w, h };
  }

  draw() {
    const c = this.canvas;
    const dpr = window.devicePixelRatio || 1;
    const cssW = c.clientWidth;
    const cssH = c.clientHeight;
    if (cssW === 0 || cssH === 0) return;
    const pw = Math.round(cssW * dpr);
    const ph = Math.round(cssH * dpr);
    if (c.width !== pw || c.height !== ph) {
      c.width = pw;
      c.height = ph;
    }
    const ctx = this.ctx;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cssW, cssH);
    const r = this.fit(cssW, cssH);
    if (this.bitmap) {
      ctx.imageSmoothingEnabled = true;
      ctx.imageSmoothingQuality = "high";
      ctx.drawImage(this.bitmap, r.x, r.y, r.w, r.h);
    } else {
      ctx.fillStyle = getComputedStyle(c).color;
      ctx.font = "16px system-ui, sans-serif";
      ctx.textAlign = "center";
      ctx.fillText("Waiting for the first picture of the screen…", cssW / 2, cssH / 2);
    }
    if (this.plan) this.drawPlan(ctx, r);
    if (this.cursor) this.drawCursor(ctx, r);
  }

  // marker draws the same shape twice, light under dark, so that it shows on any picture.
  marker(ctx, width, path) {
    ctx.lineJoin = "round";
    ctx.lineCap = "round";
    ctx.strokeStyle = PLAN_LIGHT;
    ctx.lineWidth = width + 3;
    path();
    ctx.stroke();
    ctx.strokeStyle = PLAN_DARK;
    ctx.lineWidth = width;
    path();
    ctx.stroke();
  }

  drawPlan(ctx, r) {
    const p = this.plan;
    const toX = (gx) => r.x + gx * r.s;
    const toY = (gy) => r.y + gy * r.s;
    ctx.save();
    if (p.bounds) {
      const b = p.bounds;
      this.marker(ctx, 2, () => {
        ctx.beginPath();
        ctx.rect(toX(b.x), toY(b.y), Math.max(2, b.w * r.s), Math.max(2, b.h * r.s));
      });
    }
    if (typeof p.x === "number" && typeof p.y === "number") {
      const x = toX(p.x);
      const y = toY(p.y);
      this.marker(ctx, 2, () => {
        ctx.beginPath();
        ctx.arc(x, y, 12, 0, Math.PI * 2);
        ctx.moveTo(x - 20, y);
        ctx.lineTo(x - 6, y);
        ctx.moveTo(x + 6, y);
        ctx.lineTo(x + 20, y);
        ctx.moveTo(x, y - 20);
        ctx.lineTo(x, y - 6);
        ctx.moveTo(x, y + 6);
        ctx.lineTo(x, y + 20);
      });
    }
    ctx.restore();
  }

  drawCursor(ctx, r) {
    const x = r.x + this.cursor.x * r.s;
    const y = r.y + this.cursor.y * r.s;
    const k = Math.max(0.7, Math.min(1.6, r.s));
    ctx.save();
    ctx.translate(x, y);
    ctx.scale(k, k);
    ctx.beginPath();
    ctx.moveTo(0, 0);
    ctx.lineTo(0, 17);
    ctx.lineTo(4.5, 13);
    ctx.lineTo(7.5, 20);
    ctx.lineTo(10, 19);
    ctx.lineTo(7, 12);
    ctx.lineTo(12.5, 12);
    ctx.closePath();
    ctx.fillStyle = "#ffffff";
    ctx.strokeStyle = "#000000";
    ctx.lineWidth = 1.4;
    ctx.fill();
    ctx.stroke();
    ctx.restore();
  }
}
