package ingest

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"wk/internal/model"
)

func TestMergeWarmEventsAcceptsNormalizedFixture(t *testing.T) {
	t.Parallel()

	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	warm, err := os.ReadFile("testdata/warm-event.ndjson")
	if err != nil {
		t.Fatalf("read warm fixture: %v", err)
	}
	binding := map[string]struct {
		Value string `json:"value"`
	}{
		"item":      {Value: "http://www.wikidata.org/entity/Q999000001"},
		"itemLabel": {Value: "Test warm event"},
		"time":      {Value: "1944-07-20T00:00:00Z"},
		"coord":     {Value: "Point(13.35 52.51)"},
		"sitelinks": {Value: "8"},
		"article":   {Value: "https://en.wikipedia.org/wiki/Test_event"},
		"partOf":    {Value: "http://www.wikidata.org/entity/Q362"},
	}
	record, qid, ok := normalizeBinding(binding, EventClasses[0])
	if !ok || qid != "Q999000001" {
		t.Fatalf("normalizeBinding = %#v, %q, %t", record, qid, ok)
	}
	encoded, err := EncodeWarmEvents([]WikidataRecord{*record})
	if err != nil {
		t.Fatalf("EncodeWarmEvents: %v", err)
	}
	if !bytes.Equal(encoded, warm) {
		t.Fatalf("fixture differs from producer output\n got: %s\nwant: %s", encoded, warm)
	}
	before := len(res.Entities)
	added, skipped, err := MergeWarmEvents(res, warm)
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != 1 || skipped != 0 || len(res.Rejects) != 0 {
		t.Fatalf("merge result: added=%d skipped=%d rejects=%v", added, skipped, res.Rejects)
	}
	if len(res.Entities) != before+1 {
		t.Fatalf("entities = %d, want %d", len(res.Entities), before+1)
	}
	wantTime, err := model.ParseSeedTime(json.RawMessage(record.Seed.T0))
	if err != nil {
		t.Fatal(err)
	}
	entity := res.Entities[len(res.Entities)-1]
	want := &model.Entity{
		SeedID: "wd-q999000001", Slug: "test-warm-event", Type: "event", Name: "Test warm event",
		T0: wantTime, T1: wantTime, Precision: "day", Status: "documented",
		Categories: []string{"war"}, Importance: 0.42, Point: []float64{13.35, 52.51},
		Wikidata: "Q999000001", Wikipedia: "https://en.wikipedia.org/wiki/Test_event",
		Rel: []model.SeedRel{{Type: "part_of", Target: "world-war-2"}},
	}
	if !reflect.DeepEqual(entity, want) {
		t.Fatalf("merged entity\n got: %#v\nwant: %#v", entity, want)
	}
}
