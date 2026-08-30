package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"wk/internal/bake"
	"wk/internal/blob"
	"wk/internal/duck"
	"wk/internal/ingest"
	"wk/internal/rankzoom"
)

const parquetContentType = "application/vnd.apache.parquet"

type warmModelFile struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Rows   int    `json:"rows"`
}

type warmModelManifest struct {
	SchemaVersion int             `json:"schema_version"`
	ModelID       string          `json:"model_id"`
	Files         []warmModelFile `json:"files"`
}

// runBake: seed NDJSON (+ optional warm Wikidata events) -> validate -> slugs
// -> bucketize -> artifacts -> manifest publish. Artifacts go to S3/MinIO by
// default or to a local directory with --out (the static-hosting/CI path).
func runBake(ctx context.Context, args []string) error {
	return runBakeWithCompiler(ctx, &bake.TippecanoeCompiler{}, args)
}

func runBakeWithCompiler(ctx context.Context, compiler bake.LayerCompiler, args []string) error {
	fs := flag.NewFlagSet("bake", flag.ContinueOnError)
	seedDir := fs.String("seed", "", "path to the NDJSON seed directory")
	geoDir := fs.String("geo", "data/geo", "curated geometry directory (border time-steps, front lines)")
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
	warmSource := ingest.WarmSourceNone
	warmSHA256 := ""
	switch {
	case *warmFile != "":
		warm, err = os.ReadFile(*warmFile)
		if err != nil {
			return fmt.Errorf("read --warm-file: %w", err)
		}
		warmSource = ingest.WarmSourceWarmFile
	case *withWarm:
		if cli == nil {
			return fmt.Errorf("--warm needs S3; use --warm-file with --out")
		}
		warm, err = cli.Get(ctx, envOr("BUCKET_WARM", "wk-warm"), warmEventsKey)
		if err != nil {
			return fmt.Errorf("--warm requires %s (run fetch-wikidata first): %w", warmEventsKey, err)
		}
		warmSource = ingest.WarmSourceWikidataEvents
	}
	if warm != nil {
		sum := sha256.Sum256(warm)
		warmSHA256 = fmt.Sprintf("%x", sum[:])
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
	}

	importDir, err := os.MkdirTemp("", "world-knowledge-import-")
	if err != nil {
		return fmt.Errorf("create import directory: %w", err)
	}
	defer os.RemoveAll(importDir)
	report, rejectFile, err := materializeImportArtifacts(ctx, importDir, res, warmSource, warmSHA256)
	if err != nil {
		return fmt.Errorf("materialize import artifacts: %w", err)
	}
	dataset := datasetVersion()
	if cli != nil {
		importSink := blob.BucketSink{Client: cli, Bucket: envOr("BUCKET_WARM", "wk-warm")}
		if err := publishImportArtifacts(ctx, importSink, dataset, time.Now().UTC(), report, rejectFile); err != nil {
			return err
		}
	}

	if len(res.Rejects) > 0 {
		// Only curated-seed rejects block the bake; warm rejects are tolerated.
		if seedRejectCount > 0 && !*allowRejects {
			return fmt.Errorf("%d seed lines rejected; fix the seed or pass --allow-rejects", seedRejectCount)
		}
	}

	modelDir, err := os.MkdirTemp("", "world-knowledge-model-")
	if err != nil {
		return fmt.Errorf("create model directory: %w", err)
	}
	defer os.RemoveAll(modelDir)
	modelFiles, err := duck.WriteModel(ctx, modelDir, res.Entities)
	if err != nil {
		return fmt.Errorf("materialize model: %w", err)
	}
	res.Entities, err = duck.ReadModel(ctx, modelDir)
	if err != nil {
		return fmt.Errorf("read model: %w", err)
	}
	if cli != nil {
		modelSink := blob.BucketSink{Client: cli, Bucket: envOr("BUCKET_WARM", "wk-warm")}
		if err := publishWarmModel(ctx, modelSink, dataset, modelFiles); err != nil {
			return err
		}
	}

	// Geometry resolves entity references against the ingested table, so it
	// loads after the seed (and after any warm merge).
	geo, err := ingest.LoadGeo(*geoDir, res.Entities)
	if err != nil {
		return err
	}
	fmt.Printf("geo: %d border time-steps, %d front sequences\n", len(geo.Borders), len(geo.Fronts))

	if err := rankzoom.Bucketize(res.Entities); err != nil {
		return err
	}

	start := time.Now()
	manifest, stats, err := bake.Run(ctx, sink, compiler, dataset, res.SeedVersion, res.Entities, geo, goldens)
	if err != nil {
		return err
	}
	manifest.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("golden views: %d passed\n", len(goldens.Views))
	fmt.Printf("bake: %d artifacts written, %d unchanged in %s\n", stats.Written, stats.Unchanged, time.Since(start).Round(time.Millisecond))

	return publishManifest(ctx, sink, *manifest)
}

func publishWarmModel(ctx context.Context, sink bake.Sink, dataset string, files []duck.ModelFile) error {
	if len(files) != 4 {
		return fmt.Errorf("publish warm model: got %d files, want 4", len(files))
	}
	type publicationFile struct {
		metadata warmModelFile
		body     []byte
	}
	publication := make([]publicationFile, 0, len(files))
	for _, file := range files {
		body, err := os.ReadFile(file.Path)
		if err != nil {
			return fmt.Errorf("read model file %s: %w", file.Name, err)
		}
		digest := sha256.Sum256(body)
		publication = append(publication, publicationFile{
			metadata: warmModelFile{
				Name: file.Name, Size: int64(len(body)), SHA256: fmt.Sprintf("%x", digest), Rows: file.Rows,
			},
			body: body,
		})
	}

	identity := struct {
		SchemaVersion int             `json:"schema_version"`
		Files         []warmModelFile `json:"files"`
	}{SchemaVersion: duck.SchemaVersion(), Files: make([]warmModelFile, len(publication))}
	for i := range publication {
		identity.Files[i] = publication[i].metadata
	}
	identityBody, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("encode model identity: %w", err)
	}
	modelDigest := sha256.Sum256(identityBody)
	manifest := warmModelManifest{SchemaVersion: duck.SchemaVersion(), ModelID: fmt.Sprintf("%x", modelDigest)}
	manifest.Files = make([]warmModelFile, len(publication))
	for i := range publication {
		publication[i].metadata.Key = fmt.Sprintf("model/%s/%s/%s", dataset, manifest.ModelID, publication[i].metadata.Name)
		manifest.Files[i] = publication[i].metadata
	}

	for _, file := range publication {
		if _, err := sink.Put(ctx, file.metadata.Key, file.body, parquetContentType); err != nil {
			return fmt.Errorf("publish model file %s: %w", file.metadata.Name, err)
		}
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode model manifest: %w", err)
	}
	manifestKey := fmt.Sprintf("model/%s/manifest.json", dataset)
	if _, err := sink.Put(ctx, manifestKey, manifestBody, "application/json"); err != nil {
		return fmt.Errorf("publish model manifest: %w", err)
	}
	return nil
}
