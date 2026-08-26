package bake

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"wk/internal/model"
	"wk/internal/rankzoom"
)

// memSink collects artifacts in memory and reports change like the S3 sink.
// Put is called from the writer's upload pool, so it must be goroutine-safe.
type memSink struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemSink() *memSink { return &memSink{objects: map[string][]byte{}} }

func (m *memSink) Put(_ context.Context, key string, body []byte, _ string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.objects[key]; ok && string(old) == string(body) {
		return false, nil
	}
	m.objects[key] = body
	return true, nil
}

func testEntities(t *testing.T) []*model.Entity {
	t.Helper()
	es := []*model.Entity{
		{SeedID: "big-bang", Type: "natural_event", Name: "Big Bang",
			T0: model.YearToSeconds(-13.8e9), T1: model.YearToSeconds(-13.8e9),
			Precision: "billion_year", Status: "estimated",
			Categories: []string{"universe"}, Importance: 1.0},
		{SeedID: "ww2", Type: "event", Name: "World War II",
			T0: model.YearToSeconds(1939.67), T1: model.YearToSeconds(1945.67),
			Precision: "day", Status: "documented",
			Categories: []string{"war"}, Importance: 1.0},
		{SeedID: "stalingrad", Type: "event", Name: "Battle of Stalingrad",
			T0: model.YearToSeconds(1942.64), T1: model.YearToSeconds(1943.09),
			Precision: "day", Status: "documented",
			Categories: []string{"war"}, Importance: 0.9,
			Rel: []model.SeedRel{{Type: "part_of", Target: "ww2"}}},
		{SeedID: "obscure-skirmish", Type: "event", Name: "Obscure Skirmish",
			T0: model.YearToSeconds(1942.7), T1: model.YearToSeconds(1942.7),
			Precision: "day", Status: "documented",
			Categories: []string{"war"}, Importance: 0.06},
	}
	if err := model.AssignSlugs(es); err != nil {
		t.Fatal(err)
	}
	if err := rankzoom.Bucketize(es); err != nil {
		t.Fatal(err)
	}
	return es
}

func TestBucketizeSemanticZoom(t *testing.T) {
	es := testEntities(t)
	byID := map[string]*model.Entity{}
	for _, e := range es {
		byID[e.SeedID] = e
	}
	if bb := byID["big-bang"]; bb.BucketMin != 0 || bb.BucketMax != 2 {
		t.Errorf("big bang buckets [%d,%d], want [0,2] (billion_year precision caps at T2)", bb.BucketMin, bb.BucketMax)
	}
	if ww := byID["ww2"]; ww.BucketMin != 0 || ww.BucketMax != 11 {
		// day precision would allow T13, but the 6-year span exceeds the
		// 1024-window cap at both T13 (~52k hours) and T12 (~2.2k days);
		// T11 (72 months) is the finest bucket under the cap.
		t.Errorf("ww2 buckets [%d,%d], want [0,11]", ww.BucketMin, ww.BucketMax)
	}
	if sk := byID["obscure-skirmish"]; sk.BucketMin < 12 {
		t.Errorf("importance 0.06 must not render above T12, got min %d", sk.BucketMin)
	}
}

func TestBakeChunksAndDocs(t *testing.T) {
	es := testEntities(t)
	sink := newMemSink()
	m, stats, err := Run(context.Background(), sink, "test", "seed-x", es, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written == 0 {
		t.Fatal("nothing written")
	}

	// T0 world chunk holds only the high-importance entities.
	var t0 chunkFile
	mustGet(t, sink, "v/test/chunks/T0/0/world/all.json", &t0)
	names := []string{}
	for _, i := range t0.Items {
		names = append(names, i.Name)
	}
	if len(t0.Items) != 2 || !contains(names, "Big Bang") || !contains(names, "World War II") {
		t.Errorf("T0/all = %v, want Big Bang + WWII only", names)
	}

	// Stalingrad appears in the war chunk of its 1942 window at T10.
	w := model.Buckets[10].WindowIndex(model.YearToSeconds(1942.7))
	var t10 chunkFile
	mustGet(t, sink, keyFor("test", 10, w, "war"), &t10)
	names = names[:0]
	for _, i := range t10.Items {
		names = append(names, i.Name)
	}
	if !contains(names, "Battle of Stalingrad") {
		t.Errorf("T10 war window missing Stalingrad: %v", names)
	}

	// The parent's child_count reflects part_of.
	var ww2 chunkFile
	mustGet(t, sink, keyFor("test", 8, model.Buckets[8].WindowIndex(model.YearToSeconds(1940)), "all"), &ww2)
	for _, i := range ww2.Items {
		if i.Name == "World War II" && i.ChildCount != 1 {
			t.Errorf("ww2 child_count = %d, want 1", i.ChildCount)
		}
	}

	// Entity doc: slug key, part_of visible from both sides.
	var doc EntityDoc
	mustGet(t, sink, "v/test/entity/world-war-ii.json", &doc)
	if len(doc.Children) != 1 || doc.Children[0].Slug != "battle-of-stalingrad" {
		t.Errorf("ww2 children = %+v", doc.Children)
	}

	// Manifest window lists are per category and only contain baked windows.
	if ws := m.Buckets[0].Windows["all"]; len(ws) != 1 || ws[0] != 0 {
		t.Errorf("T0 all-windows = %v", m.Buckets[0].Windows)
	}
	if ws := m.Buckets[0].Windows["war"]; len(ws) != 1 || ws[0] != 0 {
		t.Errorf("T0 war-windows = %v", m.Buckets[0].Windows)
	}
	// A category with no entities in a window must not list that window:
	// the client would 404 on it (the browser-found filtering bug).
	w1942 := model.Buckets[10].WindowIndex(model.YearToSeconds(1942.7))
	if !slices.Contains(m.Buckets[10].Windows["war"], w1942) {
		t.Errorf("T10 war windows missing 1942: %v", m.Buckets[10].Windows["war"])
	}
	if slices.Contains(m.Buckets[10].Windows["universe"], w1942) {
		t.Errorf("T10 universe windows should not contain 1942: %v", m.Buckets[10].Windows["universe"])
	}

	// Idempotency: a second run writes nothing.
	_, stats2, err := Run(context.Background(), sink, "test", "seed-x", es, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Written != 0 {
		t.Errorf("re-bake wrote %d artifacts, want 0", stats2.Written)
	}
}

func keyFor(ds string, bucket int, window int64, cat string) string {
	return fmt.Sprintf("v/%s/chunks/%s/%d/world/%s.json", ds, model.Buckets[bucket].ID, window, cat)
}

func mustGet(t *testing.T, s *memSink, key string, v any) {
	t.Helper()
	body, ok := s.objects[key]
	if !ok {
		sample := []string{}
		for k := range s.objects {
			if len(sample) < 10 && strings.Contains(k, "chunks") {
				sample = append(sample, k)
			}
		}
		t.Fatalf("missing artifact %s (sample keys: %v)", key, sample)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
}

func contains(ss []string, want string) bool {
	return slices.Contains(ss, want)
}
