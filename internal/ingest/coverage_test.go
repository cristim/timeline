package ingest

import (
	"os"
	"testing"

	"wk/internal/model"
)

// The point of the two fetched layers is that the map is never blank while the
// cursor is inside recorded history or deep time. That is a property of the
// coverage windows, not of any one slice, so it is tested here rather than
// eyeballed: every year between the first and last slice must belong to
// exactly one slice, and scrubbing must walk them in order without skipping.

func loadLayerOrSkip(t *testing.T, dir string) []model.BorderLayer {
	t.Helper()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("%s not fetched; run `make fetch-geo`", dir)
	}
	slices, err := loadAreaSlices(dir, map[string]*model.Entity{})
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	if len(slices) < 2 {
		t.Fatalf("%s: %d slices, expected a whole layer", dir, len(slices))
	}
	return slices
}

func TestCoverageWindowsTileWithoutGaps(t *testing.T) {
	for _, dir := range []string{"../../data/geo/borders", "../../data/geo/paleo"} {
		slices := loadLayerOrSkip(t, dir)
		for i, s := range slices {
			if s.TFrom > s.Year || s.Year > s.TTo {
				t.Errorf("%s: slice %d sits outside its own window %d..%d", dir, s.Year, s.TFrom, s.TTo)
			}
			if i == 0 {
				continue
			}
			if prev := slices[i-1]; s.TFrom != prev.TTo+1 {
				t.Errorf("%s: %d ends at %d but %d starts at %d; windows must tile",
					dir, prev.Year, prev.TTo, s.Year, s.TFrom)
			}
		}
	}
}

// TestScrubVisitsEverySlice walks a cursor forward one year at a time through
// every window boundary and checks that the slice it lands on advances by
// exactly one each time. This is the "each meaningful change of the map is
// reflected when moving through time" property: no slice can be stepped over,
// and none can be visited twice.
func TestScrubVisitsEverySlice(t *testing.T) {
	for _, dir := range []string{"../../data/geo/borders", "../../data/geo/paleo"} {
		slices := loadLayerOrSkip(t, dir)

		// The client's rule, mirrored: the slice whose window holds the year
		// (coveringTimestep in web/src/lib/keyscheme.ts). Deliberately not
		// "nearest step" - spacing is uneven enough that the nearest slice is
		// routinely one whose window ended long before.
		coveringSlice := func(year int) int {
			for i, s := range slices {
				if year >= s.TFrom && year <= s.TTo {
					return i
				}
			}
			return -1
		}

		// Three probes per slice - first year, its own year, last year - plus
		// the year on either side of every boundary.
		visited := make([]bool, len(slices))
		prev := -1
		for i, s := range slices {
			for _, year := range []int{s.TFrom, s.Year, s.TTo} {
				got := coveringSlice(year)
				if got != i {
					t.Fatalf("%s: year %d is inside slice %d's window %d..%d but resolves to %v",
						dir, year, s.Year, s.TFrom, s.TTo, sliceYear(slices, got))
				}
				if prev >= 0 && got != prev && got != prev+1 {
					t.Fatalf("%s: scrubbing reached slice %d straight from %d, skipping %d slice(s)",
						dir, got, prev, got-prev-1)
				}
				visited[got] = true
				prev = got
			}
		}
		for i, ok := range visited {
			if !ok {
				t.Errorf("%s: slice %d is never reachable by scrubbing", dir, slices[i].Year)
			}
		}
	}
}

func sliceYear(slices []model.BorderLayer, i int) any {
	if i < 0 {
		return "no coverage"
	}
	return slices[i].Year
}

// TestLayersHandOverAtTheBoundary pins the one moment where the map changes
// kind: the year the paleo layer stops and the political layer starts. A gap
// there would blank the map; an overlap would make the choice arbitrary.
func TestLayersHandOverAtTheBoundary(t *testing.T) {
	borders := loadLayerOrSkip(t, "../../data/geo/borders")
	paleo := loadLayerOrSkip(t, "../../data/geo/paleo")
	last := paleo[len(paleo)-1]
	if last.TTo+1 != borders[0].TFrom {
		t.Errorf("deep time ends at %d and recorded history starts at %d; they must meet exactly",
			last.TTo, borders[0].TFrom)
	}
}
