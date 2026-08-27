# Map geometry

GeoJSON the baker turns into serving artifacts. Unlike `data/seed/`, this
directory is **not** covered by `seed_version`, so editing it does not force a
golden-view review (`data/goldens.json` is pinned to the seed). It has its own
validation instead: `internal/ingest/geo.go` rejects the bake outright on a
malformed file, an unknown entity reference, coverage windows that do not tile,
or a front sequence whose positions do not share a vertex count.

Two of the three layers are **fetched, not committed**:

| directory | origin | in git? |
|---|---|---|
| `borders/` | `baker fetch-borders` from historical-basemaps (GPL-3.0) | no, ~16 MB |
| `paleo/` | `baker fetch-paleo` from the GPlates Web Service (CC-BY 4.0) | no, ~6.6 MB |
| `fronts/` | hand-curated | yes |

Run `make fetch-geo` once after cloning; `make verify-geo` proves both fetched
layers are whole. CI caches them on `baker geo-fingerprint`, a hash of the
pinned upstream commit, plate model and slice list, so an unchanged pin never
touches the network. Each fetched directory keeps a committed `NOTICE.md`
recording its provenance, licence and the modifications the fetch applies.

## `borders/<year>.geojson` and `paleo/<year>.geojson` — world-state snapshots

Both use the format below, and the baker treats them as two instances of one
layer kind. They cover disjoint spans that meet exactly at 123000 BC: political
borders from there to AD 2035, reconstructed coastlines from 540 Ma back to it.
A paleo slice's year is its reconstruction time in years, so 250 Ma is
`-250000000.geojson`; nothing about bucket or window maths changes for it.

One GeoJSON `FeatureCollection` per time-step, baked to
`/v/<dataset>/layers/<layer>/<year>.json` (the API-4 key shape) and listed in
the manifest under `layers` / `timesteps` (API-0). The client renders the
covering slice as a fill + outline that crossfades when the cursor crosses a
slice boundary (FE-3, FE-4). Deep time additionally paints an opaque ocean
under its landmasses, because there the modern basemap is not background, it
is wrong.

Top-level `properties`:

| field | meaning |
|---|---|
| `year` | the time-step, an integer year; must match the file name |
| `t_from`, `t_to` | the era's coverage window, in years |
| `label` | what the map chip shows |
| `source` | where the outlines were traced from |

`t_from`/`t_to` decide which slice the client draws. Windows must **tile**:
each slice runs to the year before the next, so every moment between the first
and last slice belongs to exactly one of them and none is unreachable. A gap
would blank the map mid-scrub, and is also how a half-finished fetch shows up
- which is why the loader treats one as fatal rather than as silence.

The client picks the slice whose window *covers* the cursor, not the nearest
one. With slices this unevenly spaced - 113,000 years between the first two,
six between two of the last - the nearest slice year is routinely one whose
window ended long before.

Per-feature `properties`: `name`, `representation` (the DM-7 vocabulary), and
an optional `entity` naming a **seed id** — the baker resolves it to the
entity's slug so clicking the shape opens that entity. An unknown seed id
fails the bake. The fetched layers set no `entity`: nothing reconciles an
upstream polity name against the seed, so their shapes are not clickable.

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

Everything here is approximate and marked `representation: "estimated"`
(DM-7). The fetched layers are simplified copies of upstream approximations
(see each `NOTICE.md` for what the simplification does and what it costs); the
front lines are traced by eye from published atlases at 10-50 vertices. All of
it is good enough to show *that* territory changed and roughly where. None of
it is survey data, and none of it carries a claim about disputed frontiers.
Baking the area layers to PMTiles rather than GeoJSON is still milestone M4
(DEV-6); the key scheme and coverage index already match what that needs.

There is a line between a simplification and an error, and it is worth naming.
A frontier drawn 200km off is a simplification. An empire that excludes its
own capital province, a union that has lost a republic, or a neutral state
shaded as a belligerent is an error, and no "approximate" disclaimer covers
it. `internal/ingest/geo_plausibility_test.go` holds each outline against
places whose status on the stated date is not in dispute, precisely because
structural validation cannot tell the two apart: a shape can be perfectly
well-formed GeoJSON and still be the wrong empire. Add cases there when you
add or edit an outline.
