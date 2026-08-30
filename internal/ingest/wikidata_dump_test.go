package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestScanWikidataDumpExtractsFacts(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/wikidata-dump-mini.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var got []wikidataDumpItemFacts
	if _, err := scanWikidataDump(strings.NewReader(string(body)), func(facts wikidataDumpItemFacts) error {
		got = append(got, facts)
		return nil
	}); err != nil {
		t.Fatalf("scanWikidataDump: %v", err)
	}

	want := []wikidataDumpItemFacts{
		{
			QID:             "Q1001",
			HasEnglishLabel: true,
			InstanceOfQIDs:  []string{"Q5"},
			SubclassOfQIDs:  []string{"Q215627", "Q35120"},
			TimeClaims: []wikidataDumpTimeFact{
				{
					Property:      "P569",
					Time:          "+00000001900-01-01T00:00:00Z",
					Precision:     11,
					CalendarModel: "http://www.wikidata.org/entity/Q1985727",
				},
				{
					Property:      "P570",
					Time:          "+00000001950-01-01T00:00:00Z",
					Precision:     9,
					CalendarModel: "http://www.wikidata.org/entity/Q1985786",
				},
				{
					Property:      "P577",
					Time:          "+00000002001-12-31T00:00:00Z",
					Precision:     10,
					CalendarModel: "http://www.wikidata.org/entity/Q1985727",
				},
				{
					Property:      "P580",
					Time:          "-0000000044-03-15T00:00:00Z",
					Precision:     11,
					CalendarModel: "http://www.wikidata.org/entity/Q1985786",
				},
				{
					Property:      "P585",
					Time:          "+00000001969-07-20T20:17:00Z",
					Precision:     7,
					CalendarModel: "http://www.wikidata.org/entity/Q1985727",
				},
			},
			HasCoordinates:      true,
			HasEnglishWikipedia: true,
			SitelinkCount:       3,
		},
		{
			QID:                 "Q1002",
			HasEnglishLabel:     false,
			InstanceOfQIDs:      []string{},
			SubclassOfQIDs:      []string{},
			TimeClaims:          nil,
			HasCoordinates:      false,
			HasEnglishWikipedia: false,
			SitelinkCount:       1,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("facts mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestScanWikidataDumpVisitorErrorsIncludeIndexAndWrap(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("stop")
	input := `[
{"id":"P31","type":"property","claims":{}},
{"id":"Q1001","type":"item","labels":{"en":{"value":"Item"}},"claims":{}}
]`
	_, err := scanWikidataDump(strings.NewReader(input), func(wikidataDumpItemFacts) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(err, sentinel) = false for %v", err)
	}
	if !strings.Contains(err.Error(), "wikidata dump entity 1") {
		t.Fatalf("error %q does not include source-array index 1", err)
	}
}

func TestScanWikidataDumpSkipsMalformedStatementElementAndKeepsValidSiblings(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q2001","type":"item","claims":{"P585":[
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001900-01-01T00:00:00Z","precision":9,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"},
"malformed sibling",
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001901-01-01T00:00:00Z","precision":"11","calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"preferred"}
]}}
]`
	var got []wikidataDumpItemFacts
	if _, err := scanWikidataDump(strings.NewReader(input), func(facts wikidataDumpItemFacts) error {
		got = append(got, facts)
		return nil
	}); err != nil {
		t.Fatalf("scanWikidataDump: %v", err)
	}
	want := []wikidataDumpTimeFact{
		{
			Property:      "P585",
			Time:          "+00000001900-01-01T00:00:00Z",
			Precision:     9,
			CalendarModel: "http://www.wikidata.org/entity/Q1985727",
		},
		{
			Property:      "P585",
			Time:          "+00000001901-01-01T00:00:00Z",
			Precision:     11,
			CalendarModel: "http://www.wikidata.org/entity/Q1985727",
		},
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].TimeClaims, want) {
		t.Fatalf("time facts = %#v, want %#v", got, want)
	}
}

func TestScanWikidataDumpCoordinateCoverageBranches(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q2101","type":"item","claims":{"P625":[{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":52.5,"longitude":13.3,"globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"normal"}]}},
{"id":"Q2102","type":"item","claims":{"P625":[{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":"48.8566","longitude":"2.3522","globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"preferred"}]}},
{"id":"Q2103","type":"item","claims":{"P625":[
{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":4.5,"longitude":137.4,"globe":"http://www.wikidata.org/entity/Q111"},"type":"globecoordinate"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":91,"longitude":0,"globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":1,"longitude":1,"globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"deprecated"}
]}}
]`
	got := map[string]bool{}
	if _, err := scanWikidataDump(strings.NewReader(input), func(facts wikidataDumpItemFacts) error {
		got[facts.QID] = facts.HasCoordinates
		return nil
	}); err != nil {
		t.Fatalf("scanWikidataDump: %v", err)
	}
	want := map[string]bool{
		"Q2101": true,
		"Q2102": true,
		"Q2103": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coordinate coverage = %#v, want %#v", got, want)
	}
}

func TestScanWikidataDumpIgnoresFractionalAndOutOfRangeTimePrecision(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q2201","type":"item","claims":{"P585":[
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001900-01-01T00:00:00Z","precision":9,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001901-01-01T00:00:00Z","precision":"11.5","calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001902-01-01T00:00:00Z","precision":15,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001903-01-01T00:00:00Z","precision":"14","calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"preferred"}
]}}
]`
	var got []wikidataDumpItemFacts
	if _, err := scanWikidataDump(strings.NewReader(input), func(facts wikidataDumpItemFacts) error {
		got = append(got, facts)
		return nil
	}); err != nil {
		t.Fatalf("scanWikidataDump: %v", err)
	}
	want := []wikidataDumpTimeFact{
		{
			Property:      "P585",
			Time:          "+00000001900-01-01T00:00:00Z",
			Precision:     9,
			CalendarModel: "http://www.wikidata.org/entity/Q1985727",
		},
		{
			Property:      "P585",
			Time:          "+00000001903-01-01T00:00:00Z",
			Precision:     14,
			CalendarModel: "http://www.wikidata.org/entity/Q1985727",
		},
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].TimeClaims, want) {
		t.Fatalf("time facts = %#v, want %#v", got, want)
	}
}

func TestScanWikidataDumpRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		input      string
		want       string
		wantSyntax bool  // a truncated array surfaces as a typed json.SyntaxError
		wantErr    error // an unterminated record surfaces as io.ErrUnexpectedEOF
	}{
		{name: "object root", input: `{}`, want: "root is not an array"},
		{name: "scalar root", input: `1`, want: "root is not an array"},
		{
			name: "truncated array", input: `[{"id":"Q1","type":"item"}`,
			want: "wikidata dump entity 1: decode", wantSyntax: true,
		},
		{
			name: "malformed entity", input: `[{"id":"Q1","type":"item"`,
			want: "wikidata dump entity 0: decode", wantErr: io.ErrUnexpectedEOF,
		},
		{name: "trailing JSON", input: `[{"id":"Q1","type":"item"}] {}`, want: "trailing JSON after array"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := scanWikidataDump(strings.NewReader(tc.input), func(wikidataDumpItemFacts) error {
				return nil
			})
			if err == nil {
				t.Fatalf("scanWikidataDump error = nil, want error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("scanWikidataDump error = %v, want substring %q", err, tc.want)
			}
			var syntaxErr *json.SyntaxError
			if tc.wantSyntax && !errors.As(err, &syntaxErr) {
				t.Fatalf("scanWikidataDump error = %v (%T), want a wrapped *json.SyntaxError", err, err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("scanWikidataDump error = %v, want one wrapping %v", err, tc.wantErr)
			}
		})
	}
}

// A single hostile or corrupt record must not be buffered into memory in full.
func TestScanWikidataDumpBoundsOneRecord(t *testing.T) {
	t.Parallel()

	oversized := `[{"id":"Q1","type":"item","labels":{"en":{"value":"` + strings.Repeat("x", 4096) + `"}}}]`
	_, err := scanWikidataDumpLimited(strings.NewReader(oversized), 512, newDumpCounters(), func(wikidataDumpItemFacts) error {
		return nil
	})
	if !errors.Is(err, errDumpRecordTooLarge) {
		t.Fatalf("scanWikidataDumpLimited error = %v, want one wrapping errDumpRecordTooLarge", err)
	}
	if !strings.Contains(err.Error(), "Q1") {
		t.Fatalf("error %q does not name the offending record", err)
	}

	// An unterminated record cannot be read into memory without end: the
	// buffer guard fires before the decoder ever produces a value.
	unterminated := `[{"id":"Q1","type":"item","labels":{"en":{"value":"` + strings.Repeat("x", 400_000)
	if _, err := scanWikidataDumpLimited(strings.NewReader(unterminated), 512, newDumpCounters(), func(wikidataDumpItemFacts) error {
		return nil
	}); !errors.Is(err, errDumpBufferOverrun) {
		t.Fatalf("scanWikidataDumpLimited error = %v, want one wrapping errDumpBufferOverrun", err)
	}

	// The same limit accepts a dump whose records are individually small,
	// however many of them there are.
	var record strings.Builder
	record.WriteString(`[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			record.WriteString(",")
		}
		fmt.Fprintf(&record, `{"id":"Q%d","type":"item","labels":{"en":{"value":"name"}}}`, i+1)
	}
	record.WriteString(`]`)
	stats, err := scanWikidataDumpLimited(strings.NewReader(record.String()), 512, newDumpCounters(), func(wikidataDumpItemFacts) error {
		return nil
	})
	if err != nil {
		t.Fatalf("scanWikidataDumpLimited over many small records: %v", err)
	}
	if stats.Items != 40 {
		t.Fatalf("items = %d, want 40", stats.Items)
	}
}

func TestScanWikidataDumpRejectsNonPositiveRecordLimit(t *testing.T) {
	t.Parallel()

	_, err := scanWikidataDumpLimited(strings.NewReader(`[]`), 0, newDumpCounters(), func(wikidataDumpItemFacts) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "record size limit must be positive") {
		t.Fatalf("scanWikidataDumpLimited error = %v", err)
	}
}

// Every declined claim increments a named counter; nothing is dropped silently.
func TestScanWikidataDumpCountsEveryDropReason(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q3001","type":"item","claims":{
"P31":"not an array",
"P279":[{"mainsnak":{"snaktype":"value","property":"P279","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"property","id":"P31"},"type":"wikibase-entityid"}},"rank":"normal"}],
"P585":[
{"mainsnak":{"snaktype":"somevalue","property":"P585","datatype":"time"},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"","precision":9,"calendarmodel":"c"},"type":"time"}},"rank":"normal"},
"not an object"
],
"P625":[
{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":4.5,"longitude":137.4,"globe":"http://www.wikidata.org/entity/Q111"},"type":"globecoordinate"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":91,"longitude":0,"globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":1,"longitude":1,"globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"deprecated"}
]}}
]`
	stats, err := scanWikidataDump(strings.NewReader(input), func(wikidataDumpItemFacts) error { return nil })
	if err != nil {
		t.Fatalf("scanWikidataDump: %v", err)
	}
	want := map[WikidataDumpSkipReason]int{
		SkipClaimGroupNotArray:   1,
		SkipStatementNotObject:   1,
		SkipEntityValueInvalid:   1,
		SkipSnakShape:            1,
		SkipTimeValueInvalid:     1,
		SkipCoordinateNotOnEarth: 1,
		SkipCoordinateOutOfRange: 1,
		SkipStatementRank:        1,
	}
	if !reflect.DeepEqual(stats.Skips, want) {
		t.Fatalf("skips = %#v, want %#v", stats.Skips, want)
	}
}

func TestScanWikidataDumpRejectsNilBoundaries(t *testing.T) {
	t.Parallel()

	if _, err := scanWikidataDump(nil, func(wikidataDumpItemFacts) error { return nil }); err == nil || !strings.Contains(err.Error(), "nil reader") {
		t.Fatalf("nil reader error = %v, want nil reader", err)
	}
	if _, err := scanWikidataDump(strings.NewReader(`[]`), nil); err == nil || !strings.Contains(err.Error(), "nil visitor") {
		t.Fatalf("nil visitor error = %v, want nil visitor", err)
	}
}

func TestScanWikidataDumpRejectsInvalidEntities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		entity string
		want   string
	}{
		{name: "missing type", entity: `{"id":"Q1"}`, want: "missing entity type"},
		{name: "unknown type", entity: `{"id":"Q1","type":"lexeme"}`, want: `unknown entity type "lexeme"`},
		{name: "missing item id", entity: `{"type":"item"}`, want: `invalid item id ""`},
		{name: "invalid item zero", entity: `{"id":"Q0","type":"item"}`, want: `invalid item id "Q0"`},
		{name: "invalid item leading zero", entity: `{"id":"Q01","type":"item"}`, want: `invalid item id "Q01"`},
		{name: "invalid item prefix", entity: `{"id":"xQ1","type":"item"}`, want: `invalid item id "xQ1"`},
		{name: "invalid item suffix", entity: `{"id":"Q1x","type":"item"}`, want: `invalid item id "Q1x"`},
		{name: "invalid property zero", entity: `{"id":"P0","type":"property"}`, want: `invalid property id "P0"`},
		{name: "invalid property leading zero", entity: `{"id":"P01","type":"property"}`, want: `invalid property id "P01"`},
		{name: "invalid property suffix", entity: `{"id":"P1x","type":"property"}`, want: `invalid property id "P1x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := scanWikidataDump(strings.NewReader("["+tc.entity+"]"), func(wikidataDumpItemFacts) error {
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("scanWikidataDump error = %v, want substring %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "wikidata dump entity 0") {
				t.Fatalf("error %q does not include source-array index 0", err)
			}
		})
	}
}

func TestValidNumericEntityID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id     string
		prefix byte
		want   bool
	}{
		{id: "Q1", prefix: 'Q', want: true},
		{id: "Q123", prefix: 'Q', want: true},
		{id: "P1", prefix: 'P', want: true},
		{id: "P999", prefix: 'P', want: true},
		{id: "", prefix: 'Q', want: false},
		{id: "Q", prefix: 'Q', want: false},
		{id: "Q0", prefix: 'Q', want: false},
		{id: "Q01", prefix: 'Q', want: false},
		{id: "q1", prefix: 'Q', want: false},
		{id: "P0", prefix: 'P', want: false},
		{id: "P01", prefix: 'P', want: false},
		{id: "P-1", prefix: 'P', want: false},
		{id: "Q1a", prefix: 'Q', want: false},
		{id: "Q1", prefix: 'P', want: false},
	}
	for _, tc := range cases {
		if got := validNumericEntityID(tc.id, tc.prefix); got != tc.want {
			t.Errorf("validNumericEntityID(%q, %q) = %t, want %t", tc.id, tc.prefix, got, tc.want)
		}
	}
}
