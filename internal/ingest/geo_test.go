package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	rawBorders, err := loadAreaSlices("../../data/geo/borders", map[string]*model.Entity{})
	if err != nil {
		t.Fatalf("load raw borders: %v", err)
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
	byYear := func(year int) model.BorderLayer {
		t.Helper()
		for _, layer := range set.Borders {
			if layer.Year == year {
				return layer
			}
		}
		t.Fatalf("no composited border layer for %d", year)
		return model.BorderLayer{}
	}
	for _, raw := range rawBorders {
		if raw.Year >= 1900 {
			continue
		}
		if got := byYear(raw.Year); !reflect.DeepEqual(got, raw) {
			t.Errorf("pre-1900 border %d changed during OHM composition", raw.Year)
		}
	}
	if names := featureNames(byYear(1900)); !slices.Contains(names, "Metropolitan Borough of Chelsea") ||
		!slices.Contains(names, "Metropolitan Borough of Holborn") ||
		!slices.Contains(names, "Metropolitan Borough of Paddington") {
		t.Errorf("1900 border features omit predecessor boroughs: %v", names)
	}
	layer1960 := byYear(1960)
	if layer1960.TTo != 1964 {
		t.Errorf("1960 border covers %d..%d, want its original start through 1964", layer1960.TFrom, layer1960.TTo)
	}
	if names := featureNames(layer1960); !slices.Contains(names, "Metropolitan Borough of Chelsea") ||
		!slices.Contains(names, "Metropolitan Borough of Holborn") ||
		!slices.Contains(names, "Metropolitan Borough of Paddington") ||
		slices.Contains(names, "London Borough of Westminster") {
		t.Errorf("1960 border has wrong London detail: %v", names)
	}
	layer1965 := byYear(1965)
	raw1960 := model.BorderLayer{}
	for _, layer := range rawBorders {
		if layer.Year == 1960 {
			raw1960 = layer
			break
		}
	}
	if raw1960.Year != 1960 {
		t.Fatal("raw borders have no 1960 layer")
	}
	if layer1965.TFrom != 1965 || layer1965.TTo != raw1960.TTo {
		t.Errorf("1965 border covers %d..%d, want 1965..%d", layer1965.TFrom, layer1965.TTo, raw1960.TTo)
	}
	if names := featureNames(layer1965); !slices.Contains(names, "London Borough of Westminster") ||
		slices.Contains(names, "Metropolitan Borough of Chelsea") ||
		slices.Contains(names, "Metropolitan Borough of Holborn") ||
		slices.Contains(names, "Metropolitan Borough of Paddington") {
		t.Errorf("1965 border has wrong London detail: %v", names)
	}
	if err := validateAreaCoverage("real composited borders", set.Borders); err != nil {
		t.Errorf("composited border coverage: %v", err)
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

func TestCompositeOHMBorders(t *testing.T) {
	feature := func(name string, rank int) model.BorderFeature {
		return model.BorderFeature{Name: name, RenderRank: rank}
	}
	layer := func(year, tFrom, tTo int, label, source string, features ...model.BorderFeature) model.BorderLayer {
		return model.BorderLayer{
			Year: year, TFrom: tFrom, TTo: tTo, Label: label, Source: source,
			Features: features,
		}
	}

	t.Run("no OHM overlap", func(t *testing.T) {
		borders := []model.BorderLayer{layer(100, 100, 109, "base", "base source", feature("country", 0))}
		ohm := []model.BorderLayer{layer(200, 200, 209, "detail", "OHM source", feature("borough", 1))}
		wantBorders := cloneBorderLayers(borders)
		got, err := compositeOHMBorders(borders, ohm)
		if err != nil {
			t.Fatalf("composite: %v", err)
		}
		if !reflect.DeepEqual(got, wantBorders) {
			t.Fatalf("composited = %#v, want unchanged values %#v", got, wantBorders)
		}
		got[0].Features[0].Name = "mutated"
		if !reflect.DeepEqual(borders, wantBorders) {
			t.Fatal("composite output shares its feature backing array with the border input")
		}
	})

	t.Run("OHM boundary equal to base start", func(t *testing.T) {
		borders := []model.BorderLayer{layer(105, 100, 109, "base", "base source", feature("country", 0))}
		ohm := []model.BorderLayer{layer(100, 100, 109, "detail", "OHM source", feature("borough", 1))}
		got, err := compositeOHMBorders(borders, ohm)
		if err != nil {
			t.Fatalf("composite: %v", err)
		}
		if len(got) != 1 || got[0].Year != 105 || got[0].TFrom != 100 || got[0].TTo != 109 {
			t.Fatalf("layers = %#v, want one unsplit layer with original year", got)
		}
		if names := featureNames(got[0]); !reflect.DeepEqual(names, []string{"country", "borough"}) {
			t.Fatalf("feature order = %v, want base then OHM", names)
		}
	})

	t.Run("interior boundary and consecutive base windows", func(t *testing.T) {
		borders := []model.BorderLayer{
			layer(1950, 1950, 1959, "world 1950", "base source", feature("country 1950", 0)),
			layer(1960, 1960, 1969, "world 1960", "base source", feature("country 1960", 0)),
			layer(1970, 1970, 1979, "world 1970", "base source", feature("country 1970", 0)),
		}
		ohm := []model.BorderLayer{
			layer(1900, 1900, 1964, "detail 1900", "OHM source", feature("Chelsea", 1), feature("Holborn", 1)),
			layer(1965, 1965, 1979, "detail 1965", "OHM source", feature("Westminster", 1)),
		}
		wantBorders := cloneBorderLayers(borders)
		wantOHM := cloneBorderLayers(ohm)
		got, err := compositeOHMBorders(borders, ohm)
		if err != nil {
			t.Fatalf("composite: %v", err)
		}
		wantWindows := [][3]int{{1950, 1950, 1959}, {1960, 1960, 1964}, {1965, 1965, 1969}, {1970, 1970, 1979}}
		if len(got) != len(wantWindows) {
			t.Fatalf("got %d layers, want %d: %#v", len(got), len(wantWindows), got)
		}
		for i, want := range wantWindows {
			if tuple := [3]int{got[i].Year, got[i].TFrom, got[i].TTo}; tuple != want {
				t.Errorf("layer %d window = %v, want %v", i, tuple, want)
			}
		}
		if names := featureNames(got[1]); !reflect.DeepEqual(names, []string{"country 1960", "Chelsea", "Holborn"}) {
			t.Errorf("1960 feature order = %v", names)
		}
		if names := featureNames(got[2]); !reflect.DeepEqual(names, []string{"country 1960", "Westminster"}) {
			t.Errorf("1965 feature order = %v", names)
		}
		if got[1].Label != "world 1960 · London boundaries · 1900 · OpenHistoricalMap" ||
			got[2].Label != "world 1960 · London boundaries · 1965 · OpenHistoricalMap" {
			t.Errorf("split labels = %q and %q", got[1].Label, got[2].Label)
		}
		if got[1].Source != "base source + OHM source" || got[2].Source != "base source + OHM source" {
			t.Errorf("split sources = %q and %q", got[1].Source, got[2].Source)
		}
		if err := validateAreaCoverage("test composite", got); err != nil {
			t.Errorf("coverage: %v", err)
		}

		got[1].Features[0].Name = "mutated base"
		got[1].Features[1].Name = "mutated OHM"
		if got[2].Features[0].Name != "country 1960" {
			t.Fatal("split segments share their base-feature backing array")
		}
		if got[0].Features[1].Name != "Chelsea" {
			t.Fatal("output segments share their OHM-feature backing array")
		}
		if !reflect.DeepEqual(borders, wantBorders) || !reflect.DeepEqual(ohm, wantOHM) {
			t.Fatal("composition mutated an input layer or feature slice")
		}
	})
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
	// Front lines are curated against specific entities, so a dataset without
	// those entities legitimately has no fronts/ at all. Absent is a
	// configuration; present but empty or broken is still fatal.
	half := t.TempDir()
	write(t, half, "borders/100.geojson", `{"type":"FeatureCollection",
	  "properties":{"year":100,"t_from":90,"t_to":110,"label":"L","source":"S"},
	  "features":[{"type":"Feature","properties":{"name":"N","representation":"estimated"},
	  "geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`)
	set, err := LoadGeo(half, testEntities())
	if err != nil {
		t.Fatalf("a geo dir with no fronts/ must still bake: %v", err)
	}
	if len(set.Fronts) != 0 {
		t.Fatalf("fronts = %#v, want none", set.Fronts)
	}

	empty := t.TempDir()
	write(t, empty, "borders/100.geojson", `{"type":"FeatureCollection",
	  "properties":{"year":100,"t_from":90,"t_to":110,"label":"L","source":"S"},
	  "features":[{"type":"Feature","properties":{"name":"N","representation":"estimated"},
	  "geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`)
	if err := os.MkdirAll(filepath.Join(empty, "fronts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGeo(empty, testEntities()); err == nil {
		t.Error("a present but empty fronts/ must fail the bake")
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

func cloneBorderLayers(layers []model.BorderLayer) []model.BorderLayer {
	cloned := append([]model.BorderLayer(nil), layers...)
	for i := range cloned {
		cloned[i].Features = append([]model.BorderFeature(nil), layers[i].Features...)
	}
	return cloned
}
