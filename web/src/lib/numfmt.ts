/** Compact labels for claim values, which span grams to light-years. */
export function fmtNum(v: number): string {
  return fmtScaled(v, scaleFor([v]));
}

/** A divisor and suffix shared by a group of values. */
export interface Scale {
  div: number;
  suffix: string;
}

/**
 * One scale for a whole group of numbers, chosen from the largest of them.
 * Picking per number would print "8000 - 10.2k kg" for a single range, and put
 * three different scales in the one table row that exists to be compared.
 */
export function scaleFor(values: number[]): Scale {
  const max = Math.max(...values.map((v) => Math.abs(v)), 0);
  if (max >= 1e9) return { div: 1e9, suffix: "B" };
  if (max >= 1e6) return { div: 1e6, suffix: "M" };
  if (max >= 1e4) return { div: 1e3, suffix: "k" };
  return { div: 1, suffix: "" };
}

export function fmtScaled(v: number, scale: Scale): string {
  return `${round2(v / scale.div)}${scale.suffix}`;
}

function round2(v: number): number {
  return Math.round(v * 100) / 100;
}
