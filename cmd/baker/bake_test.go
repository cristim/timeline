package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"wk/internal/bake"
	"wk/internal/duck"
	"wk/internal/ingest"
	"wk/internal/model"
)

const testBasemapBody = "tiny basemap fixture"

type testLayerCompiler struct{}

func (testLayerCompiler) Compile(_ context.Context, request bake.LayerCompileRequest) ([]byte, error) {
	return append([]byte("pmtiles:"), request.GeoJSON...), nil
}

type recordedPut struct {
	key         string
	body        []byte
	contentType string
}

type recordingSink struct {
	failAt int
	puts   []recordedPut
}

func (s *recordingSink) Put(_ context.Context, key string, body []byte, contentType string) (bool, error) {
	s.puts = append(s.puts, recordedPut{key: key, body: append([]byte(nil), body...), contentType: contentType})
	if s.failAt == len(s.puts) {
		return false, errors.New("injected put failure")
	}
	return true, nil
}

func TestPublishWarmModelUsesContentAddressedFilesAndWritesManifestLast(t *testing.T) {
	t.Parallel()

	files := testModelFiles(t)
	first := new(recordingSink)
	if err := publishWarmModel(context.Background(), first, "dataset-1", files); err != nil {
		t.Fatalf("publishWarmModel: %v", err)
	}
	second := new(recordingSink)
	if err := publishWarmModel(context.Background(), second, "dataset-1", files); err != nil {
		t.Fatalf("second publishWarmModel: %v", err)
	}
	if !reflect.DeepEqual(first.puts, second.puts) {
		t.Fatal("identical model files produced different publications")
	}
	if len(first.puts) != len(files)+1 {
		t.Fatalf("Put calls = %d, want %d", len(first.puts), len(files)+1)
	}

	manifestPut := first.puts[len(first.puts)-1]
	if manifestPut.key != "model/dataset-1/manifest.json" {
		t.Fatalf("last key = %q", manifestPut.key)
	}
	if manifestPut.contentType != "application/json" {
		t.Fatalf("manifest content type = %q", manifestPut.contentType)
	}
	var manifest warmModelManifest
	if err := json.Unmarshal(manifestPut.body, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.SchemaVersion != duck.SchemaVersion() || manifest.ModelID == "" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if len(manifest.Files) != len(files) {
		t.Fatalf("manifest files = %d, want %d", len(manifest.Files), len(files))
	}
	for i, file := range manifest.Files {
		wantPrefix := "model/dataset-1/" + manifest.ModelID + "/"
		if file.Name != files[i].Name || file.Key != wantPrefix+files[i].Name {
			t.Errorf("manifest file %d = %#v", i, file)
		}
		if file.Rows != files[i].Rows || file.Size != int64(len(first.puts[i].body)) || file.SHA256 == "" {
			t.Errorf("manifest metadata %d = %#v", i, file)
		}
		if wantSHA := fmt.Sprintf("%x", sha256.Sum256(first.puts[i].body)); file.SHA256 != wantSHA {
			t.Errorf("manifest SHA-256 %d = %q, want %q", i, file.SHA256, wantSHA)
		}
		if first.puts[i].key != file.Key || first.puts[i].contentType != parquetContentType {
			t.Errorf("file Put %d = %#v", i, first.puts[i])
		}
	}

	if err := os.WriteFile(files[0].Path, []byte("changed parquet"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := new(recordingSink)
	if err := publishWarmModel(context.Background(), changed, "dataset-1", files); err != nil {
		t.Fatalf("changed publishWarmModel: %v", err)
	}
	var changedManifest warmModelManifest
	if err := json.Unmarshal(changed.puts[len(changed.puts)-1].body, &changedManifest); err != nil {
		t.Fatalf("decode changed manifest: %v", err)
	}
	if changedManifest.ModelID == manifest.ModelID {
		t.Fatal("changed model content reused the prior model ID")
	}
}

func TestPublishWarmModelDoesNotWriteManifestAfterFileFailure(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{failAt: 2}
	err := publishWarmModel(context.Background(), sink, "dataset-1", testModelFiles(t))
	if err == nil || !strings.Contains(err.Error(), "injected put failure") {
		t.Fatalf("publishWarmModel error = %v", err)
	}
	for _, put := range sink.puts {
		if put.key == "model/dataset-1/manifest.json" {
			t.Fatal("manifest was attempted after a file upload failed")
		}
	}
}

func TestMaterializeImportArtifactsBuildsMatchingReportAndRejects(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	result := testImportResult()
	report, rejectFile, err := materializeImportArtifacts(context.Background(), dir, result, ingest.WarmSourceWarmFile, testSHA256String("warm"), nil)
	if err != nil {
		t.Fatalf("materializeImportArtifacts: %v", err)
	}
	if rejectFile.Rows != len(result.Rejects) {
		t.Fatalf("reject rows = %d, want %d", rejectFile.Rows, len(result.Rejects))
	}
	if report.Rejected != (ingest.ImportCounts{Seed: 1, Warm: 1, Total: 2}) {
		t.Fatalf("Rejected = %#v, want 1/1/2", report.Rejected)
	}
	wantReasons := []ingest.RejectReasonCount{
		{Source: ingest.RejectSourceSeed, Reason: "bad seed", Count: 1},
		{Source: ingest.RejectSourceWarm, Reason: "bad warm", Count: 1},
	}
	if !reflect.DeepEqual(report.RejectReasons, wantReasons) {
		t.Fatalf("RejectReasons = %#v, want %#v", report.RejectReasons, wantReasons)
	}
}

func TestMaterializeImportArtifactsIncludesOHMSummary(t *testing.T) {
	t.Parallel()

	ohm := &ingest.OHMImportSummary{
		Source: "OpenHistoricalMap", InputSHA256: testSHA256String("ohm"), RetrievedAt: "2026-08-30T06:51:56Z",
		Parsed: 1, Accepted: 1,
		Licenses: []ingest.LicenseCount{{License: "CC0-1.0", Count: 1}},
	}
	report, _, err := materializeImportArtifacts(context.Background(), t.TempDir(), testImportResult(), ingest.WarmSourceWarmFile, testSHA256String("warm"), ohm)
	if err != nil {
		t.Fatalf("materializeImportArtifacts: %v", err)
	}
	if !reflect.DeepEqual(report.OHM, ohm) {
		t.Fatalf("OHM report = %#v, want %#v", report.OHM, ohm)
	}
}

func TestMaterializeImportArtifactsFailsOnInvalidWarmSourceOrDigest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		warmSource ingest.WarmSource
		warmSHA256 string
	}{
		{name: "unknown source", warmSource: ingest.WarmSource("mystery"), warmSHA256: ""},
		{name: "missing digest", warmSource: ingest.WarmSourceWarmFile, warmSHA256: ""},
	}

	for _, tc := range cases {
		_, _, err := materializeImportArtifacts(context.Background(), t.TempDir(), testImportResult(), tc.warmSource, tc.warmSHA256, nil)
		if err == nil || !strings.Contains(err.Error(), "build import report") {
			t.Fatalf("%s: materializeImportArtifacts error = %v", tc.name, err)
		}
	}
}

func TestPublishImportArtifactsUsesContentAddressedKeysAndWritesManifestLast(t *testing.T) {
	t.Parallel()

	report, rejectFile := testImportArtifacts(t)
	first := new(recordingSink)
	firstTime := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	if err := publishImportArtifacts(context.Background(), first, "dataset-1", firstTime, report, rejectFile); err != nil {
		t.Fatalf("publishImportArtifacts: %v", err)
	}
	second := new(recordingSink)
	secondTime := firstTime.Add(5 * time.Minute)
	if err := publishImportArtifacts(context.Background(), second, "dataset-1", secondTime, report, rejectFile); err != nil {
		t.Fatalf("second publishImportArtifacts: %v", err)
	}
	if len(first.puts) != 3 {
		t.Fatalf("Put calls = %d, want 3", len(first.puts))
	}
	if first.puts[0].contentType != parquetContentType || first.puts[1].contentType != "application/json" || first.puts[2].contentType != "application/json" {
		t.Fatalf("content types = %#v", first.puts)
	}
	if first.puts[2].key != "imports/dataset-1/manifest.json" {
		t.Fatalf("last key = %q, want imports/dataset-1/manifest.json", first.puts[2].key)
	}

	var firstManifest importManifest
	if err := json.Unmarshal(first.puts[2].body, &firstManifest); err != nil {
		t.Fatalf("decode first manifest: %v", err)
	}
	if firstManifest.SchemaVersion != ingest.ImportReportSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", firstManifest.SchemaVersion, ingest.ImportReportSchemaVersion)
	}
	if firstManifest.GeneratedAt != firstTime.Format(time.RFC3339) {
		t.Fatalf("GeneratedAt = %q, want %q", firstManifest.GeneratedAt, firstTime.Format(time.RFC3339))
	}
	if firstManifest.Report.Key != "imports/dataset-1/"+firstManifest.ImportID+"/report.json" {
		t.Fatalf("report key = %q", firstManifest.Report.Key)
	}
	if firstManifest.Rejects.Key != "imports/dataset-1/"+firstManifest.ImportID+"/reject.parquet" {
		t.Fatalf("reject key = %q", firstManifest.Rejects.Key)
	}
	if firstManifest.Rejects.Rows != rejectFile.Rows {
		t.Fatalf("reject rows = %d, want %d", firstManifest.Rejects.Rows, rejectFile.Rows)
	}
	if firstManifest.Report.Size != int64(len(first.puts[1].body)) || firstManifest.Rejects.Size != int64(len(first.puts[0].body)) {
		t.Fatalf("manifest sizes = report:%d reject:%d", firstManifest.Report.Size, firstManifest.Rejects.Size)
	}
	if firstManifest.Report.SHA256 != fmt.Sprintf("%x", sha256.Sum256(first.puts[1].body)) {
		t.Fatalf("report sha256 = %q", firstManifest.Report.SHA256)
	}
	if firstManifest.Rejects.SHA256 != fmt.Sprintf("%x", sha256.Sum256(first.puts[0].body)) {
		t.Fatalf("reject sha256 = %q", firstManifest.Rejects.SHA256)
	}

	var secondManifest importManifest
	if err := json.Unmarshal(second.puts[2].body, &secondManifest); err != nil {
		t.Fatalf("decode second manifest: %v", err)
	}
	if firstManifest.ImportID != secondManifest.ImportID {
		t.Fatalf("timestamp changed import ID: %q vs %q", firstManifest.ImportID, secondManifest.ImportID)
	}
	if bytes.Equal(first.puts[2].body, second.puts[2].body) {
		t.Fatal("manifest body did not change when generated_at changed")
	}
}

func TestPublishImportArtifactsDifferentReportBytesChangeImportID(t *testing.T) {
	t.Parallel()

	first := new(recordingSink)
	firstReport, rejectFile := testImportArtifacts(t)
	firstReport.WarmSHA256 = testSHA256String("warm-a")
	if err := publishImportArtifacts(context.Background(), first, "dataset-1", time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), firstReport, rejectFile); err != nil {
		t.Fatalf("publishImportArtifacts(first): %v", err)
	}
	second := new(recordingSink)
	secondReport, _ := testImportArtifacts(t)
	secondReport.WarmSHA256 = testSHA256String("warm-b")
	if err := publishImportArtifacts(context.Background(), second, "dataset-1", time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), secondReport, rejectFile); err != nil {
		t.Fatalf("publishImportArtifacts(second): %v", err)
	}

	firstManifest := decodeImportManifest(t, first.puts[2].body)
	secondManifest := decodeImportManifest(t, second.puts[2].body)
	if firstManifest.ImportID == secondManifest.ImportID {
		t.Fatalf("different report bytes reused import ID %q", firstManifest.ImportID)
	}
}

func TestPublishImportArtifactsPublishesEmptyRejects(t *testing.T) {
	t.Parallel()

	report, rejectFile := testEmptyImportArtifacts(t)
	sink := new(recordingSink)
	if err := publishImportArtifacts(context.Background(), sink, "dataset-1", time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), report, rejectFile); err != nil {
		t.Fatalf("publishImportArtifacts: %v", err)
	}

	manifest := decodeImportManifest(t, sink.puts[2].body)
	if manifest.Rejects.Rows != 0 {
		t.Fatalf("reject rows = %d, want 0", manifest.Rejects.Rows)
	}
	if manifest.Rejects.Size == 0 || manifest.Rejects.SHA256 == "" {
		t.Fatalf("empty reject metadata = %#v", manifest.Rejects)
	}
}

func TestPublishImportArtifactsDoesNotWriteManifestAfterImmutableFailures(t *testing.T) {
	t.Parallel()

	report, rejectFile := testImportArtifacts(t)

	for _, failAt := range []int{1, 2} {
		sink := &recordingSink{failAt: failAt}
		err := publishImportArtifacts(context.Background(), sink, "dataset-1", time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), report, rejectFile)
		if err == nil || !strings.Contains(err.Error(), "injected put failure") {
			t.Fatalf("failAt=%d error = %v", failAt, err)
		}
		for _, put := range sink.puts {
			if put.key == "imports/dataset-1/manifest.json" {
				t.Fatalf("failAt=%d wrote manifest after immutable failure", failAt)
			}
		}
	}
}

func TestPublishImportArtifactsPropagatesLatestManifestFailure(t *testing.T) {
	t.Parallel()

	report, rejectFile := testImportArtifacts(t)
	sink := &recordingSink{failAt: 3}
	err := publishImportArtifacts(context.Background(), sink, "dataset-1", time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC), report, rejectFile)
	if err == nil || !strings.Contains(err.Error(), "publish import manifest") {
		t.Fatalf("publishImportArtifacts error = %v", err)
	}
	if len(sink.puts) != 3 {
		t.Fatalf("Put calls = %d, want 3", len(sink.puts))
	}
}

func TestRunBakeOutRoundTripsWithoutPublishingWarmModel(t *testing.T) {
	t.Setenv("DATASET_VERSION", "dev")
	spec := testBasemapSpec()
	outDir := t.TempDir()
	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, spec, []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
		"--out", outDir,
	})
	if err != nil {
		t.Fatalf("runBake: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err != nil {
		t.Fatalf("hot manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "v", "dev", "layers", "borders", "0.pmtiles")); err != nil {
		t.Fatalf("PMTiles layer: %v", err)
	}
	basemapPath := filepath.Join(outDir, "v", "dev", filepath.FromSlash(spec.Key()))
	basemapBody, err := os.ReadFile(basemapPath)
	if err != nil {
		t.Fatalf("basemap artifact: %v", err)
	}
	if string(basemapBody) != testBasemapBody {
		t.Fatalf("basemap body = %q, want %q", basemapBody, testBasemapBody)
	}
	wantBasemap := model.BasemapDescriptor{
		Key: spec.Key(), Source: spec.Source,
		Attribution: spec.Attribution, SHA256: spec.SHA256,
	}
	for _, manifestPath := range []string{
		filepath.Join(outDir, "manifest.json"),
		filepath.Join(outDir, "v", "dev", "manifest.json"),
	} {
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("read %s: %v", manifestPath, err)
		}
		var manifest model.Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatalf("decode %s: %v", manifestPath, err)
		}
		if manifest.Basemap != wantBasemap {
			t.Fatalf("%s basemap = %#v, want %#v", manifestPath, manifest.Basemap, wantBasemap)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "v", "dev", "layers", "borders", "0.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy GeoJSON layer exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "model")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote warm model directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "imports")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote import directory: %v", err)
	}
}

func TestRunBakeRejectsBasemapBeforeWritingOutput(t *testing.T) {
	spec := testBasemapSpec()
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "missing", want: spec.Filename},
		{name: "changed", body: []byte(strings.Repeat("x", len(testBasemapBody))), want: "sha256 "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			geoDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(geoDir, "basemap"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.body != nil {
				if err := os.WriteFile(filepath.Join(geoDir, "basemap", spec.Filename), tt.body, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			outDir := t.TempDir()
			err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, spec, []string{
				"--seed", "../../data/seed",
				"--geo", geoDir,
				"--goldens", "../../data/goldens.json",
				"--out", outDir,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runBake error = %v, want %q", err, tt.want)
			}
			entries, readErr := os.ReadDir(outDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("output contains artifacts after basemap failure: %v", entries)
			}
		})
	}
}

func TestRunBakeReportsMissingTippecanoe(t *testing.T) {
	t.Setenv("PATH", "")
	outDir := t.TempDir()
	err := runBakeWithCompiler(context.Background(), &bake.TippecanoeCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
		"--out", outDir,
	})
	if err == nil || !strings.Contains(err.Error(), "tippecanoe") || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("runBake error = %v", err)
	}
}

func TestRunBakeOutWithMalformedWarmFilePublishesHotOutputOnly(t *testing.T) {
	t.Setenv("DATASET_VERSION", "dev")
	outDir := t.TempDir()
	warmPath := filepath.Join(t.TempDir(), "warm.ndjson")
	validWarm, err := os.ReadFile("../../internal/ingest/testdata/warm-event.ndjson")
	if err != nil {
		t.Fatalf("read warm fixture: %v", err)
	}
	if err := os.WriteFile(warmPath, append(validWarm, []byte("{bad json}\n")...), 0o644); err != nil {
		t.Fatalf("write warm file: %v", err)
	}

	err = runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
		"--out", outDir,
		"--warm-file", warmPath,
	})
	if err != nil {
		t.Fatalf("runBake: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err != nil {
		t.Fatalf("hot manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "model")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote warm model directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "imports")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote import directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "v", "dev", "entity", "test-warm-event.json")); err != nil {
		t.Fatalf("missing valid warm entity document: %v", err)
	}
}

func TestRunBakeOutFailsWhenImportTempDirCannotBeCreated(t *testing.T) {
	tmpParent := t.TempDir()
	tmpFile := filepath.Join(tmpParent, "not-a-dir")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write tmp blocker: %v", err)
	}
	t.Setenv("TMPDIR", tmpFile)

	outDir := t.TempDir()
	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
		"--out", outDir,
	})
	if err == nil || !strings.Contains(err.Error(), "create import directory") {
		t.Fatalf("runBake error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "manifest.json")); !os.IsNotExist(statErr) {
		t.Fatalf("hot manifest after import-dir failure: %v", statErr)
	}
}

func TestRunBakeWarmModelFailureStopsBeforeHotPublication(t *testing.T) {
	server := newBakeS3Server(nil)
	server.failPutSubstring = "/wk-warm-test/model/"
	defer server.Close()
	server.installEnv(t)

	unusedGeoDir := t.TempDir()
	writeTestBasemap(t, unusedGeoDir)
	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", unusedGeoDir,
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "publish model file") {
		t.Fatalf("runBake error = %v", err)
	}
	requests := server.requests()
	if !containsRequest(requests, "/wk-warm-test/model/") {
		t.Fatalf("warm model was not attempted; requests = %v", requests)
	}
	if containsRequest(requests, "/wk-artifacts-test/") {
		t.Fatalf("hot artifact request after warm-model failure: %v", requests)
	}
}

func TestRunBakeSeedRejectPublishesImportDiagnosticsButNoModelOrHot(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", writeSeedDir(t, seedFixture{
			seedVersion: "seed-rejects",
			files: map[string]string{
				"seed.ndjson": validSeedLine("entity-1", "Valid") + "\n" + invalidSeedLine("entity-2", "Broken") + "\n",
			},
		}),
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "seed lines rejected") {
		t.Fatalf("runBake error = %v", err)
	}

	requests := server.requests()
	if !containsRequest(requests, "/wk-warm-test/imports/dev/") {
		t.Fatalf("import diagnostics were not published: %v", requests)
	}
	if containsRequest(requests, "/wk-warm-test/model/") {
		t.Fatalf("warm model request after seed reject: %v", requests)
	}
	if containsRequest(requests, "/wk-artifacts-test/") {
		t.Fatalf("hot artifact request after seed reject: %v", requests)
	}
}

func TestRunBakeAllowRejectsContinuesWithStaticOutput(t *testing.T) {
	seedDir := writeSeedDir(t, seedFixture{
		seedVersion: "seed-allow-rejects",
		files: map[string]string{
			"seed.ndjson": validSeedLineWithRange("entity-1", "Valid", "1901-01-01") + "\n" + invalidSeedLine("entity-2", "Broken") + "\n",
		},
	})
	goldensPath := writeGoldensFile(t, "seed-allow-rejects")
	outDir := t.TempDir()

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", seedDir,
		"--geo", geoDirForEntity(t, "entity-1"),
		"--goldens", goldensPath,
		"--out", outDir,
		"--allow-rejects",
	})
	if err != nil {
		t.Fatalf("runBake: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err != nil {
		t.Fatalf("hot manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "model")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote warm model directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "imports")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote import directory: %v", err)
	}
}

func TestRunBakeImportImmutableFailureStopsBeforeModelOrHot(t *testing.T) {
	server := newBakeS3Server(nil)
	server.failPutSubstring = "/wk-warm-test/imports/dev/"
	defer server.Close()
	server.installEnv(t)

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "publish reject parquet") {
		t.Fatalf("runBake error = %v", err)
	}

	requests := server.requests()
	if containsRequest(requests, "/wk-warm-test/model/") {
		t.Fatalf("warm model request after import failure: %v", requests)
	}
	if containsRequest(requests, "/wk-artifacts-test/") {
		t.Fatalf("hot artifact request after import failure: %v", requests)
	}
}

func TestRunBakeImportPointerFailureStopsBeforeModelOrHot(t *testing.T) {
	server := newBakeS3Server(nil)
	server.failPutSubstring = "/wk-warm-test/imports/dev/manifest.json"
	defer server.Close()
	server.installEnv(t)

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "publish import manifest") {
		t.Fatalf("runBake error = %v", err)
	}

	requests := server.requests()
	if containsRequest(requests, "/wk-warm-test/model/") {
		t.Fatalf("warm model request after import pointer failure: %v", requests)
	}
	if containsRequest(requests, "/wk-artifacts-test/") {
		t.Fatalf("hot artifact request after import pointer failure: %v", requests)
	}
}

func TestRunBakeSeedManifestFailuresPublishNothing(t *testing.T) {
	cases := []struct {
		name string
		fixt seedFixture
		want string
	}{
		{
			name: "checksum mismatch",
			fixt: seedFixture{
				seedVersion: "seed-bad-sha",
				files: map[string]string{
					"seed.ndjson": validSeedLine("entity-1", "Valid") + "\n",
				},
				mutateManifest: func(m *ingest.SeedManifest) {
					file := m.Files["seed.ndjson"]
					file.SHA256 = testSHA256String("wrong")
					m.Files["seed.ndjson"] = file
				},
			},
			want: "sha256 mismatch",
		},
		{
			name: "count mismatch",
			fixt: seedFixture{
				seedVersion: "seed-bad-count",
				files: map[string]string{
					"seed.ndjson": validSeedLine("entity-1", "Valid") + "\n",
				},
				mutateManifest: func(m *ingest.SeedManifest) {
					file := m.Files["seed.ndjson"]
					file.Count++
					m.Files["seed.ndjson"] = file
				},
			},
			want: "manifest says",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newBakeS3Server(nil)
			defer server.Close()
			server.installEnv(t)

			err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
				"--seed", writeSeedDir(t, tc.fixt),
				"--geo", testGeoDir(t),
				"--goldens", "../../data/goldens.json",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runBake error = %v", err)
			}
			if got := server.requests(); len(got) != 0 {
				t.Fatalf("requests after hard seed failure: %v", got)
			}
		})
	}
}

func TestRunBakeDuplicateSeedIDPublishesNothing(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", writeSeedDir(t, seedFixture{
			seedVersion: "seed-dup-id",
			files: map[string]string{
				"seed.ndjson": validSeedLine("dup", "One") + "\n" + validSeedLine("dup", "Two") + "\n",
			},
		}),
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate seed id") {
		t.Fatalf("runBake error = %v", err)
	}
	if got := server.requests(); len(got) != 0 {
		t.Fatalf("requests after duplicate seed id: %v", got)
	}
}

func TestRunBakeUnresolvedRelationshipPublishesNothing(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", writeSeedDir(t, seedFixture{
			seedVersion: "seed-bad-rel",
			files: map[string]string{
				"seed.ndjson": validSeedLineWithRel("entity-1", "Valid", "missing-target") + "\n",
			},
		}),
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("runBake error = %v", err)
	}
	if got := server.requests(); len(got) != 0 {
		t.Fatalf("requests after unresolved relationship: %v", got)
	}
}

func TestRunBakeOversizedWarmScannerFailurePublishesNothing(t *testing.T) {
	server := newBakeS3Server(nil)
	defer server.Close()
	server.installEnv(t)

	warmPath := filepath.Join(t.TempDir(), "warm.ndjson")
	if err := os.WriteFile(warmPath, append(bytes.Repeat([]byte("x"), 1024*1024+1), '\n'), 0o644); err != nil {
		t.Fatalf("write warm file: %v", err)
	}

	err := runBakeWithCompiler(context.Background(), testLayerCompiler{}, testBasemapSpec(), []string{
		"--seed", "../../data/seed",
		"--geo", testGeoDir(t),
		"--goldens", "../../data/goldens.json",
		"--warm-file", warmPath,
	})
	if err == nil || !strings.Contains(err.Error(), "scan warm events") {
		t.Fatalf("runBake error = %v", err)
	}
	if got := server.requests(); len(got) != 0 {
		t.Fatalf("requests after oversized warm failure: %v", got)
	}
}

func testModelFiles(t *testing.T) []duck.ModelFile {
	t.Helper()
	dir := t.TempDir()
	names := []string{"entity.parquet", "entity_category.parquet", "relationship.parquet", "claim.parquet"}
	files := make([]duck.ModelFile, 0, len(names))
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("parquet-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, duck.ModelFile{Name: name, Path: path, Rows: i + 1})
	}
	return files
}

func testGeoDir(t *testing.T) string {
	t.Helper()
	dir := emptyGeoDir(t)
	front := `{"type":"FeatureCollection","properties":{"entity":"eastern-front","source":"test"},"features":[{"type":"Feature","properties":{"valid_from":"1941-06-22","label":"A","representation":"estimated"},"geometry":{"type":"LineString","coordinates":[[20,50],[21,51]]}},{"type":"Feature","properties":{"valid_from":"1942-01-01","label":"B","representation":"estimated"},"geometry":{"type":"LineString","coordinates":[[22,50],[23,51]]}}]}`
	if err := os.WriteFile(filepath.Join(dir, "fronts", "test.geojson"), []byte(front), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func emptyGeoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"borders", "fronts", "basemap"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	border := `{"type":"FeatureCollection","properties":{"year":0,"t_from":0,"t_to":0,"label":"Test","source":"test"},"features":[{"type":"Feature","properties":{"name":"Test","representation":"administrative"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}}]}`
	if err := os.WriteFile(filepath.Join(dir, "borders", "0.geojson"), []byte(border), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestBasemap(t, dir)
	return dir
}

func testBasemapSpec() ingest.BasemapSpec {
	digest := sha256.Sum256([]byte(testBasemapBody))
	return ingest.BasemapSpec{
		Source:      "https://example.test/basemap.pmtiles",
		Filename:    "test.pmtiles",
		Size:        int64(len(testBasemapBody)),
		SHA256:      fmt.Sprintf("%x", digest),
		Attribution: `<a href="https://github.com/protomaps/basemaps">Protomaps</a> · © <a href="https://www.openstreetmap.org/copyright">OpenStreetMap contributors</a> · <a href="https://docs.overturemaps.org/attribution/">© ESA WorldCover project 2020 / Contains modified Copernicus Sentinel data (2020) processed by ESA WorldCover consortium</a> (<a href="https://creativecommons.org/licenses/by/4.0/">CC BY 4.0</a>)`,
	}
}

func writeTestBasemap(t *testing.T, geoDir string) {
	t.Helper()
	dir := filepath.Join(geoDir, "basemap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, testBasemapSpec().Filename), []byte(testBasemapBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

func geoDirForEntity(t *testing.T, entityID string) string {
	t.Helper()
	dir := emptyGeoDir(t)
	front := fmt.Sprintf(`{"type":"FeatureCollection","properties":{"entity":%q,"source":"test"},"features":[{"type":"Feature","properties":{"valid_from":"1900-01-01","label":"A","representation":"estimated"},"geometry":{"type":"LineString","coordinates":[[20,50],[21,51]]}},{"type":"Feature","properties":{"valid_from":"1901-01-01","label":"B","representation":"estimated"},"geometry":{"type":"LineString","coordinates":[[22,50],[23,51]]}}]}`, entityID)
	if err := os.WriteFile(filepath.Join(dir, "fronts", "test.geojson"), []byte(front), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testImportArtifacts(t *testing.T) (ingest.ImportReport, duck.ModelFile) {
	t.Helper()
	dir := t.TempDir()
	report, file, err := materializeImportArtifacts(context.Background(), dir, testImportResult(), ingest.WarmSourceWarmFile, testSHA256String("warm"), nil)
	if err != nil {
		t.Fatalf("materializeImportArtifacts: %v", err)
	}
	return report, file
}

func testEmptyImportArtifacts(t *testing.T) (ingest.ImportReport, duck.ModelFile) {
	t.Helper()
	dir := t.TempDir()
	report, file, err := materializeImportArtifacts(context.Background(), dir, &ingest.Result{
		SeedVersion:     "seed-deadbeef",
		SeedInputSHA256: testSHA256String("seed"),
		SeedParsed:      1,
		SeedAccepted:    1,
	}, ingest.WarmSourceNone, "", nil)
	if err != nil {
		t.Fatalf("materializeImportArtifacts: %v", err)
	}
	return report, file
}

func testImportResult() *ingest.Result {
	return &ingest.Result{
		SeedVersion:           "seed-deadbeef",
		SeedInputSHA256:       testSHA256String("seed"),
		SeedParsed:            3,
		SeedAccepted:          2,
		WarmParsed:            2,
		WarmAccepted:          1,
		WarmDuplicatesSkipped: 0,
		Rejects: []ingest.Reject{
			{Source: ingest.RejectSourceSeed, File: "seed.ndjson", Line: 3, Reason: "bad seed"},
			{Source: ingest.RejectSourceWarm, File: "warm:events", Line: 8, Reason: "bad warm"},
		},
	}
}

func decodeImportManifest(t *testing.T, body []byte) importManifest {
	t.Helper()
	var manifest importManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode import manifest: %v", err)
	}
	return manifest
}

func testSHA256String(input string) string {
	sum := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", sum[:])
}

type bakeS3Server struct {
	server           *httptest.Server
	warmBody         []byte
	failPutSubstring string
	mu               sync.Mutex
	recorded         []string
}

func newBakeS3Server(warmBody []byte) *bakeS3Server {
	s := &bakeS3Server{warmBody: warmBody}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.recorded = append(s.recorded, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, "/wk-warm-test/"+warmEventsKey) && s.warmBody != nil {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(s.warmBody)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		case http.MethodPut:
			if s.failPutSubstring != "" && strings.Contains(r.URL.Path, s.failPutSubstring) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<Error><Code>InvalidRequest</Code><Message>injected put failure</Message></Error>`))
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return s
}

func (s *bakeS3Server) Close() {
	s.server.Close()
}

func (s *bakeS3Server) installEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("S3_ENDPOINT", s.server.URL)
	t.Setenv("S3_FORCE_PATH_STYLE", "true")
	t.Setenv("BUCKET_WARM", "wk-warm-test")
	t.Setenv("BUCKET_ARTIFACTS", "wk-artifacts-test")
	t.Setenv("DATASET_VERSION", "dev")
}

func (s *bakeS3Server) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.recorded)
}

func containsRequest(requests []string, substring string) bool {
	for _, request := range requests {
		if strings.Contains(request, substring) {
			return true
		}
	}
	return false
}

type seedFixture struct {
	seedVersion    string
	files          map[string]string
	mutateManifest func(*ingest.SeedManifest)
}

func writeSeedDir(t *testing.T, fixture seedFixture) string {
	t.Helper()
	dir := t.TempDir()
	manifest := ingest.SeedManifest{
		SeedVersion: fixture.seedVersion,
		Files:       map[string]ingest.SeedFileMD{},
	}
	for name, body := range fixture.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write seed file %s: %v", name, err)
		}
		manifest.Files[name] = ingest.SeedFileMD{
			Count:  countSeedRecords(body),
			SHA256: testSHA256String(body),
		}
	}
	if fixture.mutateManifest != nil {
		fixture.mutateManifest(&manifest)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBody, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func countSeedRecords(body string) int {
	count := 0
	for _, line := range strings.Split(body, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		count++
	}
	return count
}

func validSeedLine(id, name string) string {
	return fmt.Sprintf(`{"id":%q,"type":"event","name":%q,"t0":"1900-01-01","precision":"day","status":"documented","categories":["war"],"importance":0.5}`, id, name)
}

func validSeedLineWithRange(id, name, t1 string) string {
	return fmt.Sprintf(`{"id":%q,"type":"event","name":%q,"t0":"1900-01-01","t1":%q,"precision":"day","status":"documented","categories":["war"],"importance":0.5}`, id, name, t1)
}

func invalidSeedLine(id, name string) string {
	return fmt.Sprintf(`{"id":%q,"type":"event","name":%q,"t0":"not-a-date","precision":"day","status":"documented","categories":["war"],"importance":0.5}`, id, name)
}

func validSeedLineWithRel(id, name, target string) string {
	return fmt.Sprintf(`{"id":%q,"type":"event","name":%q,"t0":"1900-01-01","precision":"day","status":"documented","categories":["war"],"importance":0.5,"rel":[{"type":"part_of","target":%q}]}`, id, name, target)
}

func writeGoldensFile(t *testing.T, seedVersion string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goldens.json")
	body, err := json.Marshal(map[string]any{
		"seed_version": seedVersion,
		"views":        []any{},
	})
	if err != nil {
		t.Fatalf("marshal goldens: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write goldens: %v", err)
	}
	return path
}

func TestDatasetVersionDerivesFromContent(t *testing.T) {
	t.Setenv("DATASET_VERSION", "")
	t.Setenv("GITHUB_SHA", "")
	a := datasetVersion("seed-1")
	if a[:2] != "d-" || len(a) != 14 {
		t.Fatalf("derived id shape: %q", a)
	}
	if b := datasetVersion("seed-2"); b == a {
		t.Fatalf("seed change must change the dataset id")
	}
	t.Setenv("GITHUB_SHA", "abc123")
	if c := datasetVersion("seed-1"); c == a {
		t.Fatalf("code revision must change the dataset id")
	}
	t.Setenv("DATASET_VERSION", "pinned")
	if got := datasetVersion("seed-1"); got != "pinned" {
		t.Fatalf("explicit DATASET_VERSION must win, got %q", got)
	}
}
