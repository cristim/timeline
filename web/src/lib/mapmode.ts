export type MapMode =
  /** Reconstructed coastlines: deep time. */
  | "paleo"
  /** Historical polities over a neutral land base: recorded history. */
  | "political"
  /** The plain modern basemap, past everything the atlas covers. */
  | "modern"
  /** No reconstruction exists for this moment. Sphere, no geography. */
  | "void";

/** Conventional age of Earth's formation. */
export const EARTH_FORMED_YEAR = -4.54e9;

export interface MapModeInput {
  /** The cursor as a calendar year (keyscheme.secondsToYear). */
  year: number;
  hasPaleo: boolean;
  hasEra: boolean;
  /** Last year the political layer speaks for; null until its index loads. */
  eraTo: number | null;
}

export function mapMode({ year, hasPaleo, hasEra, eraTo }: MapModeInput): MapMode {
  if (hasPaleo) return "paleo";
  if (hasEra) return "political";
  // Drawing today's countries is a claim about the world, so make it only
  // where it is the best available answer: past the end of the atlas, in the
  // future. Everything else - including "the layer index has not loaded yet" -
  // gets the void globe. A momentary blank is honest; a momentary modern Earth
  // at 2 Gyr ago is not.
  if (eraTo !== null && year > eraTo) return "modern";
  return "void";
}

/** Specific void-state copy, or null while coverage is unknown. */
export function voidChipLabel(year: number, paleoFrom: number | null): string | null {
  if (year < EARTH_FORMED_YEAR) {
    return "no map: Earth does not exist yet (it forms ≈ 4.54 Ga)";
  }
  if (paleoFrom !== null && year < paleoFrom) {
    return `no map: no reconstruction earlier than ${Math.round(-paleoFrom / 1e6)} Ma`;
  }
  return null;
}
