# Timeline - project rules

Read `specs/index.html` (spec pack) before non-trivial work; requirement IDs
(DM-*, API-*, ZOOM-*, FE-*, DEV-*) are the contract. `known-issues.md` lists
found-but-deferred items - check it before starting, prune what you resolve.

## Prod-parity local run (the only supported way to verify)

- `make up` - MinIO (S3 stand-in) + Caddy gateway (CDN stand-in, real cache
  headers) + web dev server at http://localhost:8080
- `make bake` (seed) / `make bake-full` (seed + Wikidata warm set)
- The dev server alone proves nothing: verify through the gateway or the
  built artifact.

## Verification (invoke the `playwright-verify` skill after any web change)

- `make e2e` - Playwright against the local gateway
- `make e2e-static` - same suite against the built static site exactly as
  GitHub Pages serves it (subpath base, baked seed artifacts)
- `make test` / `make smoke` - Go + data tests (golden views gate the bake),
  end-to-end serving check
- The Pages workflow browser-verifies the artifact before deploying; a red
  e2e blocks the deploy.

## Gotchas

- Artifacts are immutable; the manifest is the only mutable object. Never
  hand-edit anything under wk-artifacts - re-bake instead.
- Baker (Go) and client (TS) share the chunk-key scheme; both are pinned by
  `web/src/lib/keycases.json`. Changing bucket/window math requires
  regenerating that fixture and keeping both sides green.
- Seed edits: run `./scripts/seed-manifest.sh`, then re-bake; golden views
  (`data/goldens.json`) are pinned to the seed version and will demand review.
- Map geometry lives in `data/geo/` (see its README), deliberately outside
  `data/seed/` and so outside `seed_version` - editing it does not force a
  golden review. It has its own fail-loud validation instead: entity
  references resolve against the seed, coverage windows must TILE (a gap is
  fatal, and is how a partial fetch is caught), rings must be closed and wound
  per RFC 7946, and front positions must share a vertex count. Re-bake after
  editing.
- `data/geo/borders/`, `data/geo/paleo/`, and `data/geo/basemap/` are **fetched, not committed**:
  run `make fetch-geo` once after cloning or the bake fails loudly. CI caches
  them on `baker geo-fingerprint`. Read each directory's committed `NOTICE.md`
  before moving, rendering, or relicensing its data.
- The client draws the slice whose coverage window CONTAINS the cursor, not
  the nearest slice. Slice spacing is uneven enough that those differ across
  most of prehistory.
- Dev-only browser hooks for e2e: `window.__wkmap`, `window.__wkhits`,
  `window.__wkera`, `window.__wkpaleo`, `window.__wkfront`, `window.__wkmode`
  (stripped from prod builds - the suite runs against the static artifact too, so a test that
  needs one of these will pass locally and fail in CI; assert on the DOM or on
  the requests instead).
