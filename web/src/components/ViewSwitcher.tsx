import type { ViewMode } from "../lib/state";

interface Props {
  active: ViewMode;
  onSwitch: (mode: ViewMode) => void;
}

const enabled: ReadonlyArray<{ label: string; mode: ViewMode }> = [
  { label: "Map", mode: "map" },
  { label: "Compare", mode: "compare" },
];
const unavailable = ["Graph", "Charts", "Scale"];

export function ViewSwitcher({ active, onSwitch }: Props) {
  return (
    <nav className="view-switcher" aria-label="View">
      {enabled.map(({ label, mode }) => (
        <button
          key={mode}
          className={active === mode ? "active" : ""}
          aria-pressed={active === mode}
          onClick={() => onSwitch(mode)}
        >
          {label}
        </button>
      ))}
      {unavailable.map((label) => (
        <button key={label} disabled aria-label={`${label} unavailable`}>
          {label}<small>Unavailable</small>
        </button>
      ))}
    </nav>
  );
}
