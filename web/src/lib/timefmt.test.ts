import { describe, expect, it } from "vitest";
import { formatDuration } from "./timefmt";
import { SECONDS_PER_YEAR } from "./keyscheme";

describe("formatDuration", () => {
  it("scales from minutes to deep time", () => {
    expect(formatDuration(90)).toBe("2 minutes");
    expect(formatDuration(2 * 3600)).toBe("2 hours");
    expect(formatDuration(3 * 86_400)).toBe("3 days");
    expect(formatDuration(6 * SECONDS_PER_YEAR)).toBe("6 years");
    expect(formatDuration(12_000 * SECONDS_PER_YEAR)).toBe("12 kyr");
    expect(formatDuration(66e6 * SECONDS_PER_YEAR)).toBe("66 Myr");
    expect(formatDuration(13.8e9 * SECONDS_PER_YEAR)).toBe("13.8 Gyr");
  });

  it("is a length, not a direction: a negative gap reads the same", () => {
    expect(formatDuration(-6 * SECONDS_PER_YEAR)).toBe("6 years");
  });

  it("does not report a real overlap as none", () => {
    expect(formatDuration(20)).toBe("under a minute");
    expect(formatDuration(0)).toBe("under a minute");
  });

  it("keeps the singular singular", () => {
    expect(formatDuration(SECONDS_PER_YEAR)).toBe("1 year");
    expect(formatDuration(86_400)).toBe("1 day");
  });
});
