# Known issues

- **Artifact count grows ~27x entity count at fine buckets.** The 10.5k-entity
  warm bake produces ~280k chunk objects (window duplication x categories,
  API-1), taking ~10 min against local MinIO even with a 48-way upload pool.
  Fine for incremental re-bakes (unchanged objects skip), but revisit
  per-bucket caps / coarse-bucket rollups before dump-scale (M5 full).

- **Deep-time zoom can feel empty (T3-T5).** Zooming into e.g. 2.8 Gyr ago
  shows "0 shown": billion-year-precision entities (Sun, Earth, eras) are
  capped at T2 by the precision rule (DM-4), and nothing else exists there in
  the seed. Correct per spec, but the view lacks context bands. Fix belongs in
  the ZOOM-4 "overlap factor / context floor" work at dump scale (M5), or by
  giving era-type entities a dedicated background-band rendering.
- **Timeline draws only via requestAnimationFrame.** In visibility-throttled
  tabs (headless automation, background tabs) draws defer until the tab is
  visible again. Fine for real users; e2e tests must trigger a resize or wait
  for visibility before reading `window.__wkhits`.
- **maplibre demotiles basemap is a remote dev dependency.** Replaced by a
  local pmtiles basemap in M4 (FE-3).
- **Phone/stacked layout (FE-1) not implemented yet.** Desktop/tablet only.
- **Timeline keyboard navigation (FE-9) is only half done.** Arrow keys move
  the time cursor; FE-9 also asks for arrow-to-pan and +/- to zoom, which have
  no binding. Whatever lands for those has to share the arrow keys with the
  cursor, probably by making pan the modified chord.
- **The borders layer is GeoJSON, not PMTiles.** `layers/borders/<year>.json`
  matches the API-4 key shape and the manifest carries the time-steps
  (API-0), but the bodies are plain FeatureCollections rather than the tile
  archives ARCH-3 specifies. M4 replaces the body format and the ingest
  source; the key scheme, the coverage-window index and the client's
  nearest-step snap and crossfade all carry over unchanged.
- **Curated era coverage is five windows wide, so most dates show nothing.**
  98-180, 1260-1300, 1900-1918, 1939-1945 and 1946-1991 have extents; every
  other date honestly reports "no border data". Modern borders are absent on
  purpose: hand-tracing them would be neither feasible nor honest, and the
  real answer is the OpenHistoricalMap ingest in M4.
- **Era extents are coarse blocks, and merge distinct polities.** The 1914
  British and French empires are drawn as a few contiguous blocks, so enclaves
  and rival holdings inside a block are not distinguished, and the 1942 Axis
  and Japanese outlines include the sea inside their envelope. Each file says
  so in its `source` string, and everything is marked
  `representation: "estimated"` so it renders dashed rather than as a hard
  border. Do not read areas off these shapes.
- **Front-line interpolation needs matching vertex counts.** Positions in one
  sequence must share a vertex count, because the client interpolates vertex
  by vertex; the baker rejects a sequence that does not. This makes a front
  that genuinely gains or loses a segment awkward to curate. Only the Eastern
  Front is curated so far, at ten sample points per date, with the northern
  vertices simplified after Finland's 1944 armistice.
