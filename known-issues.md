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
- **Phone/stacked layout (FE-1) and timeline keyboard navigation (FE-9) not
  implemented yet.** Desktop/tablet only.
