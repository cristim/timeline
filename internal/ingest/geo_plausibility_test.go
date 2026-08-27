package ingest

import (
	"encoding/json"
	"testing"

	"wk/internal/model"
)

// The structural checks in geo_test.go say a shape is well-formed GeoJSON.
// They say nothing about whether it is the polity it claims to be, and a
// simplification pipeline drifts into outright error easily: an empire that
// loses its capital because one ring self-intersected, a whole slice silently
// coarsened, a continent dropped as a speck. These cases pin the fetched
// outlines against places whose status on the stated date is not in dispute.
//
// "Approximate" covers a frontier drawn 200km off. It does not cover this.
//
// Test points are deliberately INLAND. The dataset's coastlines are coarse
// enough that a coastal or strait city - Alexandria, Constantinople, Chicago
// on its lake - falls outside its own polity's polygon, upstream as much as
// here. That is a property of the coastline, not of the empire's extent, and
// pinning it would test the wrong thing.

type place struct {
	name     string
	lon, lat float64
	in       bool // inside the named feature on that date
}

func TestFetchedExtentsCoverTheRightPlaces(t *testing.T) {
	cases := []struct {
		year    int
		feature string
		places  []place
	}{
		{-500, "Achaemenid Empire", []place{
			{"Persepolis", 52.89, 29.94, true},
			{"Babylon", 44.42, 32.54, true},
			{"Sardis", 28.04, 38.49, true},
			// Greece held; Persia never took the Italian peninsula.
			{"Athens", 23.73, 37.98, false},
			{"Rome", 12.5, 41.9, false},
		}},
		{100, "Roman Empire", []place{
			{"Rome", 12.5, 41.9, true},
			{"Londinium", -0.1, 51.5, true},
			{"Lugdunum (Lyon)", 4.83, 45.76, true},
			{"Corduba", -4.78, 37.89, true},
			// Free Germania and Hibernia never fell.
			{"Berlin", 13.4, 52.5, false},
			{"Dublin", -6.3, 53.3, false},
		}},
		{800, "Carolingian Empire", []place{
			{"Aachen", 6.08, 50.78, true},
			{"Paris", 2.35, 48.86, true},
			{"Milan", 9.19, 45.46, true},
			{"London", -0.1, 51.5, false},
		}},
		{800, "Abbasid Caliphate", []place{
			{"Baghdad", 44.4, 33.3, true},
			{"Damascus", 36.29, 33.51, true},
			{"Paris", 2.35, 48.86, false},
		}},
		{1279, "Great Khanate", []place{
			// Kublai's realm at the fall of the Song, distinct from the
			// Ilkhanate and Chagatai khanates the same slice names separately.
			{"Khanbaliq (Beijing)", 116.4, 39.9, true},
			{"Karakorum", 102.8, 47.2, true},
			{"Baghdad (Ilkhanate)", 44.4, 33.3, false},
			{"Delhi", 77.2, 28.6, false},
		}},
		{1500, "Aztec Empire", []place{
			{"Tenochtitlan", -99.13, 19.43, true},
			{"Cusco", -71.97, -13.53, false},
		}},
		{1500, "Inca Empire", []place{
			{"Cusco", -71.97, -13.53, true},
			{"Tenochtitlan", -99.13, 19.43, false},
		}},
		{1500, "Ming Chinese Empire", []place{
			{"Beijing", 116.4, 39.9, true},
			{"Xi'an", 108.9, 34.3, true},
			{"Delhi", 77.2, 28.6, false},
		}},
		{1500, "Ottoman Empire", []place{
			{"Ankara", 32.85, 39.93, true},
			{"Bursa", 29.06, 40.19, true},
			// Vienna was besieged in 1529, never held.
			{"Vienna", 16.37, 48.21, false},
		}},
		{1900, "British Raj", []place{
			{"Delhi", 77.2, 28.6, true},
			{"Lahore", 74.35, 31.55, true},
			{"Kabul", 69.2, 34.5, false},
		}},
		{1900, "Russian Empire", []place{
			{"Moscow", 37.6, 55.8, true},
			{"Kyiv", 30.5, 50.5, true},
			{"Tashkent", 69.24, 41.3, true},
			{"Berlin", 13.4, 52.5, false},
		}},
		{1900, "Germany", []place{
			{"Berlin", 13.4, 52.5, true},
			{"Munich", 11.58, 48.14, true},
			{"Paris", 2.35, 48.86, false},
		}},
		{1960, "United States", []place{
			{"Denver", -104.99, 39.74, true},
			{"Kansas City", -94.58, 39.1, true},
			{"Toronto", -79.4, 43.65, false},
		}},
	}

	slices := loadLayerOrSkip(t, "../../data/geo/borders")
	byYear := map[int]model.BorderLayer{}
	for _, l := range slices {
		byYear[l.Year] = l
	}

	for _, tc := range cases {
		layer, ok := byYear[tc.year]
		if !ok {
			t.Errorf("no border slice for %d", tc.year)
			continue
		}
		polys := featurePolygons(t, layer, tc.feature)
		if polys == nil {
			t.Errorf("%d: no feature named %q", tc.year, tc.feature)
			continue
		}
		for _, p := range tc.places {
			if got := inAny(polys, p.lon, p.lat); got != p.in {
				verb := "must not be inside"
				if p.in {
					verb = "must be inside"
				}
				t.Errorf("%d %s: %s %s the outline (got inside=%v)", tc.year, tc.feature, p.name, verb, got)
			}
		}
	}
}

// TestPaleoSlicesHoldTheRightAmountOfWorld is the deep-time equivalent. There
// are no polities to name and no fixed places to test - the whole point is
// that the ground moved - so what is pinned instead is that every slice still
// carries a plausible amount of land, spread over both hemispheres. A
// simplification bug that ate continents, or a reconstruction that collapsed
// to a blob, shows up here.
func TestPaleoSlicesHoldTheRightAmountOfWorld(t *testing.T) {
	slices := loadLayerOrSkip(t, "../../data/geo/paleo")

	// Earth's land is ~29% of a 64800 sq-deg sphere in lon/lat, but lon/lat
	// area over-counts high latitudes badly, so these bounds are deliberately
	// loose: they catch "a continent vanished", not a percent of drift.
	const minArea, maxArea = 2000.0, 40000.0
	for _, s := range slices {
		var total float64
		var minLon, maxLon = 180.0, -180.0
		var minLat, maxLat = 90.0, -90.0
		for _, f := range s.Features {
			for _, poly := range decodePolygons(t, f.Geometry) {
				total += shoelace(poly[0])
				for _, c := range poly[0] {
					minLon, maxLon = min(minLon, c[0]), max(maxLon, c[0])
					minLat, maxLat = min(minLat, c[1]), max(maxLat, c[1])
				}
			}
		}
		switch {
		case total < minArea:
			t.Errorf("%s: only %.0f sq deg of land left; a landmass was lost", s.Label, total)
		case total > maxArea:
			t.Errorf("%s: %.0f sq deg of land is more world than exists", s.Label, total)
		case maxLon-minLon < 180:
			t.Errorf("%s: land spans only %.0f deg of longitude", s.Label, maxLon-minLon)
		case maxLat-minLat < 90:
			t.Errorf("%s: land spans only %.0f deg of latitude", s.Label, maxLat-minLat)
		}
	}
}

func shoelace(ring [][]float64) float64 {
	sum := 0.0
	for i := 0; i+1 < len(ring); i++ {
		sum += ring[i][0]*ring[i+1][1] - ring[i+1][0]*ring[i][1]
	}
	if sum < 0 {
		return -sum / 2
	}
	return sum / 2
}

func decodePolygons(t *testing.T, raw json.RawMessage) [][][][]float64 {
	t.Helper()
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if g.Type == "Polygon" {
		var p [][][]float64
		if err := json.Unmarshal(g.Coordinates, &p); err != nil {
			t.Fatal(err)
		}
		return [][][][]float64{p}
	}
	var mp [][][][]float64
	if err := json.Unmarshal(g.Coordinates, &mp); err != nil {
		t.Fatal(err)
	}
	return mp
}

func featurePolygons(t *testing.T, layer model.BorderLayer, name string) [][][][]float64 {
	t.Helper()
	var out [][][][]float64
	for _, f := range layer.Features {
		if f.Name != name {
			continue
		}
		out = append(out, decodePolygons(t, f.Geometry)...)
	}
	return out
}

// inAny: inside any polygon's exterior ring and outside all of its holes.
func inAny(polys [][][][]float64, lon, lat float64) bool {
	for _, poly := range polys {
		if !inRing(poly[0], lon, lat) {
			continue
		}
		inHole := false
		for _, hole := range poly[1:] {
			if inRing(hole, lon, lat) {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}

// inRing is the standard even-odd ray cast.
func inRing(ring [][]float64, lon, lat float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > lat) != (yj > lat) && lon < (xj-xi)*(lat-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}
