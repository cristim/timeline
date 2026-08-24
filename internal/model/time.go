package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Timeline times are float64 seconds since 1970 (DM-4). float64 spans the
// full range (Big Bang -4.35e17 s .. heat death ~3e107 s) with precision far
// finer than each era's time_precision; the seed/authoring format accepts
// either an ISO date/datetime string or {"y": <year number>} for deep time.

// ParseSeedTime decodes a seed-file time value into seconds since 1970.
func ParseSeedTime(raw json.RawMessage) (float64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		for _, layout := range []string{"2006-01-02", time.RFC3339} {
			if t, err := time.Parse(layout, s); err == nil {
				return float64(t.Unix()), nil
			}
		}
		return 0, fmt.Errorf("unparseable time string %q (want YYYY-MM-DD or RFC3339)", s)
	}
	var y struct {
		Y *float64 `json:"y"`
	}
	if err := json.Unmarshal(raw, &y); err == nil && y.Y != nil {
		return YearToSeconds(*y.Y), nil
	}
	return 0, fmt.Errorf("unparseable time value %s (want string or {\"y\": n})", string(raw))
}

// YearToSeconds converts an (astronomical) year number to seconds since 1970.
// At the scales where {"y":...} is used (|y| >= thousands), the difference
// between calendar year boundaries and the average-year approximation is far
// below the stated time_precision.
func YearToSeconds(y float64) float64 {
	return (y - 1970) * SecondsPerYear
}

// SecondsToYear is the inverse, used for display and window labels.
func SecondsToYear(t float64) float64 {
	return t/SecondsPerYear + 1970
}

var precisionFinestBucket = map[string]int{
	"billion_year": 2,
	"million_year": 4,
	"millennium":   7,
	"century":      8,
	"decade":       9,
	"year":         10,
	"month":        11,
	"day":          12,
	"hour":         13,
	"minute":       13,
	"second":       13,
}

// FinestBucketFor returns the finest bucket index at which an entity with the
// given time precision may render (ZOOM-3): a million-year estimate has no
// business inside a day-scale view.
func FinestBucketFor(precision string) (int, bool) {
	b, ok := precisionFinestBucket[precision]
	return b, ok
}
