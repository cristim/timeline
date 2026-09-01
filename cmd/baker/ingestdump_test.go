package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"wk/internal/bake"
	"wk/internal/ingest"
	"wk/internal/model"
)

const censusDumpFixture = "../../internal/ingest/testdata/wikidata-dump-census.json"

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

func TestIngestWikidataDumpWritesModelRejectsAndReport(t *testing.T) {
	t.Parallel()

	out, report, stdout := ingestCensusFixture(t)
	for _, name := range []string{
		"entity.parquet", "entity_category.parquet", "relationship.parquet",
		"claim.parquet", "reject.parquet", dumpImportReportName,
	} {
		if info, err := os.Stat(filepath.Join(out, name)); err != nil || info.Size() == 0 {
			t.Fatalf("model file %s: err %v", name, err)
		}
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

	for _, name := range []string{"entity.parquet", "entity_category.parquet", "reject.parquet", dumpImportReportName} {
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
	modelDir, report, _ := ingestCensusFixture(t)
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

	// Every window the manifest advertises must exist as a chunk artifact under
	// the shared key scheme, and each chunk must hold renderable items.
	chunks := 0
	items := 0
	for i, bucket := range manifest.Buckets {
		for category, windows := range bucket.Windows {
			for _, window := range windows {
				key := filepath.Join(outDir, "v", manifest.Dataset, "chunks",
					model.Buckets[i].ID, strconv.FormatInt(window, 10), "world", category+".json")
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

func TestBakeFromModelPromotesOnlyAboveTheImportanceFloor(t *testing.T) {
	modelDir, _, _ := ingestCensusFixture(t)
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
