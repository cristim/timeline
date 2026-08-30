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

func censusFixtureReport(t *testing.T) WikidataDumpCoverageReport {
	t.Helper()
	body, err := os.ReadFile("testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	report, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}
	return report
}

// ROAD-2 asks its question in Wikidata's vocabulary: how many battles, wars,
// political events, disasters, scientific events, people, species, products.
// The class rows answer exactly that.
func TestWikidataDumpCensusCountsTheROAD2Classes(t *testing.T) {
	t.Parallel()

	report := censusFixtureReport(t)
	byClass := map[string]WikidataDumpClassRow{}
	for _, row := range report.Classes {
		byClass[row.ClassQID] = row
	}

	want := map[string]struct {
		label string
		typ   string
		count int
	}{
		"Q178561":   {label: "battle", typ: "event", count: 1},
		"Q198":      {label: "war", typ: "event", count: 1},
		"Q40231":    {label: "public election", typ: "event", count: 1},
		"Q7944":     {label: "earthquake", typ: "natural_event", count: 1},
		"Q12184":    {label: "pandemic", typ: "natural_event", count: 1},
		"Q13442814": {label: "scholarly article", typ: "paper", count: 1},
		"Q5":        {label: "human", typ: "person", count: 1},
		"Q16521":    {label: "taxon", typ: "species", count: 1},
		"Q23038290": {label: "fossil taxon", typ: "species", count: 1},
		"Q40218":    {label: "spacecraft", typ: "product", count: 1},
		"Q811979":   {label: "architectural structure", typ: "structure", count: 1},
	}
	for qid, expected := range want {
		row, ok := byClass[qid]
		if !ok {
			t.Fatalf("class %s (%s) missing from the census", qid, expected.label)
		}
		if row.ClassLabel != expected.label || row.Type != expected.typ || row.Stats.Count != expected.count {
			t.Fatalf("class %s = %#v, want label %q type %q count %d", qid, row, expected.label, expected.typ, expected.count)
		}
	}
	if len(byClass) != len(want) {
		t.Fatalf("class rows = %d, want %d: %#v", len(byClass), len(want), report.Classes)
	}
}

// Nothing is dropped silently: the disambiguation page is excluded by name and
// the protein domain is reported as a class the table should grow to cover.
func TestWikidataDumpCensusReportsExcludedAndUnclassifiedItems(t *testing.T) {
	t.Parallel()

	report := censusFixtureReport(t)
	byType := map[string]WikidataDumpTypeRow{}
	for _, row := range report.Types {
		byType[row.Type] = row
	}
	if got := byType[CensusTypeExcluded].Stats.Count; got != 1 {
		t.Fatalf("excluded count = %d, want 1", got)
	}
	if got := byType[CensusTypeUnclassified].Stats.Count; got != 1 {
		t.Fatalf("unclassified count = %d, want 1", got)
	}
	want := []WikidataDumpUnclassifiedClassCount{{ClassQID: "Q898273", Count: 1}}
	if !reflect.DeepEqual(report.UnclassifiedClasses, want) {
		t.Fatalf("unclassified classes = %#v, want %#v", report.UnclassifiedClasses, want)
	}
	if report.UnclassifiedClassesTotal != 1 {
		t.Fatalf("unclassified classes total = %d, want 1", report.UnclassifiedClassesTotal)
	}

	// Every item is accounted for under exactly one type row.
	total := 0
	for _, row := range report.Types {
		total += row.Stats.Count
	}
	if total != report.Items.Count {
		t.Fatalf("type rows total %d, want the item count %d", total, report.Items.Count)
	}
}

func TestWikidataDumpCensusAttributesItemsToTimeSlices(t *testing.T) {
	t.Parallel()

	report := censusFixtureReport(t)
	got := map[float64]int{}
	spans := map[float64]float64{}
	for _, bucket := range report.Buckets {
		got[bucket.StartYear] = bucket.Total.Count
		spans[bucket.StartYear] = bucket.SpanYears
	}

	want := map[float64]int{
		-66000000: 1, // Tyrannosaurus, million-year slice
		-2600:     1, // Great Pyramid, century precision BCE
		-100:      1, // Caesar, Julian day precision BCE
		1800:      1, // Waterloo
		1900:      3, // World War I, San Francisco, Hubble
		2000:      3, // the paper, the election, the pandemic
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bucket counts = %#v, want %#v", got, want)
	}
	if spans[-66000000] != 1e6 {
		t.Fatalf("deep-time span = %v, want 1e6", spans[-66000000])
	}
	if spans[1900] != 100 {
		t.Fatalf("century span = %v, want 100", spans[1900])
	}

	// The taxon, the disambiguation page and the protein domain carry no time
	// claim: counted, and kept out of the time slices rather than parked in an
	// invented century.
	if report.ItemsWithoutTime != 3 {
		t.Fatalf("items without time = %d, want 3", report.ItemsWithoutTime)
	}
	sliced := 0
	for _, bucket := range report.Buckets {
		sliced += bucket.Total.Count
	}
	if sliced+report.ItemsWithoutTime != report.Items.Count {
		t.Fatalf("%d sliced + %d untimed != %d items", sliced, report.ItemsWithoutTime, report.Items.Count)
	}
}

func TestWikidataDumpCensusReportsPrecisionAndCalendarDistribution(t *testing.T) {
	t.Parallel()

	report := censusFixtureReport(t)
	wantPrecision := []WikidataDumpPrecisionCount{
		{Precision: "century", Count: 1},
		{Precision: "day", Count: 7},
		{Precision: "million_year", Count: 1},
		{Precision: "month", Count: 1},
	}
	if !reflect.DeepEqual(report.TimePrecision, wantPrecision) {
		t.Fatalf("time precision = %#v, want %#v", report.TimePrecision, wantPrecision)
	}

	wantCalendars := []WikidataDumpCalendarCount{
		{CalendarModel: calendarModelGregorian, Era: eraBCE, Count: 2},
		{CalendarModel: calendarModelGregorian, Era: eraCE, Count: 7},
		{CalendarModel: calendarModelJulian, Era: eraBCE, Count: 1},
	}
	if !reflect.DeepEqual(report.Calendars, wantCalendars) {
		t.Fatalf("calendars = %#v, want %#v", report.Calendars, wantCalendars)
	}
}

func TestWikidataDumpCensusReportsCoverageRatios(t *testing.T) {
	t.Parallel()

	report := censusFixtureReport(t)
	if report.Items.Count != 13 {
		t.Fatalf("items = %d, want 13", report.Items.Count)
	}
	if report.Items.HasCoordinates != 4 {
		t.Fatalf("coordinates = %d, want 4", report.Items.HasCoordinates)
	}
	if got, want := report.Items.Ratios.Coordinates, coverageRatio(4, 13); got != want {
		t.Fatalf("coordinate ratio = %v, want %v", got, want)
	}
	if got, want := report.Items.Ratios.Date, coverageRatio(report.Items.HasDate, 13); got != want {
		t.Fatalf("date ratio = %v, want %v", got, want)
	}
	if report.Items.Ratios.EnglishWikipedia <= 0 || report.Items.Ratios.EnglishWikipedia > 1 {
		t.Fatalf("wikipedia ratio = %v, want a fraction", report.Items.Ratios.EnglishWikipedia)
	}
}

func TestBuildWikidataDumpCoverageReportIsDeterministic(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	first, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("first BuildWikidataDumpCoverageReport: %v", err)
	}
	second, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("second BuildWikidataDumpCoverageReport: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated report differs")
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("repeated JSON mismatch\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}

	digest := sha256.Sum256(body)
	if first.InputSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("input sha256 = %q, want the fixture digest", first.InputSHA256)
	}
	if first.SchemaVersion != wikidataDumpCoverageReportSchemaVersion {
		t.Fatalf("schema version = %d", first.SchemaVersion)
	}
}

func TestBuildWikidataDumpCoverageReportCountsSkippedClaims(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/wikidata-dump-mini.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	report, err := BuildWikidataDumpCoverageReport(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}
	// The mini fixture's deliberately broken claims are counted, not silently
	// discarded.
	want := []WikidataDumpSkipCount{
		{Reason: SkipClaimGroupNotArray, Count: 1},
		{Reason: SkipEntityValueInvalid, Count: 1},
		{Reason: SkipSnakShape, Count: 4},
		{Reason: SkipStatementRank, Count: 4},
		{Reason: SkipTimeValueInvalid, Count: 1},
	}
	if !reflect.DeepEqual(report.SkippedClaims, want) {
		t.Fatalf("skipped claims = %#v, want %#v", report.SkippedClaims, want)
	}
	if report.Properties != 1 {
		t.Fatalf("properties = %d, want 1", report.Properties)
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

	plainReport.InputSHA256 = ""
	whitespaceReport.InputSHA256 = ""
	if !reflect.DeepEqual(plainReport, whitespaceReport) {
		t.Fatalf("aggregate reports differ\nplain: %#v\nwhitespace: %#v", plainReport, whitespaceReport)
	}
}

func TestBuildWikidataDumpCoverageReportEmptyDumpHasNonNilSlices(t *testing.T) {
	t.Parallel()

	report, err := BuildWikidataDumpCoverageReport(strings.NewReader("[]"))
	if err != nil {
		t.Fatalf("BuildWikidataDumpCoverageReport: %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal empty report: %v", err)
	}
	for _, field := range []string{
		`"types":[]`, `"classes":[]`, `"buckets":[]`, `"time_precision":[]`,
		`"calendars":[]`, `"unclassified_classes":[]`, `"time_claims":[]`, `"skipped_claims":[]`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("empty report JSON = %s, want %s", encoded, field)
		}
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
