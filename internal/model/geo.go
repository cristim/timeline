package model

import "encoding/json"

// ValidLonLat reports whether a coordinate pair is a usable WGS84 position.
func ValidLonLat(lon, lat float64) bool {
	return lon >= -180 && lon <= 180 && lat >= -90 && lat <= 90
}

// Curated geometry (DM-7). Two shapes, because they answer different
// questions: a BorderLayer is a world-state snapshot that the whole map shows
// for a span of time, while a FrontPosition is one dated geometry record
// belonging to a single entity.

// GeoSet is everything data/geo holds, resolved against the entity table.
type GeoSet struct {
	Borders []BorderLayer // ascending by Year, non-overlapping windows
	// Paleo holds reconstructed coastlines for deep time, in the same shape
	// as Borders and covering the span before the political record starts.
	Paleo  []BorderLayer
	Fronts map[string][]FrontPosition
}

// BorderLayer is one source GeoJSON time-step, baked to
// layers/<layer>/<year>.pmtiles (API-4 key shape) and listed in the manifest's
// timesteps (API-0). TFrom/TTo bound the years the snapshot actually speaks
// for, so the client can report "no data" instead of drawing the nearest era
// at a date it never covered.
type BorderLayer struct {
	Year     int
	TFrom    int
	TTo      int
	Label    string
	Source   string
	Features []BorderFeature
}

type BorderFeature struct {
	Name           string
	Entity         string // seed id, optional
	Slug           string // resolved from Entity at bake time
	Representation string
	// Geometry is passed through verbatim: the client renders it, nothing
	// server-side ever reads inside a polygon.
	Geometry json.RawMessage
}

// FrontPosition is one dated front line for a war entity: a DM-7 geometry
// record (geometry + valid_from + representation + source), baked onto the
// entity document. Coordinates are typed because the client interpolates
// between consecutive positions vertex by vertex, which only means anything
// if every position in a sequence has the same vertex count.
type FrontPosition struct {
	ValidFrom      float64 // seconds since 1970
	Label          string
	Source         string
	Representation string
	Coordinates    [][2]float64
}
