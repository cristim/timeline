# Known issues

- **ROAD-2 and ROAD-3 step 2 are built but have never been run on a real
  dump.** Classification, calendar conversion, time-slice attribution, native
  bz2/gz decompression, S3 publication, the normalized bulk ingest and the
  `bake --model` path are all in and proven end to end on the truncated
  fixture. Nothing has been measured at 115M items, so: the HOT-tier targets
  in the README are SRC-5's budget rather than an observation, the importance
  floor that hits 1-5M entities is a guess, and the bake time and artifact
  count at dump scale are unknown (see the artifact-count issue below, which
  bites first). Run the census on a full dump before trusting any of them.

- **The curated Wikidata class table maps directly and does not walk the
  subclass tree.** An item whose only `P31` is an unlisted descendant of a
  listed class (a "naval battle" that is not `Q178561`) lands in the
  unclassified bucket instead of folding into its ancestor, so real recall is
  below what the table's size suggests. The census reports unmatched classes
  ranked by count precisely so the table can be grown from evidence; the
  alternative, a two-pass P279 closure over the whole class graph, is worth
  building only once those counts show it is needed.

- **The Parquet model has no per-row provenance columns.** SRC-2/DM-9 want
  `source, source_id, license, attribution, source_url, retrieved_at` on every
  record. Today provenance is recorded per import run (the report carries the
  source, URL, CC0 licence, dump digest and container) because both current
  sources are uniformly licensed. The columns become necessary as soon as a
  third source with per-record licensing lands, and OHM already has per-feature
  `license=*` tags.

- **Several SRC-3 validation gates do not exist.** The reject-rate gate and the
  duplicate-`wikidata_id` check are enforced; cross-entity date sanity,
  coordinate-versus-country checks, >5σ outlier detection and a
  reject-rate-jump comparison against the previous run are not.

- **Curated geometry is pinned to seed entities, so a bulk bake gets no front
  lines.** `data/geo/fronts/` resolves each file against a seed id, which a
  dump-derived dataset does not have, so `bake --model` has to point at a geo
  directory without them (an absent `fronts/` is now a legitimate
  configuration, matching the paleo layer). Reconciling upstream and curated
  geometry against bulk entities is the same unsolved problem as the
  unclickable fetched border shapes below.

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
- **Phone/stacked layout (FE-1) not implemented yet.** Desktop/tablet only.
- **Timeline keyboard navigation (FE-9) is only half done.** Arrow keys move
  the time cursor; FE-9 also asks for arrow-to-pan and +/- to zoom, which have
  no binding. Whatever lands for those has to share the arrow keys with the
  cursor, probably by making pan the modified chord.
- **The paleo layer is pinned by model name, not by version.** `fetch-borders`
  pins a git commit, and a re-fetch is byte-identical (verified). `fetch-paleo`
  can only ask the GPlates Web Service for `MERDITH2021`, so if EarthByte
  revises that model's data the output changes silently while
  `baker geo-fingerprint` stays the same, and CI would keep serving the cached
  old copy until the key changes for some other reason. The coverage and
  plausibility tests would catch a gross change; a subtle one would pass.
- **No future plate motion.** The timeline runs into the far future but the
  paleo layer stops at the present. This is an upstream limit, not an
  oversight: the GPlates Web Service rejects every negative (future) time on
  all 15 of its models with `small_time: 0`, verified live; Scotese's Pangea
  Proxima future reconstructions exist only as atlas figures, not as a
  fetchable rotation dataset; and the published future-supercontinent
  scenarios (Davies & Duarte) are bespoke desktop-GPlates constructions with
  no citable, downloadable geometry. Extrapolating plate velocities ourselves
  would be fabrication, so the far future simply reports no map data. Revisit
  if EarthByte ever publishes a future model as a citable artifact.
- **Upstream border slices carry degenerate polygons, and we drop them.**
  41 distinct polity names across the 53 slices are lost because their
  upstream geometry is a 4-vertex, zero-area rectangle: Switzerland in 1994,
  2000 and 2010, Jordan and Kuwait in 1938, the Bahmani Kingdom in 1500,
  Algiers and Tunis in 1650. Everything else lost is either an obscure
  archaeological culture or one of ~30 modern US/Canadian reservations
  anachronistically present in the 1492 slice, which fall under that slice's
  coarser area threshold. 192 of 193 names survive in 2010. Fixing this means
  repairing the upstream file, not weakening the validator.
- **Coastal and strait points fall outside their own polity.** The slices are
  coarse enough that Alexandria, Constantinople and Chicago-on-its-lake sit
  just offshore of the polygon that should contain them - in the upstream data
  as much as in our simplified copy. Point-in-polygon checks against this
  layer must use inland points;
  `internal/ingest/geo_plausibility_test.go` says so and does.
- **At 540 Ma the default camera looks at open ocean.** The land centroid is
  at latitude -52 (Gondwana over the south pole) while the map opens centred on
  35N, so the oldest slices look empty until the globe is rotated. The data is
  right and the layer is drawn; nothing points the camera at the land.
- **Fetched border shapes are not clickable.** The hand-traced eras resolved a
  seed id per feature, so clicking an empire opened its entity. Nothing
  reconciles an upstream polity name against the seed, so the fetched layers
  set no `entity` and the map-click-to-inspector path is dead for them.
- **Front-line interpolation needs matching vertex counts, and vertex *n* has
  to mean the same thing at every date.** The baker enforces the count; it
  cannot enforce the meaning, and getting it wrong is not a small error. An
  earlier draft folded the Finnish and Arctic fronts into the same ten
  vertices as the German-Soviet one, and interpolating across the point where
  they vanished walked the front line through neutral Sweden. The traces now
  cover only the German-Soviet front, north to south. A front that genuinely
  gains or loses a segment is still awkward to curate.
- **Paleo shapes cannot be named on hover.** The political slices carry a
  polity name per feature and the map tooltips read it. The GPlates coastlines
  arrive with no properties at all and are all written as `land`
  (`internal/ingest/paleo.go`), so there is nothing to show for deep time.
