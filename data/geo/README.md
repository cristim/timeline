# Curated geometry

Hand-curated GeoJSON the baker turns into serving artifacts. Unlike
`data/seed/`, this directory is **not** covered by `seed_version`, so editing
it does not force a golden-view review (`data/goldens.json` is pinned to the
seed). It has its own validation instead: `internal/ingest/geo.go` rejects the
bake outright on a malformed file, an unknown entity reference, overlapping
era windows, or a front sequence whose positions do not share a vertex count.

## `borders/<year>.geojson` — world-state snapshots

One GeoJSON `FeatureCollection` per time-step, baked to
`/v/<dataset>/layers/borders/<year>.json` (the API-4 key shape) and listed in
the manifest under `layers` / `timesteps` (API-0). The client snaps the time
cursor to the nearest step (ARCH-3) and renders it as a fill + outline that
crossfades when the cursor crosses an era boundary (FE-3, FE-4).

Top-level `properties`:

| field | meaning |
|---|---|
| `year` | the time-step, an integer year; must match the file name |
| `t_from`, `t_to` | the era's coverage window, in years |
| `label` | what the map chip shows |
| `source` | where the outlines were traced from |

`t_from`/`t_to` exist so the client can say "no border data for 1750" instead
of silently drawing the nearest era at a date it never covered. Windows may
not overlap between files.

Per-feature `properties`: `name`, `representation` (the DM-7 vocabulary), and
an optional `entity` naming a **seed id** — the baker resolves it to the
entity's slug so clicking the shape opens that entity. An unknown seed id
fails the bake.

## `fronts/<name>.geojson` — dated front positions

A `LineString` per dated front position for one war, with top-level
`properties.entity` naming the seed id it belongs to. These bake into that
entity's document as DM-7 `geometry` records; the client interpolates between
the bracketing dates as the time cursor moves.

**Every feature in one file must have the same number of vertices, and vertex
*n* must mean the same place on the line at every date.** The client
interpolates vertex-by-vertex. The baker enforces the count; nothing can
enforce the meaning, so this is on the curator. Getting it wrong is not a
small error: an earlier draft of the Eastern Front folded the Finnish and
Arctic sectors into the same ten vertices, and interpolating across the date
they vanished dragged the front line through neutral Sweden.

## Accuracy

Everything here is a coarse approximation traced by eye from published
historical atlases, at roughly 10-50 vertices per shape. It is good enough to
show *that* territory changed and roughly where; it is not survey data, it
carries no claim about disputed or fuzzy frontiers, and it is marked
`representation: "estimated"` (DM-7) so the client draws it dashed rather than
as a hard border. Replacing it with a real OpenHistoricalMap extract baked to
PMTiles is milestone M4 (DEV-6).

There is a line between a simplification and an error, and it is worth naming.
A frontier drawn 200km off is a simplification. An empire that excludes its
own capital province, a union that has lost a republic, or a neutral state
shaded as a belligerent is an error, and no "approximate" disclaimer covers
it. `internal/ingest/geo_plausibility_test.go` holds each outline against
places whose status on the stated date is not in dispute, precisely because
structural validation cannot tell the two apart: a shape can be perfectly
well-formed GeoJSON and still be the wrong empire. Add cases there when you
add or edit an outline.
