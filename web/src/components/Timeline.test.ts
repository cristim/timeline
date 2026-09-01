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
