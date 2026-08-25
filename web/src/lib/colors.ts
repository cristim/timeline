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
