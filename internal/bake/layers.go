package bake

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf16"

	"wk/internal/model"
)

const (
	BordersLayer       = "borders"
	PaleoLayer         = "paleocoast"
	PMTilesContentType = "application/vnd.pmtiles"
	BordersAttribution = "historical-basemaps (GPL-3.0): https://github.com/aourednik/historical-basemaps · OpenHistoricalMap (CC0/public domain): https://www.openhistoricalmap.org/"
	PaleoAttribution   = "Merdith et al. 2021 (CC-BY 4.0): https://doi.org/10.1016/j.earscirev.2020.103477"
)

// LayerCompiler compiles one area-layer slice into a PMTiles archive.
type LayerCompiler interface {
	Compile(context.Context, LayerCompileRequest) ([]byte, error)
}

// LayerCompileRequest contains the complete deterministic input for one archive.
type LayerCompileRequest struct {
	Layer       string
	Year        int
	TFrom       int
	TTo         int
	Label       string
	Source      string
	Attribution string
	GeoJSON     []byte
}

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
	Color          string `json:"color,omitempty"`
	RenderRank     int    `json:"render_rank"`
	Source         string `json:"source,omitempty"`
	SourceID       string `json:"source_id,omitempty"`
	License        string `json:"license,omitempty"`
	Attribution    string `json:"attribution,omitempty"`
	SourceURL      string `json:"source_url,omitempty"`
	RetrievedAt    string `json:"retrieved_at,omitempty"`
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
	Year   int    `json:"year"`
	TFrom  int    `json:"t_from"`
	TTo    int    `json:"t_to"`
	Label  string `json:"label"`
	Source string `json:"source"`
}

// LayerKey is the artifact key for one time-step, relative to /v/<dataset>/.
// The client computes the same string from the manifest (API-1/API-5).
func LayerKey(layer string, timestep int) string {
	return fmt.Sprintf("layers/%s/%d.pmtiles", layer, timestep)
}

// LayerIndexKey is the artifact key for a layer's time-step index.
func LayerIndexKey(layer string) string {
	return fmt.Sprintf("layers/%s/index.json", layer)
}

// bakeAreaLayer writes one artifact per time-step plus the index, and returns
// the time-steps for the manifest (API-0). Layers arrive sorted by year and
// window-disjoint from ingest.
func bakeAreaLayer(ctx context.Context, w *writer, compiler LayerCompiler, dataset, layer string, layers []model.BorderLayer) ([]int, error) {
	if len(layers) == 0 {
		return nil, nil
	}
	if compiler == nil {
		return nil, fmt.Errorf("%s layer compiler is required", layer)
	}
	attribution, err := layerAttribution(layer)
	if err != nil {
		return nil, err
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
			color := ""
			if layer == BordersLayer {
				color = polityColor(f.Name)
			}
			doc.Features = append(doc.Features, layerFeat{
				Type: "Feature",
				Properties: layerFeatProps{
					Name: f.Name, Slug: f.Slug, Representation: f.Representation, Color: color,
					RenderRank: f.RenderRank, Source: f.Source, SourceID: f.SourceID,
					License: f.License, Attribution: f.Attribution, SourceURL: f.SourceURL,
					RetrievedAt: f.RetrievedAt,
				},
				Geometry: f.Geometry,
			})
		}
		geoJSON, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshal %s layer %d: %w", layer, l.Year, err)
		}
		body, err := compiler.Compile(ctx, LayerCompileRequest{
			Layer: layer, Year: l.Year, TFrom: l.TFrom, TTo: l.TTo,
			Label: l.Label, Source: l.Source, Attribution: attribution, GeoJSON: geoJSON,
		})
		if err != nil {
			return nil, fmt.Errorf("compile %s layer %d: %w", layer, l.Year, err)
		}
		key := fmt.Sprintf("v/%s/%s", dataset, LayerKey(layer, l.Year))
		if err := w.putBytes(key, body, PMTilesContentType); err != nil {
			return nil, err
		}
		steps = append(steps, l.Year)
		index.Steps = append(index.Steps, layerIndexRow{
			Year: l.Year, TFrom: l.TFrom, TTo: l.TTo, Label: l.Label, Source: l.Source,
		})
	}
	if err := w.flush(); err != nil {
		return nil, err
	}
	if err := w.putJSON(fmt.Sprintf("v/%s/%s", dataset, LayerIndexKey(layer)), index); err != nil {
		return nil, err
	}
	return steps, nil
}

func layerAttribution(layer string) (string, error) {
	switch layer {
	case BordersLayer:
		return BordersAttribution, nil
	case PaleoLayer:
		return PaleoAttribution, nil
	default:
		return "", fmt.Errorf("unknown area layer %q", layer)
	}
}

func polityColor(name string) string {
	hash := uint32(0x811c9dc5)
	for _, codeUnit := range utf16.Encode([]rune(name)) {
		hash ^= uint32(codeUnit)
		hash *= 0x01000193
	}
	return fmt.Sprintf("hsl(%d, 34%%, 45%%)", hash%360)
}
