import { describe, expect, it } from "vitest";
import { fmtNum, fmtScaled, scaleFor } from "./numfmt";

describe("value formatting", () => {
  it("keeps a group of values on one scale", () => {
    const scale = scaleFor([8000, 10200, 3500, 5300]);
    expect(fmtScaled(8000, scale)).toBe("8k");
    expect(fmtScaled(10200, scale)).toBe("10.2k");
    expect(fmtScaled(3500, scale)).toBe("3.5k");
  });

  it("picks the scale from the largest absolute value", () => {
    expect(scaleFor([-2, 5e9]).suffix).toBe("B");
    expect(scaleFor([2, -5e6]).suffix).toBe("M");
    expect(scaleFor([2, 5]).suffix).toBe("");
    expect(scaleFor([]).suffix).toBe("");
  });

  it("preserves the inspector's compact value format", () => {
    expect(fmtNum(9.5)).toBe("9.5");
    expect(fmtNum(10200)).toBe("10.2k");
    expect(fmtNum(2.4e6)).toBe("2.4M");
    expect(fmtNum(1.5e9)).toBe("1.5B");
  });
});
