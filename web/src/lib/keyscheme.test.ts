// API-5: pins the client window-index implementation to the committed fixture
// generated from the Go implementation. A failure here means baker and client
// would compute different chunk keys - the architecture's failure mode.
import { describe, expect, it } from "vitest";
import {
  windowIndex,
  bucketForSpan,
  layerKey,
  coveringTimestep,
  SECONDS_PER_YEAR,
  windowsInRange,
} from "./keyscheme";
import type { Bucket } from "./manifest";
import cases from "./keycases.json";

// Mirror of internal/model/buckets.go - the fixture cross-checks it.
const BUCKETS: Bucket[] = [
  { id: "T0", window_s: 0 },
  { id: "T1", window_s: 0 },
  { id: "T2", window_s: 0 },
  { id: "T3", window_s: 1e7 * SECONDS_PER_YEAR },
  { id: "T4", window_s: 1e6 * SECONDS_PER_YEAR },
  { id: "T5", window_s: 1e5 * SECONDS_PER_YEAR },
  { id: "T6", window_s: 1e4 * SECONDS_PER_YEAR },
  { id: "T7", window_s: 1e3 * SECONDS_PER_YEAR },
  { id: "T8", window_s: 100 * SECONDS_PER_YEAR },
  { id: "T9", window_s: 10 * SECONDS_PER_YEAR },
  { id: "T10", window_s: 1 * SECONDS_PER_YEAR },
  { id: "T11", window_s: SECONDS_PER_YEAR / 12 },
  { id: "T12", window_s: 86_400 },
  { id: "T13", window_s: 3_600 },
];
const byId = new Map(BUCKETS.map((b) => [b.id, b]));

describe("windowIndex fixture parity", () => {
  it("matches every generated Go case", () => {
    expect(cases.length).toBeGreaterThan(100);
    for (const c of cases as { bucket: string; t: number; window: number }[]) {
      const bucket = byId.get(c.bucket);
      expect(bucket, c.bucket).toBeDefined();
      expect(windowIndex(bucket!, c.t), `${c.bucket} t=${c.t}`).toBe(c.window);
    }
  });
});

describe("layer time-steps", () => {
  // A tiling layer, spaced as unevenly as the real one: a 113,000-year window
  // followed by century and decade windows.
  const steps = [
    { year: -123000, t_from: -123000, t_to: -10001 },
    { year: -10000, t_from: -10000, t_to: -8001 },
    { year: 1900, t_from: 1900, t_to: 1913 },
    { year: 1914, t_from: 1914, t_to: 1919 },
    { year: 1920, t_from: 1920, t_to: 2035 },
  ];

  it("builds the API-4 layer key", () => {
    expect(layerKey("borders", 1942)).toBe("layers/borders/1942.pmtiles");
    expect(layerKey("borders", -500)).toBe("layers/borders/-500.pmtiles");
  });

  it("picks the step whose window covers the year", () => {
    expect(coveringTimestep(steps, 1916)?.year).toBe(1914);
    expect(coveringTimestep(steps, 1900)?.year).toBe(1900); // first year of a window
    expect(coveringTimestep(steps, 1913)?.year).toBe(1900); // last year of a window
    expect(coveringTimestep(steps, 2035)?.year).toBe(1920);
  });

  it("covers a long window rather than snapping to a nearer step", () => {
    // The regression this replaced nearest-step snapping for: 66500 BC is
    // nearer to the 10000 BC step, but only the 123000 BC step covers it, and
    // snapping to the nearer one blanked the map across most of prehistory.
    expect(coveringTimestep(steps, -66500)?.year).toBe(-123000);
    expect(coveringTimestep(steps, -10001)?.year).toBe(-123000);
    expect(coveringTimestep(steps, -10000)?.year).toBe(-10000);
  });

  it("has no answer outside every window, rather than the nearest one", () => {
    expect(coveringTimestep(steps, -500000)).toBeNull(); // before the layer
    expect(coveringTimestep(steps, -9000)).not.toBeNull();
    expect(coveringTimestep(steps, -5000)).toBeNull(); // in the layer's own gap
    expect(coveringTimestep(steps, 3000)).toBeNull(); // after the layer
    expect(coveringTimestep([], 1942)).toBeNull();
  });
});

describe("bucketForSpan", () => {
  const pick = (span: number) => BUCKETS[bucketForSpan(BUCKETS, span)].id;
  it("chooses sensible buckets across scales", () => {
    expect(pick(3_600)).toBe("T13"); // an hour
    expect(pick(5 * 86_400)).toBe("T12"); // days
    expect(pick(5 * SECONDS_PER_YEAR)).toBe("T10"); // years
    expect(pick(500 * SECONDS_PER_YEAR)).toBe("T8"); // centuries
    expect(pick(2e4 * SECONDS_PER_YEAR)).toBe("T6");
    expect(pick(5e7 * SECONDS_PER_YEAR)).toBe("T3");
    expect(pick(5e9 * SECONDS_PER_YEAR)).toBe("T1"); // billion-year scale
    expect(pick(3e10 * SECONDS_PER_YEAR)).toBe("T0"); // whole-universe
  });
});

describe("windowsInRange", () => {
  it("resolves any overlapping run to that run's start key", () => {
    const bucket: Bucket = {
      id: "T10",
      window_s: 1,
      windows: { war: [[10, 12], [15, 15], [20, 22]] },
    };

    expect(windowsInRange(bucket, "war", 11, 11)).toEqual([10]);
    expect(windowsInRange(bucket, "war", 12, 20)).toEqual([10, 15, 20]);
    expect(windowsInRange(bucket, "war", 13, 14)).toEqual([]);
  });

  it("keeps adjacent runs distinct and returns each overlapped start once", () => {
    const bucket: Bucket = {
      id: "T10",
      window_s: 1,
      windows: { war: [[1, 1], [2, 4], [2, 4], [4, 6]] },
    };

    expect(windowsInRange(bucket, "war", 1, 2)).toEqual([1, 2]);
    expect(windowsInRange(bucket, "war", 4, 4)).toEqual([2, 4]);
  });

  it("handles negative windows and missing categories", () => {
    const bucket: Bucket = {
      id: "T10",
      window_s: 1,
      windows: { all: [[-31, -29], [-10, -10]] },
    };

    expect(windowsInRange(bucket, "all", -30, -10)).toEqual([-31, -10]);
    expect(windowsInRange(bucket, "war", -30, -10)).toEqual([]);
  });

  it("uses run coverage for all-time buckets instead of assuming T0 exists", () => {
    expect(windowsInRange({ id: "T0", window_s: 0, windows: { all: [[0, 0]] } }, "all", -1e20, 1e20)).toEqual([0]);
    expect(windowsInRange({ id: "T0", window_s: 0, windows: { all: [[1, 1]] } }, "all", -1e20, 1e20)).toEqual([]);
  });
});
