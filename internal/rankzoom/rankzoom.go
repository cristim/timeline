// Package rankzoom implements ZOOM-2/3: importance-driven bucket assignment.
// All of this runs at bake time; rendering never ranks anything live (ARCH-4).
package rankzoom

import (
	"fmt"
	"math"

	"wk/internal/model"
)

// importanceFloor[b] is the minimum importance to render at bucket b: the
// coarser the view, the more important an entity must be to earn a slot
// (ZOOM-1: ~20 things at universe scale, thousands at year scale). Config
// values, deliberately transparent and hand-tunable (07 design guard).
var importanceFloor = []float64{
	0.92, 0.90, 0.88, // T0-T2
	0.86, 0.84, 0.80, // T3-T5
	0.75, 0.65, 0.55, // T6-T8
	0.40, 0.25, 0.15, // T9-T11
	0.05, 0.0, // T12-T13
}

// MaxWindowsPerEntity caps how many windows one entity may be duplicated into
// at a given bucket (ARCH-4 denormalization, bounded). An entity whose span
// exceeds the cap at bucket b simply stops at the coarser bucket b-1: a
// billion-year era has no business as a year-lane band anyway.
const MaxWindowsPerEntity = 1024

// Bucketize sets BucketMin/BucketMax on every entity. Deterministic.
func Bucketize(entities []*model.Entity) error {
	if len(importanceFloor) != len(model.Buckets) {
		return fmt.Errorf("importanceFloor table has %d entries, want %d", len(importanceFloor), len(model.Buckets))
	}
	for _, e := range entities {
		finest, ok := model.FinestBucketFor(e.Precision)
		if !ok {
			return fmt.Errorf("entity %q: unknown precision %q", e.Slug, e.Precision)
		}
		// Windowed-bucket overflow guard (validated at ingest; enforced here too).
		if math.Abs(e.T0) > model.MaxWindowedTime || math.Abs(e.T1) > model.MaxWindowedTime {
			if finest > 2 {
				return fmt.Errorf("entity %q: |t|>MaxWindowedTime with finest bucket T%d", e.Slug, finest)
			}
		}
		// Long spans stop at the bucket where their window count exceeds the cap.
		for finest > 2 {
			w := model.Buckets[finest].WindowS
			if (e.T1-e.T0)/w <= MaxWindowsPerEntity {
				break
			}
			finest--
		}
		coarsest := 0
		for coarsest < len(model.Buckets)-1 && e.Importance < importanceFloor[coarsest] {
			coarsest++
		}
		if coarsest > finest {
			// Low importance + coarse precision: render only at its finest bucket.
			coarsest = finest
		}
		e.BucketMin = coarsest
		e.BucketMax = finest
	}
	return nil
}

// ChunkCap is the max items per chunk file (API-1 "bounded by construction").
const ChunkCap = 200

// DiversityCap bounds any single category's share of an "all" chunk (ZOOM-4:
// 1942 must not render as 200 battles and nothing else).
const DiversityCap = 80
