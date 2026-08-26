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
	if err := loadBorders(filepath.Join(dir, "borders"), bySeedID, set); err != nil {
		return nil, err
	}
	if err := loadFronts(filepath.Join(dir, "fronts"), bySeedID, set); err != nil {
		return nil, err
	}
	return set, nil
}

func loadBorders(dir string, bySeedID map[string]*model.Entity, set *model.GeoSet) error {
	paths, err := geojsonPaths(dir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		year, err := strconv.Atoi(strings.TrimSuffix(name, ".geojson"))
		if err != nil {
			return fmt.Errorf("border file %s: name must be <year>.geojson", name)
		}
		var f borderFile
		if err := readJSON(path, &f); err != nil {
			return err
		}
		if f.Type != "FeatureCollection" {
			return fmt.Errorf("border file %s: type %q, want FeatureCollection", name, f.Type)
		}
		p := f.Properties
		switch {
		case p.Year == nil || p.TFrom == nil || p.TTo == nil:
			return fmt.Errorf("border file %s: properties need year, t_from and t_to", name)
		case *p.Year != year:
			return fmt.Errorf("border file %s: properties.year is %d", name, *p.Year)
		case *p.TFrom > year || year > *p.TTo:
			return fmt.Errorf("border file %s: year %d outside its own window %d..%d", name, year, *p.TFrom, *p.TTo)
		case p.Label == "" || p.Source == "":
			return fmt.Errorf("border file %s: properties need a label and a source", name)
		case len(f.Features) == 0:
			return fmt.Errorf("border file %s: no features", name)
		}

		layer := model.BorderLayer{
			Year: year, TFrom: *p.TFrom, TTo: *p.TTo, Label: p.Label, Source: p.Source,
		}
		for i, feat := range f.Features {
			where := fmt.Sprintf("border file %s feature %d", name, i)
			if err := checkFeature(where, feat, bySeedID); err != nil {
				return err
			}
			if feat.Properties.Name == "" {
				return fmt.Errorf("%s: properties.name is required", where)
			}
			if err := checkPolygons(where, feat.Geometry); err != nil {
				return err
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
		set.Borders = append(set.Borders, layer)
	}

	sort.Slice(set.Borders, func(i, j int) bool { return set.Borders[i].Year < set.Borders[j].Year })
	// Comparing adjacent pairs is enough: every file already satisfies
	// TFrom <= Year <= TTo, which makes disjointness transitive once sorted.
	for i := 1; i < len(set.Borders); i++ {
		if prev, cur := set.Borders[i-1], set.Borders[i]; cur.TFrom <= prev.TTo {
			return fmt.Errorf("border windows overlap: %d covers %d..%d, %d covers %d..%d",
				prev.Year, prev.TFrom, prev.TTo, cur.Year, cur.TFrom, cur.TTo)
		}
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
	// Glob returns nothing for a missing directory. A mis-mounted volume or a
	// wrong --geo must not bake as an empty layer.
	if len(paths) == 0 {
		return nil, fmt.Errorf("geo dir %s holds no .geojson files", dir)
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
