// @vitest-environment jsdom

import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import type { ChunkItem } from "../lib/data";
import { Timeline } from "./Timeline";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("restores full opacity after drawing only a past comparison gutter", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const context = canvasContext();
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    context as unknown as CanvasRenderingContext2D,
  );
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(220);
  vi.stubGlobal("requestAnimationFrame", (draw: FrameRequestCallback) => {
    draw(0);
    return 1;
  });
  vi.stubGlobal("cancelAnimationFrame", () => undefined);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });

  const past: ChunkItem = {
    slug: "past",
    type: "event",
    name: "Past subject",
    t0: -100,
    t1: -100,
    precision: "year",
    status: "documented",
    categories: ["earth"],
    importance: 1,
  };
  const root = createRoot(document.createElement("div"));
  await act(async () => root.render(createElement(Timeline, {
    t0: 0,
    t1: 100,
    items: [past],
    selected: null,
    compare: ["past"],
    tc: 50,
    onRange: () => undefined,
    onSelect: () => undefined,
    onCursor: () => undefined,
  })));
  expect(context.globalAlpha).toBe(1);
  await act(async () => root.unmount());
});

it("pans with arrows, moves the cursor with Shift+arrows, and zooms with plus/minus", async () => {
  const { canvas, cursors, ranges, unmount } = await renderKeyboardTimeline(0, 10_000, 5_000);

  canvas.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
  expect(ranges.at(-1)).toEqual([1_000, 11_000]);
  expect(cursors).toEqual([]);

  canvas.dispatchEvent(new KeyboardEvent("keydown", {
    key: "ArrowRight", shiftKey: true, bubbles: true,
  }));
  expect(ranges.at(-1)).toEqual([1_000, 11_000]);
  expect(cursors).toEqual([6_000]);

  canvas.dispatchEvent(new KeyboardEvent("keydown", { key: "+", bubbles: true }));
  expect(ranges.at(-1)).toEqual([2_000, 10_000]);
  canvas.dispatchEvent(new KeyboardEvent("keydown", { key: "-", bubbles: true }));
  expect(ranges.at(-1)).toEqual([1_000, 11_000]);
  await unmount();
});

it.each([
  ["minimum", -4.75e17, -4.75e17 + 10_000, "ArrowLeft"],
  ["maximum", 2.2e17 - 10_000, 2.2e17, "ArrowRight"],
] as const)("preserves the viewport span while repeatedly panning at the %s bound", async (
  _bound,
  t0,
  t1,
  key,
) => {
  const { canvas, ranges, unmount } = await renderKeyboardTimeline(t0, t1);

  for (let i = 0; i < 3; i++) {
    canvas.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  }
  expect(ranges).toHaveLength(3);
  for (const [a, b] of ranges) {
    expect([a, b]).toEqual([t0, t1]);
    expect(b - a).toBe(t1 - t0);
  }
  await unmount();
});

it.each([
  ["minimum", -4.75e17, -4.75e17 + 900],
  ["maximum", 2.2e17 - 900, 2.2e17],
] as const)("keeps minimum-span zoom inside the %s bound", async (_bound, t0, t1) => {
  const { canvas, ranges, unmount } = await renderKeyboardTimeline(t0, t1);

  canvas.dispatchEvent(new KeyboardEvent("keydown", { key: "+", bubbles: true }));
  expect(ranges.at(-1)).toEqual([t0, t1]);
  await unmount();
});

it.each([
  ["minimum", -4.75e17, -4.75e17 + 10_000],
  ["maximum", 2.2e17 - 10_000, 2.2e17],
] as const)("preserves the zoom factor at the %s bound", async (_bound, t0, t1) => {
  const { canvas, ranges, unmount } = await renderKeyboardTimeline(t0, t1);

  canvas.dispatchEvent(new KeyboardEvent("keydown", { key: "-", bubbles: true }));
  const [a, b] = ranges.at(-1)!;
  expect((b - a) / (t1 - t0)).toBeGreaterThanOrEqual(1.24);
  expect((b - a) / (t1 - t0)).toBeLessThanOrEqual(1.26);
  expect(_bound === "minimum" ? a : b).toBe(_bound === "minimum" ? t0 : t1);
  await unmount();
});

async function renderKeyboardTimeline(t0: number, t1: number, tc: number | null = null) {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    canvasContext() as unknown as CanvasRenderingContext2D,
  );
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(220);
  vi.stubGlobal("requestAnimationFrame", () => 1);
  vi.stubGlobal("cancelAnimationFrame", () => undefined);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  const ranges: [number, number][] = [];
  const cursors: (number | null)[] = [];
  const container = document.body.appendChild(document.createElement("div"));
  const root = createRoot(container);
  await act(async () => root.render(createElement(Timeline, {
    t0,
    t1,
    items: [],
    selected: null,
    compare: [],
    tc,
    onRange: (a, b) => ranges.push([a, b]),
    onSelect: () => undefined,
    onCursor: (value) => cursors.push(value),
  })));
  const canvas = container.querySelector("canvas") as HTMLCanvasElement;
  return {
    canvas,
    cursors,
    ranges,
    unmount: async () => {
      await act(async () => root.unmount());
      container.remove();
    },
  };
}

function canvasContext() {
  const savedAlpha: number[] = [];
  return {
    globalAlpha: 1,
    setTransform() {},
    clearRect() {},
    fillRect() {},
    beginPath() {},
    moveTo() {},
    lineTo() {},
    stroke() {},
    fill() {},
    arc() {},
    fillText() {},
    setLineDash() {},
    roundRect() {},
    quadraticCurveTo() {},
    closePath() {},
    measureText(text: string) { return { width: text.length * 6 }; },
    save() { savedAlpha.push(this.globalAlpha); },
    restore() { this.globalAlpha = savedAlpha.pop() ?? 1; },
  };
}
