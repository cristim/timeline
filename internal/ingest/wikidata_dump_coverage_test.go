package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestBuildWikidataDumpCoverageReportBuildsExactFixtureReport(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/wikidata-dump-mini.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	want := WikidataDumpCoverageReport{
		SchemaVersion: 1,
		CoverageBasis: "wikidata-item-facts-after-statement-validation-before-type-classification",
		InputSHA256:   "3d59b3bde012de266b41ffadc982e24eb820f8d3297d63130ae61851e49af6d4",
		Items: WikidataDumpCoverageStats{
			Count:               2,
			HasEnglishLabel:     1,
			HasDate:             1,
			HasCoordinates:      1,
			HasEnglishWikipedia: 1,
			HasAnySitelink:      2,
			HasAll:              1,
			TotalSitelinks:      4,
		},
		Properties: 1,
		TimeClaims: []WikidataDumpTimeClaimCount{
			{Property: "P569", Precision: 11, Count: 1},
			{Property: "P570", Precision: 9, Count: 1},
			{Property: "P577", Precision: 10, Count: 1},
			{Property: "P580", Precision: 11, Count: 1},
			{Property: "P585", Precision: 7, Count: 1},
		},
		// The fixture's deliberately broken claims are now counted rather
		// than silently discarded.
		SkippedClaims: []WikidataDumpSkipCount{
			{Reason: SkipClaimGroupNotArray, Count: 1},
			{Reason: SkipEntityValueInvalid, Count: 1},
			{Reason: SkipSnakShape, Count: 4},
			{Reason: SkipStatementRank, Count: 4},
			{Reason: SkipTimeValueInvalid, Count: 1},
		},
	}

	first, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}
	second, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("second BuildWikidataDumpCoverageReport: %v", err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("report mismatch\n got: %#v\nwant: %#v", first, want)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("repeated report mismatch\nfirst: %#v\nsecond: %#v", first, second)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first report: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second report: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("repeated JSON mismatch\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestBuildWikidataDumpCoverageReportHashesEveryInputByte(t *testing.T) {
	t.Parallel()

	plain := []byte("[]")
	withTrailingWhitespace := []byte("[] \n")
	plainReport, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(plain)))
	if err != nil {
		t.Fatalf("plain BuildWikidataDumpCoverageReport: %v", err)
	}
	whitespaceReport, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(withTrailingWhitespace)))
	if err != nil {
		t.Fatalf("whitespace BuildWikidataDumpCoverageReport: %v", err)
	}

	plainDigest := sha256.Sum256(plain)
	whitespaceDigest := sha256.Sum256(withTrailingWhitespace)
	if got, want := plainReport.InputSHA256, hex.EncodeToString(plainDigest[:]); got != want {
		t.Fatalf("plain InputSHA256 = %q, want %q", got, want)
	}
	if got, want := whitespaceReport.InputSHA256, hex.EncodeToString(whitespaceDigest[:]); got != want {
		t.Fatalf("whitespace InputSHA256 = %q, want %q", got, want)
	}
	if plainReport.InputSHA256 == whitespaceReport.InputSHA256 {
		t.Fatalf("InputSHA256 = %q for inputs with different trailing bytes", plainReport.InputSHA256)
	}

	plainReport.InputSHA256 = ""
	whitespaceReport.InputSHA256 = ""
	if !reflect.DeepEqual(plainReport, whitespaceReport) {
		t.Fatalf("aggregate reports differ\nplain: %#v\nwhitespace: %#v", plainReport, whitespaceReport)
	}
}

func TestBuildWikidataDumpCoverageReportAggregatesItemsAndSortedClaims(t *testing.T) {
	t.Parallel()

	input := `[{
"id":"Q1",
"type":"item",
"labels":{"en":{"value":""}},
"sitelinks":{"enwiki":{"title":"Item"},"dewiki":{"title":""},"simplewiki":{"title":"Item"}},
"claims":{
"P569":[{"mainsnak":{"snaktype":"value","property":"P569","datatype":"time","datavalue":{"value":{"time":"+00000001900-01-01T00:00:00Z","precision":11,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}],
"P585":[
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001901-01-01T00:00:00Z","precision":11,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"},
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001902-01-01T00:00:00Z","precision":9,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"preferred"},
{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"+00000001903-01-01T00:00:00Z","precision":11,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}
],
"P625":[{"mainsnak":{"snaktype":"value","property":"P625","datatype":"globe-coordinate","datavalue":{"value":{"latitude":1,"longitude":2,"globe":"http://www.wikidata.org/entity/Q2"},"type":"globecoordinate"}},"rank":"normal"}]
}
}]`
	report, err := BuildWikidataDumpCoverageReport(strings.NewReader(input))
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}

	wantItems := WikidataDumpCoverageStats{
		Count:               1,
		HasDate:             1,
		HasCoordinates:      1,
		HasEnglishWikipedia: 1,
		HasAnySitelink:      1,
		HasAll:              1,
		TotalSitelinks:      3,
	}
	if !reflect.DeepEqual(report.Items, wantItems) {
		t.Fatalf("Items = %#v, want %#v", report.Items, wantItems)
	}
	wantClaims := []WikidataDumpTimeClaimCount{
		{Property: "P569", Precision: 11, Count: 1},
		{Property: "P585", Precision: 9, Count: 1},
		{Property: "P585", Precision: 11, Count: 2},
	}
	if !reflect.DeepEqual(report.TimeClaims, wantClaims) {
		t.Fatalf("TimeClaims = %#v, want %#v", report.TimeClaims, wantClaims)
	}
}

func TestBuildWikidataDumpCoverageReportEmptyDumpHasNonNilClaims(t *testing.T) {
	t.Parallel()

	report, err := BuildWikidataDumpCoverageReport(strings.NewReader("[]"))
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}
	if report.TimeClaims == nil {
		t.Fatal("TimeClaims = nil, want non-nil empty slice")
	}
	if len(report.TimeClaims) != 0 {
		t.Fatalf("len(TimeClaims) = %d, want 0", len(report.TimeClaims))
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal empty report: %v", err)
	}
	if !strings.Contains(string(encoded), `"time_claims":[]`) {
		t.Fatalf("empty report JSON = %s, want time_claims array", encoded)
	}
}

func TestBuildWikidataDumpCoverageReportRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	if _, err := BuildWikidataDumpCoverageReport(nil); err == nil || !strings.Contains(err.Error(), "build wikidata dump coverage report: nil reader") {
		t.Fatalf("nil reader error = %v", err)
	}

	_, err := BuildWikidataDumpCoverageReport(strings.NewReader(`[{"id":"Q1","type":"item"`))
	if err == nil {
		t.Fatal("malformed dump error = nil, want error")
	}
	if !strings.Contains(err.Error(), "build wikidata dump coverage report: wikidata dump entity 0: decode") {
		t.Fatalf("malformed dump error = %v, want builder and indexed scanner context", err)
	}
}
