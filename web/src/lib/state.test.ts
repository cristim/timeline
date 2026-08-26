// API-6: every view is shareable, so every field has to survive the round
// trip through the URL - including the ones that are legitimately absent.
import { describe, expect, it } from "vitest";
import { cursorTime, DEFAULT_VIEW, parseView, serializeView } from "./state";

describe("time cursor state", () => {
  it("is unpinned by default and follows the centre of the view", () => {
    expect(DEFAULT_VIEW.tc).toBeNull();
    expect(cursorTime({ t0: -100, t1: 300, tc: null })).toBe(100);
    expect(cursorTime({ t0: -100, t1: 300, tc: 42 })).toBe(42);
  });

  it("stays out of the URL until it is pinned", () => {
    expect(serializeView(DEFAULT_VIEW)).not.toContain("tc=");
    expect(serializeView({ ...DEFAULT_VIEW, tc: -8.6e8 })).toContain("tc=");
  });

  it("round-trips a pinned cursor", () => {
    const v = { ...DEFAULT_VIEW, tc: -8.64e8 };
    expect(parseView(serializeView(v)).tc).toBeCloseTo(-8.64e8, 3);
  });

  it("treats a pinned zero as pinned, not as absent", () => {
    // Number(null) is 0: without the has() guard an absent param would pin
    // the cursor to 1970.
    expect(parseView("?t0=-1e9&t1=1e9&tc=0").tc).toBe(0);
    expect(parseView("?t0=-1e9&t1=1e9").tc).toBeNull();
  });

  it("ignores a non-numeric cursor rather than pinning NaN", () => {
    expect(parseView("?tc=yesterday").tc).toBeNull();
  });
});
