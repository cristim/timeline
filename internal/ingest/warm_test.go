package ingest

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"wk/internal/model"
)

type warmBoundaryCase struct {
	qid          string
	name         string
	at           string
	articleURL   string
	expectedUnix int64
}

func TestWarmEventProducerPreservesCenturyBoundaryUTCSeconds(t *testing.T) {
	t.Parallel()

	cases := []warmBoundaryCase{
		{
			qid:          "Q9990001900",
			name:         "Boundary 1900 event",
			at:           "1900-01-01T00:00:00Z",
			articleURL:   "https://en.wikipedia.org/wiki/Boundary_1900_event",
			expectedUnix: mustUnixSecond(t, "1900-01-01T00:00:00Z"),
		},
		{
			qid:          "Q9990002000",
			name:         "Boundary 2000 event",
			at:           "2000-01-01T00:00:00Z",
			articleURL:   "https://en.wikipedia.org/wiki/Boundary_2000_event",
			expectedUnix: mustUnixSecond(t, "2000-01-01T00:00:00Z"),
		},
	}

	records := make([]WikidataRecord, 0, len(cases))
	for _, tc := range cases {
		record, qid, ok := normalizeBinding(warmBoundaryBinding(tc), EventClasses[0])
		if !ok || qid != tc.qid {
			t.Fatalf("normalizeBinding(%s) = %#v, %q, %t", tc.at, record, qid, ok)
		}
		records = append(records, *record)
	}

	encoded, err := EncodeWarmEvents(records)
	if err != nil {
		t.Fatalf("EncodeWarmEvents: %v", err)
	}
	fixture, err := os.ReadFile("testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}
	if !bytes.Equal(encoded, fixture) {
		t.Fatalf("boundary fixture differs from producer output\n got: %s\nwant: %s", encoded, fixture)
	}

	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	added, skipped, err := MergeWarmEvents(res, fixture)
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != len(cases) || skipped != 0 || len(res.Rejects) != 0 {
		t.Fatalf("merge result: added=%d skipped=%d rejects=%v", added, skipped, res.Rejects)
	}
	if res.WarmParsed != len(cases) || res.WarmAccepted != len(cases) || res.WarmDuplicatesSkipped != 0 {
		t.Fatalf("warm counters = parsed:%d accepted:%d skipped:%d, want %d/%d/0", res.WarmParsed, res.WarmAccepted, res.WarmDuplicatesSkipped, len(cases), len(cases))
	}

	for _, tc := range cases {
		entity := findEntityByWikidata(t, res, tc.qid)
		if got := int64(math.Round(entity.T0)); got != tc.expectedUnix {
			t.Fatalf("%s rounded T0 = %d, want %d", tc.qid, got, tc.expectedUnix)
		}
	}
}

func TestWarmEventProducerKeepsOutOfCalendarRangeDecember31InStatedCentury(t *testing.T) {
	t.Parallel()

	tc := warmBoundaryCase{
		qid:        "Q9990000101",
		name:       "Boundary 101 BCE event",
		at:         "-0101-12-31T00:00:00Z",
		articleURL: "https://en.wikipedia.org/wiki/Boundary_101_BCE_event",
	}
	record, qid, ok := normalizeBinding(warmBoundaryBinding(tc), EventClasses[0])
	if !ok || qid != tc.qid {
		t.Fatalf("normalizeBinding(%s) = %#v, %q, %t", tc.at, record, qid, ok)
	}

	encoded, err := EncodeWarmEvents([]WikidataRecord{*record})
	if err != nil {
		t.Fatalf("EncodeWarmEvents: %v", err)
	}
	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	before, err := BuildCensusReport(res, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport(before): %v", err)
	}
	beforeStatedCentury := censusReportBucketTotal(before, -200, 100)
	beforeNextCentury := censusReportBucketTotal(before, -100, 100)

	added, skipped, err := MergeWarmEvents(res, encoded)
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != 1 || skipped != 0 || len(res.Rejects) != 0 {
		t.Fatalf("merge result: added=%d skipped=%d rejects=%v", added, skipped, res.Rejects)
	}

	entity := findEntityByWikidata(t, res, tc.qid)
	if got := model.SecondsToYear(entity.T0); got <= -101 || got >= -100 {
		t.Fatalf("SecondsToYear(T0) = %v, want inside stated year (-101, -100)", got)
	}
	after, err := BuildCensusReport(res, WarmSourceWarmFile, testSHA256("warm-dec31"))
	if err != nil {
		t.Fatalf("BuildCensusReport(after): %v", err)
	}
	if got := censusReportBucketTotal(after, -200, 100); got != beforeStatedCentury+1 {
		t.Fatalf("stated-century bucket count = %d, want %d", got, beforeStatedCentury+1)
	}
	if got := censusReportBucketTotal(after, -100, 100); got != beforeNextCentury {
		t.Fatalf("next-century bucket count = %d, want unchanged %d", got, beforeNextCentury)
	}
}

func TestMergeWarmEventsRejectsIncompatibleCensusMetadata(t *testing.T) {
	t.Parallel()

	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	before, err := BuildCensusReport(res, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport(before): %v", err)
	}
	beforeBucket := censusReportBucketTotal(before, -200, 100)
	base := warmBoundaryCase{
		qid:        "Q9990000201",
		name:       "Metadata boundary event",
		at:         "-0101-01-01T00:00:00Z",
		articleURL: "https://en.wikipedia.org/wiki/Metadata_boundary_event",
	}
	record, _, ok := normalizeBinding(warmBoundaryBinding(base), EventClasses[0])
	if !ok {
		t.Fatal("normalizeBinding failed")
	}

	modern := *record
	modern.CensusYear = float64Pointer(1900)
	fractional := *record
	fractional.Seed.ID = "wd-q9990000202"
	fractional.Seed.Wikidata = "Q9990000202"
	fractional.CensusYear = float64Pointer(-101.5)
	outOfRange := *record
	outOfRange.Seed.ID = "wd-q9990000203"
	outOfRange.Seed.Wikidata = "Q9990000203"
	outOfRange.CensusYear = float64Pointer(-201)
	wrongPrecision := *record
	wrongPrecision.Seed.ID = "wd-q9990000204"
	wrongPrecision.Seed.Wikidata = "Q9990000204"
	wrongPrecision.Seed.Precision = "century"
	wrongPrecision.CensusYear = float64Pointer(-101)
	adjacentYear := *record
	adjacentYear.Seed.ID = "wd-q9990000205"
	adjacentYear.Seed.Wikidata = "Q9990000205"
	adjacentYear.CensusYear = float64Pointer(-102)
	valid := *record
	valid.Seed.ID = "wd-q9990000206"
	valid.Seed.Wikidata = "Q9990000206"
	valid.CensusYear = float64Pointer(-101)
	modernQIDReuse := modern
	modernQIDReuse.Seed.ID = "wd-q9990000207"
	modernQIDReuse.CensusYear = float64Pointer(-101)

	warm := marshalWarmEvents(t, modern, fractional, outOfRange, wrongPrecision, adjacentYear, valid, modernQIDReuse)
	added, skipped, err := MergeWarmEvents(res, warm)
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != 2 || skipped != 0 {
		t.Fatalf("merge result: added=%d skipped=%d, want 2/0", added, skipped)
	}
	if res.WarmParsed != 7 || res.WarmAccepted != 2 || res.WarmDuplicatesSkipped != 0 {
		t.Fatalf("warm counters = parsed:%d accepted:%d skipped:%d, want 7/2/0", res.WarmParsed, res.WarmAccepted, res.WarmDuplicatesSkipped)
	}
	if len(res.Rejects) != 5 {
		t.Fatalf("rejects = %d, want 5: %#v", len(res.Rejects), res.Rejects)
	}
	report, err := BuildCensusReport(res, WarmSourceWarmFile, testSHA256("warm-metadata"))
	if err != nil {
		t.Fatalf("BuildCensusReport: %v", err)
	}
	if got := censusReportBucketTotal(report, -200, 100); got != beforeBucket+2 {
		t.Fatalf("accepted metadata census bucket = %d, want %d", got, beforeBucket+2)
	}
	if findEntityByWikidata(t, res, "Q9990000206").CensusYear == nil ||
		*findEntityByWikidata(t, res, "Q9990000206").CensusYear != -101 {
		t.Fatal("accepted source year was not preserved")
	}
	if got := findEntityByWikidata(t, res, "Q9990000201").SeedID; got != "wd-q9990000207" {
		t.Fatalf("reused QID merged as %q, want wd-q9990000207", got)
	}
	for _, qid := range []string{"Q9990000202", "Q9990000203", "Q9990000204", "Q9990000205"} {
		for _, entity := range res.Entities {
			if entity.Wikidata == qid {
				t.Fatalf("rejected QID %s entered merged entities", qid)
			}
		}
	}
}

func TestMergeWarmEventsAcceptsProducerCensusYearBoundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		qid         string
		at          string
		year        float64
		bucketStart float64
		bucketSpan  float64
	}{
		{qid: "Q9990000300", at: "-0101-12-31T00:00:00Z", year: -101, bucketStart: -200, bucketSpan: 100},
		{qid: "Q9990000301", at: "0000-01-01T00:00:00Z", year: 0, bucketStart: 0, bucketSpan: 100},
		{qid: "Q9990000302", at: "10000-01-01T00:00:00Z", year: 10000, bucketStart: 10000, bucketSpan: 10000},
		{qid: "Q9990000303", at: "-30000000000-01-01T00:00:00Z", year: -30000000000, bucketStart: -30000000000, bucketSpan: 1000000000},
		{qid: "Q9990000304", at: "-30000000000-12-31T00:00:00Z", year: -30000000000, bucketStart: -30000000000, bucketSpan: 1000000000},
		{qid: "Q9990000305", at: "30000000000-01-01T00:00:00Z", year: 30000000000, bucketStart: 30000000000, bucketSpan: 1000000000},
		{qid: "Q9990000306", at: "30000000000-12-31T00:00:00Z", year: 30000000000, bucketStart: 30000000000, bucketSpan: 1000000000},
	}
	records := make([]WikidataRecord, 0, len(cases))
	for _, tc := range cases {
		input := warmBoundaryCase{qid: tc.qid, name: "Boundary " + tc.qid, at: tc.at, articleURL: "https://example.com/" + tc.qid}
		record, _, ok := normalizeBinding(warmBoundaryBinding(input), EventClasses[0])
		if !ok {
			t.Fatalf("normalizeBinding(%s) failed: record=%#v", tc.at, record)
		}
		if record.CensusYear == nil || *record.CensusYear != tc.year {
			t.Fatalf("producer census year for %s = %v, want %v", tc.at, record.CensusYear, tc.year)
		}
		records = append(records, *record)
	}
	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	before, err := BuildCensusReport(res, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport(before): %v", err)
	}
	beforeCounts := make(map[[2]float64]int)
	for _, tc := range cases {
		beforeCounts[[2]float64{tc.bucketStart, tc.bucketSpan}] = censusReportBucketTotal(before, tc.bucketStart, tc.bucketSpan)
	}
	wantCounts := make(map[[2]float64]int)
	for key, count := range beforeCounts {
		wantCounts[key] = count
	}
	for _, tc := range cases {
		key := [2]float64{tc.bucketStart, tc.bucketSpan}
		wantCounts[key]++
	}
	added, skipped, err := MergeWarmEvents(res, marshalWarmEvents(t, records...))
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != len(cases) || skipped != 0 || len(res.Rejects) != 0 {
		t.Fatalf("merge result: added=%d skipped=%d rejects=%v", added, skipped, res.Rejects)
	}
	after, err := BuildCensusReport(res, WarmSourceWarmFile, testSHA256("warm-boundaries"))
	if err != nil {
		t.Fatalf("BuildCensusReport(after): %v", err)
	}
	for key, want := range wantCounts {
		if got := censusReportBucketTotal(after, key[0], key[1]); got != want {
			t.Fatalf("census bucket start=%v span=%v count=%d, want %d", key[0], key[1], got, want)
		}
	}
	for _, tc := range cases {
		entity := findEntityByWikidata(t, res, tc.qid)
		if entity.CensusYear == nil || *entity.CensusYear != tc.year {
			t.Fatalf("merged census year for %s = %v, want %v", tc.qid, entity.CensusYear, tc.year)
		}
	}
}

func TestWarmEventProducerRetainsExtremeTimeRejection(t *testing.T) {
	t.Parallel()

	for _, year := range []string{"100000000000000-01-01T00:00:00Z", "10000000000000000-01-01T00:00:00Z"} {
		input := warmBoundaryCase{qid: "Q99900004" + year[:1], name: year, at: year, articleURL: "https://example.com/extreme"}
		record, _, ok := normalizeBinding(warmBoundaryBinding(input), EventClasses[0])
		if !ok {
			t.Fatalf("normalizeBinding(%s) failed", year)
		}
		res, err := LoadSeed("../../data/seed")
		if err != nil {
			t.Fatalf("LoadSeed: %v", err)
		}
		added, _, err := MergeWarmEvents(res, marshalWarmEvents(t, *record))
		if err != nil {
			t.Fatalf("MergeWarmEvents(%s): %v", year, err)
		}
		if added != 0 || res.WarmAccepted != 0 || len(res.Rejects) != 1 {
			t.Fatalf("extreme %s: added=%d accepted=%d rejects=%d", year, added, res.WarmAccepted, len(res.Rejects))
		}
	}
}

func censusReportBucketTotal(report CensusReport, startYear, spanYears float64) int {
	for _, bucket := range report.Buckets {
		if bucket.StartYear == startYear && bucket.SpanYears == spanYears {
			return bucket.Total.Count
		}
	}
	return 0
}

func marshalWarmEvents(t *testing.T, records ...WikidataRecord) []byte {
	t.Helper()
	encoded, err := EncodeWarmEvents(records)
	if err != nil {
		t.Fatalf("EncodeWarmEvents: %v", err)
	}
	return encoded
}

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

func warmBoundaryBinding(tc warmBoundaryCase) map[string]struct {
	Value string `json:"value"`
} {
	return map[string]struct {
		Value string `json:"value"`
	}{
		"item":      {Value: "http://www.wikidata.org/entity/" + tc.qid},
		"itemLabel": {Value: tc.name},
		"time":      {Value: tc.at},
		"coord":     {Value: "Point(13.35 52.51)"},
		"sitelinks": {Value: "8"},
		"article":   {Value: tc.articleURL},
		"partOf":    {Value: "http://www.wikidata.org/entity/Q362"},
	}
}

func mustUnixSecond(t *testing.T, value string) int64 {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", value, err)
	}
	return parsed.Unix()
}

func findEntityByWikidata(t *testing.T, res *Result, qid string) *model.Entity {
	t.Helper()

	for _, entity := range res.Entities {
		if entity.Wikidata == qid {
			return entity
		}
	}
	t.Fatalf("entity with wikidata %s not found", qid)
	return nil
}
