package bake

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"

	"wk/internal/model"
)

// EntityDoc is the self-contained per-slug document (API-2): claims,
// relationships, contemporaries, and children inlined - there is no server to
// compose them at read time.
type EntityDoc struct {
	Slug        string        `json:"slug"`
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Temporal    TemporalDoc   `json:"temporal"`
	Categories  []string      `json:"categories"`
	Importance  float64       `json:"importance"`
	Point       []float64     `json:"point,omitempty"`
	Properties  []PropertyDoc `json:"properties,omitempty"`
	Rel         []RelDoc      `json:"relationships,omitempty"`
	Contemps    []RefDoc      `json:"contemporaries,omitempty"`
	Children    []RefDoc      `json:"children,omitempty"`
	Links       LinksDoc      `json:"links"`
	MediaThumb  string        `json:"media_thumb,omitempty"`
}

type TemporalDoc struct {
	T0        float64 `json:"t0"`
	T1        float64 `json:"t1"`
	Precision string  `json:"precision"`
	Status    string  `json:"status"`
}

// PropertyDoc groups the claims for one property with a synthesized range
// (DM-5: show the spread, never a fake exact value).
type PropertyDoc struct {
	Property  string           `json:"property"`
	Synthesis SynthesisDoc     `json:"synthesis"`
	Claims    []model.SeedProp `json:"claims"`
}

type SynthesisDoc struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Unit string  `json:"unit,omitempty"`
	N    int     `json:"claim_count"`
}

type RelDoc struct {
	Type   string `json:"type"`
	Target RefDoc `json:"target"`
}

type RefDoc struct {
	Slug string  `json:"slug"`
	Name string  `json:"name"`
	Type string  `json:"type"`
	T0   float64 `json:"t0"`
	T1   float64 `json:"t1"`
}

type LinksDoc struct {
	Wikipedia string `json:"wikipedia,omitempty"`
	Wikidata  string `json:"wikidata,omitempty"`
}

const maxContemporaries = 50

// contemporarySpanFactor excludes near-eternal entities from a short entity's
// contemporaries: the Sun "coexists" with the Battle of Waterloo but listing it
// is noise. An entity qualifies only if its span is under this multiple of the
// subject's span (moment entities get a day floor so battles still see wars).
const contemporarySpanFactor = 50.0

// inverseRel maps a relationship type to the label shown on the TARGET's
// document, so declaring one direction in the seed is enough (API-2).
var inverseRel = map[string]string{
	"contemporary_with": "contemporary_with",
	"caused_by":         "resulted_in",
	"resulted_in":       "caused_by",
	"preceded":          "succeeded",
	"succeeded":         "preceded",
	"built_upon":        "influenced",
	"based_on":          "influenced",
	"supports":          "confirmed_by",
}

func bakeEntityDocs(w *writer, dataset string, entities []*model.Entity) error {
	bySeedID := map[string]*model.Entity{}
	for _, e := range entities {
		bySeedID[e.SeedID] = e
	}
	children := map[string][]*model.Entity{}
	incoming := map[string][]RelDoc{} // inverse edges, keyed by target seed id
	for _, e := range entities {
		for _, r := range e.Rel {
			if r.Type == "part_of" {
				children[r.Target] = append(children[r.Target], e)
				continue
			}
			if inv, ok := inverseRel[r.Type]; ok {
				incoming[r.Target] = append(incoming[r.Target], RelDoc{Type: inv, Target: ref(e)})
			}
		}
	}

	for _, e := range entities {
		doc := EntityDoc{
			Slug: e.Slug, Type: e.Type, Name: e.Name, Description: e.Description,
			Temporal:   TemporalDoc{T0: e.T0, T1: e.T1, Precision: e.Precision, Status: e.Status},
			Categories: e.Categories, Importance: e.Importance, Point: e.Point,
			Properties: buildProperties(e),
			Rel:        append(buildRels(e, bySeedID), incoming[e.SeedID]...),
			Contemps:   contemporaries(e, entities),
			Children:   refs(children[e.SeedID]),
			Links:      LinksDoc{Wikipedia: e.Wikipedia, Wikidata: e.Wikidata},
			MediaThumb: e.MediaThumb,
		}
		key := fmt.Sprintf("v/%s/entity/%s.json", dataset, e.Slug)
		if err := w.putJSON(key, doc); err != nil {
			return err
		}
	}
	return nil
}

func buildProperties(e *model.Entity) []PropertyDoc {
	byName := map[string][]model.SeedProp{}
	order := []string{}
	for _, p := range e.Props {
		if _, seen := byName[p.Property]; !seen {
			order = append(order, p.Property)
		}
		byName[p.Property] = append(byName[p.Property], p)
	}
	docs := make([]PropertyDoc, 0, len(order))
	for _, name := range order {
		claims := byName[name]
		syn := SynthesisDoc{N: len(claims)}
		first := true
		for _, c := range claims {
			lo, hi := claimRange(c)
			if first {
				syn.Min, syn.Max, syn.Unit, first = lo, hi, c.Unit, false
				continue
			}
			if lo < syn.Min {
				syn.Min = lo
			}
			if hi > syn.Max {
				syn.Max = hi
			}
		}
		docs = append(docs, PropertyDoc{Property: name, Synthesis: syn, Claims: claims})
	}
	return docs
}

func claimRange(c model.SeedProp) (float64, float64) {
	if c.Min != nil && c.Max != nil {
		return *c.Min, *c.Max
	}
	return *c.Value, *c.Value
}

func buildRels(e *model.Entity, bySeedID map[string]*model.Entity) []RelDoc {
	out := make([]RelDoc, 0, len(e.Rel))
	for _, r := range e.Rel {
		t := bySeedID[r.Target] // existence checked at ingest
		out = append(out, RelDoc{Type: r.Type, Target: ref(t)})
	}
	return out
}

// contemporaries: top-importance entities whose time range overlaps e's
// (VIS "what existed at the same time?"). O(n^2) is fine at seed scale; the
// dump-scale bake (M5) moves this into the DuckDB stage.
func contemporaries(e *model.Entity, all []*model.Entity) []RefDoc {
	span := e.T1 - e.T0
	if span < 86_400 {
		span = 86_400
	}
	overlapping := make([]*model.Entity, 0, 64)
	for _, o := range all {
		if o == e || o.T1 < e.T0 || o.T0 > e.T1 {
			continue
		}
		if o.T1-o.T0 > span*contemporarySpanFactor {
			continue
		}
		overlapping = append(overlapping, o)
	}
	sort.Slice(overlapping, func(i, j int) bool {
		if overlapping[i].Importance != overlapping[j].Importance {
			return overlapping[i].Importance > overlapping[j].Importance
		}
		return overlapping[i].Slug < overlapping[j].Slug
	})
	if len(overlapping) > maxContemporaries {
		overlapping = overlapping[:maxContemporaries]
	}
	return refs(overlapping)
}

func refs(es []*model.Entity) []RefDoc {
	sort.Slice(es, func(i, j int) bool { return es[i].T0 < es[j].T0 })
	out := make([]RefDoc, 0, len(es))
	for _, e := range es {
		out = append(out, ref(e))
	}
	return out
}

func ref(e *model.Entity) RefDoc {
	return RefDoc{Slug: e.Slug, Name: e.Name, Type: e.Type, T0: e.T0, T1: e.T1}
}

// writer uploads artifacts concurrently: object stores take single-digit
// milliseconds per PUT, so a 100k-object bake must not serialize them
// (measured: 13 min sequential vs. well under a minute pooled).
type writer struct {
	ctx   context.Context
	sink  Sink
	g     *errgroup.Group
	mu    sync.Mutex
	stats *Stats
}

const uploadConcurrency = 48

func newWriter(ctx context.Context, sink Sink, stats *Stats) *writer {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	return &writer{ctx: gctx, sink: sink, g: g, stats: stats}
}

// putJSON marshals synchronously (deterministic ordering of any marshal
// failure) and schedules the upload on the pool.
func (w *writer) putJSON(key string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	w.g.Go(func() error {
		changed, err := w.sink.Put(w.ctx, key, body, "application/json")
		if err != nil {
			return err
		}
		w.mu.Lock()
		w.stats.add(changed)
		w.mu.Unlock()
		return nil
	})
	return nil
}

func (w *writer) wait() error { return w.g.Wait() }
