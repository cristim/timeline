import { describe, expect, it } from "vitest";
import { frontAt, frontBounds } from "./fronts";
import type { GeometryRecord } from "./data";

const pos = (t: number, label: string, coords: [number, number][]): GeometryRecord => ({
  valid_from: t,
  label,
  representation: "estimated",
  source: "atlas",
  geometry: { type: "LineString", coordinates: coords },
});

const seq = [
  pos(0, "start", [
    [0, 0],
    [0, 10],
  ]),
  pos(100, "middle", [
    [10, 0],
    [10, 10],
  ]),
  pos(200, "end", [
    [30, 0],
    [30, 10],
  ]),
];

describe("frontAt", () => {
  it("has nothing to say without positions", () => {
    expect(frontAt([], 50)).toBeNull();
  });

  it("returns a dated position exactly at its own date", () => {
    expect(frontAt(seq, 100)!.coordinates).toEqual([
      [10, 0],
      [10, 10],
    ]);
  });

  it("interpolates vertex by vertex between the bracketing dates", () => {
    expect(frontAt(seq, 50)!.coordinates).toEqual([
      [5, 0],
      [5, 10],
    ]);
    // Second interval moves twice as far in the same time.
    expect(frontAt(seq, 150)!.coordinates).toEqual([
      [20, 0],
      [20, 10],
    ]);
  });

  it("holds the nearest end outside the curated range instead of extrapolating", () => {
    const before = frontAt(seq, -5000)!;
    expect(before.coordinates).toEqual([
      [0, 0],
      [0, 10],
    ]);
    expect(before.held).toBe(true);
    const after = frontAt(seq, 5000)!;
    expect(after.coordinates[0]).toEqual([30, 0]);
    expect(after.held).toBe(true);
  });

  it("labels a sample with the dated position it is nearest to", () => {
    expect(frontAt(seq, 10)!.label).toBe("start");
    expect(frontAt(seq, 90)!.label).toBe("middle");
    expect(frontAt(seq, 150)!.label).toBe("end");
    expect(frontAt(seq, 50)!.held).toBe(false);
  });

  it("handles a two-position sequence, the minimum the baker allows", () => {
    expect(frontAt(seq.slice(0, 2), 25)!.coordinates[0]).toEqual([2.5, 0]);
  });
});

describe("frontBounds", () => {
  it("covers every dated position, not just the current one", () => {
    expect(frontBounds(seq)).toEqual([
      [0, 0],
      [30, 10],
    ]);
  });

  it("is null when there is nothing to frame", () => {
    expect(frontBounds([])).toBeNull();
  });
});
