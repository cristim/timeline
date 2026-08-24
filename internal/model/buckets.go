package model

// Temporal buckets T0..T13 (ZOOM-3). Buckets T0-T2 are "single-window": one
// chunk per (tile, category) covering the whole axis, because far-past and
// far-future times (up to ~1e107 s for the heat-death horizon) would overflow
// integer window indexes. From T3 down, |t| is bounded by MaxWindowedTime so
// floor(t/window) always fits in int64 (enforced at bucketize time).
const (
	SecondsPerYear = 31_556_952 // average Gregorian year

	// MaxWindowedTime bounds |t| (seconds from 1970) for entities allowed in
	// windowed buckets T3+: ~31.7 billion years, comfortably past both the Big
	// Bang (-4.35e17 s) and any solar-system-scale future date.
	MaxWindowedTime = 1e18
)

type Bucket struct {
	ID      string  `json:"id"`
	WindowS float64 `json:"window_s"` // 0 = single window spanning all time
	// Windows lists the non-empty window indexes for this bucket in the baked
	// dataset (set by bake, shipped in the manifest). The client renders any
	// other window as empty locally - so no empty chunk files exist and a 404
	// on a listed window is a bake bug (API-1). Indexes stay < 2^53, safe as
	// JSON numbers.
	Windows []int64 `json:"windows,omitempty"`
}

// Buckets is ordered coarse -> fine. Index in this slice is the bucket number.
var Buckets = []Bucket{
	{ID: "T0", WindowS: 0},                    // universe scale
	{ID: "T1", WindowS: 0},                    // billion-year scale
	{ID: "T2", WindowS: 0},                    // 100 My scale
	{ID: "T3", WindowS: 1e7 * SecondsPerYear}, // 10 My windows
	{ID: "T4", WindowS: 1e6 * SecondsPerYear}, // 1 My
	{ID: "T5", WindowS: 1e5 * SecondsPerYear}, // 100 ky
	{ID: "T6", WindowS: 1e4 * SecondsPerYear}, // 10 ky
	{ID: "T7", WindowS: 1e3 * SecondsPerYear}, // millennium
	{ID: "T8", WindowS: 100 * SecondsPerYear}, // century
	{ID: "T9", WindowS: 10 * SecondsPerYear},  // decade
	{ID: "T10", WindowS: 1 * SecondsPerYear},  // year
	{ID: "T11", WindowS: SecondsPerYear / 12}, // month
	{ID: "T12", WindowS: 86_400},              // day
	{ID: "T13", WindowS: 3_600},               // hour
}

// WindowIndex returns the chunk window index for a time t (seconds since 1970)
// in bucket b. Single-window buckets always return 0. The same computation runs
// client-side from manifest data; both use float64 floor (API-5 drift rule).
func (b Bucket) WindowIndex(t float64) int64 {
	if b.WindowS == 0 {
		return 0
	}
	return int64(floorDiv(t, b.WindowS))
}

func floorDiv(t, w float64) float64 {
	q := t / w
	f := float64(int64(q))
	if q < 0 && q != f {
		f--
	}
	return f
}
