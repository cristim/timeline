// SRCH-1 client half: fold the query like the baker folds names, fetch the
// matching prefix shards, filter and rank locally. No search server.
import { useEffect, useRef, useState } from "react";
import { fetchSearchShard, type SearchEntry } from "../lib/data";
import type { Manifest } from "../lib/manifest";

interface Props {
  manifest: Manifest;
  onPick: (entry: SearchEntry) => void;
}

function fold(s: string): string[] {
  return s
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .split(/[^a-z0-9]+/)
    .filter((t) => t.length > 0);
}

export function SearchBox({ manifest, onPick }: Props) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<SearchEntry[]>([]);
  const [open, setOpen] = useState(false);
  const [cursor, setCursor] = useState(0);
  const boxRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const tokens = fold(q).filter((t) => t.length >= 2);
    if (!tokens.length) {
      setResults([]);
      return;
    }
    let live = true;
    const shards = [...new Set(tokens.map((t) => t[0]))].filter((s) =>
      manifest.search_shards.includes(s),
    );
    Promise.all(shards.map((s) => fetchSearchShard(manifest, s)))
      .then((shardData) => {
        if (!live) return;
        const bySlug = new Map<string, SearchEntry>();
        for (const sd of shardData) {
          for (const e of sd.entries) {
            bySlug.set(e.slug, e);
          }
        }
        const matches = [...bySlug.values()]
          .filter((e) => {
            const nameTokens = fold(e.name);
            return tokens.every((t) => nameTokens.some((nt) => nt.startsWith(t)));
          })
          .sort((a, b) => b.importance - a.importance)
          .slice(0, 8);
        setResults(matches);
        setCursor(0);
      })
      .catch((err: unknown) => console.error("search failed:", err));
    return () => {
      live = false;
    };
  }, [q, manifest]);

  useEffect(() => {
    const onDocClick = (e: MouseEvent) => {
      if (!boxRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, []);

  const pick = (e: SearchEntry) => {
    setOpen(false);
    setQ("");
    onPick(e);
  };

  return (
    <div className="searchbox" ref={boxRef}>
      <input
        value={q}
        placeholder="Search anything — “stalingrad”, “rex”, “transistor”…"
        onChange={(e) => {
          setQ(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") setCursor((c) => Math.min(c + 1, results.length - 1));
          else if (e.key === "ArrowUp") setCursor((c) => Math.max(c - 1, 0));
          else if (e.key === "Enter" && results[cursor]) pick(results[cursor]);
          else if (e.key === "Escape") setOpen(false);
        }}
      />
      {open && results.length > 0 && (
        <ul className="search-results">
          {results.map((r, i) => (
            <li key={r.slug} className={i === cursor ? "active" : ""} onMouseDown={() => pick(r)}>
              <span className="r-name">{r.name}</span>
              <span className="r-type">{r.type.replace(/_/g, " ")}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
