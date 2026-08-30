package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wk/internal/model"
)

// The committed geometry is loaded against the committed seed, so a curation
// mistake (an entity id that no longer exists, an era window that grew into
// its neighbour, a front position with a stray vertex) fails CI rather than
// reaching a bake.
func TestLoadRealGeo(t *testing.T) {
	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("load seed: %v", err)
	}
	ohm, err := LoadOHMSummary("../../data/geo")
	if err != nil {
		t.Fatalf("load OHM summary: %v", err)
	}
	set, err := LoadGeo("../../data/geo", res.Entities)
	if err != nil {
		t.Fatalf("load geo: %v", err)
	}
	if len(set.Borders) < 2 {
		t.Fatalf("%d border time-steps, want at least 2", len(set.Borders))
	}
	if ohm == nil || len(set.OHM) != 2 {
		t.Fatalf("OHM load = summary %#v, %d snapshots; want summary and 2 snapshots", ohm, len(set.OHM))
	}
	for i, l := range set.Borders {
		if i > 0 && set.Borders[i-1].Year >= l.Year {
			t.Errorf("border time-steps not ascending at %d", l.Year)
		}
		if l.Source == "" {
			t.Errorf("border %d has no source: curated geometry must say where it came from", l.Year)
		}
	}
	fronts, ok := set.Fronts["eastern-front"]
	if !ok {
		t.Fatalf("no front positions for eastern-front; have %v", keys(set.Fronts))
	}
	if len(fronts) < 3 {
		t.Errorf("%d eastern-front positions, want at least 3", len(fronts))
	}
	for _, p := range fronts {
		if len(p.Coordinates) != len(fronts[0].Coordinates) {
			t.Errorf("front vertex counts differ: %d vs %d", len(p.Coordinates), len(fronts[0].Coordinates))
		}
	}
}

// Each case writes one bad file into an otherwise valid tree and asserts the
// load fails with a message naming the problem.
func TestLoadGeoRejects(t *testing.T) {
	valid := `{"type":"FeatureCollection","properties":{"year":100,"t_from":90,"t_to":110,"label":"L","source":"S"},
	  "features":[{"type":"Feature","properties":{"name":"N","representation":"estimated"},
	  "geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`
	validFront := `{"type":"FeatureCollection","properties":{"entity":"war","source":"S"},"features":[
	  {"type":"Feature","properties":{"valid_from":"1941-06-22","representation":"estimated"},
	   "geometry":{"type":"LineString","coordinates":[[10,50],[12,52]]}},
	  {"type":"Feature","properties":{"valid_from":"1943-06-22","representation":"estimated"},
	   "geometry":{"type":"LineString","coordinates":[[20,50],[22,52]]}}]}`

	cases := []struct {
		name    string
		file    string // relative to the geo dir
		body    string
		wantErr string
	}{
		{"year disagrees with file name", "borders/100.geojson",
			strings.Replace(valid, `"year":100`, `"year":101`, 1), "properties.year"},
		{"file name is not a year", "borders/roman.geojson", valid, "<year>.geojson"},
		{"year outside its own window", "borders/100.geojson",
			strings.Replace(valid, `"t_to":110`, `"t_to":95`, 1), "outside its own window"},
		{"unsourced era", "borders/100.geojson",
			strings.Replace(valid, `"source":"S"`, `"source":""`, 1), "label and a source"},
		{"unknown representation", "borders/100.geojson",
			strings.Replace(valid, `"estimated"`, `"vibes"`, 1), "unknown representation"},
		{"unknown entity reference", "borders/100.geojson",
			strings.Replace(valid, `"name":"N"`, `"name":"N","entity":"atlantis"`, 1), "not a seed entity"},
		{"overlapping era windows", "borders/105.geojson",
			strings.NewReplacer(`"year":100`, `"year":105`, `"t_from":90`, `"t_from":100`, `"t_to":110`, `"t_to":120`).Replace(valid),
			"windows overlap"},
		{"missing name", "borders/100.geojson",
			strings.Replace(valid, `"name":"N",`, ``, 1), "properties.name is required"},
		{"wrong geometry type for a border", "borders/100.geojson",
			strings.Replace(valid, `"Polygon"`, `"LineString"`, 1), "want Polygon"},
		// Geometry goes into an immutable artifact verbatim, so this is the
		// last place a malformed ring can be caught.
		{"unclosed ring", "borders/100.geojson",
			strings.Replace(valid, `[[0,0],[1,0],[1,1],[0,0]]`, `[[0,0],[1,0],[1,1],[0,1]]`, 1), "not closed"},
		{"ring too short to be a polygon", "borders/100.geojson",
			strings.Replace(valid, `[[0,0],[1,0],[1,1],[0,0]]`, `[[0,0],[1,0],[0,0]]`, 1), "want at least 4"},
		{"ring winds the wrong way", "borders/100.geojson",
			strings.Replace(valid, `[[0,0],[1,0],[1,1],[0,0]]`, `[[0,0],[1,1],[1,0],[0,0]]`, 1), "winds the wrong way"},
		{"coordinate off the planet", "borders/100.geojson",
			strings.Replace(valid, `[1,1]`, `[1,999]`, 1), "not a valid [lon,lat]"},
		{"polygon with no rings", "borders/100.geojson",
			strings.Replace(valid, `[[[0,0],[1,0],[1,1],[0,0]]]`, `[]`, 1), "has no rings"},
		{"multipolygon with no polygons", "borders/100.geojson",
			strings.NewReplacer(`"Polygon"`, `"MultiPolygon"`, `[[[0,0],[1,0],[1,1],[0,0]]]`, `[]`).Replace(valid),
			"empty coordinates"},
		{"coordinates that are not coordinates", "borders/100.geojson",
			strings.Replace(valid, `[[[0,0],[1,0],[1,1],[0,0]]]`, `["nonsense"]`, 1), "polygon coordinates"},
		// Go's json zero-pads a short fixed-size array; decoding through a
		// slice is what makes this an error instead of a vertex at 0 degrees.
		{"front vertex missing its latitude", "fronts/w.geojson",
			strings.Replace(validFront, `[[20,50],[22,52]]`, `[[20],[22,52]]`, 1), "not a [lon,lat] pair"},
		{"front vertex with a third component", "fronts/w.geojson",
			strings.Replace(validFront, `[[20,50],[22,52]]`, `[[20,50,7],[22,52]]`, 1), "not a [lon,lat] pair"},
		{"front feature claiming its own owner", "fronts/w.geojson",
			strings.Replace(validFront, `"valid_from":"1941-06-22"`, `"valid_from":"1941-06-22","entity":"war"`, 1),
			"ownership comes from the file"},
		{"front with a single position", "fronts/w.geojson",
			`{"type":"FeatureCollection","properties":{"entity":"war","source":"S"},"features":[
			 {"type":"Feature","properties":{"valid_from":"1941-06-22","representation":"estimated"},
			  "geometry":{"type":"LineString","coordinates":[[10,50],[12,52]]}}]}`,
			"at least 2"},
		{"front vertex counts differ", "fronts/w.geojson",
			strings.Replace(validFront, `[[20,50],[22,52]]`, `[[20,50],[22,52],[24,54]]`, 1),
			"share a vertex count"},
		{"front position outside the entity's time range", "fronts/w.geojson",
			strings.Replace(validFront, `"1943-06-22"`, `"1980-06-22"`, 1), "outside"},
		{"front positions out of order", "fronts/w.geojson",
			strings.Replace(validFront, `"1943-06-22"`, `"1940-06-22"`, 1), "not after the previous"},
		{"front owned by nobody", "fronts/w.geojson",
			strings.Replace(validFront, `"entity":"war"`, `"entity":"peace"`, 1), "not a seed entity"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "borders/100.geojson", valid)
			write(t, dir, "fronts/w.geojson", validFront)
			write(t, dir, tc.file, tc.body)
			_, err := LoadGeo(dir, testEntities())
			if err == nil {
				t.Fatalf("expected a rejection mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// A mis-mounted volume or a wrong --geo must not publish a manifest with an
// empty layer list and no error to show for it.
func TestLoadGeoMissingDirs(t *testing.T) {
	if _, err := LoadGeo(filepath.Join(t.TempDir(), "nope"), nil); err == nil {
		t.Error("a missing geo dir must fail the bake")
	}
	half := t.TempDir()
	write(t, half, "borders/100.geojson", `{"type":"FeatureCollection",
	  "properties":{"year":100,"t_from":90,"t_to":110,"label":"L","source":"S"},
	  "features":[{"type":"Feature","properties":{"name":"N","representation":"estimated"},
	  "geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`)
	if _, err := LoadGeo(half, testEntities()); err == nil {
		t.Error("a geo dir with no fronts/ must fail the bake")
	}
}

func testEntities() []*model.Entity {
	return []*model.Entity{{
		SeedID: "war", Slug: "war", Name: "War", Type: "event",
		T0: model.YearToSeconds(1939), T1: model.YearToSeconds(1945),
	}}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]model.FrontPosition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
