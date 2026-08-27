# Historical world borders — provenance and licence

**The `<year>.geojson` slices this directory holds at build time are a modified
version of a GPL-3.0 work and are themselves distributed under GPL-3.0.** The
full licence text is in `LICENSE` alongside this notice.

The slices are **not committed** — `.gitignore` excludes them and `make
fetch-geo` regenerates them from the pinned upstream. This notice and the
licence are committed, because they describe what the build produces and where
it came from. Anyone redistributing a built site is redistributing GPL-3.0 data
and inherits its terms.

## Upstream

| | |
|---|---|
| Work | [`aourednik/historical-basemaps`](https://github.com/aourednik/historical-basemaps) |
| Author | André Ourednik and contributors |
| Licence | GNU General Public License v3.0 |
| Pinned commit | `62d8f1a03a71f2d3ff17f2d166f7553f256bce68` (2026-01-26) |
| Files taken | `geojson/world_*.geojson` — 53 world polity snapshots, 123000 BC to AD 2010 |

`geojson/places.geojson` is a settlement point layer with a different schema
and is not used.

## Modifications (GPLv3 §5a)

Modified on 2026-08-27 by `baker fetch-borders`
(`internal/ingest/borders.go`), which is the corresponding source for this
transformation and is in this repository. Re-running it against the pinned
commit reproduces these files. What it changes:

- **Simplified** every polygon ring with Douglas-Peucker, 0.02° (~2 km) for all
  slices but `1492.geojson`, which upstream ships at 4 MB and which the
  per-slice budget re-simplifies more coarsely.
- **Quantized** coordinates to 4 decimal places (~11 m).
- **Dropped** rings enclosing less than one tolerance-square, and rings that
  still cross themselves at every tolerance tried.
- **Repaired** pinched rings by splitting at the repeated vertex and keeping
  the larger loop.
- **Rewound** rings to RFC 7946 (exterior counterclockwise, holes clockwise).
- **Reshaped** the container to this repo's curated-layer format: kept only the
  `NAME` property (as `name`), added `representation: "estimated"`, and added
  the top-level `year` / `t_from` / `t_to` / `label` / `source` that the baker
  and client read. `SUBJECTO` is used as the name only where `NAME` is empty;
  `ABBREVN`, `PARTOF` and `BORDERPRECISION` are dropped.

Nothing was added to the data: no border was moved, invented, or re-attributed.

## Accuracy

The upstream dataset is itself a set of approximations compiled from published
historical atlases, and this copy is a simplification of it. Frontiers are
indicative, not surveyed; every feature is marked `representation: "estimated"`
so the client draws it dashed. Do not read areas or exact boundaries off these
shapes.

Known upstream defects that survive into this copy are listed in the repository
root `known-issues.md`.
