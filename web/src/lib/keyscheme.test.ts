// API-5: pins the client window-index implementation to the committed fixture
// generated from the Go implementation. A failure here means baker and client
// would compute different chunk keys - the architecture's failure mode.
import { describe, expect, it } from "vitest";
import {
  windowIndex,
  bucketForSpan,
  layerKey,
  nearestTimestep,
  SECONDS_PER_YEAR,
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
  const steps = [117, 1279, 1914, 1942, 1990];

  it("builds the API-4 layer key", () => {
    expect(layerKey("borders", 1942)).toBe("layers/borders/1942.json");
    expect(layerKey("borders", -500)).toBe("layers/borders/-500.json");
  });

  it("snaps to the nearest step, forwards or backwards", () => {
    expect(nearestTimestep(steps, 1943)).toBe(1942);
    expect(nearestTimestep(steps, 1900)).toBe(1914);
    expect(nearestTimestep(steps, -3000)).toBe(117); // far outside: still nearest
    expect(nearestTimestep(steps, 1928)).toBe(1914); // exact midpoint: earlier wins
  });

  it("has no answer when a layer has no steps", () => {
    expect(nearestTimestep([], 1942)).toBeNull();
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
