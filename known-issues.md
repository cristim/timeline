# Known issues

- **The deterministic census is not the full ROAD-2 census.** It measures
  accepted normalized seed entities plus the current bounded Wikidata feed,
  and its local dump mode now streams decoded Wikidata JSON into aggregate raw
  fact and time-precision coverage. It still lacks taxonomy/type
  classification, calendar conversion, per-century attribution, native dump
  decompression, S3 publication, normalized bulk ingest and bulk bake, and
  final dump-scale HOT-tier targets.

- **Artifact count still tracks entity count at T11/T12.** Consecutive windows
  holding identical chunk bodies now share one artifact (manifest window runs),
  which removed the window-duplication multiplier: the seed bake went from
  8,173 chunk objects to 897, and a dump-shaped synthetic set drops from 30.8x
  entity count to 4.6x at 10k entities and from 7.5x to 2.0x at 1M. What runs
  cannot touch is a window holding one moment event nobody else shares - 88% of
  the residual at 1M is T11/T12 cells with a single item, where every entity is
  its own boundary. That is ~2M objects at 1M entities and ~8M at 5M, so M5
  still needs a lever runs do not provide: not baking T12/T13 for entities
  below an importance floor, or letting the client fall back to the parent
  bucket where the finer one has nothing of its own. Both change what the
  client can find, unlike the run collapse, so both want their own ZOOM-3
  review.

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
