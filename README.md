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
  diagnostics under `reports/imports/<dataset>/<id>/`, with immutable
  `report.json` and `reject.parquet` addressed by content. The report records
  seed and warm source digests, parsed/accepted/rejected counts, skipped warm
  duplicates, and reject reason groups. The mutable latest pointer
  `reports/imports/<dataset>/manifest.json` is written last and is the only
  import diagnostics object that carries `generated_at`. Every per-run report
  lives under `wk-warm/reports/` (DEV-5).
- **Accepted-entity census**: `baker census` groups the validated seed and
  normalized warm entities into sparse time-slice and type rows, including date,
  coordinate, English Wikipedia, combined-coverage, and precision counts. The
  immutable report is written to `reports/census/<sha256>/report.json`; the
  latest pointer at `reports/census/manifest.json` is written last and is the
  only census object containing `generated_at`. The report identity depends
  only on its exact deterministic JSON bytes.
- **Wikidata dump census (ROAD-2)**: `baker census --wikidata-dump <path|->`
  streams a dump and writes the deterministic census to stdout, optionally
  publishing it with `--publish`. See "Wikidata census and bulk ingest" below.
- **Bulk ingest and bake (ROAD-3 step 2)**: `baker ingest-wikidata-dump` turns a
  dump into the normalized Parquet model plus a reject table and import report;
  `baker bake --model <dir>` then bakes the HOT tier from that model instead of
  the seed.
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
PMTiles archive with its complete attribution in the manifest. The browser
range-loads that archive through the same static or gateway artifact path as
the time layers; its basemap style makes no external map or style requests.

By default, `make census` requires the normalized
`BUCKET_WARM/wikidata/events.ndjson` object and fails if it cannot be read. Use
`go run ./cmd/baker census --seed-only` for an explicit seed-only report, or
`go run ./cmd/baker census --warm-file <path>` to use a local normalized warm
artifact. The report describes accepted normalized entities after source
filters.

## Wikidata census and bulk ingest

The dump is read natively: `.json`, `.json.gz` and `.json.bz2` are detected by
magic bytes, decompressed with the standard library, and streamed, so neither
command needs an external tool and neither holds the dump in memory.

```sh
# ROAD-2 census: what is actually in the dump.
go run ./cmd/baker census --wikidata-dump /path/to/latest-all.json.bz2
go run ./cmd/baker census --wikidata-dump - --publish < latest-all.json.bz2

# ROAD-3 step 2: the same dump as the normalized Parquet model.
go run ./cmd/baker ingest-wikidata-dump \
  --dump /path/to/latest-all.json.bz2 --out ./model --publish

# Bake the HOT tier from that model (SRC-5).
go run ./cmd/baker bake --model ./model --importance-floor 0.38 --out ./site
```

### What the census reports

Per time slice (a century through recorded history, then ten-thousand-,
million- and billion-year slices in deep time) and in total:

- **per class and per type.** ROAD-2 asks how many battles, wars, political
  events, disasters, scientific events, people, species and products exist,
  which is a question in Wikidata's vocabulary; a battle and a war are both
  `event` in ours. Class rows answer the question as asked, type rows answer it
  in the model's terms.
- **coverage** of dates, coordinates, English Wikipedia and any sitelink, as
  counts and as ratios.
- **time precision** in the model vocabulary, and a split by **calendar model
  and era**, because Julian and Gregorian values are not interchangeable.
- **excluded** items (Wikimedia housekeeping classes) and **unclassified** ones,
  the latter ranked by class so the curated table grows from evidence rather
  than guesswork. Nothing is dropped silently.
- **skipped claims per reason**, so a dump-format change that starts discarding
  claims shows up as a number instead of as a suspiciously small dataset.

The report is deterministic: the same dump and the same code produce
byte-identical JSON. `input_sha256` covers the input artifact exactly as
supplied, compressed or not, so a report names the dump file it read.

### Provisional HOT-tier targets

SRC-5 budgets the HOT tier at 1-5M entities. The promotion knob is
`--importance-floor`: the model in `wk-warm` holds every normalized entity, and
only those at or above the floor become baked artifacts. Importance for bulk
imports is derived from sitelink count, so the floor is a sitelink threshold:

| sitelinks | 0 | 1 | 2 | 5 | 10 | 20 | 50 | 100 |
|---|---|---|---|---|---|---|---|---|
| importance | 0.22 | 0.28 | 0.32 | 0.38 | 0.44 | 0.49 | 0.57 | 0.64 |

Provisional starting points, to be replaced by measured numbers once a full
dump has been censused (these are SRC-5's budget, not yet an observation):
events 500k-1M, people 500k, places/states/cities 200k, species 300k,
inventions/technologies 50k, books/media/games 500k, vehicles/products 100k,
papers 100k, patents 100k, other 500k. Start at a floor of **0.38** (five or
more sitelinks) and read `accepted_by_type` in the import report against the
budget above; the census's per-class counts and coverage ratios say how much
headroom each type has before the floor has to move.

### Bulk ingest gates

The importer separates two things that are usually conflated:

- **filtered** items are ones we deliberately do not want (a Wikimedia
  housekeeping class, an unlabelled item, a class the curated table does not
  cover, an item with no usable date). Each is counted by reason.
- **rejected** items are ones we wanted but could not normalize. The reject
  rate gates the run and a breach fails it, rather than shipping a quietly
  smaller dataset (ROAD-3 step 2: "reject rates within gates"). Override with
  `--max-reject-rate`.

Rejects are data: they land in `reject.parquet` beside the model with source,
line and reason. A repeated `wikidata_id` is fatal, because SRC-3 joins on it.
The import report records provenance for the run (source, URL, CC0 licence,
dump digest and container) and is content-addressed like the census.

The model directory is self-describing: `import.json` is the content address
the dataset version derives from, so the same dump and the same code always
name the same version and a different dump never reuses one. `bake --model`
reads it, so a bulk bake can always name the dump it came from.

Golden views still gate a model bake, checked against the model's own version.
A dump-derived dataset has no pinned expectations until someone writes them, so
`--goldens ""` turns the gate off explicitly rather than by accident.

Map geometry comes from four pinned upstreams. Borders, paleo coastlines, and
the Protomaps extract are fetched by `make fetch-geo` and cached in CI; the
small OHM response is committed so ingest and licence checks never need its
live API:

| layer | source | licence |
|---|---|---|
| Political borders, 123000 BC to AD 2010 | [aourednik/historical-basemaps](https://github.com/aourednik/historical-basemaps) at `62d8f1a` | **GPL-3.0** |
| Reconstructed coastlines, 540 Ma to 1 Ma | [GPlates Web Service](https://gws.gplates.org), MERDITH2021 plate model | CC-BY 4.0 |
| London administrative boundaries, 1900 and 1965 | [OpenHistoricalMap](https://www.openhistoricalmap.org/) pinned Overpass response | CC0/public domain unless relation-tagged otherwise |
| Global basemap, zooms 0-6 | [Protomaps daily build](https://build.protomaps.com/20260829.pmtiles), extracted with go-pmtiles v1.30.0 under Go 1.26.7 | ODbL Produced Work; includes ESA WorldCover CC-BY 4.0 landcover |

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

Status: working prototype (spec milestones M0-M4, pinned OpenHistoricalMap
ingest/rendering, the local Protomaps renderer, plus a bounded M5). Next:
dump-scale ingestion and the fun-test gate.
See `specs/10-roadmap.html` and `specs/11-local-dev.html`.
