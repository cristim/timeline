// The baker is the only compute we own (ARCH-1): it turns source data into the
// static artifacts that ARE the product. It is a job, never a service (DEV-3).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"wk/internal/bake"
	"wk/internal/model"
)

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
	case "census":
		err = runCensus(ctx, os.Args[2:])
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
	fmt.Fprintln(os.Stderr, `usage: baker <command>
  bake --seed <dir> [--warm]   full bake: validate, rank, bake artifacts,
                               publish manifest; --warm merges the wk-warm
                               Wikidata event set; --geo <dir> points at the
                               curated geometry (default data/geo)
  fetch-wikidata               pull the bounded Wikidata event slice into
                               wk-dumps (raw) + wk-warm (normalized)
  census [--seed <dir>]        per-era/type counts + coverage report (ROAD-2)`)
}

func artifactsBucket() string { return envOr("BUCKET_ARTIFACTS", "wk-artifacts") }

func datasetVersion() string { return envOr("DATASET_VERSION", "dev") }

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
