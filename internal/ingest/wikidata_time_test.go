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
			if got.Year != model.SecondsToYear(got.T0) {
				t.Fatalf("year = %v, want %v", got.Year, model.SecondsToYear(got.T0))
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
