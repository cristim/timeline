package ingest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBasemapAcceptsPinnedBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec, body := testBasemapSpec()
	if err := os.WriteFile(filepath.Join(dir, spec.Filename), body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := VerifyBasemap(dir, spec)
	if err != nil {
		t.Fatalf("VerifyBasemap: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if got := spec.Key(); got != "basemap/tiny.pmtiles" {
		t.Fatalf("Key = %q", got)
	}
}

func TestProductionBasemapPin(t *testing.T) {
	t.Parallel()
	wantAttribution := `<a href="https://github.com/protomaps/basemaps">Protomaps</a> · © <a href="https://www.openstreetmap.org/copyright">OpenStreetMap contributors</a> · <a href="https://docs.overturemaps.org/attribution/">© ESA WorldCover project 2020 / Contains modified Copernicus Sentinel data (2020) processed by ESA WorldCover consortium</a> (<a href="https://creativecommons.org/licenses/by/4.0/">CC BY 4.0</a>)`
	if ProductionBasemap.Source != "https://build.protomaps.com/20260829.pmtiles" ||
		ProductionBasemap.Tool != "github.com/protomaps/go-pmtiles@v1.30.0" ||
		ProductionBasemap.GoToolchain != "go1.26.7" ||
		ProductionBasemap.BBox != "-180,-85.0511,180,85.0511" || ProductionBasemap.MaxZoom != 6 ||
		ProductionBasemap.Overfetch != 0 || ProductionBasemap.Filename != "protomaps-20260829-z0-6.pmtiles" ||
		ProductionBasemap.Size != 44_856_968 ||
		ProductionBasemap.SHA256 != "91578880b31e965f7e1c27c3efe1e2f53bb60e87b758349761a5f32cbb37b675" ||
		ProductionBasemap.Attribution != wantAttribution {
		t.Fatalf("production basemap pin = %#v", ProductionBasemap)
	}
}

func TestVerifyBasemapRejectsMissingSizeAndDigest(t *testing.T) {
	t.Parallel()
	spec, body := testBasemapSpec()
	tests := []struct {
		name    string
		write   bool
		mutate  func(*BasemapSpec)
		wantErr string
	}{
		{name: "missing", wantErr: "no such file"},
		{name: "wrong size", write: true, mutate: func(s *BasemapSpec) { s.Size++ }, wantErr: "size 12, want 13"},
		{name: "wrong digest", write: true, mutate: func(s *BasemapSpec) { s.SHA256 = strings.Repeat("0", 64) }, wantErr: "sha256"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gotSpec := spec
			if tc.mutate != nil {
				tc.mutate(&gotSpec)
			}
			path := filepath.Join(dir, gotSpec.Filename)
			if tc.write {
				if err := os.WriteFile(path, body, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := VerifyBasemap(dir, gotSpec)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("VerifyBasemap error = %v, want path and %q", err, tc.wantErr)
			}
		})
	}
}

func testBasemapSpec() (BasemapSpec, []byte) {
	body := []byte("tiny-pmtiles")
	digest := sha256.Sum256(body)
	return BasemapSpec{
		Source: "https://example.test/world.pmtiles", Tool: "example.test/tool@v1.2.3",
		GoToolchain: "go1.26.7",
		BBox:        "-1,-2,3,4", MaxZoom: 2, Overfetch: 0, Filename: "tiny.pmtiles",
		Size: int64(len(body)), SHA256: fmt.Sprintf("%x", digest), Attribution: "test attribution",
	}, body
}
