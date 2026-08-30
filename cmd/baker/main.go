// The baker is the only compute we own (ARCH-1): it turns source data into the
// static artifacts that ARE the product. It is a job, never a service (DEV-3).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"wk/internal/bake"
	"wk/internal/ingest"
	"wk/internal/model"
)

const usageText = `usage: baker <command>
  bake --seed <dir> [--warm]   full bake: validate, rank, bake artifacts,
                               publish manifest; --warm merges the wk-warm
                               Wikidata event set; --geo <dir> points at the
                               curated geometry (default data/geo)
  fetch-wikidata               pull the bounded Wikidata event slice into
                               wk-dumps (raw) + wk-warm (normalized)
  fetch-borders [--out <dir>]  pull + simplify the historical-basemaps world
                               border slices into data/geo/borders
  fetch-paleo [--out <dir>]    pull + simplify GPlates reconstructed coastlines
                               into data/geo/paleo
  fetch-basemap [--out <dir>]  extract the pinned zoom 0-6 Protomaps archive
                               into data/geo/basemap
  geo-fingerprint              hash of the pinned upstreams; the CI cache key
                               for the three fetched map inputs
  geo-verify [--geo <dir>]     prove fetched layers tile their range and the
                               basemap matches its size and digest pin
  census --wikidata-dump <path|->
                               read decoded Wikidata JSON and write its coverage
                               report to stdout; pipe externally decompressed JSON
                               to - on stdin; mutually exclusive with seed/warm inputs
  census [--seed <dir>] [--seed-only | --warm-file <path>]
                               deterministic census from seed plus default
                               BUCKET_WARM/` + warmEventsKey + `, or explicit
                               --seed-only / --warm-file input
  ingest-wikidata-dump --dump <path|-> --out <dir>
                               normalize a Wikidata dump (.json, .json.gz,
                               .json.bz2 or stdin) into the Parquet model plus
                               reject table and import report;
                               --importance-floor promotes only entities at or
                               above it, --publish writes to BUCKET_WARM`

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "bake":
		err = runBake(ctx, os.Args[2:])
	case "fetch-wikidata":
		err = runFetchWikidata(ctx)
	case "fetch-borders":
		err = runFetchBorders(ctx, os.Args[2:])
	case "fetch-paleo":
		err = runFetchPaleo(ctx, os.Args[2:])
	case "fetch-basemap":
		err = runFetchBasemap(ctx, os.Args[2:])
	case "geo-fingerprint":
		err = runGeoFingerprint()
	case "geo-verify":
		err = runGeoVerify(os.Args[2:])
	case "census":
		err = runCensus(ctx, os.Args[2:])
	case "ingest-wikidata-dump":
		err = runIngestWikidataDump(ctx, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "baker %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, usageText)
}

func artifactsBucket() string { return envOr("BUCKET_ARTIFACTS", "wk-artifacts") }

// datasetVersion names the /v/<dataset>/ prefix a bake writes into. An
// explicit DATASET_VERSION wins; otherwise the id is derived from what the
// bake will contain (seed version, pinned geo upstreams) plus the exact code
// revision when CI provides one - so changed content always lands on a fresh
// immutable prefix instead of mutating objects the CDN serves as immutable
// (API-0/ARCH-2), which for range-read PMTiles means corruption, not staleness.
func datasetVersion(seedVersion string) string {
	if v := os.Getenv("DATASET_VERSION"); v != "" {
		return v
	}
	h := sha256.New()
	fmt.Fprintf(h, "seed=%s\n", seedVersion)
	fmt.Fprintf(h, "geo=%s\n", geoFingerprint(ingest.ProductionBasemap))
	fmt.Fprintf(h, "rev=%s\n", os.Getenv("GITHUB_SHA"))
	return "d-" + hex.EncodeToString(h.Sum(nil))[:12]
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// publishManifest writes the immutable per-dataset copy first, then repoints
// the root manifest (ARCH-2: release = pointer flip, rollback = the previous
// /v/<dataset>/manifest.json still exists in full). Works against any sink:
// S3/MinIO or a local directory.
func publishManifest(ctx context.Context, sink bake.Sink, m model.Manifest) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := sink.Put(ctx, fmt.Sprintf("v/%s/manifest.json", m.Dataset), body, "application/json"); err != nil {
		return err
	}
	if _, err := sink.Put(ctx, "manifest.json", body, "application/json"); err != nil {
		return err
	}
	fmt.Printf("published dataset %q (%d entities)\n", m.Dataset, m.Counts["entities"])
	return nil
}
