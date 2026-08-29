package main

import (
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
	"strings"
	"sync"
	"testing"

	"wk/internal/duck"
)

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

func TestRunBakeOutRoundTripsWithoutPublishingWarmModel(t *testing.T) {
	outDir := t.TempDir()
	err := runBake(context.Background(), []string{
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
	if _, err := os.Stat(filepath.Join(outDir, "model")); !os.IsNotExist(err) {
		t.Fatalf("static bake wrote warm model directory: %v", err)
	}
}

func TestRunBakeWarmModelFailureStopsBeforeHotPublication(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<Error><Code>InvalidRequest</Code><Message>injected put failure</Message></Error>`))
	}))
	defer server.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("S3_ENDPOINT", server.URL)
	t.Setenv("S3_FORCE_PATH_STYLE", "true")
	t.Setenv("BUCKET_WARM", "wk-warm-test")
	t.Setenv("BUCKET_ARTIFACTS", "wk-artifacts-test")

	err := runBake(context.Background(), []string{
		"--seed", "../../data/seed",
		"--geo", filepath.Join(t.TempDir(), "unused"),
		"--goldens", "../../data/goldens.json",
	})
	if err == nil || !strings.Contains(err.Error(), "publish model file") {
		t.Fatalf("runBake error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 || !strings.Contains(strings.Join(requests, "\n"), "/wk-warm-test/model/") {
		t.Fatalf("warm model was not attempted; requests = %v", requests)
	}
	for _, request := range requests {
		if strings.Contains(request, "/wk-artifacts-test/") {
			t.Fatalf("hot artifact request after warm-model failure: %s", request)
		}
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
	dir := t.TempDir()
	for _, name := range []string{"borders", "fronts"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	border := `{"type":"FeatureCollection","properties":{"year":0,"t_from":0,"t_to":0,"label":"Test","source":"test"},"features":[{"type":"Feature","properties":{"name":"Test","representation":"administrative"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}}]}`
	if err := os.WriteFile(filepath.Join(dir, "borders", "0.geojson"), []byte(border), 0o644); err != nil {
		t.Fatal(err)
	}
	front := `{"type":"FeatureCollection","properties":{"entity":"eastern-front","source":"test"},"features":[{"type":"Feature","properties":{"valid_from":"1941-06-22","label":"A","representation":"estimated"},"geometry":{"type":"LineString","coordinates":[[20,50],[21,51]]}},{"type":"Feature","properties":{"valid_from":"1942-01-01","label":"B","representation":"estimated"},"geometry":{"type":"LineString","coordinates":[[22,50],[23,51]]}}]}`
	if err := os.WriteFile(filepath.Join(dir, "fronts", "test.geojson"), []byte(front), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
