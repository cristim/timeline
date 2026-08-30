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

// censusYearAt resolves an instant to a year, reconciling the two encodings.
// Below day precision an entity's start is a year boundary in whichever
// encoding produced it, so an instant sitting less than a day short of the next
// boundary belongs to that next year. At day precision and finer, 31 December
// is a real date and is left alone.
func censusYearAt(seconds float64, precision string) float64 {
	approximate := normalizeSignedZero(model.SecondsToYear(seconds))
	year, ok := gregorianYearAt(seconds)
	if !ok {
		return approximate
	}
	if snapsToYearBoundary(precision) && jdnSeconds(gregorianJDN(year+1, 1, 1))-seconds <= 86400 {
		year++
	}
	return normalizeSignedZero(float64(year))
}

func snapsToYearBoundary(precision string) bool {
	switch precision {
	case "day", "hour", "minute", "second":
		return false
	default:
		return true
	}
}

func normalizeSignedZero(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}
