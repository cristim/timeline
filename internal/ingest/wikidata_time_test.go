package ingest

import (
	"math"
	"strings"
	"testing"
	"time"

	"wk/internal/model"
)

func gregorianFact(value string, precision int) wikidataDumpTimeFact {
	return wikidataDumpTimeFact{Property: "P585", Time: value, Precision: precision, CalendarModel: calendarModelGregorian}
}

func julianFact(value string, precision int) wikidataDumpTimeFact {
	return wikidataDumpTimeFact{Property: "P585", Time: value, Precision: precision, CalendarModel: calendarModelJulian}
}

func unixDate(t *testing.T, value string) float64 {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return float64(parsed.Unix())
}

func TestConvertWikidataTimeKnownConversions(t *testing.T) {
	tests := []struct {
		name          string
		fact          wikidataDumpTimeFact
		wantPrecision string
		wantT0        float64
		wantT1        float64
		wantYear      float64 // zero means "not asserted"
	}{
		{
			name:          "Apollo 11 landing, Gregorian day",
			fact:          gregorianFact("+1969-07-20T00:00:00Z", 11),
			wantPrecision: "day",
			wantT0:        unixDate(t, "1969-07-20"),
			wantT1:        unixDate(t, "1969-07-21"),
		},
		{
			name:          "year precision is a year window, not an instant",
			fact:          gregorianFact("+1815-01-01T00:00:00Z", 9),
			wantPrecision: "year",
			wantT0:        unixDate(t, "1815-01-01"),
			wantT1:        unixDate(t, "1816-01-01"),
		},
		{
			name:          "month precision spans the month",
			fact:          gregorianFact("+1815-06-00T00:00:00Z", 10),
			wantPrecision: "month",
			wantT0:        unixDate(t, "1815-06-01"),
			wantT1:        unixDate(t, "1815-07-01"),
		},
		{
			name:          "December month precision rolls the year",
			fact:          gregorianFact("+1815-12-00T00:00:00Z", 10),
			wantPrecision: "month",
			wantT0:        unixDate(t, "1815-12-01"),
			wantT1:        unixDate(t, "1816-01-01"),
		},
		{
			// Q1048 P570: 15 March 44 BCE in the Julian calendar is
			// 13 March 44 BCE proleptic Gregorian, i.e. JDN 1705426.
			name:          "Caesar's death converts Julian to Gregorian",
			fact:          julianFact("-0044-03-15T00:00:00Z", 11),
			wantPrecision: "day",
			wantT0:        float64(1705426-unixEpochJDN) * 86400,
			wantT1:        float64(1705427-unixEpochJDN) * 86400,
			wantYear:      -43, // 44 BCE, stated in the Julian calendar
		},
		{
			// 4 October 1582 Julian is the day before the Gregorian switch;
			// 15 October 1582 Gregorian is the next calendar day.
			name:          "Julian 1582-10-04 and Gregorian 1582-10-15 are consecutive days",
			fact:          julianFact("+1582-10-04T00:00:00Z", 11),
			wantPrecision: "day",
			wantT0:        unixDate(t, "1582-10-14"),
			wantT1:        unixDate(t, "1582-10-15"),
		},
		{
			name:          "post-1582 Julian dates still shift",
			fact:          julianFact("+1917-10-25T00:00:00Z", 11),
			wantPrecision: "day",
			wantT0:        unixDate(t, "1917-11-07"),
			wantT1:        unixDate(t, "1917-11-08"),
			wantYear:      1917, // the Julian date's own year, not the Gregorian one
		},
		{
			name:          "hour precision floors below the hour",
			fact:          gregorianFact("+1969-07-20T20:17:40Z", 12),
			wantPrecision: "hour",
			wantT0:        unixDate(t, "1969-07-20") + 20*3600,
			wantT1:        unixDate(t, "1969-07-20") + 21*3600,
		},
		{
			name:          "second precision keeps every field",
			fact:          gregorianFact("+1969-07-20T20:17:40Z", 14),
			wantPrecision: "second",
			wantT0:        unixDate(t, "1969-07-20") + 20*3600 + 17*60 + 40,
			wantT1:        unixDate(t, "1969-07-20") + 20*3600 + 17*60 + 41,
		},
		{
			name:          "decade precision floors to the decade",
			fact:          gregorianFact("+1967-00-00T00:00:00Z", 8),
			wantPrecision: "decade",
			wantT0:        model.YearToSeconds(1960),
			wantT1:        model.YearToSeconds(1970),
		},
		{
			name:          "century precision floors to the century",
			fact:          gregorianFact("+1815-00-00T00:00:00Z", 7),
			wantPrecision: "century",
			wantT0:        model.YearToSeconds(1800),
			wantT1:        model.YearToSeconds(1900),
			wantYear:      1800,
		},
		{
			name:          "millennium precision",
			fact:          gregorianFact("+1815-00-00T00:00:00Z", 6),
			wantPrecision: "millennium",
			wantT0:        model.YearToSeconds(1000),
			wantT1:        model.YearToSeconds(2000),
		},
		{
			name:          "ten thousand years has no model peer and rounds coarse",
			fact:          gregorianFact("-0012000-00-00T00:00:00Z", 5),
			wantPrecision: "million_year",
			wantT0:        model.YearToSeconds(-20000),
			wantT1:        model.YearToSeconds(-10000),
		},
		{
			name:          "million year precision",
			fact:          gregorianFact("-66000000-00-00T00:00:00Z", 3),
			wantPrecision: "million_year",
			wantT0:        model.YearToSeconds(-66000000),
			wantT1:        model.YearToSeconds(-65000000),
			wantYear:      -66000000,
		},
		{
			name:          "billion year precision",
			fact:          gregorianFact("-4500000000-00-00T00:00:00Z", 0),
			wantPrecision: "billion_year",
			wantT0:        model.YearToSeconds(-5000000000),
			wantT1:        model.YearToSeconds(-4000000000),
		},
		{
			name:          "BCE years drop the missing year zero",
			fact:          gregorianFact("-0001-01-01T00:00:00Z", 9),
			wantPrecision: "year",
			wantT0:        float64(gregorianJDN(0, 1, 1)-unixEpochJDN) * 86400,
			wantT1:        float64(gregorianJDN(1, 1, 1)-unixEpochJDN) * 86400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertWikidataTime(tt.fact)
			if err != nil {
				t.Fatalf("ConvertWikidataTime: %v", err)
			}
			if got.Precision != tt.wantPrecision {
				t.Fatalf("precision = %q, want %q", got.Precision, tt.wantPrecision)
			}
			if got.T0 != tt.wantT0 {
				t.Fatalf("t0 = %v, want %v", got.T0, tt.wantT0)
			}
			if got.T1 != tt.wantT1 {
				t.Fatalf("t1 = %v, want %v", got.T1, tt.wantT1)
			}
			// Year is the year the source stated, in the source calendar, so
			// it is always whole and never re-derived from the seconds.
			if got.Year != math.Trunc(got.Year) {
				t.Fatalf("year = %v, want a whole year", got.Year)
			}
			if tt.wantYear != 0 && got.Year != tt.wantYear {
				t.Fatalf("year = %v, want %v", got.Year, tt.wantYear)
			}
		})
	}
}

// The Julian year 1900 runs from Julian 1 January, which is 13 January
// Gregorian: the window is computed in the source calendar, not shifted after.
func TestConvertWikidataTimeComputesYearWindowsInTheSourceCalendar(t *testing.T) {
	got, err := ConvertWikidataTime(julianFact("+1900-00-00T00:00:00Z", 9))
	if err != nil {
		t.Fatalf("ConvertWikidataTime: %v", err)
	}
	if want := unixDate(t, "1900-01-13"); got.T0 != want {
		t.Fatalf("t0 = %v, want %v", got.T0, want)
	}
	if want := unixDate(t, "1901-01-14"); got.T1 != want {
		t.Fatalf("t1 = %v, want %v", got.T1, want)
	}
}

func TestConvertWikidataTimeWidensByStatedUncertainty(t *testing.T) {
	fact := gregorianFact("+1815-01-01T00:00:00Z", 9)
	fact.Before = 2
	fact.After = 1
	got, err := ConvertWikidataTime(fact)
	if err != nil {
		t.Fatalf("ConvertWikidataTime: %v", err)
	}
	base, err := ConvertWikidataTime(gregorianFact("+1815-01-01T00:00:00Z", 9))
	if err != nil {
		t.Fatalf("ConvertWikidataTime baseline: %v", err)
	}
	unit := base.T1 - base.T0
	if want := base.T0 - 2*unit; got.T0 != want {
		t.Fatalf("t0 = %v, want %v", got.T0, want)
	}
	if want := base.T1 + unit; got.T1 != want {
		t.Fatalf("t1 = %v, want %v", got.T1, want)
	}
}

func TestConvertWikidataTimeFailsLoudly(t *testing.T) {
	tests := []struct {
		name string
		fact wikidataDumpTimeFact
		want string
	}{
		{"unknown calendar", wikidataDumpTimeFact{Time: "+1969-07-20T00:00:00Z", Precision: 11, CalendarModel: "http://www.wikidata.org/entity/Q9999"}, "unknown calendar model"},
		{"missing calendar", wikidataDumpTimeFact{Time: "+1969-07-20T00:00:00Z", Precision: 11}, "unknown calendar model"},
		{"unsupported precision", gregorianFact("+1969-07-20T00:00:00Z", 15), "unsupported time precision"},
		{"year zero", gregorianFact("+0000-01-01T00:00:00Z", 9), "year zero"},
		{"no sign", gregorianFact("1969-07-20T00:00:00Z", 11), "has no sign"},
		{"short year", gregorianFact("+69-07-20T00:00:00Z", 11), "short year field"},
		{"truncated", gregorianFact("+1969-07", 11), "malformed time"},
		{"month 13", gregorianFact("+1969-13-20T00:00:00Z", 11), "has month 13"},
		{"day 32", gregorianFact("+1969-07-32T00:00:00Z", 11), "has day 32"},
		{"zero day at day precision", gregorianFact("+1969-07-00T00:00:00Z", 11), "has day 0"},
		{"zero month at month precision", gregorianFact("+1969-00-00T00:00:00Z", 10), "has month 0"},
		{"hour 24", gregorianFact("+1969-07-20T24:00:00Z", 12), "out-of-range clock field"},
		{"day precision before the supported range", gregorianFact("-9999-01-01T00:00:00Z", 11), "outside the supported calendar range"},
		{"time zone offset", wikidataDumpTimeFact{Time: "+1969-07-20T00:00:00Z", Precision: 11, CalendarModel: calendarModelGregorian, Timezone: 60}, "unsupported time zone offset"},
		{"negative uncertainty", wikidataDumpTimeFact{Time: "+1969-07-20T00:00:00Z", Precision: 11, CalendarModel: calendarModelGregorian, Before: -1}, "negative uncertainty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConvertWikidataTime(tt.fact)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ConvertWikidataTime error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// Every converted window must stay inside the guard that model.Validate
// applies, and must be ordered.
func TestConvertWikidataTimeWindowsAreOrderedAndBounded(t *testing.T) {
	for precision := 0; precision <= 14; precision++ {
		value := "+1969-07-20T20:17:40Z"
		if precision < 10 {
			value = "+1969-00-00T00:00:00Z"
		}
		got, err := ConvertWikidataTime(gregorianFact(value, precision))
		if err != nil {
			t.Fatalf("precision %d: %v", precision, err)
		}
		if !(got.T1 > got.T0) {
			t.Fatalf("precision %d: window [%v,%v] is not ordered", precision, got.T0, got.T1)
		}
		if _, ok := model.FinestBucketFor(got.Precision); !ok {
			t.Fatalf("precision %d: %q is not a model time precision", precision, got.Precision)
		}
		if math.Abs(got.T0) > model.MaxWindowedTime && got.Precision != "billion_year" {
			t.Fatalf("precision %d: |t0| exceeds the windowed-time guard at %q", precision, got.Precision)
		}
	}
}

// The reported calendar must be the one the CHOSEN claim was stated in, not
// whichever claim of that property happened to be listed first: the census
// splits Julian from Gregorian on it.
func TestResolveWikidataItemTimeReportsTheChosenClaimsCalendar(t *testing.T) {
	claims := []wikidataDumpTimeFact{
		{Property: "P580", Time: "+1700-01-01T00:00:00Z", Precision: 11, CalendarModel: calendarModelGregorian},
		{Property: "P580", Time: "+1600-01-01T00:00:00Z", Precision: 11, CalendarModel: calendarModelJulian},
	}
	resolved, ok := resolveWikidataItemTime(newDumpCounters(), claims)
	if !ok {
		t.Fatal("resolveWikidataItemTime returned no time")
	}
	if resolved.CalendarModel != calendarModelJulian {
		t.Fatalf("calendar = %q, want the earliest claim's Julian model", resolved.CalendarModel)
	}
	if got := censusYearAt(resolved.T0, resolved.Precision); got != 1600 {
		t.Fatalf("year = %v, want 1600", got)
	}
}

// A span is no more precise than its blurriest end.
func TestResolveWikidataItemTimeTakesTheCoarserPrecision(t *testing.T) {
	claims := []wikidataDumpTimeFact{
		{Property: "P580", Time: "+1914-07-28T00:00:00Z", Precision: 11, CalendarModel: calendarModelGregorian},
		{Property: "P582", Time: "+1918-00-00T00:00:00Z", Precision: 7, CalendarModel: calendarModelGregorian},
	}
	resolved, ok := resolveWikidataItemTime(newDumpCounters(), claims)
	if !ok {
		t.Fatal("resolveWikidataItemTime returned no time")
	}
	if resolved.Precision != "century" {
		t.Fatalf("precision = %q, want century", resolved.Precision)
	}
}

// An unconvertible claim is counted, not silently ignored.
func TestResolveWikidataItemTimeCountsUnconvertibleClaims(t *testing.T) {
	counters := newDumpCounters()
	claims := []wikidataDumpTimeFact{
		{Property: "P580", Time: "+0000-01-01T00:00:00Z", Precision: 11, CalendarModel: calendarModelGregorian},
	}
	if _, ok := resolveWikidataItemTime(counters, claims); ok {
		t.Fatal("a year-zero claim resolved")
	}
	if counters.skips[SkipUnconvertibleTimeFact] != 1 {
		t.Fatalf("skips = %#v, want one unconvertible time fact", counters.skips)
	}
}

// gregorianYearAt must terminate and decline cleanly outside the range its
// formulas are valid over, rather than walking year by year on bad arithmetic.
func TestGregorianYearAtDeclinesOutsideTheCalendarRange(t *testing.T) {
	for _, year := range []float64{-1e9, -100000, 2e6, 1e12} {
		if got, ok := gregorianYearAt(model.YearToSeconds(year)); ok {
			t.Fatalf("gregorianYearAt(year %v) = %d, want a refusal", year, got)
		}
	}
	for _, year := range []float64{-4000, -44, 1, 1969, 500000} {
		got, ok := gregorianYearAt(model.YearToSeconds(year))
		if !ok {
			t.Fatalf("gregorianYearAt(year %v) refused", year)
		}
		if float64(got) < year-1 || float64(got) > year {
			t.Fatalf("gregorianYearAt(year %v) = %d, want within a year", year, got)
		}
	}
}

// Deep time keeps the mean-year scale rather than a calendar year it has no
// business claiming.
func TestCensusYearAtFallsBackInDeepTime(t *testing.T) {
	seconds := model.YearToSeconds(-4.5e9)
	got := censusYearAt(seconds, "billion_year")
	if got != model.SecondsToYear(seconds) {
		t.Fatalf("censusYearAt = %v, want the mean-year value %v", got, model.SecondsToYear(seconds))
	}
}

// Wikidata's 100 My and 10 My precisions sit between the model's billion_year
// and million_year, so they must round to the COARSER of the two. Labelling
// them million_year claimed up to 100x the precision the source stated.
func TestConvertWikidataTimeRoundsCoarseWhereTheModelHasNoPeer(t *testing.T) {
	tests := []struct {
		precision int
		want      string
	}{
		{precision: 0, want: "billion_year"}, // 1 Gy
		{precision: 1, want: "billion_year"}, // 100 My
		{precision: 2, want: "billion_year"}, // 10 My
		{precision: 3, want: "million_year"}, // 1 My, an exact peer
		{precision: 4, want: "million_year"}, // 100 ky
		{precision: 5, want: "million_year"}, // 10 ky
		{precision: 6, want: "millennium"},
	}
	for _, tt := range tests {
		got, err := ConvertWikidataTime(gregorianFact("-0066000-00-00T00:00:00Z", tt.precision))
		if err != nil {
			t.Fatalf("precision %d: %v", tt.precision, err)
		}
		if got.Precision != tt.want {
			t.Fatalf("precision %d -> %q, want %q", tt.precision, got.Precision, tt.want)
		}
		// The window still carries the true span; only the label is coarse.
		unit := wikidataPrecisionUnitYears[tt.precision]
		if span := model.SecondsToYear(got.T1) - model.SecondsToYear(got.T0); math.Abs(span-unit) > 1 {
			t.Fatalf("precision %d span = %v years, want %v", tt.precision, span, unit)
		}
	}
}

// Uncertainty that widens a window must widen its label too: a 61-day window
// is not a day-precision value however it was stated.
func TestConvertWikidataTimeWidensThePrecisionLabelWithTheWindow(t *testing.T) {
	fact := gregorianFact("+1969-07-20T00:00:00Z", 11)
	fact.Before = 30
	fact.After = 30
	got, err := ConvertWikidataTime(fact)
	if err != nil {
		t.Fatalf("ConvertWikidataTime: %v", err)
	}
	// 61 days: too wide for a month, covered by a year. Not billion_year,
	// which would be honest but useless.
	if got.Precision != "year" {
		t.Fatalf("precision = %q, want year for a 61-day window", got.Precision)
	}
	if unit := modelPrecisionSeconds[got.Precision]; unit < got.T1-got.T0 {
		t.Fatalf("precision %q has unit %v, shorter than the %v window",
			got.Precision, unit, got.T1-got.T0)
	}
}

func TestPrecisionForSpanPicksTheFinestCoveringUnit(t *testing.T) {
	for _, tt := range []struct {
		span float64
		want string
	}{
		{span: 1, want: "second"},
		{span: 45, want: "minute"},
		{span: 3600, want: "hour"},
		{span: 86400, want: "day"},
		{span: 40 * 86400, want: "year"},
		{span: 5 * model.SecondsPerYear, want: "decade"},
		{span: 200 * model.SecondsPerYear, want: "millennium"},
		{span: 2e9 * model.SecondsPerYear, want: "billion_year"},
	} {
		if got := precisionForSpan(tt.span); got != tt.want {
			t.Fatalf("precisionForSpan(%v) = %q, want %q", tt.span, got, tt.want)
		}
		if _, ok := model.FinestBucketFor(precisionForSpan(tt.span)); !ok {
			t.Fatalf("precisionForSpan(%v) is not a model precision", tt.span)
		}
	}
}

// coarserPrecision has to be a total order. hour, minute and second all cap at
// the same render bucket, so ranking on that cannot separate them.
func TestCoarserPrecisionIsATotalOrder(t *testing.T) {
	for _, tt := range []struct{ a, b, want string }{
		{"second", "hour", "hour"},
		{"hour", "second", "hour"},
		{"minute", "second", "minute"},
		{"day", "minute", "day"},
		{"century", "day", "century"},
		{"year", "year", "year"},
	} {
		if got := coarserPrecision(tt.a, tt.b); got != tt.want {
			t.Fatalf("coarserPrecision(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}
