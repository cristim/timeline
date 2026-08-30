package ingest

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"wk/internal/model"
)

// Wikidata time values -> the project timebase (DM-4: float64 seconds from
// 1970), the "calendar normalization" half of the SRC-3 normalize stage.
//
// Three things make this more than a date parse:
//
//   - Calendar model. A value carries either the proleptic Gregorian or the
//     Julian calendar. Both are converted through the Julian Day Number, so a
//     Julian date lands on the day it actually happened rather than 13 days
//     away. This is uniform: Wikidata also uses the Julian model for post-1582
//     dates (Russia until 1918), and those need the same shift.
//   - No year zero. Wikibase writes 44 BCE as "-0044", so the astronomical year
//     of a negative value is -(y)+1. Verified against Q1048 (Julius Caesar,
//     P570 = -0044-03-15, Julian, precision 11 = 15 March 44 BCE).
//   - Precision. A year-precision value is a year, not an instant (DM-5 forbids
//     inventing the missing digits), so every value converts to a [T0, T1)
//     window of its own unit, widened by the value's before/after uncertainty.
//     Where Wikidata is finer-grained than the model vocabulary (100 ky and
//     10 ky have no peer) the precision LABEL rounds coarse, never fine.
const (
	calendarModelGregorian = "http://www.wikidata.org/entity/Q1985727"
	calendarModelJulian    = "http://www.wikidata.org/entity/Q1985786"

	// unixEpochJDN is the Julian Day Number of 1970-01-01.
	unixEpochJDN = 2440588

	// The JDN formulas below use Go's truncating integer division, which
	// matches floor division only while their intermediate terms stay
	// non-negative. That holds from 4800 BCE onward; a day-precision claim
	// older than that is rejected rather than quietly widened.
	minCalendarYear = -4799
	maxCalendarYear = 999999
)

// WikidataTimeWindow is one converted time value.
type WikidataTimeWindow struct {
	T0            float64 // seconds from 1970, start of the precision unit
	T1            float64 // end of the unit; T1 > T0 always
	Precision     string  // model time_precision, never finer than the source
	Year          float64 // astronomical year of T0, for census attribution
	CalendarModel string  // the calendar the source stated it in
}

// modelPrecisionForWikidata maps Wikidata's precision scale onto the model
// vocabulary, always rounding to a COARSER label when there is no exact peer,
// never a finer one: overstating precision is the failure that matters. The
// model jumps straight from billion_year to million_year to millennium, so
// Wikidata's 100 My and 10 My round up to billion_year, and its 100 ky and
// 10 ky round up to million_year. Each window still carries its true span;
// only the label is conservative.
var modelPrecisionForWikidata = map[int]string{
	0:  "billion_year", // 1 Gy
	1:  "billion_year", // 100 My
	2:  "billion_year", // 10 My
	3:  "million_year", // 1 My
	4:  "million_year", // 100 ky
	5:  "million_year", // 10 ky
	6:  "millennium",
	7:  "century",
	8:  "decade",
	9:  "year",
	10: "month",
	11: "day",
	12: "hour",
	13: "minute",
	14: "second",
}

// modelPrecisionOrder runs coarsest to finest. FinestBucketFor cannot stand in
// for it: hour, minute and second all cap at the same render bucket, so it
// cannot rank them against each other.
var modelPrecisionOrder = []string{
	"billion_year", "million_year", "millennium", "century", "decade",
	"year", "month", "day", "hour", "minute", "second",
}

// modelPrecisionSeconds is the nominal length of each precision unit, used to
// pick an honest label for a window that stated uncertainty has widened.
var modelPrecisionSeconds = map[string]float64{
	"billion_year": 1e9 * model.SecondsPerYear,
	"million_year": 1e6 * model.SecondsPerYear,
	"millennium":   1e3 * model.SecondsPerYear,
	"century":      100 * model.SecondsPerYear,
	"decade":       10 * model.SecondsPerYear,
	"year":         model.SecondsPerYear,
	"month":        model.SecondsPerYear / 12,
	"day":          86400,
	"hour":         3600,
	"minute":       60,
	"second":       1,
}

// precisionForSpan returns the finest label whose unit still covers the span,
// so a value widened by its own before/after uncertainty stops claiming the
// precision of its unwidened form. It walks finest to coarsest: taking the
// first match in coarsest-first order would return billion_year for every
// span, understating as badly as the bug it exists to prevent.
func precisionForSpan(span float64) string {
	for i := len(modelPrecisionOrder) - 1; i >= 0; i-- {
		precision := modelPrecisionOrder[i]
		if modelPrecisionSeconds[precision] >= span {
			return precision
		}
	}
	return modelPrecisionOrder[0]
}

// wikidataPrecisionUnitYears covers the precisions coarser than a year, where
// calendar boundaries are irrelevant next to the unit and the project's
// average-year approximation (model.YearToSeconds) is the right arithmetic.
var wikidataPrecisionUnitYears = map[int]float64{
	0: 1e9, 1: 1e8, 2: 1e7, 3: 1e6, 4: 1e5, 5: 1e4, 6: 1e3, 7: 100, 8: 10,
}

type wikidataTimestamp struct {
	year                 int64 // astronomical: -0044 (44 BCE) parses to -43
	month, day           int
	hour, minute, second int
}

// ConvertWikidataTime turns one dump time claim into a precision-honest window.
func ConvertWikidataTime(fact wikidataDumpTimeFact) (WikidataTimeWindow, error) {
	precision, ok := modelPrecisionForWikidata[fact.Precision]
	if !ok {
		return WikidataTimeWindow{}, fmt.Errorf("unsupported time precision %d", fact.Precision)
	}
	if fact.Timezone != 0 {
		// Wikibase documents the field as unused and every value observed
		// carries 0; a non-zero one would silently shift the instant.
		return WikidataTimeWindow{}, fmt.Errorf("unsupported time zone offset %d", fact.Timezone)
	}
	if fact.Before < 0 || fact.After < 0 {
		return WikidataTimeWindow{}, fmt.Errorf("negative uncertainty before=%d after=%d", fact.Before, fact.After)
	}
	var calendar string
	switch fact.CalendarModel {
	case calendarModelGregorian:
		calendar = calendarModelGregorian
	case calendarModelJulian:
		calendar = calendarModelJulian
	default:
		return WikidataTimeWindow{}, fmt.Errorf("unknown calendar model %q", fact.CalendarModel)
	}

	ts, err := parseWikidataTimestamp(fact.Time, fact.Precision)
	if err != nil {
		return WikidataTimeWindow{}, err
	}

	start, end, err := precisionWindow(calendar, ts, fact.Precision)
	if err != nil {
		return WikidataTimeWindow{}, err
	}
	// The attribution year is the one the source stated, floored to its own
	// unit, in its own calendar. Recovering it from `start` instead would read
	// a Julian date against the Gregorian calendar, which lands a year early
	// for most of antiquity: exactly the range the census is about.
	year := float64(ts.year)
	if unit, ok := wikidataPrecisionUnitYears[fact.Precision]; ok {
		year = math.Floor(float64(ts.year)/unit) * unit
	}
	if fact.Before > 0 || fact.After > 0 {
		unit := end - start
		start -= float64(fact.Before) * unit
		end += float64(fact.After) * unit
		// A 61-day window is not a day-precision value however it was stated.
		precision = coarserPrecision(precision, precisionForSpan(end-start))
	}
	if !(end > start) || math.IsNaN(start) || math.IsInf(start, 0) || math.IsInf(end, 0) {
		return WikidataTimeWindow{}, fmt.Errorf("degenerate time window [%v,%v]", start, end)
	}
	return WikidataTimeWindow{
		T0:            start,
		T1:            end,
		Precision:     precision,
		Year:          normalizeSignedZero(year),
		CalendarModel: calendar,
	}, nil
}

// precisionWindow returns the half-open [start, end) of the value's own unit,
// computed in the value's own calendar so a Julian year runs from Julian
// 1 January to Julian 1 January.
func precisionWindow(calendar string, ts wikidataTimestamp, precision int) (float64, float64, error) {
	if unit, ok := wikidataPrecisionUnitYears[precision]; ok {
		start := math.Floor(float64(ts.year)/unit) * unit
		return model.YearToSeconds(start), model.YearToSeconds(start + unit), nil
	}
	if ts.year < minCalendarYear || ts.year > maxCalendarYear {
		return 0, 0, fmt.Errorf("year %d is outside the supported calendar range for precision %d", ts.year, precision)
	}

	dayStart := jdnSeconds(calendarJDN(calendar, ts.year, ts.month, ts.day))
	switch precision {
	case 9: // year
		return jdnSeconds(calendarJDN(calendar, ts.year, 1, 1)),
			jdnSeconds(calendarJDN(calendar, ts.year+1, 1, 1)), nil
	case 10: // month
		nextYear, nextMonth := ts.year, ts.month+1
		if nextMonth > 12 {
			nextYear, nextMonth = nextYear+1, 1
		}
		return jdnSeconds(calendarJDN(calendar, ts.year, ts.month, 1)),
			jdnSeconds(calendarJDN(calendar, nextYear, nextMonth, 1)), nil
	case 11: // day
		return dayStart, dayStart + 86400, nil
	case 12: // hour
		start := dayStart + float64(ts.hour)*3600
		return start, start + 3600, nil
	case 13: // minute
		start := dayStart + float64(ts.hour)*3600 + float64(ts.minute)*60
		return start, start + 60, nil
	case 14: // second
		start := dayStart + float64(ts.hour)*3600 + float64(ts.minute)*60 + float64(ts.second)
		return start, start + 1, nil
	}
	return 0, 0, fmt.Errorf("unsupported time precision %d", precision)
}

// parseWikidataTimestamp reads the "+YYYY-MM-DDThh:mm:ssZ" form. Wikibase pads
// month and day with zeros for values coarser than a month; a zero component at
// a precision that needs it is a malformed value.
func parseWikidataTimestamp(value string, precision int) (wikidataTimestamp, error) {
	if value == "" {
		return wikidataTimestamp{}, fmt.Errorf("empty time value")
	}
	sign := int64(1)
	switch value[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return wikidataTimestamp{}, fmt.Errorf("time %q has no sign", value)
	}
	rest := value[1:]
	yearEnd := strings.IndexByte(rest, '-')
	if yearEnd < 4 {
		return wikidataTimestamp{}, fmt.Errorf("time %q has a short year field", value)
	}
	year, err := strconv.ParseInt(rest[:yearEnd], 10, 64)
	if err != nil {
		return wikidataTimestamp{}, fmt.Errorf("time %q has an unparseable year: %w", value, err)
	}
	if year == 0 {
		// Wikibase numbers years without a zero: -0001 is 1 BCE.
		return wikidataTimestamp{}, fmt.Errorf("time %q uses year zero, which Wikidata does not define", value)
	}

	tail := rest[yearEnd+1:]
	if len(tail) != len("01-01T00:00:00Z") || tail[2] != '-' || tail[5] != 'T' ||
		tail[8] != ':' || tail[11] != ':' || tail[14] != 'Z' {
		return wikidataTimestamp{}, fmt.Errorf("malformed time %q", value)
	}
	fields := make([]int, 0, 5)
	for _, span := range [][2]int{{0, 2}, {3, 5}, {6, 8}, {9, 11}, {12, 14}} {
		n, err := strconv.Atoi(tail[span[0]:span[1]])
		if err != nil {
			return wikidataTimestamp{}, fmt.Errorf("malformed time %q", value)
		}
		fields = append(fields, n)
	}

	ts := wikidataTimestamp{
		year:   sign * year,
		month:  fields[0],
		day:    fields[1],
		hour:   fields[2],
		minute: fields[3],
		second: fields[4],
	}
	if sign < 0 {
		ts.year++ // -0044 (44 BCE) is astronomical year -43
	}
	// Components below the stated precision carry no information; zero them so
	// the window floors to its unit instead of inheriting stray digits.
	if precision < 14 {
		ts.second = 0
	}
	if precision < 13 {
		ts.minute = 0
	}
	if precision < 12 {
		ts.hour = 0
	}
	if precision < 11 {
		ts.day = 1
	}
	if precision < 10 {
		ts.month = 1
	}
	if ts.month < 1 || ts.month > 12 {
		return wikidataTimestamp{}, fmt.Errorf("time %q has month %d at precision %d", value, ts.month, precision)
	}
	if ts.day < 1 || ts.day > daysInMonth(ts.year, ts.month) {
		return wikidataTimestamp{}, fmt.Errorf("time %q has day %d at precision %d", value, ts.day, precision)
	}
	if ts.hour > 23 || ts.minute > 59 || ts.second > 59 {
		return wikidataTimestamp{}, fmt.Errorf("time %q has an out-of-range clock field", value)
	}
	return ts, nil
}

// daysInMonth uses the Julian leap rule, which is the more permissive of the
// two, as an upper bound. It only rejects days that exist in neither calendar;
// a Gregorian 1900-02-29 slips through and resolves to 1 March through the
// JDN conversion, which is not worth a second calendar-specific branch.
func daysInMonth(year int64, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if year%4 == 0 {
			return 29
		}
		return 28
	}
	return 0
}

func calendarJDN(calendar string, year int64, month, day int) int64 {
	if calendar == calendarModelJulian {
		return julianJDN(year, month, day)
	}
	return gregorianJDN(year, month, day)
}

// gregorianJDN and julianJDN are the standard Fliegel-Van Flandern conversions,
// valid for the proleptic calendars over the range guarded above.
func gregorianJDN(year int64, month, day int) int64 {
	a := int64(14-month) / 12
	y := year + 4800 - a
	m := int64(month) + 12*a - 3
	return int64(day) + (153*m+2)/5 + 365*y + y/4 - y/100 + y/400 - 32045
}

func julianJDN(year int64, month, day int) int64 {
	a := int64(14-month) / 12
	y := year + 4800 - a
	m := int64(month) + 12*a - 3
	return int64(day) + (153*m+2)/5 + 365*y + y/4 - 32083
}

func jdnSeconds(jdn int64) float64 {
	return float64(jdn-unixEpochJDN) * 86400
}

// gregorianYearAt returns the proleptic Gregorian year containing an instant,
// for the calendar range the JDN arithmetic covers.
func gregorianYearAt(seconds float64) (int64, bool) {
	days := math.Floor(seconds / 86400)
	if math.Abs(days) > 4e11 { // keeps the JDN arithmetic inside int64
		return 0, false
	}
	// Start from the mean-year estimate, then walk. The mean Gregorian year is
	// exact over the calendar's 400-year cycle, so the estimate is under a day
	// out and this settles in one step. The estimate is range-checked BEFORE
	// the walk: outside the guarded range the JDN formulas are not valid, so
	// the loop would have nothing sound to converge on.
	estimate := int64(math.Floor(model.SecondsToYear(seconds)))
	if estimate < minCalendarYear || estimate > maxCalendarYear {
		return 0, false
	}
	jdn := unixEpochJDN + int64(days)
	year := estimate
	for year > minCalendarYear && gregorianJDN(year, 1, 1) > jdn {
		year--
	}
	for year < maxCalendarYear && gregorianJDN(year+1, 1, 1) <= jdn {
		year++
	}
	if year < minCalendarYear || year > maxCalendarYear {
		return 0, false
	}
	return year, true
}

// wikidataStartTimeProperties and wikidataEndTimeProperties give the order in
// which an item's claims are consulted for the start and end of its span. A
// person has no P580, a battle has no P569, so one global order suffices.
var (
	wikidataStartTimeProperties = []string{"P580", "P585", "P571", "P569", "P575", "P577", "P574"}
	wikidataEndTimeProperties   = []string{"P582", "P576", "P570"}
)

// WikidataItemTime is an item's resolved span on the project timebase.
type WikidataItemTime struct {
	T0, T1        float64
	Precision     string
	Year          float64 // attribution year, in the calendar the source used
	CalendarModel string
}

// resolveWikidataItemTime picks the item's span from its time claims. The
// precision reported is the coarser of the two ends: a span is no more precise
// than its blurriest bound.
func resolveWikidataItemTime(counters *dumpCounters, claims []wikidataDumpTimeFact) (WikidataItemTime, bool) {
	earliest := func(candidate, best WikidataTimeWindow) bool { return candidate.T0 < best.T0 }
	latest := func(candidate, best WikidataTimeWindow) bool { return candidate.T1 > best.T1 }

	start, ok := selectWindow(counters, claims, wikidataStartTimeProperties, earliest)
	if !ok {
		return WikidataItemTime{}, false
	}
	resolved := WikidataItemTime{
		T0:            start.T0,
		T1:            start.T1,
		Precision:     start.Precision,
		Year:          start.Year,
		CalendarModel: start.CalendarModel,
	}
	if end, ok := selectWindow(counters, claims, wikidataEndTimeProperties, latest); ok && end.T1 >= start.T0 {
		resolved.T1 = end.T1
		resolved.Precision = coarserPrecision(start.Precision, end.Precision)
	}
	return resolved, true
}

// selectWindow walks the properties in priority order and, within the first
// property that yields anything convertible, keeps the window `better` picks.
// Conversion failures are counted, never silently ignored.
func selectWindow(
	counters *dumpCounters,
	claims []wikidataDumpTimeFact,
	properties []string,
	better func(candidate, best WikidataTimeWindow) bool,
) (WikidataTimeWindow, bool) {
	for _, property := range properties {
		best := WikidataTimeWindow{}
		found := false
		for _, claim := range claims {
			if claim.Property != property {
				continue
			}
			window, err := ConvertWikidataTime(claim)
			if err != nil {
				counters.skip(SkipUnconvertibleTimeFact)
				continue
			}
			if !found || better(window, best) {
				best, found = window, true
			}
		}
		if found {
			return best, true
		}
	}
	return WikidataTimeWindow{}, false
}

// coarserPrecision returns whichever of two model precisions is the coarser.
// Unknown names cannot reach here: ConvertWikidataTime only emits vocabulary
// members.
func coarserPrecision(a, b string) string {
	if precisionRank(b) < precisionRank(a) {
		return b
	}
	return a
}

func precisionRank(precision string) int {
	for rank, name := range modelPrecisionOrder {
		if name == precision {
			return rank
		}
	}
	return len(modelPrecisionOrder)
}
