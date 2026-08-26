// FE-2: the Canvas timeline. Google-Maps-like interaction (wheel = zoom about
// cursor, drag = pan), semantic zoom via bucket switching upstream, temporal
// status marker language (FE-7). Not DOM, not SVG.
import { useCallback, useEffect, useRef } from "react";
import type { ChunkItem } from "../lib/data";
import { colorFor, markerStyle } from "../lib/colors";
import { formatTime } from "../lib/timefmt";
import { laneItems } from "../lib/visible";
import { SECONDS_PER_YEAR } from "../lib/keyscheme";

// Hard clamps: a linear axis cannot reach the 1e100-year tail; far-future
// items pin to a right-edge gutter instead (the deep-time overview strip in
// the mockup is the V2 answer).
const T_MIN = -4.75e17;
const T_MAX = 2.2e17;
const MIN_SPAN = 900; // 15 minutes
const AXIS_H = 26;
const ROW_H = 22;
const NOW_S = Date.now() / 1000;

interface Props {
  t0: number;
  t1: number;
  items: ChunkItem[];
  selected: string | null;
  onRange: (t0: number, t1: number) => void;
  onSelect: (slug: string | null) => void;
}

interface HitBox {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
  slug: string;
}

export function Timeline({ t0, t1, items, selected, onRange, onSelect }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const hits = useRef<HitBox[]>([]);
  const drag = useRef<{ x: number; t0: number; t1: number; moved: boolean } | null>(null);
  // Refs so the non-passive wheel listener (attached once) sees fresh state.
  const rangeRef = useRef({ t0, t1 });
  rangeRef.current = { t0, t1 };
  const onRangeRef = useRef(onRange);
  onRangeRef.current = onRange;

  const draw = useCallback(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const w = canvas.clientWidth;
    const h = canvas.clientHeight;
    if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
      canvas.width = w * dpr;
      canvas.height = h * dpr;
    }
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const span = t1 - t0;
    const x = (t: number) => ((t - t0) / span) * w;

    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = "#0b0f17";
    ctx.fillRect(0, 0, w, h);

    // ── axis ─────────────────────────────────────────────
    ctx.strokeStyle = "#1f2940";
    ctx.beginPath();
    ctx.moveTo(0, AXIS_H + 0.5);
    ctx.lineTo(w, AXIS_H + 0.5);
    ctx.stroke();
    const step = tickStep(span, w);
    ctx.font = "10px ui-monospace, monospace";
    ctx.fillStyle = "#5d6880";
    for (let t = Math.ceil(t0 / step) * step; t <= t1; t += step) {
      const px = x(t);
      ctx.strokeStyle = "#1f2940";
      ctx.beginPath();
      ctx.moveTo(px + 0.5, 0);
      ctx.lineTo(px + 0.5, h);
      ctx.stroke();
      ctx.fillText(formatTime(t, span), px + 4, 16);
    }

    // ── NOW line (FE-7) ─────────────────────────────────
    if (NOW_S >= t0 && NOW_S <= t1) {
      const px = x(NOW_S);
      ctx.strokeStyle = "#6fd4d0";
      ctx.setLineDash([4, 3]);
      ctx.beginPath();
      ctx.moveTo(px, 0);
      ctx.lineTo(px, h);
      ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = "#6fd4d0";
      ctx.fillText("NOW", px + 4, h - 6);
    }

    // ── items: greedy row packing, importance first ────
    // Lanes hold the top items that start or end inside the view; items that
    // merely pass through the whole view are skipped (lib/visible.ts).
    hits.current = [];
    const rows: number[] = []; // per-row rightmost occupied x
    const maxRows = Math.floor((h - AXIS_H - 6) / ROW_H);
    const offRight: ChunkItem[] = [];
    for (const item of items) {
      if (item.t0 > t1 && offRight.length < 12) offRight.push(item);
    }
    ctx.font = "11px system-ui, sans-serif";

    for (const item of laneItems(items, t0, t1)) {
      const isSpan = item.t1 > item.t0 && x(item.t1) - x(item.t0) > 8;
      const x0 = Math.max(-200, x(item.t0));
      const x1 = isSpan ? Math.min(w + 200, x(item.t1)) : x0;
      const label = item.name;
      const labelW = ctx.measureText(label).width;
      const needX0 = isSpan ? x0 : x0 - 5;
      const needX1 = (isSpan ? Math.min(x1, w) : x0) + 8 + labelW;

      let row = -1;
      for (let r = 0; r < Math.min(rows.length, maxRows); r++) {
        if (rows[r] < needX0 - 6) {
          row = r;
          break;
        }
      }
      if (row < 0) {
        if (rows.length >= maxRows) continue; // least important drop out first
        row = rows.length;
        rows.push(-Infinity);
      }
      rows[row] = needX1;
      const y = AXIS_H + 10 + row * ROW_H;

      const color = colorFor(item.categories);
      const style = markerStyle(item.status);
      const isSel = item.slug === selected;
      ctx.globalAlpha = 1;

      if (isSpan) {
        const bh = 10;
        if (style === "fuzzy") ctx.setLineDash([3, 3]);
        ctx.fillStyle = color;
        ctx.strokeStyle = isSel ? "#e8b45a" : color;
        ctx.lineWidth = isSel ? 2 : 1.2;
        if (style === "solid") {
          ctx.globalAlpha = isSel ? 1 : 0.85;
          fillRoundRect(ctx, x0, y - bh / 2, x1 - x0, bh, 3);
        } else {
          strokeRoundRect(ctx, x0, y - bh / 2, x1 - x0, bh, 3);
        }
        ctx.setLineDash([]);
        ctx.globalAlpha = 1;
        ctx.fillStyle = isSel ? "#e8b45a" : "#c8cdd8";
        ctx.fillText(label, Math.max(x0, 4) + 6, y + 4);
        hits.current.push({ x0: Math.max(x0, 0), y0: y - 10, x1: Math.min(x1, w) + 8 + labelW, y1: y + 10, slug: item.slug });
      } else {
        const r = isSel ? 6 : 4.5;
        ctx.strokeStyle = isSel ? "#e8b45a" : color;
        ctx.fillStyle = isSel ? "#e8b45a" : color;
        ctx.lineWidth = 1.6;
        ctx.beginPath();
        ctx.arc(x0, y, r, 0, Math.PI * 2);
        if (style === "solid") {
          ctx.fill();
        } else {
          if (style === "fuzzy") ctx.setLineDash([2, 2]);
          ctx.stroke();
          ctx.setLineDash([]);
        }
        ctx.fillStyle = isSel ? "#e8b45a" : "#c8cdd8";
        ctx.fillText(label, x0 + 9, y + 4);
        hits.current.push({ x0: x0 - 7, y0: y - 9, x1: x0 + 9 + labelW, y1: y + 9, slug: item.slug });
      }
      if (item.child_count) {
        ctx.fillStyle = "#5d6880";
        ctx.fillText(`+${item.child_count}`, (isSpan ? Math.max(x0, 4) : x0 + 9) + labelW + 10, y + 4);
      }
    }

    if (import.meta.env.DEV) {
      // e2e test hook (dev server only)
      (window as unknown as Record<string, unknown>).__wkhits = hits.current;
    }

    // ── beyond-range future gutter ─────────────────────
    if (offRight.length) {
      ctx.fillStyle = "#6fd4d0";
      ctx.font = "10px ui-monospace, monospace";
      offRight.slice(0, 8).forEach((item, i) => {
        const y = AXIS_H + 16 + i * 16;
        ctx.globalAlpha = 0.8;
        ctx.fillText(`⇥ ${item.name}`, w - 190, y);
        hits.current.push({ x0: w - 195, y0: y - 10, x1: w - 8, y1: y + 4, slug: item.slug });
      });
      ctx.globalAlpha = 1;
    }
  }, [t0, t1, items, selected]);

  useEffect(() => {
    const raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, [draw]);

  useEffect(() => {
    const onResize = () => draw();
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [draw]);

  // ── interaction ──────────────────────────────────────
  // Wheel zoom needs preventDefault (else the page scrolls), and React
  // attaches wheel listeners passively - so bind natively, non-passive, once.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const { t0: rt0, t1: rt1 } = rangeRef.current;
      const rect = canvas.getBoundingClientRect();
      const frac = (e.clientX - rect.left) / rect.width;
      const span = rt1 - rt0;
      const cursorT = rt0 + frac * span;
      const factor = Math.exp(e.deltaY * 0.0015);
      const newSpan = Math.min(T_MAX - T_MIN, Math.max(MIN_SPAN, span * factor));
      const [a, b] = clampRange(cursorT - frac * newSpan, cursorT + (1 - frac) * newSpan);
      // Optimistically update the ref: multiple wheel events can land in one
      // frame, and each must build on the previous one, not on stale props.
      rangeRef.current = { t0: a, t1: b };
      onRangeRef.current(a, b);
    };
    canvas.addEventListener("wheel", onWheel, { passive: false });
    return () => canvas.removeEventListener("wheel", onWheel);
  }, []);

  const onPointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    drag.current = { x: e.clientX, t0, t1, moved: false };
  };
  const onPointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    if (Math.abs(dx) > 3) d.moved = true;
    const rect = e.currentTarget.getBoundingClientRect();
    const dt = (dx / rect.width) * (d.t1 - d.t0);
    const [a, b] = clampRange(d.t0 - dt, d.t1 - dt);
    onRange(a, b);
  };
  const onPointerUp = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const d = drag.current;
    drag.current = null;
    if (d?.moved) return;
    // click: hit-test
    const rect = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const py = e.clientY - rect.top;
    const hit = [...hits.current].reverse().find((b) => px >= b.x0 && px <= b.x1 && py >= b.y0 && py <= b.y1);
    onSelect(hit ? hit.slug : null);
  };

  return (
    <canvas
      ref={canvasRef}
      className="timeline-canvas"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
    />
  );
}

function clampRange(a: number, b: number): [number, number] {
  let na = Math.max(T_MIN, a);
  let nb = Math.min(T_MAX, b);
  if (nb - na < MIN_SPAN) {
    const mid = (na + nb) / 2;
    na = mid - MIN_SPAN / 2;
    nb = mid + MIN_SPAN / 2;
  }
  return [na, nb];
}

// Tick steps from 1 hour up to 1 Gyr, chosen so labels stay ~90px apart.
const STEPS: number[] = (() => {
  const hours = [3_600, 6 * 3_600, 86_400, 7 * 86_400];
  const years = [1 / 12, 1, 10, 100, 1_000, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10].map(
    (y) => y * SECONDS_PER_YEAR,
  );
  return [...hours, ...years];
})();

function tickStep(span: number, width: number): number {
  const target = span / (width / 90);
  for (const s of STEPS) {
    if (s >= target) return s;
  }
  return STEPS[STEPS.length - 1];
}

function fillRoundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.roundRect(x, y, Math.max(w, 2), h, r);
  ctx.fill();
}
function strokeRoundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.roundRect(x, y, Math.max(w, 2), h, r);
  ctx.stroke();
}
