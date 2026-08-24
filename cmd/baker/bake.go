package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"wk/internal/bake"
	"wk/internal/blob"
	"wk/internal/ingest"
	"wk/internal/rankzoom"
)

// runBake: seed NDJSON -> validate -> slugs -> bucketize -> artifacts ->
// manifest publish (DEV-6 M1). Rejects are data: they go to wk-warm as a
// per-run report, and any reject fails the bake (the seed is curated; a bad
// line is a bug to fix, not data to drop silently).
func runBake(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bake", flag.ContinueOnError)
	seedDir := fs.String("seed", "", "path to the NDJSON seed directory")
	allowRejects := fs.Bool("allow-rejects", false, "bake even if seed lines were rejected (report still written)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seedDir == "" {
		return fmt.Errorf("--seed <dir> is required")
	}

	res, err := ingest.LoadSeed(*seedDir)
	if err != nil {
		return err
	}
	fmt.Printf("seed %s: %d entities, %d rejects\n", res.SeedVersion, len(res.Entities), len(res.Rejects))

	cli, err := blob.New(ctx)
	if err != nil {
		return err
	}

	if len(res.Rejects) > 0 {
		reportKey := fmt.Sprintf("reports/bake-%s-rejects.json", time.Now().UTC().Format("20060102-150405"))
		if _, err := cli.PutJSON(ctx, envOr("BUCKET_WARM", "wk-warm"), reportKey, res.Rejects); err != nil {
			return fmt.Errorf("write reject report: %w", err)
		}
		for _, r := range res.Rejects {
			fmt.Printf("  reject %s:%d: %s\n", r.File, r.Line, r.Reason)
		}
		fmt.Printf("reject report -> s3://%s/%s\n", envOr("BUCKET_WARM", "wk-warm"), reportKey)
		if !*allowRejects {
			return fmt.Errorf("%d seed lines rejected; fix the seed or pass --allow-rejects", len(res.Rejects))
		}
	}

	if err := rankzoom.Bucketize(res.Entities); err != nil {
		return err
	}

	sink := blob.BucketSink{Client: cli, Bucket: artifactsBucket()}
	manifest, stats, err := bake.Run(ctx, sink, datasetVersion(), res.SeedVersion, res.Entities)
	if err != nil {
		return err
	}
	manifest.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("bake: %d artifacts written, %d unchanged\n", stats.Written, stats.Unchanged)

	if err := publishManifest(ctx, cli, *manifest); err != nil {
		return err
	}
	return nil
}
