package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"wk/internal/bake"
	"wk/internal/blob"
	"wk/internal/ingest"
	"wk/internal/rankzoom"
)

// runBake: seed NDJSON (+ optional warm Wikidata events) -> validate -> slugs
// -> bucketize -> artifacts -> manifest publish. Artifacts go to S3/MinIO by
// default or to a local directory with --out (the static-hosting/CI path).
func runBake(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bake", flag.ContinueOnError)
	seedDir := fs.String("seed", "", "path to the NDJSON seed directory")
	outDir := fs.String("out", "", "write artifacts to this directory instead of S3 (GitHub Pages / static hosting)")
	withWarm := fs.Bool("warm", false, "merge the wk-warm Wikidata event set from S3 (M5)")
	warmFile := fs.String("warm-file", "", "merge a local warm-events NDJSON file (CI path, no S3 needed)")
	goldensPath := fs.String("goldens", "data/goldens.json", "golden-view expectations (ZOOM-5); failing views block publish")
	allowRejects := fs.Bool("allow-rejects", false, "bake even if seed lines were rejected (report still written)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seedDir == "" {
		return fmt.Errorf("--seed <dir> is required")
	}
	if *withWarm && *warmFile != "" {
		return fmt.Errorf("--warm and --warm-file are mutually exclusive")
	}
	goldens, err := bake.LoadGoldens(*goldensPath)
	if err != nil {
		return err
	}

	res, err := ingest.LoadSeed(*seedDir)
	if err != nil {
		return err
	}
	fmt.Printf("seed %s: %d entities, %d rejects\n", res.SeedVersion, len(res.Entities), len(res.Rejects))
	seedRejectCount := len(res.Rejects)

	var cli *blob.Client
	var sink bake.Sink
	if *outDir != "" {
		sink = &bake.FSSink{Root: *outDir}
	} else {
		cli, err = blob.New(ctx)
		if err != nil {
			return err
		}
		sink = blob.BucketSink{Client: cli, Bucket: artifactsBucket()}
	}

	var warm []byte
	switch {
	case *warmFile != "":
		warm, err = os.ReadFile(*warmFile)
		if err != nil {
			return fmt.Errorf("read --warm-file: %w", err)
		}
	case *withWarm:
		if cli == nil {
			return fmt.Errorf("--warm needs S3; use --warm-file with --out")
		}
		warm, err = cli.Get(ctx, envOr("BUCKET_WARM", "wk-warm"), warmEventsKey)
		if err != nil {
			return fmt.Errorf("--warm requires %s (run fetch-wikidata first): %w", warmEventsKey, err)
		}
	}
	if warm != nil {
		// Bulk-data rejects are counted and reported, never fatal (unlike the
		// curated seed) - so a handful of bad upstream rows cannot block a bake.
		added, skipped, err := ingest.MergeWarmEvents(res, warm)
		if err != nil {
			return err
		}
		fmt.Printf("warm events: %d merged, %d deduped, %d rejected\n", added, skipped, len(res.Rejects)-seedRejectCount)
	}

	if len(res.Rejects) > 0 {
		for _, r := range res.Rejects {
			fmt.Printf("  reject %s:%d: %s\n", r.File, r.Line, r.Reason)
		}
		if cli != nil {
			reportKey := fmt.Sprintf("reports/bake-%s-rejects.json", time.Now().UTC().Format("20060102-150405"))
			if _, err := cli.PutJSON(ctx, envOr("BUCKET_WARM", "wk-warm"), reportKey, res.Rejects); err != nil {
				return fmt.Errorf("write reject report: %w", err)
			}
			fmt.Printf("reject report -> s3://%s/%s\n", envOr("BUCKET_WARM", "wk-warm"), reportKey)
		}
		// Only curated-seed rejects block the bake; warm rejects are tolerated.
		if seedRejectCount > 0 && !*allowRejects {
			return fmt.Errorf("%d seed lines rejected; fix the seed or pass --allow-rejects", seedRejectCount)
		}
	}

	if err := rankzoom.Bucketize(res.Entities); err != nil {
		return err
	}

	start := time.Now()
	manifest, stats, err := bake.Run(ctx, sink, datasetVersion(), res.SeedVersion, res.Entities, goldens)
	if err != nil {
		return err
	}
	manifest.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("golden views: %d passed\n", len(goldens.Views))
	fmt.Printf("bake: %d artifacts written, %d unchanged in %s\n", stats.Written, stats.Unchanged, time.Since(start).Round(time.Millisecond))

	return publishManifest(ctx, sink, *manifest)
}
