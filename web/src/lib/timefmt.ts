// Human labels for timeline_time values across 100+ orders of magnitude.
import { SECONDS_PER_YEAR, secondsToYear } from "./keyscheme";

/** Formats a time (seconds since 1970) at a scale suited to `span` seconds. */
export function formatTime(t: number, span: number): string {
  const year = secondsToYear(t);
  const spanYears = span / SECONDS_PER_YEAR;

  if (Math.abs(year) >= 1e6) {
    const [div, unit] = Math.abs(year) >= 1e9 ? [1e9, "Gyr"] : [1e6, "Myr"];
    const v = trim(Math.abs(year) / div);
    return year < 0 ? `${v} ${unit} ago` : `in ${v} ${unit}`;
  }
  if (year < -10000) return `${trim(-year / 1000)} kyr ago`;
  if (year <= 0) return `${Math.round(-year) + 1} BCE`;

  if (spanYears > 3) return `${Math.floor(year)}`;
  const d = new Date(t * 1000);
  if (spanYears > 0.2) {
    return d.toLocaleDateString("en", { month: "short", year: "numeric" });
  }
  if (span > 3 * 86_400) {
    return d.toLocaleDateString("en", { day: "numeric", month: "short", year: "numeric" });
  }
  return d.toLocaleString("en", {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Label for an entity's time range, respecting its stated precision. */
export function formatRange(t0: number, t1: number, precision: string): string {
  const span = rangeDisplaySpan(precision);
  const a = formatTime(t0, span);
  if (t1 === t0) return a;
  const b = formatTime(t1, span);
  return a === b ? a : `${a} – ${b}`;
}

function rangeDisplaySpan(precision: string): number {
  switch (precision) {
    case "billion_year":
    case "million_year":
    case "millennium":
    case "century":
    case "decade":
    case "year":
      return 10 * SECONDS_PER_YEAR; // year-level labels
    case "month":
      return 0.5 * SECONDS_PER_YEAR;
    case "day":
      return 5 * 86_400;
    default:
      return 3_600;
  }
}

function trim(v: number): string {
  const r = Math.abs(v) >= 100 ? Math.round(v) : Math.round(v * 10) / 10;
  return `${r}`;
}

/** True if the entity is best treated as ongoing ("through present"). */
export function isOngoing(t1: number): boolean {
  return secondsToYear(t1) >= new Date().getFullYear() - 1;
}
