package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wk/internal/bake"
	"wk/internal/blob"
	"wk/internal/duck"
	"wk/internal/ingest"
	"wk/internal/model"
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
	return runBakeWithCompiler(ctx, &bake.TippecanoeCompiler{}, ingest.ProductionBasemap, args)
}

func runBakeWithCompiler(ctx context.Context, compiler bake.LayerCompiler, basemapSpec ingest.BasemapSpec, args []string) error {
	fs := flag.NewFlagSet("bake", flag.ContinueOnError)
	seedDir := fs.String("seed", "", "path to the NDJSON seed directory")
	modelDirFlag := fs.String("model", "", "bake from a normalized Parquet model directory (baker ingest-wikidata-dump --out) instead of the seed")
	geoDir := fs.String("geo", "data/geo", "curated geometry directory (border time-steps, front lines)")
	outDir := fs.String("out", "", "write artifacts to this directory instead of S3 (GitHub Pages / static hosting)")
	withWarm := fs.Bool("warm", false, "merge the wk-warm Wikidata event set from S3 (M5)")
	warmFile := fs.String("warm-file", "", "merge a local warm-events NDJSON file (CI path, no S3 needed)")
	goldensPath := fs.String("goldens", "data/goldens.json", "golden-view expectations (ZOOM-5); failing views block publish")
	allowRejects := fs.Bool("allow-rejects", false, "bake even if seed lines were rejected (report still written)")
	importanceFloor := fs.Float64("importance-floor", 0, "bake only entities at or above this importance; the rest stay WARM (SRC-5)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*seedDir == "") == (*modelDirFlag == "") {
		return fmt.Errorf("exactly one of --seed <dir> and --model <dir> is required")
	}
	if *modelDirFlag != "" && (*withWarm || *warmFile != "") {
		return fmt.Errorf("--model is mutually exclusive with --warm and --warm-file")
	}
	if *importanceFloor < 0 || *importanceFloor > 1 {
		return fmt.Errorf("--importance-floor %v is outside [0,1]", *importanceFloor)
	}
	if *withWarm && *warmFile != "" {
		return fmt.Errorf("--warm and --warm-file are mutually exclusive")
	}
	basemapBody, err := ingest.VerifyBasemap(filepath.Join(*geoDir, "basemap"), basemapSpec)
	if err != nil {
		return err
	}
	basemap := bake.BasemapArtifact{
		Key: basemapSpec.Key(), Source: basemapSpec.Source,
		Attribution: basemapSpec.Attribution, SHA256: basemapSpec.SHA256,
		Body: basemapBody,
	}
	// A bulk bake has no pinned expectations until someone writes them against
	// its dataset, so --goldens "" turns the gate off there. The seed bake has
	// goldens and always runs them: letting the curated dataset switch its own
	// gate off is exactly the silent breakage ZOOM-5 exists to prevent.
	var goldens *bake.GoldenFile
	switch {
	case *goldensPath != "":
		goldens, err = bake.LoadGoldens(*goldensPath)
		if err != nil {
			return err
		}
	case *modelDirFlag == "":
		return fmt.Errorf("--goldens is required with --seed; an empty value is only allowed for --model")
	}

	if *modelDirFlag != "" {
		return bakeFromModel(ctx, compiler, bakeModelRequest{
			modelDir:        *modelDirFlag,
			geoDir:          *geoDir,
			outDir:          *outDir,
			importanceFloor: *importanceFloor,
			basemap:         basemap,
			goldens:         goldens,
		})
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
	dataset := datasetVersion(res.SeedVersion)
	var ohmSummary *ingest.OHMImportSummary
	if seedRejectCount == 0 || *allowRejects {
		ohmSummary, err = ingest.LoadOHMSummary(*geoDir)
		if err != nil {
			return err
		}
	}
	report, rejectFile, err := materializeImportArtifacts(ctx, importDir, res, warmSource, warmSHA256, ohmSummary)
	if err != nil {
		return fmt.Errorf("materialize import artifacts: %w", err)
	}
	if cli != nil {
		importSink := blob.BucketSink{Client: cli, Bucket: envOr("BUCKET_WARM", "wk-warm")}
		if err := publishImportArtifacts(ctx, importSink, dataset, time.Now().UTC(), report, rejectFile); err != nil {
			return err
		}
	}

	if seedRejectCount > 0 && !*allowRejects {
		return fmt.Errorf("%d seed lines rejected; fix the seed or pass --allow-rejects", seedRejectCount)
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

	// Geometry resolves entity references against the normalized model. OHM is
	// parsed again here to validate its windows against political coverage.
	geo, err := ingest.LoadGeo(*geoDir, res.Entities)
	if err != nil {
		return err
	}
	fmt.Printf("geo: %d border time-steps, %d OHM snapshots, %d front sequences\n",
		len(geo.Borders), len(geo.OHM), len(geo.Fronts))
	if err := rankzoom.Bucketize(res.Entities); err != nil {
		return err
	}

	start := time.Now()
	manifest, stats, err := bake.Run(ctx, sink, compiler, dataset, res.SeedVersion, res.Entities, basemap, geo, goldens)
	if err != nil {
		return err
	}
	manifest.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if goldens != nil {
		fmt.Printf("golden views: %d passed\n", len(goldens.Views))
	}
	fmt.Printf("bake: %d artifacts written, %d unchanged in %s\n", stats.Written, stats.Unchanged, time.Since(start).Round(time.Millisecond))

	return publishManifest(ctx, sink, *manifest)
}

type bakeModelRequest struct {
	modelDir        string
	geoDir          string
	outDir          string
	importanceFloor float64
	basemap         bake.BasemapArtifact
	goldens         *bake.GoldenFile
}

// bakeFromModel is the SRC-5 promotion step: the WARM Parquet model holds every
// normalized entity, and the entities at or above the importance floor become
// HOT serving artifacts. It is the same bake the seed takes, reading its
// entities from Parquet rather than NDJSON.
func bakeFromModel(ctx context.Context, compiler bake.LayerCompiler, req bakeModelRequest) error {
	version, err := loadDumpModelVersion(req.modelDir)
	if err != nil {
		return err
	}
	entities, err := duck.ReadModel(ctx, req.modelDir)
	if err != nil {
		return fmt.Errorf("read model: %w", err)
	}
	promoted, held, droppedRels := applyImportanceFloor(entities, req.importanceFloor)
	fmt.Printf("model %s: %d entities, %d promoted above importance %.2f, %d held WARM\n",
		version, len(entities), len(promoted), req.importanceFloor, held)
	if droppedRels > 0 {
		fmt.Printf("model %s: %d relationships dropped with their held targets\n", version, droppedRels)
	}
	if len(promoted) == 0 {
		return fmt.Errorf("model %s: the importance floor %.2f promoted no entities", version, req.importanceFloor)
	}

	var sink bake.Sink
	if req.outDir != "" {
		sink = &bake.FSSink{Root: req.outDir}
	} else {
		cli, err := blob.New(ctx)
		if err != nil {
			return err
		}
		sink = blob.BucketSink{Client: cli, Bucket: artifactsBucket()}
	}

	geo, err := ingest.LoadGeo(req.geoDir, promoted)
	if err != nil {
		return err
	}
	if err := rankzoom.Bucketize(promoted); err != nil {
		return err
	}

	start := time.Now()
	manifest, stats, err := bake.Run(ctx, sink, compiler, datasetVersion(), version, promoted, req.basemap, geo, req.goldens)
	if err != nil {
		return err
	}
	manifest.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if req.goldens != nil {
		fmt.Printf("golden views: %d passed\n", len(req.goldens.Views))
	}
	fmt.Printf("bake: %d artifacts written, %d unchanged in %s\n",
		stats.Written, stats.Unchanged, time.Since(start).Round(time.Millisecond))
	return publishManifest(ctx, sink, *manifest)
}

// applyImportanceFloor splits the model into the promoted set and the rest.
// Relationships whose target stayed behind are dropped and counted: an entity
// document must not point at something the bake never wrote.
func applyImportanceFloor(entities []*model.Entity, floor float64) ([]*model.Entity, int, int) {
	promoted := make([]*model.Entity, 0, len(entities))
	kept := make(map[string]bool, len(entities))
	for _, entity := range entities {
		if entity.Importance >= floor {
			promoted = append(promoted, entity)
			kept[entity.SeedID] = true
		}
	}
	droppedRels := 0
	for _, entity := range promoted {
		if len(entity.Rel) == 0 {
			continue
		}
		surviving := entity.Rel[:0]
		for _, rel := range entity.Rel {
			if kept[rel.Target] {
				surviving = append(surviving, rel)
				continue
			}
			droppedRels++
		}
		entity.Rel = surviving
	}
	return promoted, len(entities) - len(promoted), droppedRels
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
