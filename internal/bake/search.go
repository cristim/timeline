package bake

import (
	"fmt"
	"sort"
	"strings"

	"wk/internal/model"
)

// Search shards (SRCH-1, API-4): client-side prefix search over static JSON.
// Every name token maps into the shard named by its first character; the
// client folds the query the same way, fetches the matching shards, and
// filters/ranks locally. No search server exists.

type SearchEntry struct {
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	T0         float64 `json:"t0"`
	T1         float64 `json:"t1"`
	Importance float64 `json:"importance"`
	MediaThumb string  `json:"media_thumb,omitempty"`
}

type searchShard struct {
	Entries []SearchEntry `json:"entries"`
}

const minTokenLen = 3 // "of", "to" etc. would put half the dataset in one shard

// SearchTokens folds a name to the lowercase ASCII tokens used for sharding
// and matching; exported so the key-scheme test suite can pin it against the
// client implementation (API-5 drift rule).
func SearchTokens(name string) []string {
	tokens := strings.Split(model.Slugify(name), "-")
	out := tokens[:0]
	for _, t := range tokens {
		if len(t) >= minTokenLen {
			out = append(out, t)
		}
	}
	return out
}

func bakeSearch(w *writer, dataset string, entities []*model.Entity) ([]string, error) {
	shards := map[string]map[string]SearchEntry{} // shard -> slug -> entry
	for _, e := range entities {
		entry := SearchEntry{
			Slug: e.Slug, Name: e.Name, Type: e.Type,
			T0: e.T0, T1: e.T1, Importance: e.Importance, MediaThumb: e.MediaThumb,
		}
		for _, tok := range SearchTokens(e.Name) {
			key := tok[:1]
			if shards[key] == nil {
				shards[key] = map[string]SearchEntry{}
			}
			shards[key][e.Slug] = entry
		}
	}

	names := make([]string, 0, len(shards))
	for k := range shards {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		entries := make([]SearchEntry, 0, len(shards[name]))
		for _, en := range shards[name] {
			entries = append(entries, en)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Importance != entries[j].Importance {
				return entries[i].Importance > entries[j].Importance
			}
			return entries[i].Slug < entries[j].Slug
		})
		key := fmt.Sprintf("v/%s/search/%s.json", dataset, name)
		if err := w.putJSON(key, searchShard{Entries: entries}); err != nil {
			return nil, err
		}
	}
	return names, nil
}
