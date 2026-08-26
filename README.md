# Everything Timeline

A zoomable timeline and map of everything: from the Big Bang, through natural
history and every era of human history, to the present and on to the projected
far future - in one continuous interface. Zoom the map past the globe and keep
going: solar system, Milky Way, observable universe.

**Live demo:** https://cristim.github.io/everything-timeline/

There is no database and no API server. A batch "baker" turns curated and open
data into static JSON artifacts (viewport chunks, entity documents, search
shards, one manifest); the client computes every artifact URL itself and a CDN
serves the files. Facts are stored as *claims*: sourced statements with
uncertainty ranges that coexist rather than overwrite each other, so
"T. rex mass: 8.0-10.2 t across 2 estimates" is the honest answer the UI shows.

## Run it locally

Prerequisites: Docker, Go 1.26+, Node 22+.

```sh
make up      # MinIO (S3 stand-in) + Caddy gateway (CDN stand-in) + web dev server
make bake    # bake the seed dataset (142 curated entities) and publish it
open http://localhost:8080
```

The gateway serves the exact URL contract and cache headers production uses,
so local behavior is production behavior. Useful targets:

```sh
make test            # Go unit + data tests (golden views gate the bake)
make smoke           # end-to-end: bake, headers, serving, web app
make fetch-wikidata  # pull ~10k battles/wars/sieges from Wikidata (CC0)
make bake-full       # bake seed + the fetched Wikidata events
make census          # per-era coverage report over the merged dataset
```

## How it works

```
data/seed/*.ndjson  ──┐
Wikidata (SPARQL)   ──┤   baker (Go)                          client (React+TS)
                      ├─► validate → rank → bucketize ─► S3:  manifest.json
                      │   golden views must pass  ─────►      /v/<ds>/chunks/…
                      └─► publish = atomic manifest flip      /v/<ds>/entity/…
                                                              /v/<ds>/search/…
```

- **Semantic zoom**: time is divided into buckets T0 (whole universe) through
  T13 (hours). Importance scores decide which entities appear at which bucket;
  the timeline shows the top 100 items that start or end in the visible range.
- **The manifest is the only mutable object.** Everything under `/v/<dataset>/`
  is immutable and cacheable forever; a release is a manifest repoint and so
  is a rollback.
- **Golden views**: checked-in expectations ("the universe view must contain
  the Big Bang and the Cretaceous extinction") are evaluated inside every bake
  and a failure blocks publishing.
- Deploying is `bake --out` plus a static web build - GitHub Pages runs the
  whole product (see `.github/workflows/pages.yml`).

## Data

Seed entities are hand-curated NDJSON under `data/seed/` with per-claim
sources. Bulk events come from [Wikidata](https://www.wikidata.org/) (CC0);
raw fetch responses are archived for provenance and curated seed entries win
over bulk imports on conflict. The map basemap is © OpenStreetMap
contributors via MapLibre demotiles.

## Where things live

| Path | What |
|---|---|
| `specs/` | the full spec pack (architecture, data model, read contract, roadmap) |
| `cmd/baker`, `internal/` | the baker: ingest, rank, bake, publish |
| `web/` | the app: canvas timeline, MapLibre globe, cosmic zoom, inspector |
| `data/seed/` | curated seed dataset + manifest |
| `known-issues.md` | found-but-deferred items |

Status: working prototype (spec milestones M0-M3 plus a bounded M5). Next:
OpenHistoricalMap border tiles, dump-scale ingestion, the fun-test gate -
see `specs/10-roadmap.html` and `specs/11-local-dev.html`.
