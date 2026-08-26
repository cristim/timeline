package bake

import (
	"encoding/json"
	"fmt"

	"wk/internal/model"
)

// Map layers (FE-4) are baked per time-step under the API-4 key shape
// layers/<layer>/<timestep>. Spec ARCH-3 calls for PMTiles there; until the
// OpenHistoricalMap ingest and the tippecanoe stage land (M4), the bodies are
// plain GeoJSON. The key scheme, the manifest timesteps and the client's
// nearest-step snap are the parts M4 has to keep, and they are the parts
// built here.
const BordersLayer = "borders"

// layerDoc is the served body: a GeoJSON FeatureCollection carrying the
// era's own metadata. MapLibre ignores the extra top-level member; the client
// reads it for the era label and for the coverage window that decides whether
// the snapshot may be shown at all.
type layerDoc struct {
	Type       string        `json:"type"`
	Properties layerDocProps `json:"properties"`
	Features   []layerFeat   `json:"features"`
}

type layerDocProps struct {
	Layer  string `json:"layer"`
	Year   int    `json:"year"`
	TFrom  int    `json:"t_from"`
	TTo    int    `json:"t_to"`
	Label  string `json:"label"`
	Source string `json:"source"`
}

type layerFeat struct {
	Type       string          `json:"type"`
	Properties layerFeatProps  `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type layerFeatProps struct {
	Name           string `json:"name"`
	Slug           string `json:"slug,omitempty"` // selectable entity, if any
	Representation string `json:"representation"`
}

// layerIndex is layers/<layer>/index.json: every time-step with its coverage
// window, so the client knows whether any snapshot covers the cursor before
// fetching one. The manifest already lists the years (API-0 timesteps), but
// not the windows, and the windows are what decide coverage - booting the
// whole-universe view would otherwise download Roman Britain only to discover
// it speaks for no date in sight. Era metadata belongs here rather than in
// the manifest for the same reason everything else does: this is immutable
// and cached for a year, the manifest is re-fetched every minute (API-5).
type layerIndex struct {
	Layer string          `json:"layer"`
	Steps []layerIndexRow `json:"steps"`
}

type layerIndexRow struct {
	Year  int    `json:"year"`
	TFrom int    `json:"t_from"`
	TTo   int    `json:"t_to"`
	Label string `json:"label"`
}

// LayerKey is the artifact key for one time-step, relative to /v/<dataset>/.
// The client computes the same string from the manifest (API-1/API-5).
func LayerKey(layer string, timestep int) string {
	return fmt.Sprintf("layers/%s/%d.json", layer, timestep)
}

// LayerIndexKey is the artifact key for a layer's time-step index.
func LayerIndexKey(layer string) string {
	return fmt.Sprintf("layers/%s/index.json", layer)
}

// bakeAreaLayer writes one artifact per time-step plus the index, and returns
// the time-steps for the manifest (API-0). Layers arrive sorted by year and
// window-disjoint from ingest.
func bakeAreaLayer(w *writer, dataset, layer string, layers []model.BorderLayer) ([]int, error) {
	if len(layers) == 0 {
		return nil, nil
	}
	steps := make([]int, 0, len(layers))
	index := layerIndex{Layer: layer, Steps: make([]layerIndexRow, 0, len(layers))}
	for _, l := range layers {
		doc := layerDoc{
			Type: "FeatureCollection",
			Properties: layerDocProps{
				Layer: layer, Year: l.Year, TFrom: l.TFrom, TTo: l.TTo,
				Label: l.Label, Source: l.Source,
			},
			Features: make([]layerFeat, 0, len(l.Features)),
		}
		for _, f := range l.Features {
			doc.Features = append(doc.Features, layerFeat{
				Type: "Feature",
				Properties: layerFeatProps{
					Name: f.Name, Slug: f.Slug, Representation: f.Representation,
				},
				Geometry: f.Geometry,
			})
		}
		key := fmt.Sprintf("v/%s/%s", dataset, LayerKey(layer, l.Year))
		if err := w.putJSON(key, doc); err != nil {
			return nil, err
		}
		steps = append(steps, l.Year)
		index.Steps = append(index.Steps, layerIndexRow{
			Year: l.Year, TFrom: l.TFrom, TTo: l.TTo, Label: l.Label,
		})
	}
	if err := w.putJSON(fmt.Sprintf("v/%s/%s", dataset, LayerIndexKey(layer)), index); err != nil {
		return nil, err
	}
	return steps, nil
}
