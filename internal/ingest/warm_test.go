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
	if res.WarmParsed != 1 || res.WarmAccepted != 1 || res.WarmDuplicatesSkipped != 0 {
		t.Fatalf("warm counters = parsed:%d accepted:%d skipped:%d, want 1/1/0", res.WarmParsed, res.WarmAccepted, res.WarmDuplicatesSkipped)
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

func TestMergeWarmEventsRejectsCommentLookingLine(t *testing.T) {
	t.Parallel()

	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}

	added, skipped, err := MergeWarmEvents(res, []byte("# not a seed-style comment\n"))
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != 0 || skipped != 0 {
		t.Fatalf("added=%d skipped=%d, want 0/0", added, skipped)
	}
	if res.WarmParsed != 1 || res.WarmAccepted != 0 || res.WarmDuplicatesSkipped != 0 {
		t.Fatalf("warm counters = parsed:%d accepted:%d skipped:%d, want 1/0/0", res.WarmParsed, res.WarmAccepted, res.WarmDuplicatesSkipped)
	}
	if len(res.Rejects) != 1 {
		t.Fatalf("rejects = %d, want 1", len(res.Rejects))
	}
	if res.Rejects[0].Source != RejectSourceWarm {
		t.Fatalf("reject source = %q, want %q", res.Rejects[0].Source, RejectSourceWarm)
	}
	if res.Rejects[0].File != "warm:events" || res.Rejects[0].Line != 1 {
		t.Fatalf("reject location = %s:%d, want warm:events:1", res.Rejects[0].File, res.Rejects[0].Line)
	}
}

func TestMergeWarmEventsTracksAcceptedRejectedAndDuplicates(t *testing.T) {
	t.Parallel()

	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	warm := []byte(
		`{"id":"wd-q362","type":"event","name":"Duplicate world war 2","t0":"1939-09-01","precision":"day","status":"documented","categories":["war"],"importance":0.42,"wikidata":"Q362"}` + "\n" +
			`{"id":"wd-q999000002","type":"event","name":"Broken warm event","t0":"nope","precision":"day","status":"documented","categories":["war"],"importance":0.42,"wikidata":"Q999000002"}` + "\n" +
			`{"id":"wd-q999000003","type":"event","name":"Valid warm event","t0":"1944-07-20","precision":"day","status":"documented","categories":["war"],"importance":0.42,"wikidata":"Q999000003"}` + "\n",
	)

	added, skipped, err := MergeWarmEvents(res, warm)
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != 1 || skipped != 1 {
		t.Fatalf("added=%d skipped=%d, want 1/1", added, skipped)
	}
	if res.WarmParsed != 3 || res.WarmAccepted != 1 || res.WarmDuplicatesSkipped != 1 {
		t.Fatalf("warm counters = parsed:%d accepted:%d skipped:%d, want 3/1/1", res.WarmParsed, res.WarmAccepted, res.WarmDuplicatesSkipped)
	}
	if len(res.Rejects) != 1 {
		t.Fatalf("rejects = %d, want 1", len(res.Rejects))
	}
	if res.Rejects[0].Source != RejectSourceWarm {
		t.Fatalf("reject source = %q, want %q", res.Rejects[0].Source, RejectSourceWarm)
	}
	if len(res.Entities) == 0 || res.Entities[len(res.Entities)-1].SeedID != "wd-q999000003" {
		t.Fatalf("last entity = %#v, want wd-q999000003", res.Entities[len(res.Entities)-1])
	}
}
