// Package bake materializes the serving artifacts (ARCH-4): viewport chunks,
// entity documents, aliases, and the dataset manifest. Everything it writes is
// immutable under /v/<dataset>/ (ARCH-2).
package bake

import (
	"context"
	"fmt"
	"slices"
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
// A non-nil goldens file is evaluated against the baked chunks; any failure
// aborts before the manifest exists, so a failing golden view cannot publish
// (ZOOM-5).
func Run(ctx context.Context, sink Sink, dataset, seedVersion string, entities []*model.Entity, goldens *GoldenFile) (*model.Manifest, *Stats, error) {
	stats := &Stats{}

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
	buckets, captured, err := bakeChunks(w, dataset, entities, childCount, goldenKeys)
	if err != nil {
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

	if err := bakeEntityDocs(w, dataset, entities); err != nil {
		return nil, stats, err
	}
	shards, err := bakeSearch(w, dataset, entities)
	if err != nil {
		return nil, stats, err
	}
	if err := w.putJSON(fmt.Sprintf("v/%s/aliases.json", dataset), map[string]string{}); err != nil {
		return nil, stats, err
	}
	if err := w.wait(); err != nil {
		return nil, stats, err
	}

	m := &model.Manifest{
		Dataset:      dataset,
		SeedVersion:  seedVersion,
		Buckets:      buckets,
		Categories:   categorySet(entities),
		Layers:       []string{},
		Timesteps:    map[string][]int{},
		SearchShards: shards,
		GoldenViews:  goldenStatus,
		Counts: map[string]int{
			"entities": len(entities),
		},
	}
	return m, stats, nil
}

// bakeChunks writes /v/<ds>/chunks/<tb>/<window>/world/<category>.json and
// returns the bucket table with per-bucket non-empty window lists (shipped in
// the manifest so the client never fetches, or 404s on, an empty window), plus
// the chunks named by goldenKeys for evaluation.
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

	windowSets := make([]map[string]map[int64]bool, len(model.Buckets))
	for i := range windowSets {
		windowSets[i] = map[string]map[int64]bool{}
	}

	keys := make([]cell, 0, len(cells))
	for c := range cells {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.bucket != b.bucket {
			return a.bucket < b.bucket
		}
		if a.window != b.window {
			return a.window < b.window
		}
		return a.cat < b.cat
	})

	captured := map[string]chunkFile{}
	for _, c := range keys {
		items := rankCell(cells[c], c.cat == "all", childCount)
		relKey := fmt.Sprintf("chunks/%s/%d/world/%s.json",
			model.Buckets[c.bucket].ID, c.window, c.cat)
		if err := w.putJSON(fmt.Sprintf("v/%s/%s", dataset, relKey), chunkFile{Items: items}); err != nil {
			return nil, nil, err
		}
		if goldenKeys[relKey] {
			captured[relKey] = chunkFile{Items: items}
		}
		if windowSets[c.bucket][c.cat] == nil {
			windowSets[c.bucket][c.cat] = map[int64]bool{}
		}
		windowSets[c.bucket][c.cat][c.window] = true
	}

	out := make([]model.Bucket, len(model.Buckets))
	for i, b := range model.Buckets {
		b.Windows = map[string][]int64{}
		for cat, set := range windowSets[i] {
			ws := make([]int64, 0, len(set))
			for w := range set {
				ws = append(ws, w)
			}
			slices.Sort(ws)
			b.Windows[cat] = ws
		}
		out[i] = b
	}
	return out, captured, nil
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
