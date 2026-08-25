// The baker is the only compute we own (ARCH-1): it turns source data into the
// static artifacts that ARE the product. It is a job, never a service (DEV-3).
package main

import (
	"context"
	"fmt"
	"os"

	"wk/internal/blob"
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
  bake --seed <dir>   run the full bake pipeline from the NDJSON seed set
                      (validates, ranks, bakes artifacts, publishes manifest)`)
}

func artifactsBucket() string { return envOr("BUCKET_ARTIFACTS", "wk-artifacts") }

func datasetVersion() string { return envOr("DATASET_VERSION", "dev") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// publishManifest writes the immutable per-dataset copy first, then atomically
// repoints the root manifest (ARCH-2: release = pointer flip, rollback = the
// previous /v/<dataset>/manifest.json still exists in full).
func publishManifest(ctx context.Context, cli *blob.Client, m model.Manifest) error {
	bucket := artifactsBucket()
	versioned := fmt.Sprintf("v/%s/manifest.json", m.Dataset)
	if _, err := cli.PutJSON(ctx, bucket, versioned, m); err != nil {
		return err
	}
	if _, err := cli.PutJSON(ctx, bucket, "manifest.json", m); err != nil {
		return err
	}
	fmt.Printf("published dataset %q -> s3://%s/manifest.json\n", m.Dataset, bucket)
	return nil
}
