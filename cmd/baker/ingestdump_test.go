package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"wk/internal/bake"
	"wk/internal/blob"
	"wk/internal/duck"
	"wk/internal/ingest"
	"wk/internal/model"
)

const censusDumpFixture = "../../internal/ingest/testdata/wikidata-dump-census.json"

const rejectingDumpFixture = `[
{"id":"Q9101","type":"item","labels":{"en":{"value":"Impossible"}},"claims":{
"P31":[{"mainsnak":{"snaktype":"value","property":"P31","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"item","id":"Q178561"},"type":"wikibase-entityid"}},"rank":"normal"}],
"P585":[{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"-100000000000-00-00T00:00:00Z","timezone":0,"before":0,"after":0,"precision":6,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}]
}}
]`

func ingestCensusFixture(t *testing.T, extraArgs ...string) (string, ingest.WikidataDumpImportReport, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "model")
	args := append([]string{"--dump", censusDumpFixture, "--out", out}, extraArgs...)
	var stdout bytes.Buffer
	if err := runIngestWikidataDumpWithIO(context.Background(), args, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatalf("runIngestWikidataDump: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(out, dumpImportReportName))
	if err != nil {
		t.Fatalf("read import report: %v", err)
	}
	var report ingest.WikidataDumpImportReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("parse import report: %v", err)
	}
	return out, report, stdout.String()
}

func committedCensusFixture(t *testing.T) (string, ingest.WikidataDumpImportReport) {
	t.Helper()
	body, err := os.ReadFile(censusDumpFixture)
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	out := filepath.Join(t.TempDir(), "model")
	sink, err := duck.NewWikidataDumpStagingSink(context.Background(), out)
	if err != nil {
		t.Fatalf("NewWikidataDumpStagingSink: %v", err)
	}
	report, err := ingest.ImportWikidataDumpToSink(bytes.NewReader(body), ingest.WikidataDumpImportOptions{}, sink)
	if err != nil {
		t.Fatalf("ImportWikidataDumpToSink: %v", err)
	}
	reportBody, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := sink.Finalize(context.Background(), reportBody); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return out, report
}

func TestIngestWikidataDumpWritesModelRejectsAndReport(t *testing.T) {
	t.Parallel()

	out, report, stdout := ingestCensusFixture(t)
	wantFiles := []string{
		"model-files.json", dumpImportReportName,
		"entity.parquet", "entity_category.parquet", "relationship.parquet",
		"claim.parquet", "reject.parquet",
	}
	for _, name := range wantFiles {
		if info, err := os.Stat(filepath.Join(out, name)); err != nil || info.Size() == 0 {
			t.Fatalf("model file %s: err %v", name, err)
		}
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != len(wantFiles) {
		t.Fatalf("model entries = %d, %v; want exactly %d", len(entries), err, len(wantFiles))
	}
	if report.Accepted == 0 || report.Accepted+report.Filtered+report.Rejected != report.Items {
		t.Fatalf("report does not account for every item: %#v", report)
	}
	if report.License != "CC0-1.0" || report.Source != "wikidata" {
		t.Fatalf("provenance = %#v", report)
	}
	if !strings.Contains(stdout, "wikidata dump") || !strings.Contains(stdout, "model wikidata-") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// The same dump and the same code must produce byte-identical Parquet and an
// identical dataset version (SRC-3 reproducibility).
func TestIngestWikidataDumpIsReproducible(t *testing.T) {
	t.Parallel()

	firstDir, _, _ := ingestCensusFixture(t)
	secondDir, _, _ := ingestCensusFixture(t)

	for _, name := range []string{
		"model-files.json", dumpImportReportName, "entity.parquet", "entity_category.parquet",
		"relationship.parquet", "claim.parquet", "reject.parquet",
	} {
		first, err := os.ReadFile(filepath.Join(firstDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		second, err := os.ReadFile(filepath.Join(secondDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("%s differs between runs (%d vs %d bytes)", name, len(first), len(second))
		}
	}
	firstVersion, err := loadDumpModelVersion(firstDir)
	if err != nil {
		t.Fatalf("loadDumpModelVersion: %v", err)
	}
	secondVersion, err := loadDumpModelVersion(secondDir)
	if err != nil {
		t.Fatalf("loadDumpModelVersion: %v", err)
	}
	if firstVersion != secondVersion {
		t.Fatalf("dataset version %q vs %q", firstVersion, secondVersion)
	}
}

func TestIngestWikidataDumpReadsCompressedDumpFromStdin(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../internal/ingest/testdata/wikidata-dump-mini.json.bz2")
	if err != nil {
		t.Fatalf("read bz2 fixture: %v", err)
	}
	out := filepath.Join(t.TempDir(), "model")
	var stdout bytes.Buffer
	if err := runIngestWikidataDumpWithIO(context.Background(),
		[]string{"--dump", "-", "--out", out}, bytes.NewReader(body), &stdout, io.Discard); err != nil {
		t.Fatalf("runIngestWikidataDump: %v", err)
	}
	if !strings.Contains(stdout.String(), "(bzip2)") {
		t.Fatalf("stdout = %q, want the detected container", stdout.String())
	}
}

func TestIngestWikidataDumpFailureLeavesNoPartialModel(t *testing.T) {
	t.Run("reject gate", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "model")
		err := runIngestWikidataDumpWithIO(context.Background(),
			[]string{"--dump", "-", "--out", out, "--max-reject-rate", "0"},
			strings.NewReader(rejectingDumpFixture), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "reject rate") {
			t.Fatalf("runIngestWikidataDumpWithIO error = %v", err)
		}
		if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
			t.Fatalf("failed import left output: %v", statErr)
		}
	})

	t.Run("corrupt compressed trailer", func(t *testing.T) {
		body, err := os.ReadFile("../../internal/ingest/testdata/wikidata-dump-mini.json.bz2")
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "model")
		err = runIngestWikidataDumpWithIO(context.Background(),
			[]string{"--dump", "-", "--out", out}, bytes.NewReader(body[:len(body)-2]), io.Discard, io.Discard)
		if err == nil {
			t.Fatal("accepted corrupt compressed trailer")
		}
		if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
			t.Fatalf("corrupt import left output: %v", statErr)
		}
	})

	t.Run("existing target", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "model")
		if err := os.Mkdir(out, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(out, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runIngestWikidataDumpWithIO(context.Background(),
			[]string{"--dump", censusDumpFixture, "--out", out}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil {
			t.Fatal("accepted an existing output target")
		}
		body, readErr := os.ReadFile(sentinel)
		if readErr != nil || string(body) != "keep" {
			t.Fatalf("competitor sentinel = %q, %v", body, readErr)
		}
		entries, readErr := os.ReadDir(out)
		if readErr != nil || len(entries) != 1 {
			t.Fatalf("competitor entries = %v, %v", entries, readErr)
		}
	})
}

func TestParseIngestDumpArgsRequiresDumpAndOut(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no dump", args: []string{"--out", "dir"}, want: "--dump"},
		{name: "no out", args: []string{"--dump", "-"}, want: "--out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseIngestDumpArgs(tc.args, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseIngestDumpArgs error = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

// The whole ROAD-3 step-2 path on the fixture: dump -> Parquet model -> bake ->
// a manifest and chunks the existing key scheme and golden machinery accept.
func TestBakeFromModelProducesValidArtifacts(t *testing.T) {
	modelDir, report := committedCensusFixture(t)
	version, err := loadDumpModelVersion(modelDir)
	if err != nil {
		t.Fatalf("loadDumpModelVersion: %v", err)
	}

	outDir := t.TempDir()
	t.Setenv("DATASET_VERSION", "")
	t.Setenv("GITHUB_SHA", "")
	if err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--model", modelDir,
		"--geo", geoDirForEntityBetween(t, "wd-q2002", "1915-01-01", "1916-01-01"),
		"--out", outDir,
		"--goldens", writeGoldensFile(t, version),
	}); err != nil {
		t.Fatalf("runBakeWithCompiler: %v", err)
	}

	manifestBody, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Dataset != datasetVersion(version) {
		t.Fatalf("dataset = %q, want derived from model version %q", manifest.Dataset, version)
	}
	if manifest.SeedVersion != version {
		t.Fatalf("version = %q, want the model version %q", manifest.SeedVersion, version)
	}
	if manifest.Counts["entities"] != report.Accepted {
		t.Fatalf("manifest entities = %d, want the %d accepted", manifest.Counts["entities"], report.Accepted)
	}
	if manifest.GoldenViews != "pass" {
		t.Fatalf("golden views = %q, want pass", manifest.GoldenViews)
	}
	if len(manifest.Buckets) != len(model.Buckets) {
		t.Fatalf("buckets = %d, want %d", len(manifest.Buckets), len(model.Buckets))
	}

	// Every run the manifest advertises must exist as a chunk artifact under
	// the shared key scheme, and each chunk must hold renderable items.
	chunks := 0
	items := 0
	for i, bucket := range manifest.Buckets {
		for category, runs := range bucket.Windows {
			for _, run := range runs {
				if run.End() < run.Start() {
					t.Fatalf("bucket %s category %s has inverted run %v", model.Buckets[i].ID, category, run)
				}
				key := filepath.Join(outDir, "v", manifest.Dataset, "chunks",
					model.Buckets[i].ID, strconv.FormatInt(run.Start(), 10), "world", category+".json")
				body, err := os.ReadFile(key)
				if err != nil {
					t.Fatalf("chunk %s: %v", key, err)
				}
				var chunk struct {
					Items []bake.ChunkItem `json:"items"`
				}
				if err := json.Unmarshal(body, &chunk); err != nil {
					t.Fatalf("parse chunk %s: %v", key, err)
				}
				if len(chunk.Items) == 0 {
					t.Fatalf("chunk %s is empty but advertised", key)
				}
				for _, item := range chunk.Items {
					if item.Slug == "" || item.Precision == "" || item.T1 < item.T0 {
						t.Fatalf("chunk %s holds an invalid item %#v", key, item)
					}
				}
				chunks++
				items += len(chunk.Items)
			}
		}
	}
	if chunks == 0 || items == 0 {
		t.Fatalf("bake wrote %d chunks holding %d items", chunks, items)
	}
}

func TestBakeFromModelWritesNothingAfterSourceChange(t *testing.T) {
	for _, kind := range []string{"symlink replacement", "in-place mutation"} {
		t.Run(kind, func(t *testing.T) {
			modelDir, _ := committedCensusFixture(t)
			out := t.TempDir()
			spec := testBasemapSpec()
			req := bakeModelRequest{
				modelDir: modelDir, geoDir: geoDirWithoutFronts(t), outDir: out,
				basemap: bake.BasemapArtifact{Key: spec.Key(), Source: spec.Source, Attribution: spec.Attribution, SHA256: spec.SHA256, Body: []byte(testBasemapBody)},
			}
			req.beforeVerify = func() {
				path := filepath.Join(modelDir, "entity.parquet")
				if kind == "symlink replacement" {
					if err := os.Rename(path, path+".old"); err != nil {
						t.Errorf("rename entity: %v", err)
						return
					}
					if err := os.Symlink("entity.parquet.old", path); err != nil {
						t.Errorf("symlink entity: %v", err)
					}
					return
				}
				file, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Errorf("open entity: %v", err)
					return
				}
				_, writeErr := file.WriteAt([]byte("changed"), 0)
				if closeErr := file.Close(); writeErr != nil || closeErr != nil {
					t.Errorf("mutate entity: write=%v close=%v", writeErr, closeErr)
				}
			}
			if err := bakeFromModel(context.Background(), testLayerCompiler{}, req); err == nil {
				t.Fatal("bakeFromModel accepted changed source")
			}
			entries, err := os.ReadDir(out)
			if err != nil || len(entries) != 0 {
				t.Fatalf("sink writes = %v, err %v", entries, err)
			}
		})
	}
}

func TestBakeFromModelSkipsDefaultSeedFronts(t *testing.T) {
	modelDir, _ := committedCensusFixture(t)
	version, err := loadDumpModelVersion(modelDir)
	if err != nil {
		t.Fatalf("loadDumpModelVersion: %v", err)
	}
	outDir := t.TempDir()
	if err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--model", modelDir,
		"--geo", testGeoDir(t),
		"--out", outDir,
		"--goldens", writeGoldensFile(t, version),
	}); err != nil {
		t.Fatalf("runBakeWithCompiler: %v", err)
	}

	manifestBody, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	entityPaths, err := filepath.Glob(filepath.Join(outDir, "v", manifest.Dataset, "entity", "*.json"))
	if err != nil {
		t.Fatalf("glob entity docs: %v", err)
	}
	if len(entityPaths) == 0 {
		t.Fatal("bake wrote no entity docs")
	}
	for _, path := range entityPaths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read entity doc %s: %v", path, err)
		}
		var doc bake.EntityDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("parse entity doc %s: %v", path, err)
		}
		if len(doc.Geometry) != 0 {
			t.Fatalf("entity doc %s has %d front sequences, want zero", path, len(doc.Geometry))
		}
	}
}

func TestBakeFromModelPromotesOnlyAboveTheImportanceFloor(t *testing.T) {
	modelDir, _ := committedCensusFixture(t)
	version, err := loadDumpModelVersion(modelDir)
	if err != nil {
		t.Fatalf("loadDumpModelVersion: %v", err)
	}
	outDir := t.TempDir()
	t.Setenv("DATASET_VERSION", "dump-floor")
	if err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--model", modelDir,
		"--geo", geoDirForEntityBetween(t, "wd-q2002", "1915-01-01", "1916-01-01"),
		"--out", outDir,
		"--goldens", writeGoldensFile(t, version),
		"--importance-floor", "0.32",
	}); err != nil {
		t.Fatalf("runBakeWithCompiler: %v", err)
	}
	manifestBody, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	unfiltered := t.TempDir()
	if err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--model", modelDir,
		"--geo", geoDirForEntityBetween(t, "wd-q2002", "1915-01-01", "1916-01-01"),
		"--out", unfiltered,
		"--goldens", writeGoldensFile(t, version),
	}); err != nil {
		t.Fatalf("unfiltered runBakeWithCompiler: %v", err)
	}
	unfilteredBody, err := os.ReadFile(filepath.Join(unfiltered, "manifest.json"))
	if err != nil {
		t.Fatalf("read unfiltered manifest: %v", err)
	}
	var unfilteredManifest model.Manifest
	if err := json.Unmarshal(unfilteredBody, &unfilteredManifest); err != nil {
		t.Fatalf("parse unfiltered manifest: %v", err)
	}
	if manifest.Counts["entities"] >= unfilteredManifest.Counts["entities"] {
		t.Fatalf("floor promoted %d of %d entities, want fewer",
			manifest.Counts["entities"], unfilteredManifest.Counts["entities"])
	}
}

func TestIngestWikidataDumpPublishesUnderTheModelVersion(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	out := filepath.Join(t.TempDir(), "model")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runIngestWikidataDumpWithIO(context.Background(),
		[]string{"--dump", censusDumpFixture, "--out", out, "--publish"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runIngestWikidataDumpWithIO: %v", err)
	}
	version, err := loadDumpModelVersion(out)
	if err != nil {
		t.Fatalf("loadDumpModelVersion: %v", err)
	}

	requests := server.requests()
	for _, prefix := range []string{
		"/wk-warm-test/reports/wikidata-dump/" + version + "/",
		"/wk-warm-test/model/" + version + "/",
	} {
		if !containsRequest(requests, prefix) {
			t.Fatalf("requests = %v, want publication under %s", requests, prefix)
		}
	}
	if containsRequest(requests, "/wk-warm-test/model/dev/") ||
		containsRequest(requests, "/wk-warm-test/reports/wikidata-dump/dev/") {
		t.Fatalf("dump publish reused DATASET_VERSION instead of model version: %v", requests)
	}
	if !strings.Contains(stdout.String(), "model "+version+" -> "+out) {
		t.Fatalf("stdout = %q, want local model summary", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestIngestWikidataDumpPublicationFailurePreservesCommittedModel(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)
	server.failPutSubstring = "/entity.parquet"

	out := filepath.Join(t.TempDir(), "model")
	err := runIngestWikidataDumpWithIO(context.Background(),
		[]string{"--dump", censusDumpFixture, "--out", out, "--publish"},
		strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "publish model file entity.parquet") {
		t.Fatalf("runIngestWikidataDumpWithIO error = %v", err)
	}
	committed, openErr := duck.OpenCommittedModel(context.Background(), out)
	if openErr != nil {
		t.Fatalf("publication failure damaged local model: %v", openErr)
	}
	if closeErr := committed.Close(); closeErr != nil {
		t.Fatalf("close committed model: %v", closeErr)
	}
}

func TestPublishWikidataModelRetriesCommittedOutputWithoutSourceDump(t *testing.T) {
	failurePoints := []struct {
		name string
		key  string
	}{
		{name: "reject object", key: "reject.parquet"},
		{name: "report object", key: dumpImportReportName},
		{name: "report manifest", key: "report manifest"},
		{name: "entity object", key: "entity.parquet"},
		{name: "category object", key: "entity_category.parquet"},
		{name: "relationship object", key: "relationship.parquet"},
		{name: "claim object", key: "claim.parquet"},
		{name: "model manifest", key: "model manifest"},
	}
	for _, failurePoint := range failurePoints {
		t.Run(failurePoint.name, func(t *testing.T) {
			server := newBakeS3Server(nil)
			defer server.Close()
			server.installEnv(t)
			modelDir, _ := committedCensusFixture(t)
			version, err := loadDumpModelVersion(modelDir)
			if err != nil {
				t.Fatal(err)
			}
			expectation := committedPublicationExpectationFor(t, modelDir, version)
			server.failPutSubstring = expectation.paths[failurePoint.key]
			if err := runPublishWikidataModelWithIO(context.Background(), []string{"--model", modelDir}, io.Discard, io.Discard); err == nil {
				t.Fatal("first publication unexpectedly succeeded")
			}

			storedBefore := make(map[string][]byte)
			for name, path := range expectation.paths {
				storedBefore[name] = server.object(path)
			}
			requestBoundary := len(server.requests())
			server.failPutSubstring = ""
			var stdout bytes.Buffer
			if err := runPublishWikidataModelWithIO(context.Background(), []string{"--model", modelDir}, &stdout, io.Discard); err != nil {
				t.Fatalf("retry publication: %v", err)
			}
			delta := server.requests()[requestBoundary:]
			for name, body := range storedBefore {
				if name == "report manifest" || len(body) == 0 {
					continue
				}
				if slices.Contains(delta, "PUT "+expectation.paths[name]) {
					t.Fatalf("retry reuploaded unchanged %s: %v", name, delta)
				}
			}
			pointerBefore := storedBefore["report manifest"]
			pointerAfter := server.object(expectation.paths["report manifest"])
			if len(pointerBefore) != 0 {
				if !bytes.Equal(pointerAfter, pointerBefore) {
					t.Fatalf("retry rewrote report pointer\nbefore: %s\n after: %s", pointerBefore, pointerAfter)
				}
				for _, required := range []string{"PUT " + expectation.paths["report manifest"], "GET " + expectation.paths["report manifest"]} {
					if !slices.Contains(delta, required) {
						t.Fatalf("retry requests = %v, want %q", delta, required)
					}
				}
			}
			assertCommittedPublication(t, server, expectation)
			if !strings.Contains(stdout.String(), "model  -> s3://wk-warm-test/model/"+version+"/") {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

type committedPublicationExpectation struct {
	modelDir      string
	paths         map[string]string
	bodies        map[string][]byte
	importPointer importManifest
}

func committedPublicationExpectationFor(t *testing.T, modelDir, version string) committedPublicationExpectation {
	t.Helper()
	committed, err := duck.OpenCommittedModel(context.Background(), modelDir)
	if err != nil {
		t.Fatalf("open local committed model: %v", err)
	}
	defer func() {
		if err := committed.Close(); err != nil {
			t.Errorf("close local committed model: %v", err)
		}
	}()
	expectation := committedPublicationExpectation{
		modelDir: modelDir,
		paths:    make(map[string]string),
		bodies:   make(map[string][]byte),
	}
	err = committed.VisitArtifacts(context.Background(), func(artifacts []duck.CommittedArtifact) error {
		if err := validateCommittedDumpArtifacts(artifacts); err != nil {
			return err
		}
		rejectRows, err := artifactRowsAsInt(artifacts[6])
		if err != nil {
			return err
		}
		reportRoot := reportPrefix("wikidata-dump", version)
		expectation.importPointer, _, err = buildImportManifest(reportRoot, time.Unix(0, 0), ingest.WikidataDumpImportReportSchemaVersion,
			publicationObject{Size: artifacts[1].Size, SHA256: artifacts[1].SHA256},
			importRejectObject{Size: artifacts[6].Size, SHA256: artifacts[6].SHA256, Rows: rejectRows})
		if err != nil {
			return err
		}
		expectation.paths[dumpImportReportName] = "/wk-warm-test/" + expectation.importPointer.Report.Key
		expectation.paths["reject.parquet"] = "/wk-warm-test/" + expectation.importPointer.Rejects.Key
		expectation.paths["report manifest"] = "/wk-warm-test/" + reportRoot + "/manifest.json"
		for _, artifact := range artifacts[1:] {
			body, err := io.ReadAll(io.NewSectionReader(artifact.Reader, 0, artifact.Size))
			if err != nil {
				return err
			}
			expectation.bodies[artifact.Name] = body
		}
		metadata := make([]warmModelFile, 0, 4)
		for _, artifact := range artifacts[2:6] {
			rows, err := artifactRowsAsInt(artifact)
			if err != nil {
				return err
			}
			metadata = append(metadata, warmModelFile{
				Name: artifact.Name, Size: artifact.Size, SHA256: artifact.SHA256, Rows: rows,
			})
		}
		modelManifest, modelManifestBody, err := buildWarmModelManifest(version, metadata)
		if err != nil {
			return err
		}
		for _, file := range modelManifest.Files {
			expectation.paths[file.Name] = "/wk-warm-test/" + file.Key
		}
		expectation.paths["model manifest"] = "/wk-warm-test/model/" + version + "/manifest.json"
		expectation.bodies["model manifest"] = modelManifestBody
		return nil
	})
	if err != nil {
		t.Fatalf("build committed publication expectation: %v", err)
	}
	return expectation
}

func assertCommittedPublication(t *testing.T, server *bakeS3Server, expectation committedPublicationExpectation) {
	t.Helper()
	for name, want := range expectation.bodies {
		if got := server.object(expectation.paths[name]); !bytes.Equal(got, want) {
			t.Errorf("remote %s does not match committed bytes", name)
		}
	}
	pointerBody := server.object(expectation.paths["report manifest"])
	if _, err := validateWinningImportManifest(pointerBody, expectation.importPointer); err != nil {
		t.Fatalf("verify report manifest: %v", err)
	}
	committed, err := duck.OpenCommittedModel(context.Background(), expectation.modelDir)
	if err != nil {
		t.Fatalf("retry changed local model: %v", err)
	}
	if err := errors.Join(committed.Verify(context.Background()), committed.Close()); err != nil {
		t.Fatalf("verify local model after retry: %v", err)
	}
}

func TestPublishWikidataModelRequiresModel(t *testing.T) {
	if err := runPublishWikidataModelWithIO(context.Background(), nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("runPublishWikidataModelWithIO error = %v", err)
	}
}

func TestValidateWinningImportManifestPreservesExactWinner(t *testing.T) {
	want, _, err := buildImportManifest("reports/wikidata-dump/dataset", time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC),
		ingest.WikidataDumpImportReportSchemaVersion,
		publicationObject{Size: 12, SHA256: strings.Repeat("a", 64)},
		importRejectObject{Size: 34, SHA256: strings.Repeat("b", 64), Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	winner := want
	winner.GeneratedAt = "2026-09-01T09:00:00Z"
	winnerBody, err := json.Marshal(winner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := validateWinningImportManifest(winnerBody, want)
	if err != nil || got != winner {
		t.Fatalf("validateWinningImportManifest = %#v, %v", got, err)
	}

	conflict := winner
	conflict.Report.SHA256 = strings.Repeat("c", 64)
	conflictBody, _ := json.Marshal(conflict)
	for _, body := range [][]byte{
		append(append([]byte(nil), winnerBody...), '\n'),
		[]byte(`{"schema_version":1,"unknown":true}`),
		conflictBody,
	} {
		if _, err := validateWinningImportManifest(body, want); err == nil {
			t.Fatalf("accepted invalid winning manifest %s", body)
		}
	}
}

type recordingCommittedDumpBlob struct {
	calls             []string
	completed         []string
	pointer           []byte
	winnerGeneratedAt string
	conflictingWinner bool
	failKey           string
	failCreate        bool
	failGet           bool
	afterPut          func(string) error
}

func (f *recordingCommittedDumpBlob) PutReaderIfChanged(ctx context.Context, _, key string, reader io.ReaderAt, size int64, _ string) (bool, error) {
	f.calls = append(f.calls, "put "+key)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.failKey != "" && strings.Contains(key, f.failKey) {
		return false, errors.New("injected streaming put failure")
	}
	body, err := io.ReadAll(io.NewSectionReader(reader, 0, size))
	if err != nil || int64(len(body)) != size {
		return false, fmt.Errorf("read %s = %d bytes, %v; want %d", key, len(body), err, size)
	}
	if f.afterPut != nil {
		if err := f.afterPut(key); err != nil {
			return false, err
		}
	}
	f.completed = append(f.completed, "put "+key)
	return true, nil
}

func (f *recordingCommittedDumpBlob) PutIfAbsent(ctx context.Context, _, key string, body []byte, _ string) (bool, error) {
	f.calls = append(f.calls, "create "+key)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.failCreate {
		return false, errors.New("injected conditional create failure")
	}
	if f.winnerGeneratedAt == "" {
		f.pointer = append([]byte(nil), body...)
		f.completed = append(f.completed, "create "+key)
		return true, nil
	}
	var winner importManifest
	if err := json.Unmarshal(body, &winner); err != nil {
		return false, err
	}
	winner.GeneratedAt = f.winnerGeneratedAt
	if f.conflictingWinner {
		winner.Report.SHA256 = strings.Repeat("f", 64)
	}
	f.pointer, _ = json.Marshal(winner)
	return false, fmt.Errorf("conditional create: %w", blob.ErrPreconditionFailed)
}

func (f *recordingCommittedDumpBlob) GetBounded(ctx context.Context, _, key string, limit int64) ([]byte, error) {
	f.calls = append(f.calls, "get "+key)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.failGet {
		return nil, errors.New("injected winner read failure")
	}
	if int64(len(f.pointer)) > limit {
		return nil, errors.New("pointer exceeds limit")
	}
	return append([]byte(nil), f.pointer...), nil
}

func TestPublishCommittedWikidataDumpPreservesWinnerAndWritesModelManifestLast(t *testing.T) {
	dir, report := committedCensusFixture(t)
	committed, err := duck.OpenCommittedModel(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = committed.Close() })
	fake := &recordingCommittedDumpBlob{winnerGeneratedAt: "2026-09-01T09:00:00Z"}
	verifyCalls := 0
	var manifest importManifest
	err = committed.VisitArtifacts(context.Background(), func(artifacts []duck.CommittedArtifact) error {
		var publishErr error
		manifest, publishErr = publishCommittedWikidataDump(context.Background(), fake, "bucket", committed.ModelVersion(), report, artifacts,
			time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC), func() error {
				verifyCalls++
				return committed.Verify(context.Background())
			})
		return publishErr
	})
	if err != nil {
		t.Fatalf("publishCommittedWikidataDump: %v", err)
	}
	if manifest.GeneratedAt != fake.winnerGeneratedAt || verifyCalls != 2 {
		t.Fatalf("winner/verify calls = %q/%d", manifest.GeneratedAt, verifyCalls)
	}
	wantLast := "put model/" + committed.ModelVersion() + "/manifest.json"
	if len(fake.calls) != 9 || fake.calls[len(fake.calls)-1] != wantLast {
		t.Fatalf("publication calls = %v, want nine with %q last", fake.calls, wantLast)
	}
	if !strings.HasPrefix(fake.calls[0], "put reports/wikidata-dump/") || !strings.Contains(fake.calls[0], "/reject.parquet") ||
		!strings.Contains(fake.calls[1], "/report.json") || !strings.HasPrefix(fake.calls[2], "create reports/wikidata-dump/") ||
		!strings.HasPrefix(fake.calls[3], "get reports/wikidata-dump/") {
		t.Fatalf("report pointer order = %v", fake.calls[:4])
	}
}

func TestPublishCommittedWikidataDumpRejectsConflictAndStopsBeforeModelManifest(t *testing.T) {
	dir, report := committedCensusFixture(t)
	committed, err := duck.OpenCommittedModel(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = committed.Close() })
	for _, tc := range []struct {
		name              string
		conflictingWinner bool
		failKey           string
	}{
		{name: "conflicting winner", conflictingWinner: true},
		{name: "model file failure", failKey: "relationship.parquet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &recordingCommittedDumpBlob{
				winnerGeneratedAt: "2026-09-01T09:00:00Z", conflictingWinner: tc.conflictingWinner, failKey: tc.failKey,
			}
			if tc.failKey != "" {
				fake.winnerGeneratedAt = ""
			}
			err := committed.VisitArtifacts(context.Background(), func(artifacts []duck.CommittedArtifact) error {
				_, publishErr := publishCommittedWikidataDump(context.Background(), fake, "bucket", committed.ModelVersion(), report, artifacts,
					time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC), func() error {
						return committed.Verify(context.Background())
					})
				return publishErr
			})
			if err == nil {
				t.Fatal("publication unexpectedly succeeded")
			}
			for _, call := range fake.calls {
				if strings.HasSuffix(call, "/manifest.json") && strings.HasPrefix(call, "put model/") {
					t.Fatalf("model manifest attempted after failure: %v", fake.calls)
				}
			}
		})
	}
}

func TestPublishCommittedWikidataDumpFailureMatrix(t *testing.T) {
	for _, tc := range []struct {
		name              string
		failKey           string
		failCreate        bool
		failGet           bool
		winner            bool
		verifyFailure     int
		mutateAfterReport bool
		cancelAfterReject bool
		modelStage        bool
	}{
		{name: "reject object", failKey: "/reject.parquet"},
		{name: "report object", failKey: "/report.json"},
		{name: "first verify gate", verifyFailure: 1},
		{name: "pointer create", failCreate: true},
		{name: "pointer winner get", failGet: true, winner: true},
		{name: "source mutation before pointer", mutateAfterReport: true},
		{name: "entity object", failKey: "/entity.parquet", modelStage: true},
		{name: "category object", failKey: "/entity_category.parquet", modelStage: true},
		{name: "relationship object", failKey: "/relationship.parquet", modelStage: true},
		{name: "claim object", failKey: "/claim.parquet", modelStage: true},
		{name: "second verify gate", verifyFailure: 2, modelStage: true},
		{name: "model manifest", failKey: "/manifest.json", modelStage: true},
		{name: "cancellation", cancelAfterReject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, report := committedCensusFixture(t)
			committed, err := duck.OpenCommittedModel(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = committed.Close() })
			fake := &recordingCommittedDumpBlob{failKey: tc.failKey, failCreate: tc.failCreate, failGet: tc.failGet}
			if tc.winner {
				fake.winnerGeneratedAt = "2026-09-01T09:00:00Z"
			}
			if tc.mutateAfterReport {
				mutated := false
				fake.afterPut = func(key string) error {
					if mutated || !strings.HasSuffix(key, "/report.json") {
						return nil
					}
					mutated = true
					file, err := os.OpenFile(filepath.Join(dir, "entity.parquet"), os.O_WRONLY, 0)
					if err != nil {
						return err
					}
					_, writeErr := file.WriteAt([]byte("changed"), 0)
					return errors.Join(writeErr, file.Close())
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelAfterReject {
				fake.afterPut = func(key string) error {
					if strings.HasSuffix(key, "/reject.parquet") {
						cancel()
					}
					return nil
				}
			}
			verifyCalls := 0
			err = committed.VisitArtifacts(ctx, func(artifacts []duck.CommittedArtifact) error {
				_, publishErr := publishCommittedWikidataDump(ctx, fake, "bucket", committed.ModelVersion(), report, artifacts,
					time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC), func() error {
						verifyCalls++
						if verifyCalls == tc.verifyFailure {
							return errors.New("injected verification failure")
						}
						return committed.Verify(ctx)
					})
				return publishErr
			})
			if err == nil {
				t.Fatal("publication unexpectedly succeeded")
			}
			modelCalls := 0
			pointerCompleted := false
			for _, call := range fake.calls {
				if strings.HasPrefix(call, "put model/") {
					modelCalls++
				}
			}
			for _, completed := range fake.completed {
				if strings.HasPrefix(completed, "create reports/wikidata-dump/") && strings.HasSuffix(completed, "/manifest.json") {
					pointerCompleted = true
				}
				if strings.HasPrefix(completed, "put model/") && strings.HasSuffix(completed, "/manifest.json") {
					t.Fatalf("completed model manifest after failure: calls=%v completed=%v", fake.calls, fake.completed)
				}
			}
			if !tc.modelStage && modelCalls != 0 {
				t.Fatalf("model publication started before report boundary succeeded: %v", fake.calls)
			}
			if pointerCompleted != tc.modelStage {
				t.Fatalf("report pointer completed = %t, want %t; calls=%v completed=%v", pointerCompleted, tc.modelStage, fake.calls, fake.completed)
			}
			if tc.cancelAfterReject && len(fake.calls) == 0 {
				t.Fatal("cancellation happened before publisher callback")
			}
		})
	}
}

func TestBakeRejectsConflictingSourceFlags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "neither", args: []string{"--out", t.TempDir()}, want: "exactly one of --seed"},
		{name: "both", args: []string{"--seed", "s", "--model", "m"}, want: "exactly one of --seed"},
		{name: "model with warm", args: []string{"--model", "m", "--warm"}, want: "--model is mutually exclusive"},
		{name: "floor with seed", args: []string{"--seed", "s", "--importance-floor", "0"}, want: "--importance-floor only applies to --model"},
		{name: "floor out of range", args: []string{"--model", "m", "--importance-floor", "2"}, want: "outside [0,1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runBakeWithCompiler error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestApplyImportanceFloorDropsOrphanedRelationships(t *testing.T) {
	t.Parallel()

	entities := []*model.Entity{
		{SeedID: "keep", Importance: 0.9, Rel: []model.SeedRel{{Type: "part_of", Target: "drop"}, {Type: "part_of", Target: "keep2"}}},
		{SeedID: "keep2", Importance: 0.8},
		{SeedID: "drop", Importance: 0.1},
	}
	promoted, held, droppedRels := applyImportanceFloor(entities, 0.5)
	if len(promoted) != 2 || held != 1 {
		t.Fatalf("promoted %d, held %d, want 2 and 1", len(promoted), held)
	}
	if droppedRels != 1 {
		t.Fatalf("dropped relationships = %d, want 1", droppedRels)
	}
	if len(promoted[0].Rel) != 1 || promoted[0].Rel[0].Target != "keep2" {
		t.Fatalf("surviving relationships = %#v", promoted[0].Rel)
	}
}

// The ROAD-2 census publishes beside the artifacts it sizes: immutable,
// content-addressed, with the manifest written last (ARCH-2).
func TestCensusPublishesTheDumpReport(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runCensusWithIO(context.Background(),
		[]string{"--wikidata-dump", censusDumpFixture, "--publish"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runCensusWithIO: %v", err)
	}

	var report ingest.WikidataDumpCoverageReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not a single JSON report: %v\n%s", err, stdout.String())
	}
	file, err := os.Open(censusDumpFixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()
	expectedReport, err := ingest.BuildWikidataDumpCoverageReport(file)
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}
	body, err := json.Marshal(expectedReport)
	if err != nil {
		t.Fatalf("marshal expected report: %v", err)
	}
	if report.InputSHA256 != expectedReport.InputSHA256 {
		t.Fatalf("input digest = %q, want %q", report.InputSHA256, expectedReport.InputSHA256)
	}

	requests := server.requests()
	version := dumpContentVersion(body)
	prefix := "/wk-warm-test/reports/wikidata-census/" + version + "/"
	if !containsRequest(requests, prefix) {
		t.Fatalf("requests = %v, want a publication under %s", requests, prefix)
	}
	var puts []string
	for _, request := range requests {
		if strings.HasPrefix(request, "PUT ") {
			puts = append(puts, request)
		}
	}
	if len(puts) != 2 {
		t.Fatalf("PUT requests = %v, want the report then the manifest", puts)
	}
	if !strings.HasSuffix(puts[0], "/report.json") || !strings.HasSuffix(puts[1], "/manifest.json") {
		t.Fatalf("publication order = %v, want the immutable report first", puts)
	}
	if strings.Contains(stdout.String(), "report ->") {
		t.Fatalf("stdout contains diagnostics: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "report -> s3://wk-warm-test/reports/wikidata-census/"+version+"/") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCensusPublishRequiresTheDumpMode(t *testing.T) {
	t.Parallel()

	if _, err := parseCensusArgs([]string{"--publish"}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "--publish only applies to --wikidata-dump") {
		t.Fatalf("parseCensusArgs error = %v", err)
	}
}
