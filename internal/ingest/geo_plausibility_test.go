package ingest

import (
	"encoding/json"
	"testing"

	"wk/internal/model"
)

// The structural checks in geo_test.go say a shape is well-formed GeoJSON.
// They say nothing about whether it is the empire it claims to be, and a
// coarse outline drifts into outright error easily: an empire that excludes
// its own capital province, a union that has lost a republic, a neutral state
// shaded as a belligerent. These cases pin the curated outlines against places
// whose status on the stated date is not in dispute.
//
// "Approximate" covers a frontier drawn 200km off. It does not cover this.

type place struct {
	name     string
	lon, lat float64
	in       bool // inside the named feature on that date
}

func TestCuratedExtentsCoverTheRightPlaces(t *testing.T) {
	cases := []struct {
		year    int
		feature string
		places  []place
	}{
		{117, "Roman Empire", []place{
			{"Rome", 12.5, 41.9, true},
			{"Alexandria", 29.9, 31.2, true},
			{"Londinium", -0.1, 51.5, true},
			{"Byzantium", 29.0, 41.0, true},
			{"Carthage", 10.3, 36.9, true},
			// Trajan reached the Persian Gulf; Germania and Hibernia never fell.
			{"Ctesiphon", 44.6, 33.1, true},
			{"free Germania (Berlin)", 13.4, 52.5, false},
			{"Hibernia (Dublin)", -6.3, 53.3, false},
			{"Caledonia (Inverness)", -4.2, 57.5, false},
			{"Scandinavia (Stockholm)", 18.1, 59.3, false},
		}},
		{1279, "Mongol Empire", []place{
			{"Karakorum", 102.8, 47.2, true},
			{"Khanbaliq (Beijing)", 116.4, 39.9, true},
			{"Baghdad", 44.4, 33.3, true},
			{"Kyiv", 30.5, 50.5, true},
			// Goryeo was a Yuan vassal and the base for the Japan invasions.
			{"Kaesong", 126.6, 38.0, true},
			{"Hungary (Budapest)", 19.0, 47.5, false}, // raided 1241, never held
			{"Delhi", 77.2, 28.6, false},
			{"Kyoto", 135.8, 35.0, false},
			{"Cairo", 31.2, 30.0, false},
		}},
		{1914, "Russian Empire", []place{
			{"St Petersburg", 30.3, 59.9, true},
			{"Warsaw (Congress Poland)", 21.0, 52.2, true},
			{"Helsinki (Grand Duchy)", 24.9, 60.2, true},
			{"Yerevan (Transcaucasia)", 44.5, 40.2, true},
			{"Vladivostok", 131.9, 43.1, true},
			{"Konigsberg (German)", 20.5, 54.7, false},
			{"Berlin", 13.4, 52.5, false},
			{"Stockholm", 18.1, 59.3, false},
			{"Beijing", 116.4, 39.9, false},
		}},
		{1914, "British Empire", []place{
			{"Ottawa", -75.7, 45.4, true},
			{"Delhi", 77.2, 28.6, true},
			{"Sydney", 151.2, -33.9, true},
			{"Cape Town", 18.4, -33.9, true},
			{"Lagos", 3.4, 6.5, true},
			{"Accra (Gold Coast)", -0.2, 5.6, true},
			{"Khartoum", 32.5, 15.6, true},
			{"Paris", 2.3, 48.9, false},
		}},
		{1914, "French colonial empire", []place{
			{"Dakar", -17.4, 14.7, true},
			{"Algiers", 3.1, 36.8, true},
			{"Antananarivo", 47.5, -18.9, true},
			{"Hanoi", 105.8, 21.0, true},
			{"Brazzaville", 15.3, -4.3, true},
			// British, German and independent neighbours must not be claimed.
			{"Accra (British)", -0.2, 5.6, false},
			{"Lagos (British)", 3.4, 6.5, false},
			{"Douala (German)", 9.7, 4.1, false},
			{"Bangkok (independent Siam)", 100.5, 13.8, false},
		}},
		{1942, "Axis-held Europe", []place{
			{"Berlin", 13.4, 52.5, true},
			{"Paris", 2.3, 48.9, true},
			{"Warsaw", 21.0, 52.2, true},
			{"Kyiv", 30.5, 50.5, true},
			{"Stalingrad", 44.5, 48.7, true},
			{"Oslo", 10.8, 59.9, true},
			// Neutrals and the unconquered.
			{"Stockholm (neutral)", 18.1, 59.3, false},
			{"Moscow", 37.6, 55.8, false},
			{"London", -0.1, 51.5, false},
			{"Ankara (neutral)", 32.9, 39.9, false},
		}},
		{1942, "Japanese maximum sphere of control", []place{
			{"Tokyo", 139.7, 35.7, true},
			// The land empire is the largest part of what this names.
			{"Shenyang (Manchukuo)", 123.4, 41.8, true},
			{"Beijing", 116.4, 39.9, true},
			{"Shanghai", 121.5, 31.2, true},
			{"Seoul", 127.0, 37.6, true},
			{"Singapore", 103.8, 1.4, true},
			{"Manila", 121.0, 14.6, true},
			{"Jakarta", 106.8, -6.2, true},
			{"Chongqing (Free China)", 106.6, 29.6, false},
			{"Darwin", 130.8, -12.5, false},
			{"Kolkata", 88.4, 22.6, false},
		}},
		{1990, "Soviet Union", []place{
			{"Moscow", 37.6, 55.8, true},
			{"Tallinn (Estonian SSR)", 24.8, 59.4, true},
			{"Riga", 24.1, 56.9, true},
			{"Yerevan (Armenian SSR)", 44.5, 40.2, true},
			{"Tashkent", 69.2, 41.3, true},
			{"Vladivostok", 131.9, 43.1, true},
			{"Warsaw (Warsaw Pact, not Soviet)", 21.0, 52.2, false},
			{"Helsinki", 24.9, 60.2, false},
			{"Kabul", 69.2, 34.5, false},
		}},
	}

	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("load seed: %v", err)
	}
	set, err := LoadGeo("../../data/geo", res.Entities)
	if err != nil {
		t.Fatalf("load geo: %v", err)
	}
	byYear := map[int]model.BorderLayer{}
	for _, l := range set.Borders {
		byYear[l.Year] = l
	}

	for _, tc := range cases {
		layer, ok := byYear[tc.year]
		if !ok {
			t.Errorf("no border layer for %d", tc.year)
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

func featurePolygons(t *testing.T, layer model.BorderLayer, name string) [][][][]float64 {
	t.Helper()
	for _, f := range layer.Features {
		if f.Name != name {
			continue
		}
		var g struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		}
		if err := json.Unmarshal(f.Geometry, &g); err != nil {
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
	return nil
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
