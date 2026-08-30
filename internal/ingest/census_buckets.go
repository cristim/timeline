package ingest

import (
	"math"

	"wk/internal/model"
)

// Time attribution for both censuses.
//
// A census row has to say which century (or, in deep time, which coarser slice)
// an entity falls in. Two things make that less obvious than flooring a number:
//
//   - The model stores seconds, and an entity's seconds come from one of two
//     encodings: an exact calendar date, or the average-year approximation the
//     {"y": n} authoring form and every coarse Wikidata precision use. The two
//     differ by up to a day, which is nothing next to a century except exactly
//     at a century boundary, where it silently moves the row. 1900-01-01 at
//     century precision used to land in the 1800s for precisely this reason.
//   - A single century-sized bucket is meaningless at 4.5 billion years. Deep
//     time needs coarser slices or the report is one row per entity.

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

// censusYearAt resolves an instant to a year, picking the arithmetic that
// matches the encoding the entity's precision implies.
//
// At day precision and finer the instant is an exact calendar date, so the
// calendar answers: 31 December is a real date and belongs to its own year.
//
// Coarser than that, the start is a year boundary, and the two encodings
// disagree about where that boundary sits by up to 1.2 days (measured over
// [-4799, 12000]; the mean Gregorian year is exact over the calendar's
// 400-year cycle, but not within it). Rounding on the mean-year scale inverts
// the {"y": n} authoring form exactly and is within half a day of an exact
// calendar date, which is nothing against a decade, let alone a century. Going
// through the calendar here instead is what used to move an entity a whole
// century at a boundary.
func censusYearAt(seconds float64, precision string) float64 {
	if !usesExactCalendarDate(precision) {
		return normalizeSignedZero(math.Round(model.SecondsToYear(seconds)))
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

// lessCensusBucketKey orders slices by start year, then by width so a
// coincident pair is still deterministic.
func lessCensusBucketKey(a, b censusBucketKey) bool {
	if a.StartYear != b.StartYear {
		return a.StartYear < b.StartYear
	}
	return a.SpanYears < b.SpanYears
}
