package ingest

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"wk/internal/model"
)

func TestCensusYearUsesCalendarYearsAtCenturyBoundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		value     string
		precision string
		wantYear  float64
		wantStart float64
	}{
		{name: "1899-12-31", value: "1899-12-31T00:00:00Z", precision: "day", wantYear: 1899, wantStart: 1800},
		{name: "1900-01-01", value: "1900-01-01T00:00:00Z", precision: "year", wantYear: 1900, wantStart: 1900},
		{name: "1999-12-31", value: "1999-12-31T00:00:00Z", precision: "day", wantYear: 1999, wantStart: 1900},
		{name: "2000-01-01", value: "2000-01-01T00:00:00Z", precision: "year", wantYear: 2000, wantStart: 2000},
		// Precisions coarser than a year used to skip the calendar entirely and
		// land a whole century early.
		{name: "1900-01-01 century precision", value: "1900-01-01T00:00:00Z", precision: "century", wantYear: 1900, wantStart: 1900},
		{name: "1900-01-01 decade precision", value: "1900-01-01T00:00:00Z", precision: "decade", wantYear: 1900, wantStart: 1900},
		{name: "1900-01-01 millennium precision", value: "1900-01-01T00:00:00Z", precision: "millennium", wantYear: 1900, wantStart: 1900},
		{name: "2000-01-01 century precision", value: "2000-01-01T00:00:00Z", precision: "century", wantYear: 2000, wantStart: 2000},
	}

	for _, tc := range cases {
		entity := testEntity(t, tc.name, "event", tc.precision, mustUnixTime(t, tc.value))
		if got := censusYearForEntity(entity); got != tc.wantYear {
			t.Fatalf("%s censusYearForEntity = %v, want %v", tc.name, got, tc.wantYear)
		}
		if got, _ := censusBucketFor(censusYearForEntity(entity)); got != tc.wantStart {
			t.Fatalf("%s bucket start = %v, want %v", tc.name, got, tc.wantStart)
		}
	}
}

func TestCensusYearPreservesExactIntegralAstronomicalYears(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		t0        float64
		precision string
		wantYear  float64
		wantStart float64
	}{
		{name: "exact 1900 year literal", t0: model.YearToSeconds(1900), precision: "year", wantYear: 1900, wantStart: 1900},
		{name: "bce year", t0: model.YearToSeconds(-44), precision: "year", wantYear: -44, wantStart: -100},
		{name: "bce century precision", t0: model.YearToSeconds(-500), precision: "century", wantYear: -500, wantStart: -500},
		{name: "bce millennium precision", t0: model.YearToSeconds(-3000), precision: "millennium", wantYear: -3000, wantStart: -3000},
		{name: "far future", t0: model.YearToSeconds(1e20 + 45), precision: "billion_year", wantYear: model.SecondsToYear(model.YearToSeconds(1e20 + 45)), wantStart: farFutureBucketStart()},
	}

	for _, tc := range cases {
		entity := testEntity(t, tc.name, "event", tc.precision, tc.t0)
		if got := censusYearForEntity(entity); got != tc.wantYear {
			t.Fatalf("%s censusYearForEntity = %v, want %v", tc.name, got, tc.wantYear)
		}
		if got, _ := censusBucketFor(censusYearForEntity(entity)); got != tc.wantStart {
			t.Fatalf("%s bucket start = %v, want %v", tc.name, got, tc.wantStart)
		}
	}
}

func TestCensusBucketForNormalizesSignedZero(t *testing.T) {
	t.Parallel()

	got, span := censusBucketFor(math.Copysign(0, -1))
	if got != 0 {
		t.Fatalf("censusBucketFor(-0) start = %v, want 0", got)
	}
	if math.Signbit(got) {
		t.Fatalf("censusBucketFor(-0) kept a negative sign bit")
	}
	if span != 100 {
		t.Fatalf("censusBucketFor(-0) span = %v, want 100", span)
	}
}

// Deep time gets coarser slices; a century row at 4.5 billion years would be
// one row per entity and would say nothing.
func TestCensusBucketForWidensInDeepTime(t *testing.T) {
	t.Parallel()

	cases := []struct {
		year      float64
		wantStart float64
		wantSpan  float64
	}{
		{year: 1969, wantStart: 1900, wantSpan: 100},
		{year: -44, wantStart: -100, wantSpan: 100},
		{year: -12000, wantStart: -20000, wantSpan: 1e4},
		{year: -66000000, wantStart: -66000000, wantSpan: 1e6},
		{year: -4.5e9, wantStart: -5e9, wantSpan: 1e9},
	}
	for _, tc := range cases {
		start, span := censusBucketFor(tc.year)
		if start != tc.wantStart || span != tc.wantSpan {
			t.Fatalf("censusBucketFor(%v) = (%v, %v), want (%v, %v)", tc.year, start, span, tc.wantStart, tc.wantSpan)
		}
	}
}

func farFutureBucketStart() float64 {
	start, _ := censusBucketFor(model.SecondsToYear(model.YearToSeconds(1e20 + 45)))
	return start
}

func TestBuildCensusReportAggregatesCoverageAndDeterministicJSON(t *testing.T) {
	t.Parallel()

	entities := []*model.Entity{
		testEntityWithCoverage(t, "boundary event", "event", "day", mustUnixTime(t, "1900-01-01T00:00:00Z"), []float64{13.35, 52.51}, "https://en.wikipedia.org/wiki/Boundary_event"),
		testEntity(t, "exact year person", "person", "year", model.YearToSeconds(1900)),
		testEntityWithCoverage(t, "monthly place", "place", "month", mustUnixTime(t, "2000-06-01T00:00:00Z"), []float64{2.35, 48.85}, ""),
		testEntityWithCoverage(t, "future signal", "future_event", "billion_year", model.YearToSeconds(1e20+45), nil, "https://en.wikipedia.org/wiki/Future_signal"),
	}
	result := &Result{
		SeedVersion:     "seed-census",
		SeedInputSHA256: testSHA256("seed-census"),
		Entities:        entities,
		SeedParsed:      2,
		SeedAccepted:    2,
		WarmParsed:      2,
		WarmAccepted:    2,
	}

	report, err := BuildCensusReport(result, WarmSourceWarmFile, testSHA256("warm-census"))
	if err != nil {
		t.Fatalf("BuildCensusReport: %v", err)
	}

	if report.SchemaVersion != CensusReportSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", report.SchemaVersion, CensusReportSchemaVersion)
	}
	if report.CoverageBasis != CensusCoverageBasis {
		t.Fatalf("CoverageBasis = %q, want %q", report.CoverageBasis, CensusCoverageBasis)
	}
	if report.ImportReport.Accepted.Total != len(entities) {
		t.Fatalf("ImportReport.Accepted.Total = %d, want %d", report.ImportReport.Accepted.Total, len(entities))
	}

	if report.Total.Count != 4 || report.Total.HasDate != 4 || report.Total.HasCoordinates != 2 || report.Total.HasEnglishWikipedia != 2 || report.Total.HasAll != 1 {
		t.Fatalf("Total = %#v, want count/date/coords/wiki/all = 4/4/2/2/1", report.Total)
	}
	wantPrecision := []CensusPrecisionCount{
		{Precision: "billion_year", Count: 1},
		{Precision: "day", Count: 1},
		{Precision: "month", Count: 1},
		{Precision: "year", Count: 1},
	}
	if !equalPrecisionCounts(report.Total.Precision, wantPrecision) {
		t.Fatalf("Total.Precision = %#v, want %#v", report.Total.Precision, wantPrecision)
	}

	if len(report.Types) != 4 {
		t.Fatalf("Types len = %d, want 4", len(report.Types))
	}
	for i, wantType := range []string{"event", "future_event", "person", "place"} {
		if report.Types[i].Type != wantType {
			t.Fatalf("Types[%d].Type = %q, want %q", i, report.Types[i].Type, wantType)
		}
	}

	if len(report.Buckets) != 3 {
		t.Fatalf("Buckets len = %d, want 3", len(report.Buckets))
	}
	if report.Buckets[0].StartYear != 1900 || report.Buckets[1].StartYear != 2000 {
		t.Fatalf("first buckets = %#v, want starts 1900 and 2000", report.Buckets[:2])
	}
	if report.Buckets[2].StartYear != farFutureBucketStart() {
		t.Fatalf("far-future bucket start = %v, want %v", report.Buckets[2].StartYear, farFutureBucketStart())
	}

	firstJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	reversed := &Result{
		SeedVersion:     result.SeedVersion,
		SeedInputSHA256: result.SeedInputSHA256,
		Entities:        []*model.Entity{entities[3], entities[2], entities[1], entities[0]},
		SeedParsed:      result.SeedParsed,
		SeedAccepted:    result.SeedAccepted,
		WarmParsed:      result.WarmParsed,
		WarmAccepted:    result.WarmAccepted,
	}
	secondReport, err := BuildCensusReport(reversed, WarmSourceWarmFile, testSHA256("warm-census"))
	if err != nil {
		t.Fatalf("BuildCensusReport(reversed): %v", err)
	}
	secondJSON, err := json.Marshal(secondReport)
	if err != nil {
		t.Fatalf("marshal reversed report: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("deterministic JSON mismatch\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestBuildCensusReportKeepsEmptySlicesNonNil(t *testing.T) {
	t.Parallel()

	report, err := BuildCensusReport(&Result{
		SeedVersion:     "seed-empty",
		SeedInputSHA256: testSHA256("seed-empty"),
		Entities:        []*model.Entity{},
	}, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport: %v", err)
	}

	if report.Types == nil || report.Buckets == nil || report.Total.Precision == nil {
		t.Fatalf("nil slice found in %#v", report)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	want := `{"schema_version":2,"coverage_basis":"accepted-normalized-entities-after-source-filters","import_report":{"schema_version":2,"seed_version":"seed-empty","seed_input_sha256":"` + testSHA256("seed-empty") + `","warm_source":"none","warm_sha256":"","parsed":{"seed":0,"warm":0,"total":0},"accepted":{"seed":0,"warm":0,"total":0},"rejected":{"seed":0,"warm":0,"total":0},"skipped_warm_duplicates":0,"reject_reasons":[]},"total":{"count":0,"has_date":0,"has_coordinates":0,"has_english_wikipedia":0,"has_all":0,"precision":[]},"types":[],"buckets":[]}`
	if string(body) != want {
		t.Fatalf("marshal = %s\nwant   = %s", body, want)
	}
}

func TestBuildCensusReportValidatesImportInputsAndAcceptedCount(t *testing.T) {
	t.Parallel()

	validEntity := testEntity(t, "valid", "event", "day", mustUnixTime(t, "1900-01-01T00:00:00Z"))
	cases := []struct {
		name       string
		result     *Result
		warmSource WarmSource
		warmSHA256 string
		wantErr    string
	}{
		{
			name: "unknown warm source",
			result: &Result{
				SeedVersion:     "seed-census",
				SeedInputSHA256: testSHA256("seed-census"),
			},
			warmSource: WarmSource("mystery"),
			wantErr:    `unknown warm source "mystery"`,
		},
		{
			name: "warm digest required",
			result: &Result{
				SeedVersion:     "seed-census",
				SeedInputSHA256: testSHA256("seed-census"),
			},
			warmSource: WarmSourceWarmFile,
			wantErr:    `warm source "warm-file" requires sha256 digest`,
		},
		{
			name: "warm counters inconsistent",
			result: &Result{
				SeedVersion:     "seed-census",
				SeedInputSHA256: testSHA256("seed-census"),
				WarmParsed:      1,
			},
			warmSource: WarmSourceWarmFile,
			warmSHA256: testSHA256("warm"),
			wantErr:    "warm counters inconsistent",
		},
		{
			name: "accepted count mismatch",
			result: &Result{
				SeedVersion:     "seed-census",
				SeedInputSHA256: testSHA256("seed-census"),
				Entities:        []*model.Entity{validEntity, testEntity(t, "second", "event", "day", mustUnixTime(t, "2000-01-01T00:00:00Z"))},
				SeedParsed:      1,
				SeedAccepted:    1,
			},
			warmSource: WarmSourceNone,
			wantErr:    "accepted total 1 != entity count 2",
		},
	}

	for _, tc := range cases {
		_, err := BuildCensusReport(tc.result, tc.warmSource, tc.warmSHA256)
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if !bytes.Contains([]byte(err.Error()), []byte(tc.wantErr)) {
			t.Fatalf("%s: err = %v, want substring %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestBuildCensusReportUsesT0CenturyForRangeCrossingEntity(t *testing.T) {
	t.Parallel()

	start := mustUnixTime(t, "1899-12-31T00:00:00Z")
	end := mustUnixTime(t, "1900-01-02T00:00:00Z")
	entity := testEntity(t, "crossing", "event", "day", start)
	entity.T1 = end

	report, err := BuildCensusReport(&Result{
		SeedVersion:     "seed-range",
		SeedInputSHA256: testSHA256("seed-range"),
		Entities:        []*model.Entity{entity},
		SeedParsed:      1,
		SeedAccepted:    1,
	}, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport: %v", err)
	}

	if len(report.Buckets) != 1 {
		t.Fatalf("Buckets len = %d, want 1", len(report.Buckets))
	}
	if report.Buckets[0].StartYear != 1800 {
		t.Fatalf("StartYear = %v, want 1800", report.Buckets[0].StartYear)
	}
}

func TestBuildCensusReportRealSeedAndWarmBoundaryFixtureIntegration(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/warm-century-boundaries.ndjson")
	if err != nil {
		t.Fatalf("read warm boundary fixture: %v", err)
	}

	cases := []struct {
		qid        string
		name       string
		at         string
		articleURL string
	}{
		{qid: "Q9990001900", name: "Boundary 1900 event", at: "1900-01-01T00:00:00Z", articleURL: "https://en.wikipedia.org/wiki/Boundary_1900_event"},
		{qid: "Q9990002000", name: "Boundary 2000 event", at: "2000-01-01T00:00:00Z", articleURL: "https://en.wikipedia.org/wiki/Boundary_2000_event"},
	}
	records := make([]WikidataRecord, 0, len(cases))
	for _, tc := range cases {
		record, qid, ok := normalizeBinding(map[string]struct {
			Value string `json:"value"`
		}{
			"item":      {Value: "http://www.wikidata.org/entity/" + tc.qid},
			"itemLabel": {Value: tc.name},
			"time":      {Value: tc.at},
			"coord":     {Value: "Point(13.35 52.51)"},
			"sitelinks": {Value: "8"},
			"article":   {Value: tc.articleURL},
			"partOf":    {Value: "http://www.wikidata.org/entity/Q362"},
		}, EventClasses[0])
		if !ok || qid != tc.qid {
			t.Fatalf("normalizeBinding(%s) = %#v, %q, %t", tc.at, record, qid, ok)
		}
		records = append(records, *record)
	}
	produced, err := EncodeWarmEvents(records)
	if err != nil {
		t.Fatalf("EncodeWarmEvents: %v", err)
	}
	if !bytes.Equal(produced, fixture) {
		t.Fatalf("fixture differs from producer output\n got: %s\nwant: %s", produced, fixture)
	}

	seedOnly, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed(seedOnly): %v", err)
	}
	seedOnlyReport, err := BuildCensusReport(seedOnly, WarmSourceNone, "")
	if err != nil {
		t.Fatalf("BuildCensusReport(seedOnly): %v", err)
	}

	merged, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed(merged): %v", err)
	}
	added, skipped, err := MergeWarmEvents(merged, fixture)
	if err != nil {
		t.Fatalf("MergeWarmEvents: %v", err)
	}
	if added != 2 || skipped != 0 {
		t.Fatalf("MergeWarmEvents added=%d skipped=%d, want 2/0", added, skipped)
	}

	mergedReport, err := BuildCensusReport(merged, WarmSourceWarmFile, testSHA256(string(fixture)))
	if err != nil {
		t.Fatalf("BuildCensusReport(merged): %v", err)
	}
	if mergedReport.ImportReport.Accepted.Warm != 2 {
		t.Fatalf("ImportReport.Accepted.Warm = %d, want 2", mergedReport.ImportReport.Accepted.Warm)
	}

	if delta := centuryTypeCount(mergedReport, 1900, "event") - centuryTypeCount(seedOnlyReport, 1900, "event"); delta != 1 {
		t.Fatalf("1900 event delta = %d, want 1", delta)
	}
	if delta := centuryTypeCount(mergedReport, 2000, "event") - centuryTypeCount(seedOnlyReport, 2000, "event"); delta != 1 {
		t.Fatalf("2000 event delta = %d, want 1", delta)
	}
}

func testEntity(t *testing.T, name, entityType, precision string, t0 float64) *model.Entity {
	t.Helper()

	return &model.Entity{
		SeedID:     "seed-" + name,
		Type:       entityType,
		Name:       name,
		T0:         t0,
		T1:         t0,
		Precision:  precision,
		Status:     "documented",
		Categories: []string{"war"},
		Importance: 0.5,
	}
}

func testEntityWithCoverage(t *testing.T, name, entityType, precision string, t0 float64, point []float64, wikipedia string) *model.Entity {
	t.Helper()

	entity := testEntity(t, name, entityType, precision, t0)
	entity.Point = point
	entity.Wikipedia = wikipedia
	return entity
}

func mustUnixTime(t *testing.T, value string) float64 {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", value, err)
	}
	return float64(parsed.Unix())
}

func equalPrecisionCounts(got, want []CensusPrecisionCount) bool {
	if len(got) != len(want) {
		return false
	}
	for idx := range want {
		if got[idx] != want[idx] {
			return false
		}
	}
	return true
}

func centuryTypeCount(report CensusReport, start float64, entityType string) int {
	for _, bucket := range report.Buckets {
		if bucket.StartYear != start {
			continue
		}
		for _, row := range bucket.Types {
			if row.Type == entityType {
				return row.Stats.Count
			}
		}
		return 0
	}
	return 0
}

// The warm WDQS feed encodes out-of-range years as a year plus a mid-year
// fraction (parseWikidataTime), so a BCE date in the second half of its year
// arrives with a fractional T0 at year precision. Rounding to the nearest year
// pushed those into the following year, and at a century boundary into the
// following century.
func TestCensusYearKeepsMidYearInstantsInTheirOwnYear(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		value       string
		wantYear    float64
		wantCentury float64
	}{
		{name: "September 101 BCE", value: "-0101-09-15", wantYear: -101, wantCentury: -200},
		{name: "September 100 BCE", value: "-0100-09-15", wantYear: -100, wantCentury: -100},
		{name: "January 101 BCE", value: "-0101-01-01", wantYear: -101, wantCentury: -200},
		{name: "November 201 BCE", value: "-0201-11-15", wantYear: -201, wantCentury: -300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t0, precision, ok := parseWikidataTime(tc.value + "T00:00:00Z")
			if !ok {
				t.Fatalf("parseWikidataTime(%q) failed", tc.value)
			}
			if precision != "year" {
				t.Fatalf("precision = %q, want year", precision)
			}
			entity := testEntity(t, tc.name, "event", precision, t0)
			if got := censusYearForEntity(entity); got != tc.wantYear {
				t.Fatalf("censusYearForEntity = %v, want %v", got, tc.wantYear)
			}
			if got, _ := censusBucketFor(censusYearForEntity(entity)); got != tc.wantCentury {
				t.Fatalf("century = %v, want %v", got, tc.wantCentury)
			}
		})
	}
}

// Both encodings of the same year must land on that year: the {"y": n} form
// inverts exactly, an ISO 1 January sits up to a day away on the mean-year
// scale, and neither may fall into the previous year.
func TestCensusYearAgreesAcrossBothEncodings(t *testing.T) {
	t.Parallel()

	for _, year := range []float64{-4000, -2000, -1000, -500, -101, -100, -44, 1, 500, 1000, 1500, 1815, 1900, 2000, 2100} {
		if got := censusYearAt(model.YearToSeconds(year), "century"); got != year {
			t.Fatalf("mean-year encoding of %v attributed to %v", year, got)
		}
	}
	for _, tc := range []struct {
		value string
		want  float64
	}{
		{"1900-01-01T00:00:00Z", 1900},
		{"2000-01-01T00:00:00Z", 2000},
		{"1000-01-01T00:00:00Z", 1000},
		{"1500-01-01T00:00:00Z", 1500},
	} {
		if got := censusYearAt(mustUnixTime(t, tc.value), "century"); got != tc.want {
			t.Fatalf("calendar encoding of %s attributed to %v, want %v", tc.value, got, tc.want)
		}
	}
}

// Late in a year is still that year: the census must floor, not round.
func TestCensusYearFloorsLateYearInstants(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		year float64
		want float64
	}{
		{year: -201.9, want: -202},
		{year: -100.6, want: -101},
		{year: 1899.7, want: 1899},
		{year: 1999.99, want: 1999},
	} {
		if got := censusYearAt(model.YearToSeconds(tc.year), "year"); got != tc.want {
			t.Fatalf("censusYearAt(year %v) = %v, want %v", tc.year, got, tc.want)
		}
	}
}
