// Package bake materializes the serving artifacts (ARCH-4): viewport chunks,
// entity documents, aliases, and the dataset manifest. Everything it writes is
// immutable under /v/<dataset>/ (ARCH-2).
package bake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"wk/internal/model"
	"wk/internal/rankzoom"
)

// Sink abstracts the artifact destination: S3/MinIO in real runs, memory in
// tests. Put returns whether the object actually changed (incremental bake).
type Sink interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (changed bool, err error)
}

type Stats struct {
	Written   int
	Unchanged int
}

// BasemapArtifact is a verified archive and the metadata published with it.
type BasemapArtifact struct {
	Key         string
	Source      string
	Attribution string
	SHA256      string
	Body        []byte
}

func (s *Stats) add(changed bool) {
	if changed {
		s.Written++
	} else {
		s.Unchanged++
	}
}

// ChunkItem is one renderable object inside a viewport chunk (API-1).
type ChunkItem struct {
	Slug       string    `json:"slug"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	T0         float64   `json:"t0"` // seconds since 1970 (API-5 wire codec)
	T1         float64   `json:"t1"`
	Precision  string    `json:"precision"`
	Status     string    `json:"status"`
	Point      []float64 `json:"point,omitempty"`
	Categories []string  `json:"categories"`
	Importance float64   `json:"importance"`
	MediaThumb string    `json:"media_thumb,omitempty"`
	ChildCount int       `json:"child_count,omitempty"`
}

type chunkFile struct {
	Items []ChunkItem `json:"items"`
}

// Run bakes all artifacts for the dataset and returns the manifest to publish.
// geo must be non-nil; an empty GeoSet bakes no layers and needs no compiler.
// A non-empty area layer requires a compiler.
// A non-nil goldens file is evaluated against the baked chunks; any failure
// aborts before the manifest exists, so a failing golden view cannot publish
// (ZOOM-5).
func Run(ctx context.Context, sink Sink, compiler LayerCompiler, dataset, seedVersion string, entities []*model.Entity, basemap BasemapArtifact, geo *model.GeoSet, goldens *GoldenFile) (manifest *model.Manifest, stats *Stats, err error) {
	stats = &Stats{}

	childCount := map[string]int{}
	for _, e := range entities {
		for _, r := range e.Rel {
			if r.Type == "part_of" {
				childCount[r.Target]++
			}
		}
	}

	goldenKeys, err := goldenKeySet(goldens)
	if err != nil {
		return nil, stats, err
	}
	w := newWriter(ctx, sink, stats)
	defer func() {
		waitErr := w.wait()
		if waitErr == nil {
			return
		}
		manifest = nil
		if err == nil {
			err = waitErr
		} else if !errors.Is(err, waitErr) {
			err = errors.Join(err, waitErr)
		}
	}()
	if err := w.putBytes(fmt.Sprintf("v/%s/%s", dataset, basemap.Key), basemap.Body, PMTilesContentType); err != nil {
		return nil, stats, err
	}
	buckets, captured, err := bakeChunks(w, dataset, entities, childCount, goldenKeys)
	if err != nil {
		return nil, stats, err
	}
	if err := validateWindowRuns(buckets); err != nil {
		return nil, stats, err
	}
	goldenStatus := ""
	if goldens != nil {
		if fails := Evaluate(goldens, seedVersion, captured); len(fails) > 0 {
			// Uploaded chunks are harmless without a manifest repoint (ARCH-2);
			// aborting here is what "a failing golden view blocks publish" means.
			return nil, stats, fmt.Errorf("%s", formatFails(fails))
		}
		goldenStatus = "pass"
	}

	if err := bakeEntityDocs(w, dataset, entities, geo); err != nil {
		return nil, stats, err
	}
	shards, err := bakeSearch(w, dataset, entities)
	if err != nil {
		return nil, stats, err
	}
	layers := []string{}
	timesteps := map[string][]int{}
	// Two area layers of the same shape covering disjoint spans of time:
	// political borders through recorded history, reconstructed coastlines
	// before it. The client picks whichever one's window holds the cursor.
	for _, l := range []struct {
		name   string
		slices []model.BorderLayer
	}{
		{BordersLayer, geo.Borders},
		{PaleoLayer, geo.Paleo},
	} {
		steps, err := bakeAreaLayer(ctx, w, compiler, dataset, l.name, l.slices)
		if err != nil {
			return nil, stats, err
		}
		if len(steps) > 0 {
			layers = append(layers, l.name)
			timesteps[l.name] = steps
		}
	}
	if err := w.putJSON(fmt.Sprintf("v/%s/aliases.json", dataset), map[string]string{}); err != nil {
		return nil, stats, err
	}
	m := &model.Manifest{
		Dataset:     dataset,
		SeedVersion: seedVersion,
		Basemap: model.BasemapDescriptor{
			Key: basemap.Key, Source: basemap.Source,
			Attribution: basemap.Attribution, SHA256: basemap.SHA256,
		},
		Buckets:      buckets,
		Categories:   categorySet(entities),
		Layers:       layers,
		Timesteps:    timesteps,
		SearchShards: shards,
		GoldenViews:  goldenStatus,
		Counts: map[string]int{
			"entities": len(entities),
		},
	}
	return m, stats, nil
}

// chunkRelKey is the chunk artifact key relative to /v/<dataset>/ (API-1).
func chunkRelKey(bucketID string, window int64, cat string) string {
	return fmt.Sprintf("chunks/%s/%d/world/%s.json", bucketID, window, cat)
}

// bakeChunks writes chunk artifacts and returns the bucket table with
// per-category window runs, plus the chunks named by goldenKeys for evaluation.
func bakeChunks(w *writer, dataset string, entities []*model.Entity, childCount map[string]int, goldenKeys map[string]bool) ([]model.Bucket, map[string]chunkFile, error) {
	type cell struct {
		bucket int
		window int64
		cat    string // one category or "all"
	}
	cells := map[cell][]*model.Entity{}

	for _, e := range entities {
		for b := e.BucketMin; b <= e.BucketMax; b++ {
			bk := model.Buckets[b]
			w0, w1 := bk.WindowIndex(e.T0), bk.WindowIndex(e.T1)
			if w1-w0 > rankzoom.MaxWindowsPerEntity { // guarded in Bucketize; belt and braces
				return nil, nil, fmt.Errorf("entity %q: window explosion at %s", e.Slug, bk.ID)
			}
			for w := w0; w <= w1; w++ {
				for _, c := range e.Categories {
					cells[cell{b, w, c}] = append(cells[cell{b, w, c}], e)
				}
				cells[cell{b, w, "all"}] = append(cells[cell{b, w, "all"}], e)
			}
		}
	}

	type group struct {
		bucket int
		cat    string
	}
	windowsOf := map[group][]int64{}
	for c := range cells {
		g := group{c.bucket, c.cat}
		windowsOf[g] = append(windowsOf[g], c.window)
	}
	groups := make([]group, 0, len(windowsOf))
	for g := range windowsOf {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].bucket != groups[j].bucket {
			return groups[i].bucket < groups[j].bucket
		}
		return groups[i].cat < groups[j].cat
	})

	runs := make([]map[string][]model.WindowRun, len(model.Buckets))
	for i := range runs {
		runs[i] = map[string][]model.WindowRun{}
	}
	captured := map[string]chunkFile{}

	for _, g := range groups {
		ws := windowsOf[g]
		sort.Slice(ws, func(i, j int) bool { return ws[i] < ws[j] })
		bucketID := model.Buckets[g.bucket].ID

		var anchorWindow, lastWindow int64
		var anchorBody []byte

		emit := func() error {
			runs[g.bucket][g.cat] = append(runs[g.bucket][g.cat], model.WindowRun{anchorWindow, lastWindow})
			return w.putBytes(fmt.Sprintf("v/%s/%s", dataset, chunkRelKey(bucketID, anchorWindow, g.cat)),
				anchorBody, "application/json")
		}

		for _, win := range ws {
			items := rankCell(cells[cell{g.bucket, win, g.cat}], g.cat == "all", childCount)
			file := chunkFile{Items: items}
			body, err := json.Marshal(file)
			if err != nil {
				return nil, nil, fmt.Errorf("marshal chunk %s: %w", chunkRelKey(bucketID, win, g.cat), err)
			}
			if goldenKeys[chunkRelKey(bucketID, win, g.cat)] {
				captured[chunkRelKey(bucketID, win, g.cat)] = file
			}
			if anchorBody != nil && win == lastWindow+1 && bytes.Equal(body, anchorBody) {
				lastWindow = win
				continue
			}
			if anchorBody != nil {
				if err := emit(); err != nil {
					return nil, nil, err
				}
			}
			anchorWindow, lastWindow, anchorBody = win, win, body
		}
		if anchorBody != nil {
			if err := emit(); err != nil {
				return nil, nil, err
			}
		}
	}

	out := make([]model.Bucket, len(model.Buckets))
	for i, b := range model.Buckets {
		b.Windows = runs[i]
		out[i] = b
	}
	return out, captured, nil
}

func validateWindowRuns(buckets []model.Bucket) error {
	for _, bucket := range buckets {
		for cat, runs := range bucket.Windows {
			prevEnd := int64(0)
			for i, run := range runs {
				start, end := run.Start(), run.End()
				if start < -model.MaxSafeInteger || start > model.MaxSafeInteger ||
					end < -model.MaxSafeInteger || end > model.MaxSafeInteger {
					return fmt.Errorf("bucket %s category %s run %d is outside JSON safe integer bounds", bucket.ID, cat, i)
				}
				if end < start {
					return fmt.Errorf("bucket %s category %s run %d ends before it starts", bucket.ID, cat, i)
				}
				if i > 0 && start <= prevEnd {
					return fmt.Errorf("bucket %s category %s run %d overlaps or repeats a previous run", bucket.ID, cat, i)
				}
				prevEnd = end
			}
		}
	}
	return nil
}

// rankCell orders a cell's entities and applies the per-chunk cap and, for
// "all" chunks, the diversity guard (ZOOM-4). Deterministic: importance desc,
// slug asc.
func rankCell(es []*model.Entity, diversity bool, childCount map[string]int) []ChunkItem {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Importance != es[j].Importance {
			return es[i].Importance > es[j].Importance
		}
		return es[i].Slug < es[j].Slug
	})
	perCat := map[string]int{}
	items := make([]ChunkItem, 0, min(len(es), rankzoom.ChunkCap))
	seen := map[string]bool{}
	for _, e := range es {
		if len(items) >= rankzoom.ChunkCap {
			break
		}
		if seen[e.Slug] { // an entity can reach a cell via multiple categories
			continue
		}
		if diversity {
			over := true
			for _, c := range e.Categories {
				if perCat[c] < rankzoom.DiversityCap {
					over = false
					break
				}
			}
			if over {
				continue
			}
		}
		seen[e.Slug] = true
		for _, c := range e.Categories {
			perCat[c]++
		}
		items = append(items, ChunkItem{
			Slug: e.Slug, Type: e.Type, Name: e.Name,
			T0: e.T0, T1: e.T1, Precision: e.Precision, Status: e.Status,
			Point: e.Point, Categories: e.Categories, Importance: e.Importance,
			MediaThumb: e.MediaThumb, ChildCount: childCount[e.SeedID],
		})
	}
	return items
}

func categorySet(entities []*model.Entity) []string {
	set := map[string]bool{}
	for _, e := range entities {
		for _, c := range e.Categories {
			set[c] = true
		}
	}
	cats := make([]string, 0, len(set))
	for c := range set {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	return cats
}
