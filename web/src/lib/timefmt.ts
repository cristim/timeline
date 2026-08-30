// Human labels for timeline_time values across 100+ orders of magnitude.
import { SECONDS_PER_YEAR, secondsToYear } from "./keyscheme";

/** The present, in timeline seconds. Fixed at load: the NOW line must not
 *  drift between the timeline's frame and the compare view's verdict. */
export const NOW_S = Date.now() / 1000;

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

/**
 * Formats a length of time (seconds) - how long something lasted, or how much
 * empty time sits between two things. Unlike formatTime this is a duration, so
 * it never carries "ago" or "in".
 */
export function formatDuration(seconds: number): string {
  const s = Math.abs(seconds);
  const years = s / SECONDS_PER_YEAR;
  if (years >= 1e9) return `${trim(years / 1e9)} Gyr`;
  if (years >= 1e6) return `${trim(years / 1e6)} Myr`;
  if (years >= 1e4) return `${trim(years / 1000)} kyr`;
  if (years >= 1) return plural(Math.round(years), "year");
  if (s >= 86_400) return plural(Math.round(s / 86_400), "day");
  if (s >= 3600) return plural(Math.round(s / 3600), "hour");
  if (s >= 60) return plural(Math.round(s / 60), "minute");
  // Two same-day events can share seconds; "0 minutes" would read as none.
  return "under a minute";
}

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? "" : "s"}`;
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
