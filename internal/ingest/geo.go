package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"wk/internal/model"
)

// Curated geometry lives in data/geo (see its README), outside the seed and so
// outside seed_version. It gets its own validation instead: everything below
// is fatal, because a silently-dropped era, a ring the tile stage will read as
// a hole, or a front sequence the client cannot interpolate are all worse than
// a failed bake.

type geoFeature struct {
	Type       string          `json:"type"`
	Properties geoFeatureProps `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type geoFeatureProps struct {
	Name           string          `json:"name"`
	Entity         string          `json:"entity"`
	Representation string          `json:"representation"`
	ValidFrom      json.RawMessage `json:"valid_from"`
	Label          string          `json:"label"`
}

type borderFile struct {
	Type       string       `json:"type"`
	Properties borderProps  `json:"properties"`
	Features   []geoFeature `json:"features"`
}

// Pointers distinguish "absent" from a legitimate year 0.
type borderProps struct {
	Year   *int   `json:"year"`
	TFrom  *int   `json:"t_from"`
	TTo    *int   `json:"t_to"`
	Label  string `json:"label"`
	Source string `json:"source"`
}

type frontFile struct {
	Type       string       `json:"type"`
	Properties frontProps   `json:"properties"`
	Features   []geoFeature `json:"features"`
}

type frontProps struct {
	Entity string `json:"entity"`
	Source string `json:"source"`
}

// Geometries decode into plain [][]float64 rather than [][2]float64: Go's json
// zero-pads a short fixed-size array, so [[10],[12,52]] would silently become
// a vertex at 0N instead of an error.
type geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// LoadGeo reads data/geo and resolves every entity reference against the
// already-ingested entity table.
func LoadGeo(dir string, entities []*model.Entity) (*model.GeoSet, error) {
	bySeedID := map[string]*model.Entity{}
	for _, e := range entities {
		bySeedID[e.SeedID] = e
	}
	set := &model.GeoSet{Fronts: map[string][]model.FrontPosition{}}
	var err error
	if set.Borders, err = loadAreaSlices(filepath.Join(dir, "borders"), bySeedID); err != nil {
		return nil, err
	}
	ohmDir := filepath.Join(dir, "ohm")
	if _, statErr := os.Stat(ohmDir); statErr == nil {
		lastPoliticalYear := set.Borders[len(set.Borders)-1].TTo
		if set.OHM, _, err = loadOHM(ohmDir, lastPoliticalYear); err != nil {
			return nil, err
		}
		if set.Borders, err = compositeOHMBorders(set.Borders, set.OHM); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	// An absent paleo directory means the deep-time layer is not configured,
	// which is a legitimate bake; a present but broken one is still fatal.
	// Insisting both layers EXIST is `baker geo-verify`'s job, and CI runs it
	// before it trusts a fetch.
	paleoDir := filepath.Join(dir, "paleo")
	if _, statErr := os.Stat(paleoDir); statErr == nil {
		if set.Paleo, err = loadAreaSlices(paleoDir, bySeedID); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	// The two layers answer for disjoint spans, and the client picks between
	// them by coverage window alone. An overlap would make that choice
	// arbitrary, so it is fatal here rather than a rendering surprise.
	if len(set.Borders) > 0 && len(set.Paleo) > 0 {
		if last := set.Paleo[len(set.Paleo)-1]; last.TTo >= set.Borders[0].TFrom {
			return nil, fmt.Errorf("paleo coverage runs to %d but the political layer starts at %d; the two layers must not overlap",
				last.TTo, set.Borders[0].TFrom)
		}
	}
	if err := loadFronts(filepath.Join(dir, "fronts"), bySeedID, set); err != nil {
		return nil, err
	}
	return set, nil
}

// loadAreaSlices reads one directory of <year>.geojson world-state snapshots,
// ascending by year with non-overlapping coverage windows. Both area layers
// use this format, so both get the same validation.
func loadAreaSlices(dir string, bySeedID map[string]*model.Entity) ([]model.BorderLayer, error) {
	var slices []model.BorderLayer
	paths, err := geojsonPaths(dir)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		year, err := strconv.Atoi(strings.TrimSuffix(name, ".geojson"))
		if err != nil {
			return nil, fmt.Errorf("slice file %s: name must be <year>.geojson", name)
		}
		var f borderFile
		if err := readJSON(path, &f); err != nil {
			return nil, err
		}
		if f.Type != "FeatureCollection" {
			return nil, fmt.Errorf("slice file %s: type %q, want FeatureCollection", name, f.Type)
		}
		p := f.Properties
		switch {
		case p.Year == nil || p.TFrom == nil || p.TTo == nil:
			return nil, fmt.Errorf("slice file %s: properties need year, t_from and t_to", name)
		case *p.Year != year:
			return nil, fmt.Errorf("slice file %s: properties.year is %d", name, *p.Year)
		case *p.TFrom > year || year > *p.TTo:
			return nil, fmt.Errorf("slice file %s: year %d outside its own window %d..%d", name, year, *p.TFrom, *p.TTo)
		case p.Label == "" || p.Source == "":
			return nil, fmt.Errorf("slice file %s: properties need a label and a source", name)
		case len(f.Features) == 0:
			return nil, fmt.Errorf("slice file %s: no features", name)
		}

		layer := model.BorderLayer{
			Year: year, TFrom: *p.TFrom, TTo: *p.TTo, Label: p.Label, Source: p.Source,
		}
		for i, feat := range f.Features {
			where := fmt.Sprintf("border file %s feature %d", name, i)
			if err := checkFeature(where, feat, bySeedID); err != nil {
				return nil, err
			}
			if feat.Properties.Name == "" {
				return nil, fmt.Errorf("%s: properties.name is required", where)
			}
			if err := checkPolygons(where, feat.Geometry); err != nil {
				return nil, err
			}
			bf := model.BorderFeature{
				Name:           feat.Properties.Name,
				Entity:         feat.Properties.Entity,
				Representation: feat.Properties.Representation,
				Geometry:       feat.Geometry,
			}
			if bf.Entity != "" {
				bf.Slug = bySeedID[bf.Entity].Slug
			}
			layer.Features = append(layer.Features, bf)
		}
		slices = append(slices, layer)
	}

	sort.Slice(slices, func(i, j int) bool { return slices[i].Year < slices[j].Year })
	if err := validateAreaCoverage(dir, slices); err != nil {
		return nil, err
	}
	return slices, nil
}

func compositeOHMBorders(borders, ohm []model.BorderLayer) ([]model.BorderLayer, error) {
	composited := make([]model.BorderLayer, 0, len(borders)+len(ohm))
	for _, border := range borders {
		starts := []int{border.TFrom}
		for _, detail := range ohm {
			if detail.TFrom > border.TFrom && detail.TFrom <= border.TTo {
				starts = append(starts, detail.TFrom)
			}
		}
		sort.Ints(starts)

		for i, start := range starts {
			end := border.TTo
			if i+1 < len(starts) {
				end = starts[i+1] - 1
			}
			year := border.Year
			if start != border.TFrom {
				year = start
			}
			segment := model.BorderLayer{
				Year: year, TFrom: start, TTo: end, Label: border.Label, Source: border.Source,
				Features: append([]model.BorderFeature(nil), border.Features...),
			}
			for _, detail := range ohm {
				if start < detail.TFrom || start > detail.TTo {
					continue
				}
				segment.Label += fmt.Sprintf(" · London boundaries · %d · OpenHistoricalMap", detail.Year)
				segment.Source += " + " + detail.Source
				segment.Features = append(segment.Features, detail.Features...)
				break
			}
			composited = append(composited, segment)
		}
	}
	if err := validateAreaCoverage("composited political borders", composited); err != nil {
		return nil, err
	}
	return composited, nil
}

func validateAreaCoverage(source string, slices []model.BorderLayer) error {
	// Windows must TILE, not merely avoid each other: every year between the
	// first and last slice belongs to exactly one of them. Scrubbing the cursor
	// has to walk the whole layer without the map blanking between slices, and
	// a gap here is also how a half-finished fetch would show up - the ingest
	// is the last place that can tell a missing slice from a real silence.
	for i := 1; i < len(slices); i++ {
		prev, cur := slices[i-1], slices[i]
		if cur.TFrom == prev.TTo+1 {
			continue
		}
		verb := "leave a gap"
		if cur.TFrom <= prev.TTo {
			verb = "overlap"
		}
		return fmt.Errorf("%s: coverage windows %s: %d covers %d..%d, %d covers %d..%d (each slice must run to the year before the next)",
			source, verb, prev.Year, prev.TFrom, prev.TTo, cur.Year, cur.TFrom, cur.TTo)
	}
	return nil
}

func loadFronts(dir string, bySeedID map[string]*model.Entity, set *model.GeoSet) error {
	paths, err := geojsonPaths(dir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		var f frontFile
		if err := readJSON(path, &f); err != nil {
			return err
		}
		if f.Type != "FeatureCollection" {
			return fmt.Errorf("front file %s: type %q, want FeatureCollection", name, f.Type)
		}
		if f.Properties.Source == "" {
			return fmt.Errorf("front file %s: properties.source is required", name)
		}
		owner, ok := bySeedID[f.Properties.Entity]
		if !ok {
			return fmt.Errorf("front file %s: properties.entity %q is not a seed entity", name, f.Properties.Entity)
		}
		if _, dup := set.Fronts[owner.SeedID]; dup {
			return fmt.Errorf("front file %s: %q already has front positions", name, owner.SeedID)
		}
		if len(f.Features) < 2 {
			return fmt.Errorf("front file %s: %d positions, need at least 2 to interpolate between", name, len(f.Features))
		}

		positions := make([]model.FrontPosition, 0, len(f.Features))
		for i, feat := range f.Features {
			where := fmt.Sprintf("front file %s feature %d", name, i)
			if err := checkFeature(where, feat, bySeedID); err != nil {
				return err
			}
			if feat.Properties.Entity != "" {
				return fmt.Errorf("%s: ownership comes from the file's properties.entity, not a feature's", where)
			}
			coords, err := lineCoordinates(where, feat.Geometry)
			if err != nil {
				return err
			}
			// Vertex n of one date must mean the same place on the line as
			// vertex n of the next, or interpolation is meaningless.
			if i > 0 && len(coords) != len(positions[0].Coordinates) {
				return fmt.Errorf("%s: %d coordinates, but the first position has %d; every position in a sequence must share a vertex count",
					where, len(coords), len(positions[0].Coordinates))
			}
			if len(feat.Properties.ValidFrom) == 0 {
				return fmt.Errorf("%s: properties.valid_from is required", where)
			}
			t, err := model.ParseSeedTime(feat.Properties.ValidFrom)
			if err != nil {
				return fmt.Errorf("%s: valid_from: %w", where, err)
			}
			if t < owner.T0 || t > owner.T1 {
				return fmt.Errorf("%s: valid_from %s is outside %s's own time range",
					where, feat.Properties.ValidFrom, owner.SeedID)
			}
			if i > 0 && t <= positions[i-1].ValidFrom {
				return fmt.Errorf("%s: valid_from %s is not after the previous position", where, feat.Properties.ValidFrom)
			}
			positions = append(positions, model.FrontPosition{
				ValidFrom:      t,
				Label:          feat.Properties.Label,
				Source:         f.Properties.Source,
				Representation: feat.Properties.Representation,
				Coordinates:    coords,
			})
		}
		set.Fronts[owner.SeedID] = positions
	}
	return nil
}

func checkFeature(where string, f geoFeature, bySeedID map[string]*model.Entity) error {
	if f.Type != "Feature" {
		return fmt.Errorf("%s: type %q, want Feature", where, f.Type)
	}
	if len(f.Geometry) == 0 {
		return fmt.Errorf("%s: no geometry", where)
	}
	if !model.Representations[f.Properties.Representation] {
		return fmt.Errorf("%s: unknown representation %q (DM-7)", where, f.Properties.Representation)
	}
	if e := f.Properties.Entity; e != "" && bySeedID[e] == nil {
		return fmt.Errorf("%s: entity %q is not a seed entity", where, e)
	}
	return nil
}

// checkPolygons validates an area geometry that is about to be written into an
// immutable artifact verbatim. Nothing downstream re-reads it, so this is the
// only place a malformed ring can still be caught.
func checkPolygons(where string, raw json.RawMessage) error {
	var g geometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("%s: geometry: %w", where, err)
	}
	var polys [][][][]float64
	switch g.Type {
	case "Polygon":
		var p [][][]float64
		if err := json.Unmarshal(g.Coordinates, &p); err != nil {
			return fmt.Errorf("%s: polygon coordinates: %w", where, err)
		}
		polys = [][][][]float64{p}
	case "MultiPolygon":
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return fmt.Errorf("%s: multipolygon coordinates: %w", where, err)
		}
	default:
		return fmt.Errorf("%s: geometry type %q, want Polygon or MultiPolygon", where, g.Type)
	}
	if len(polys) == 0 {
		return fmt.Errorf("%s: empty coordinates", where)
	}
	for pi, poly := range polys {
		if len(poly) == 0 {
			return fmt.Errorf("%s: polygon %d has no rings", where, pi)
		}
		for ri, ring := range poly {
			at := fmt.Sprintf("%s: polygon %d ring %d", where, pi, ri)
			if len(ring) < 4 {
				return fmt.Errorf("%s: %d positions, want at least 4", at, len(ring))
			}
			if err := checkPositions(at, ring); err != nil {
				return err
			}
			if first, last := ring[0], ring[len(ring)-1]; first[0] != last[0] || first[1] != last[1] {
				return fmt.Errorf("%s: not closed (%v != %v)", at, first, last)
			}
			// RFC 7946 3.1.6. The M4 PMTiles stage reads winding to tell a
			// hole from an island, so a backwards ring is a latent hole.
			if ccw := ringIsCCW(ring); ccw != (ri == 0) {
				return fmt.Errorf("%s: winds the wrong way (RFC 7946: exterior counterclockwise, holes clockwise)", at)
			}
			if i, j := selfIntersection(ring); i >= 0 {
				return fmt.Errorf("%s: segments %d and %d cross (RFC 7946: a ring must not self-intersect); "+
					"a bowtie renders as a torn fill", at, i, j)
			}
		}
	}
	return nil
}

func lineCoordinates(where string, raw json.RawMessage) ([][2]float64, error) {
	var g geometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("%s: geometry: %w", where, err)
	}
	if g.Type != "LineString" {
		return nil, fmt.Errorf("%s: geometry type %q, want LineString", where, g.Type)
	}
	var ring [][]float64
	if err := json.Unmarshal(g.Coordinates, &ring); err != nil {
		return nil, fmt.Errorf("%s: linestring coordinates: %w", where, err)
	}
	if len(ring) < 2 {
		return nil, fmt.Errorf("%s: %d coordinates, want at least 2", where, len(ring))
	}
	if err := checkPositions(where, ring); err != nil {
		return nil, err
	}
	out := make([][2]float64, len(ring))
	for i, c := range ring {
		out[i] = [2]float64{c[0], c[1]}
	}
	return out, nil
}

func checkPositions(where string, ring [][]float64) error {
	for _, c := range ring {
		if len(c) != 2 {
			return fmt.Errorf("%s: %v is not a [lon,lat] pair", where, c)
		}
		if !model.ValidLonLat(c[0], c[1]) {
			return fmt.Errorf("%s: %v is not a valid [lon,lat]", where, c)
		}
	}
	return nil
}

// selfIntersection returns the indexes of the first pair of non-adjacent
// segments that cross, or (-1, -1). It tests for proper crossings only, not
// for segments that merely touch at a point: crossings are the failure that
// tears a rendered fill, and a coarse hand-traced outline that grazes itself
// at a vertex is not worth blocking a bake over.
func selfIntersection(ring [][]float64) (int, int) {
	n := len(ring) - 1 // the closing repeat is not its own segment
	for i := 0; i < n; i++ {
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue // first and last segments share a vertex
			}
			if segmentsCross(ring[i], ring[i+1], ring[j], ring[j+1]) {
				return i, j
			}
		}
	}
	return -1, -1
}

func segmentsCross(a, b, c, d []float64) bool {
	d1, d2 := cross(c, d, a), cross(c, d, b)
	d3, d4 := cross(a, b, c), cross(a, b, d)
	return ((d1 > 0) != (d2 > 0)) && ((d3 > 0) != (d4 > 0))
}

// cross is the z of (q-p) x (r-p): the side of line pq that r falls on.
func cross(p, q, r []float64) float64 {
	return (q[0]-p[0])*(r[1]-p[1]) - (q[1]-p[1])*(r[0]-p[0])
}

// ringIsCCW: the shoelace sum over a closed ring is negative when the ring
// winds counterclockwise in lon/lat.
func ringIsCCW(ring [][]float64) bool {
	sum := 0.0
	for i := 0; i+1 < len(ring); i++ {
		sum += (ring[i+1][0] - ring[i][0]) * (ring[i+1][1] + ring[i][1])
	}
	return sum < 0
}

func geojsonPaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.geojson"))
	if err != nil {
		return nil, err
	}
	// Glob returns nothing for a missing directory. A mis-mounted volume, a
	// wrong --geo or an unfetched layer must not bake as an empty layer.
	if len(paths) == 0 {
		return nil, fmt.Errorf("geo dir %s holds no .geojson files; the fetched layers are not in the repo - run `make fetch-geo`", dir)
	}
	sort.Strings(paths) // deterministic ingest order (SRC-3)
	return paths, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
