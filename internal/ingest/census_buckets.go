package ingest

import (
	"cmp"
	"math"

	"wk/internal/model"
)

// Time attribution for both censuses.
//
// A census row has to say which century, or in deep time which coarser slice,
// an entity falls in. Two things make that less obvious than flooring a number:
//
//   - A single century-sized slice is meaningless at 4.5 billion years, so the
//     span widens with distance from the present or the report degenerates into
//     one row per entity.
//   - The model stores seconds, and they come from one of two encodings: an
//     exact calendar date, or the average-year approximation the {"y": n}
//     authoring form and every coarse Wikidata precision use. Reading one with
//     the other's arithmetic moves a row by a fraction of a day, which is
//     nothing anywhere except exactly at a slice boundary, where it moves the
//     row a whole century. See censusYearAt.
//
// The dump census does not use censusYearAt: an imported item still knows the
// year its source stated, in the calendar it stated it in, which is better than
// anything recoverable from the seconds afterwards.

// censusBucketTiers are ordered from the finest span to the coarsest; the first
// tier whose limit covers |year| wins.
var censusBucketTiers = []struct {
	limit float64 // exclusive upper bound on |year|
	span  float64
}{
	{limit: 1e4, span: 100}, // centuries through recorded history
	{limit: 1e6, span: 1e4}, // ten-thousand-year slices
	{limit: 1e9, span: 1e6}, // million-year slices
	{limit: math.Inf(1), span: 1e9},
}

// censusBucketKey identifies one slice. Start year alone is ambiguous: the
// first year of a century can also be the first year of a ten-thousand-year
// slice (-10000 is both), and keying on it alone merges two slices of
// different width into one row.
type censusBucketKey struct {
	StartYear float64
	SpanYears float64
}

// censusBucketFor returns the start year and span of the slice holding a year.
func censusBucketFor(year float64) (float64, float64) {
	magnitude := math.Abs(year)
	for _, tier := range censusBucketTiers {
		if magnitude < tier.limit {
			return normalizeSignedZero(math.Floor(year/tier.span) * tier.span), tier.span
		}
	}
	// Unreachable: the last tier's limit is infinite.
	return normalizeSignedZero(year), 1
}

// censusYearForEntity is the year a normalized entity is attributed to.
func censusYearForEntity(entity *model.Entity) float64 {
	return censusYearAt(entity.T0, entity.Precision)
}

// maxEncodingDriftYears is how far apart the two encodings can put the same
// year boundary. The mean Gregorian year is exact over the calendar's 400-year
// cycle but not within it, so an exact 1 January can sit slightly either side
// of its mean-year position: measured at up to 1.2 days over [-4799, 12000].
// Two days carries that with margin and is still four orders of magnitude
// below the finest slice this file produces.
// The .0 is load-bearing: every other operand is an untyped integer constant,
// so an integer 86400 would make this whole expression integer division and
// silently evaluate to zero.
const maxEncodingDriftYears = 2 * 86400.0 / model.SecondsPerYear

// censusYearAt resolves an instant to the year containing it, picking the
// arithmetic that matches the encoding the entity's precision implies.
//
// At day precision and finer the instant is an exact calendar date, so the
// calendar answers: 31 December is a real date and belongs to its own year.
//
// Coarser than that the value is a year, reached by one of two routes: the
// {"y": n} authoring form and the coarse Wikidata precisions go through the
// mean-year scale, which SecondsToYear inverts exactly, while an ISO date
// carries a true 1 January that the mean-year scale puts up to a day early.
// Flooring alone would drop that second case to the previous year (and at a
// century boundary, the previous century, which is the bug this used to have).
// Flooring after nudging past the drift handles both, and keeps a mid-year
// instant in its own year rather than rounding it up the way a half-year
// round does.
func censusYearAt(seconds float64, precision string) float64 {
	if !usesExactCalendarDate(precision) {
		return normalizeSignedZero(math.Floor(model.SecondsToYear(seconds) + maxEncodingDriftYears))
	}
	year, ok := gregorianYearAt(seconds)
	if !ok {
		return normalizeSignedZero(model.SecondsToYear(seconds))
	}
	return normalizeSignedZero(float64(year))
}

func usesExactCalendarDate(precision string) bool {
	switch precision {
	case "day", "hour", "minute", "second":
		return true
	default:
		return false
	}
}

func normalizeSignedZero(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}

func censusBucketKeyFor(year float64) censusBucketKey {
	start, span := censusBucketFor(year)
	return censusBucketKey{StartYear: start, SpanYears: span}
}

// compareCensusBucketKey orders slices by start year, then by width so a
// coincident pair is still deterministic.
func compareCensusBucketKey(a, b censusBucketKey) int {
	if a.StartYear != b.StartYear {
		return cmp.Compare(a.StartYear, b.StartYear)
	}
	return cmp.Compare(a.SpanYears, b.SpanYears)
}
