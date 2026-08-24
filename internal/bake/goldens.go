package bake

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"wk/internal/model"
)

// Golden views (ZOOM-5): checked-in expectations evaluated against the baked
// chunks inside every bake. A failing view blocks publish - a data change that
// breaks a view must surface as an explicit expectation diff, never ship
// silently.

type GoldenFile struct {
	// SeedVersion pins the seed these expectations were written against; a
	// mismatch is an error so seed edits force a deliberate golden review.
	SeedVersion string       `json:"seed_version"`
	Views       []GoldenView `json:"views"`
}

type GoldenView struct {
	Name     string   `json:"name"`
	Bucket   string   `json:"bucket"`         // "T0".."T13"
	Year     *float64 `json:"year,omitempty"` // window = WindowIndex(YearToSeconds(year)); nil = window 0
	Category string   `json:"category"`       // one category or "all"
	Include  []string `json:"include,omitempty"`
	Exclude  []string `json:"exclude,omitempty"`
	MinItems int      `json:"min_items,omitempty"`
	MaxItems int      `json:"max_items,omitempty"`
}

func LoadGoldens(path string) (*GoldenFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read goldens: %w", err)
	}
	var gf GoldenFile
	if err := json.Unmarshal(b, &gf); err != nil {
		return nil, fmt.Errorf("parse goldens %s: %w", path, err)
	}
	return &gf, nil
}

// ChunkKey resolves the golden view to its chunk artifact key (relative to
// /v/<dataset>/).
func (v GoldenView) ChunkKey() (string, error) {
	bIdx := -1
	for i, b := range model.Buckets {
		if b.ID == v.Bucket {
			bIdx = i
			break
		}
	}
	if bIdx < 0 {
		return "", fmt.Errorf("golden %q: unknown bucket %q", v.Name, v.Bucket)
	}
	var window int64
	if v.Year != nil {
		window = model.Buckets[bIdx].WindowIndex(model.YearToSeconds(*v.Year))
	}
	return fmt.Sprintf("chunks/%s/%d/world/%s.json", v.Bucket, window, v.Category), nil
}

// Evaluate checks every view against the captured chunk contents and returns
// human-readable failures (empty = pass).
func Evaluate(gf *GoldenFile, seedVersion string, chunks map[string]chunkFile) []string {
	var fails []string
	if gf.SeedVersion != seedVersion {
		return []string{fmt.Sprintf(
			"goldens are pinned to %s but the seed is %s: review data/goldens.json against the new seed and update seed_version",
			gf.SeedVersion, seedVersion)}
	}
	for _, v := range gf.Views {
		key, err := v.ChunkKey()
		if err != nil {
			fails = append(fails, err.Error())
			continue
		}
		chunk, ok := chunks[key]
		if !ok {
			fails = append(fails, fmt.Sprintf("%s: chunk %s was not baked", v.Name, key))
			continue
		}
		have := map[string]bool{}
		for _, item := range chunk.Items {
			have[item.Slug] = true
		}
		for _, slug := range v.Include {
			if !have[slug] {
				fails = append(fails, fmt.Sprintf("%s: %s missing from %s", v.Name, slug, key))
			}
		}
		for _, slug := range v.Exclude {
			if have[slug] {
				fails = append(fails, fmt.Sprintf("%s: %s must not appear in %s", v.Name, slug, key))
			}
		}
		if n := len(chunk.Items); v.MinItems > 0 && n < v.MinItems {
			fails = append(fails, fmt.Sprintf("%s: %d items < min %d in %s", v.Name, n, v.MinItems, key))
		} else if v.MaxItems > 0 && n > v.MaxItems {
			fails = append(fails, fmt.Sprintf("%s: %d items > max %d in %s", v.Name, n, v.MaxItems, key))
		}
	}
	return fails
}

// goldenKeySet precomputes which chunk keys the goldens need, so bakeChunks
// can capture just those instead of holding every chunk in memory (the seed
// fits either way; dump-scale bakes will not).
func goldenKeySet(gf *GoldenFile) (map[string]bool, error) {
	if gf == nil {
		return nil, nil
	}
	keys := map[string]bool{}
	for _, v := range gf.Views {
		k, err := v.ChunkKey()
		if err != nil {
			return nil, err
		}
		keys[k] = true
	}
	return keys, nil
}

func formatFails(fails []string) string {
	return "golden views FAILED:\n  " + strings.Join(fails, "\n  ")
}
