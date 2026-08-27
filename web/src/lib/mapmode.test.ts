import { describe, expect, it } from "vitest";
import { EARTH_FORMED_YEAR, mapMode, voidChipLabel } from "./mapmode";

const PALEO_FROM = -540_000_000;

describe("mapMode", () => {
  it("prefers the paleo reconstruction where one exists", () => {
    expect(mapMode({ year: -250e6, hasPaleo: true, hasEra: false, eraTo: 2035 })).toBe("paleo");
  });
  it("draws politics through recorded history", () => {
    expect(mapMode({ year: 1500, hasPaleo: false, hasEra: true, eraTo: 2035 })).toBe("political");
  });
  it("falls back to the modern basemap only past the end of the atlas", () => {
    expect(mapMode({ year: 9000, hasPaleo: false, hasEra: false, eraTo: 2035 })).toBe("modern");
  });
  it("shows the void globe older than every reconstruction", () => {
    expect(mapMode({ year: -2e9, hasPaleo: false, hasEra: false, eraTo: 2035 })).toBe("void");
    expect(mapMode({ year: -600e6, hasPaleo: false, hasEra: false, eraTo: 2035 })).toBe("void");
  });
  it("never claims modern Earth while the layer index is still loading", () => {
    expect(mapMode({ year: 9000, hasPaleo: false, hasEra: false, eraTo: null })).toBe("void");
    expect(mapMode({ year: 1500, hasPaleo: false, hasEra: false, eraTo: null })).toBe("void");
  });
  it("keeps the last covered year political rather than modern", () => {
    expect(mapMode({ year: 2035, hasPaleo: false, hasEra: true, eraTo: 2035 })).toBe("political");
  });
});

describe("voidChipLabel", () => {
  it("says there is no planet yet before Earth formed", () => {
    expect(voidChipLabel(-5e9, PALEO_FROM)).toContain("Earth does not exist yet");
    expect(voidChipLabel(EARTH_FORMED_YEAR - 1, PALEO_FROM)).toContain("4.54 Ga");
  });
  it("names the oldest reconstruction between Earth's formation and it", () => {
    expect(voidChipLabel(-2e9, PALEO_FROM)).toBe(
      "no map: no reconstruction earlier than 540 Ma",
    );
  });
  it("reads the boundary off the layer rather than hardcoding it", () => {
    expect(voidChipLabel(-2e9, -1_000_000_000)).toContain("1000 Ma");
  });
  it("says nothing specific while the index is still loading", () => {
    expect(voidChipLabel(-2e9, null)).toBeNull();
  });
  it("says nothing specific inside the covered range", () => {
    expect(voidChipLabel(-100e6, PALEO_FROM)).toBeNull();
  });
});
