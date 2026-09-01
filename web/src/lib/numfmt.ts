export interface Scale {
  div: number;
  suffix: string;
}

export function scaleFor(values: number[]): Scale {
  const max = Math.max(...values.map((value) => Math.abs(value)), 0);
  if (max >= 1e9) return { div: 1e9, suffix: "B" };
  if (max >= 1e6) return { div: 1e6, suffix: "M" };
  if (max >= 1e4) return { div: 1e3, suffix: "k" };
  return { div: 1, suffix: "" };
}

export function fmtScaled(value: number, scale: Scale): string {
  return `${round2(value / scale.div)}${scale.suffix}`;
}

export function fmtNum(value: number): string {
  return fmtScaled(value, scaleFor([value]));
}

function round2(value: number): number {
  return Math.round(value * 100) / 100;
}
