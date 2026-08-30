package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/paulmach/osm"

	"wk/internal/model"
)

func TestLoadOHMRealFixture(t *testing.T) {
	layers, summary, err := loadOHM("../../data/geo/ohm", 2035)
	if err != nil {
		t.Fatalf("loadOHM: %v", err)
	}
	if len(layers) != 2 || layers[0].Year != 1900 || layers[0].TTo != 1964 || layers[1].Year != 1965 || layers[1].TTo != 2035 {
		t.Fatalf("layers = %#v, want 1900..1964 and 1965..2035", layers)
	}
	if got := featureNames(layers[0]); !reflect.DeepEqual(got, []string{
		"Metropolitan Borough of Chelsea",
		"Metropolitan Borough of Holborn",
		"Metropolitan Borough of Paddington",
	}) {
		t.Fatalf("1900 features = %v", got)
	}
	if got := featureNames(layers[1]); !reflect.DeepEqual(got, []string{"London Borough of Westminster"}) {
		t.Fatalf("1965 features = %v", got)
	}
	if summary.Parsed != 4 || summary.Accepted != 4 || summary.Excluded != 0 {
		t.Fatalf("summary counts = %#v", summary)
	}
	if len(summary.LicenseExceptions) != 2 {
		t.Fatalf("license exceptions = %#v, want Holborn and Paddington", summary.LicenseExceptions)
	}
	for _, layer := range layers {
		for _, feature := range layer.Features {
			if feature.Source != ohmSource || feature.SourceID == "" || feature.License != ohmDefaultLicense ||
				feature.Attribution == "" || feature.SourceURL == "" || feature.RetrievedAt == "" {
				t.Errorf("incomplete provenance: %#v", feature)
			}
		}
	}

	again, againSummary, err := loadOHM("../../data/geo/ohm", 2035)
	if err != nil {
		t.Fatalf("second loadOHM: %v", err)
	}
	firstJSON, err := json.Marshal(struct {
		Layers  []model.BorderLayer
		Summary *OHMImportSummary
	}{Layers: layers, Summary: summary})
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(struct {
		Layers  []model.BorderLayer
		Summary *OHMImportSummary
	}{Layers: again, Summary: againSummary})
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("identical pinned input produced different normalized output")
	}
}

func TestLoadOHMSummaryIsOptionalAndValidatesConfiguredSource(t *testing.T) {
	if summary, err := LoadOHMSummary(t.TempDir()); err != nil || summary != nil {
		t.Fatalf("unconfigured summary = %#v, %v; want nil, nil", summary, err)
	}
	summary, err := LoadOHMSummary("../../data/geo")
	if err != nil {
		t.Fatalf("LoadOHMSummary: %v", err)
	}
	if summary == nil || summary.Parsed != 4 || summary.InputSHA256 == "" {
		t.Fatalf("configured summary = %#v", summary)
	}
}

func TestLoadOHMRejectsPinAndGeometryDrift(t *testing.T) {
	t.Run("payload digest", func(t *testing.T) {
		dir := copyOHMFixture(t)
		path := filepath.Join(dir, "london-boroughs.overpass.json")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadOHM(dir, 2035); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("error = %v, want sha256 mismatch", err)
		}
	})

	t.Run("relation version", func(t *testing.T) {
		dir := copyOHMFixture(t)
		mutateOHMPayload(t, dir, func(data *osm.OSM) { data.Relations[0].Version++ })
		if _, _, err := loadOHM(dir, 2035); err == nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("error = %v, want version mismatch", err)
		}
	})

	t.Run("missing member geometry", func(t *testing.T) {
		dir := copyOHMFixture(t)
		mutateOHMPayload(t, dir, func(data *osm.OSM) {
			data.Ways = nil
			data.Nodes = nil
		})
		summary, err := LoadOHMSummary(filepath.Dir(dir))
		if err != nil || summary == nil || summary.Parsed != 4 {
			t.Fatalf("metadata-only summary = %#v, %v; want four parsed relations", summary, err)
		}
		if _, _, err := loadOHM(dir, 2035); err == nil || !strings.Contains(err.Error(), "no valid polygon geometry") {
			t.Fatalf("error = %v, want missing geometry", err)
		}
	})

	t.Run("reversed dates", func(t *testing.T) {
		dir := copyOHMFixture(t)
		mutateOHMPayload(t, dir, func(data *osm.OSM) {
			setOSMTag(data.Relations[0], "start_date", "2000")
		})
		if _, _, err := loadOHM(dir, 2035); err == nil || !strings.Contains(err.Error(), "after end_date") {
			t.Fatalf("error = %v, want reversed date range", err)
		}
	})

	t.Run("same-year reversed dates", func(t *testing.T) {
		dir := copyOHMFixture(t)
		mutateOHMPayload(t, dir, func(data *osm.OSM) {
			setOSMTag(data.Relations[0], "start_date", "1964-12-31")
			setOSMTag(data.Relations[0], "end_date", "1964-01-01")
		})
		if _, _, err := loadOHM(dir, 2035); err == nil || !strings.Contains(err.Error(), "after end_date") {
			t.Fatalf("error = %v, want same-year reversed date range", err)
		}
	})

	t.Run("unknown manifest field", func(t *testing.T) {
		dir := copyOHMFixture(t)
		path := filepath.Join(dir, "manifest.json")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body = []byte(strings.Replace(string(body), `"schema_version": 1`, `"schema_version": 1, "mystery": true`, 1))
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadOHM(dir, 2035); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("error = %v, want unknown field", err)
		}
	})
}

func TestLoadOHMReportsUnsupportedLicense(t *testing.T) {
	dir := copyOHMFixture(t)
	mutateOHMPayload(t, dir, func(data *osm.OSM) {
		data.Relations[0].Tags = append(data.Relations[0].Tags, osm.Tag{Key: "license", Value: "ODbL-1.0"})
	})
	layers, summary, err := loadOHM(dir, 2035)
	if err != nil {
		t.Fatalf("loadOHM: %v", err)
	}
	if summary.Accepted != 3 || summary.Excluded != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if got := featureNames(layers[0]); len(got) != 2 || slices.Contains(got, "Metropolitan Borough of Chelsea") {
		t.Fatalf("1900 features = %v; unsupported Chelsea should be absent", got)
	}
	var found bool
	for _, exception := range summary.LicenseExceptions {
		if exception.SourceID == "relation/2691852@7" {
			found = true
			if exception.Action != LicenseExcluded || exception.Reason == "" {
				t.Fatalf("Chelsea exception = %#v", exception)
			}
		}
	}
	if !found {
		t.Fatalf("no Chelsea exception in %#v", summary.LicenseExceptions)
	}
}

func TestResolveOHMLicense(t *testing.T) {
	cases := []struct {
		name     string
		tags     map[string]string
		license  string
		explicit bool
		accepted bool
		wantErr  string
	}{
		{name: "default", tags: map[string]string{}, license: "CC0-1.0", accepted: true},
		{name: "explicit", tags: map[string]string{"license": "CC0-1.0"}, license: "CC0-1.0", explicit: true, accepted: true},
		{name: "legacy", tags: map[string]string{"licence": "CC0"}, license: "CC0-1.0", explicit: true, accepted: true},
		{name: "unsupported", tags: map[string]string{"license": "CC-BY-SA-4.0"}, license: "CC-BY-SA-4.0", explicit: true},
		{name: "empty", tags: map[string]string{"license": ""}, wantErr: "empty"},
		{name: "conflict", tags: map[string]string{"license": "CC0", "licence": "ODbL-1.0"}, wantErr: "conflicting"},
		{name: "unknown", tags: map[string]string{"license": "ask-someone"}, wantErr: "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			license, explicit, accepted, _, err := resolveOHMLicense(tc.tags)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || license != tc.license || explicit != tc.explicit || accepted != tc.accepted {
				t.Fatalf("got %q explicit=%v accepted=%v err=%v", license, explicit, accepted, err)
			}
		})
	}
}

func TestParseOHMYear(t *testing.T) {
	for input, want := range map[string]*int{
		"":           nil,
		"1900":       intPtr(1900),
		"1965-04":    intPtr(1965),
		"1965-04-01": intPtr(1965),
		"-0001":      intPtr(-1),
	} {
		got, err := parseOHMYear(input)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseOHMYear(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
	for _, input := range []string{"1965-13", "1965-02-30", "about 1900", "65"} {
		if _, err := parseOHMYear(input); err == nil {
			t.Errorf("parseOHMYear(%q) accepted invalid date", input)
		}
	}
}

func copyOHMFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ohm")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "london-boroughs.overpass.json"} {
		body, err := os.ReadFile(filepath.Join("../../data/geo/ohm", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mutateOHMPayload(t *testing.T, dir string, mutate func(*osm.OSM)) {
	t.Helper()
	payloadPath := filepath.Join(dir, "london-boroughs.overpass.json")
	body, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	var data osm.OSM
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatal(err)
	}
	mutate(&data)
	body, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ohmManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	manifest.Payload.SHA256 = hex.EncodeToString(digest[:])
	manifestBody, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestBody, 0o644); err != nil {
		t.Fatal(err)
	}
}

func setOSMTag(relation *osm.Relation, key, value string) {
	for i := range relation.Tags {
		if relation.Tags[i].Key == key {
			relation.Tags[i].Value = value
			return
		}
	}
	relation.Tags = append(relation.Tags, osm.Tag{Key: key, Value: value})
}

func featureNames(layer model.BorderLayer) []string {
	names := make([]string, len(layer.Features))
	for i, feature := range layer.Features {
		names[i] = feature.Name
	}
	return names
}

func intPtr(value int) *int { return &value }
