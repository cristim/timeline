// Category palette (mockup.html is the visual target) + temporal-status
// rendering rules (FE-7): Waterloo and the heat death of the universe must
// never look like the same kind of fact.
export const categoryColor: Record<string, string> = {
  universe: "#8a7fd4",
  earth: "#5da3a0",
  life: "#6fae62",
  war: "#c96b4a",
  politics: "#7d94c9",
  science: "#63b8a0",
  technology: "#d9a45a",
  culture: "#b58ac9",
  exploration: "#c9b458",
  economy: "#a0a46b",
  religion: "#c98a9a",
  disaster: "#d96a5a",
  future: "#6fd4d0",
};

export const FALLBACK_COLOR = "#9aa3b5";

export function colorFor(categories: string[]): string {
  for (const c of categories) {
    const col = categoryColor[c];
    if (col) return col;
  }
  return FALLBACK_COLOR;
}

/**
 * A stable colour for one historical polity, keyed by its name.
 *
 * Neighbouring polities have to be told apart, and there is nothing to colour
 * by: historical-basemaps carries a name and a representation, no region, no
 * culture, no successor chain. So the name picks the hue. Saturation and
 * lightness are fixed to the band the category palette above occupies, which
 * is what keeps a hashed hue looking like part of this design rather than a
 * random-colour map.
 *
 * FNV-1a, for the usual reason: short names differing in one letter land far
 * apart, so France and Francia do not come out the same colour.
 */
export function polityColor(name: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return `hsl(${(h >>> 0) % 360}, 34%, 45%)`;
}

export type MarkerStyle = "solid" | "hollow" | "fuzzy";

export function markerStyle(status: string): MarkerStyle {
  switch (status) {
    case "observed":
    case "documented":
      return "solid";
    case "estimated":
    case "reconstructed":
    case "legendary":
    case "disputed":
      return "hollow";
    default: // projected | model_dependent | speculative
      return "fuzzy";
  }
}
