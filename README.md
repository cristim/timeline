# Timeline

A zoomable timeline and map of everything: from the Big Bang, through natural
history and every era of human history, to the present and on to the projected
far future - in one continuous interface. Zoom the map past the globe and keep
going: solar system, Milky Way, observable universe.

**Live demo:** https://cristim.github.io/timeline/

There is no database and no API server. A batch "baker" turns curated and open
data into static artifacts (JSON viewport chunks, entity documents, search
shards, PMTiles map layers, and one manifest); the client computes every
artifact URL itself and a CDN serves the files. Facts are stored as *claims*:
sourced statements with uncertainty ranges that coexist rather than overwrite
each other, so
"T. rex mass: 8.0-10.2 t across 2 estimates" is the honest answer the UI shows.

## Run it locally

Prerequisites: Docker, Go 1.26+, Node 22+. Native `baker bake` and `make bake`
also require Tippecanoe 2.79.0 on `PATH`; Docker-based bakes use the pinned,
checksum-verified compiler in the baker image.

```sh
make up        # MinIO (S3 stand-in) + Caddy gateway (CDN stand-in) + web dev server
make fetch-geo # fetch the pinned borders, paleo coastlines, and local basemap
make bake      # bake the seed dataset (142 curated entities) and publish it
open http://localhost:8080
```

The gateway serves the exact URL contract and cache headers production uses,
so local behavior is production behavior. Useful targets:

```sh
make test            # Go unit + data tests (golden views gate the bake)
make smoke           # end-to-end: bake, headers, serving, web app
make fetch-geo       # fetch the pinned borders, paleo coastlines, and local basemap
make verify-geo      # verify layer coverage plus every fetched digest
make fetch-wikidata  # pull ~10k battles/wars/sieges from Wikidata (CC0)
make bake-full       # bake seed + the fetched Wikidata events
make census          # deterministic century/type census over seed + warm events
```

## How it works

```
data/seed/*.ndjson  ──┐
Wikidata (SPARQL)   ──┤   baker (Go)                          client (React+TS)
                      ├─► validate → rank → bucketize ─► S3:  manifest.json
                      │   golden views must pass  ─────►      /v/<ds>/chunks/…
                      └─► publish = atomic manifest flip      /v/<ds>/entity/…
                                                              /v/<ds>/search/…
                                                              /v/<ds>/layers/…/*.pmtiles
                                                              /v/<ds>/basemap/…pmtiles
```

- **Semantic zoom**: time is divided into buckets T0 (whole universe) through
  T13 (hours). Importance scores decide which entities appear at which bucket;
  the timeline shows the top 100 items that start or end in the visible range.
- **The serving manifest is the only mutable public artifact.** Everything
  under `/v/<dataset>/` is immutable and cacheable forever; a release is a
  manifest repoint and so is a rollback.
- **Golden views**: checked-in expectations ("the universe view must contain
  the Big Bang and the Cretaceous extinction") are evaluated inside every bake
  and a failure blocks publishing.
- **Warm import diagnostics**: object-store bakes write deterministic ingest
  diagnostics under `imports/<dataset>/<id>/`, with immutable
  `report.json` and `reject.parquet` addressed by content. The report records
  seed and warm source digests, parsed/accepted/rejected counts, skipped warm
  duplicates, and reject reason groups. The mutable latest pointer
  `imports/<dataset>/manifest.json` is written last and is the only import
  diagnostics object that carries `generated_at`.
- **Accepted-entity census**: `baker census` groups the validated seed and
  normalized warm entities into sparse century and type rows, including date,
  coordinate, English Wikipedia, combined-coverage, and precision counts. The
  immutable report is written to `reports/census/<sha256>/report.json`; the
  latest pointer at `reports/census/manifest.json` is written last and is the
  only census object containing `generated_at`. The report identity depends
  only on its exact deterministic JSON bytes.
- Deploying is `bake --out` plus a static web build - GitHub Pages runs the
  whole product through the pinned baker image (see
  `.github/workflows/pages.yml`). Static `bake --out` exercises the same
  private import-diagnostics generation path, but it does not publish warm
  model or import artifacts into the site output.

## Data

Seed entities are hand-curated NDJSON under `data/seed/` with per-claim
sources. Bulk events come from [Wikidata](https://www.wikidata.org/) (CC0);
raw fetch responses are archived for provenance and curated seed entries win
over bulk imports on conflict. The baker publishes a pinned local Protomaps
PMTiles archive with its complete attribution in the manifest. The current
browser still uses MapLibre demotiles until the M4b-2b style switch.

By default, `make census` requires the normalized
`BUCKET_WARM/wikidata/events.ndjson` object and fails if it cannot be read. Use
`go run ./cmd/baker census --seed-only` for an explicit seed-only report, or
`go run ./cmd/baker census --warm-file <path>` to use a local normalized warm
artifact. The report describes accepted normalized entities after source
filters. The current bulk feed contains only battles, wars, and sieges, so it
is a deterministic precursor to ROAD-2 rather than the complete dump-scale
census.

Map geometry comes from four pinned upstreams. Borders, paleo coastlines, and
the Protomaps extract are fetched by `make fetch-geo` and cached in CI; the
small OHM response is committed so ingest and licence checks never need its
live API:

| layer | source | licence |
|---|---|---|
| Political borders, 123000 BC to AD 2010 | [aourednik/historical-basemaps](https://github.com/aourednik/historical-basemaps) at `62d8f1a` | **GPL-3.0** |
| Reconstructed coastlines, 540 Ma to 1 Ma | [GPlates Web Service](https://gws.gplates.org), MERDITH2021 plate model | CC-BY 4.0 |
| London administrative boundaries, 1900 and 1965 | [OpenHistoricalMap](https://www.openhistoricalmap.org/) pinned Overpass response | CC0/public domain unless relation-tagged otherwise |
| Global basemap, zooms 0-6 | [Protomaps daily build](https://build.protomaps.com/20260829.pmtiles), extracted with go-pmtiles v1.30.0 | ODbL Produced Work; includes ESA WorldCover CC-BY 4.0 landcover |

**The border data is GPL-3.0**, and the simplified copies this project builds
are a modified version of it, so they carry the same terms — including for
anyone redistributing a built site. `data/geo/borders/NOTICE.md` records the
pin and every modification; `data/geo/borders/LICENSE` is the licence itself.

The plate model is CC-BY 4.0 and asks to be cited: Merdith et al. (2021),
*Extending full-plate tectonic models into deep time*, Earth-Science Reviews
214, [doi:10.1016/j.earscirev.2020.103477](https://doi.org/10.1016/j.earscirev.2020.103477).
All three historical/reconstruction sources are named in the map's attribution
control. The local basemap descriptor carries the required Protomaps,
OpenStreetMap, and ESA WorldCover notices for the M4b-2b renderer.

The OHM response is committed as raw Overpass OSM JSON with an exact manifest
digest and relation-version pins. Ingest resolves it to validated, sorted
`BorderLayer` snapshots, preserves per-relation provenance on their features,
and adds aggregate licence counts plus explicit exceptions to import-report
schema 2. The baker composites each snapshot above the overlapping political
border windows, introducing a 1965 PMTiles time step where the London source
changes. Hover tooltips expose the OHM source and versioned relation ID.
Refresh steps and the raw-to-processed format contract are in
`data/geo/README.md` and `data/geo/ohm/NOTICE.md`.

The fetched `.geojson` snapshots are source inputs. The baker compiles each
time step into a deterministic PMTiles v3 archive with vector source-layer
`areas`, zooms 0 through 6, per-slice provenance/time metadata, and political
feature colors. The browser keeps only the small JSON layer index in memory
and reads the covering archive with HTTP byte-range requests. S3/MinIO objects
use `application/vnd.pmtiles`; static hosts may choose their own MIME but must
support byte ranges.

## Where things live

| Path | What |
|---|---|
| `specs/` | the full spec pack (architecture, data model, read contract, roadmap) |
| `cmd/baker`, `internal/` | the baker: ingest, rank, bake, publish |
| `web/` | the app: canvas timeline, MapLibre globe, cosmic zoom, inspector |
| `data/seed/` | curated seed dataset + manifest |
| `known-issues.md` | found-but-deferred items |

Status: working prototype (spec milestones M0-M3, the PMTiles delivery half of
M4, pinned OpenHistoricalMap ingest/rendering, the published local Protomaps
archive, plus a bounded M5). Next: switch MapLibre to that local basemap,
dump-scale ingestion, and the fun-test gate.
See `specs/10-roadmap.html` and `specs/11-local-dev.html`.
