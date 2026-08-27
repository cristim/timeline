package ingest

import (
	"encoding/json"
	"fmt"
	"math"
)

// Geometry reduction for the fetched border and paleo-coastline slices. The
// upstream files are ~1-4 MB each and there are ~90 of them; the repo commits
// the simplified copies (the Pages bake has no network), so the whole point is
// to lose vertices without losing the shape a reader would recognise.
//
// Douglas-Peucker in raw degrees, no projection: the tolerance is an angular
// distance and the client renders lon/lat anyway. It is deliberately gentler
// near the poles, where a degree of longitude is short and the planar metric
// therefore over-states a deviation, so polar coastlines keep more detail than
// they strictly need. That is the harmless direction to be wrong in.

// coordDecimals caps stored precision at ~11 m. The upstream files carry ~15
// decimals, which is most of their bulk and none of their information.
const coordDecimals = 4

// simplifyPolygons rewrites a Polygon or MultiPolygon with every ring
// Douglas-Peucker'd at tolDeg, rings that collapse dropped, winding forced to
// RFC 7946, and each ring re-closed. It returns nil when nothing survives:
// callers drop the feature rather than emit an empty geometry.
//
// Simplification can introduce a self-intersection that the source ring did
// not have, and internal/ingest/geo.go rejects those outright. Rather than
// weaken that check, each ring falls back to progressively finer tolerances
// and finally to its unsimplified self, so a bowtie costs vertices, not a bake.
func simplifyPolygons(raw json.RawMessage, tolDeg float64) (json.RawMessage, error) {
	var g geometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("geometry: %w", err)
	}
	var polys [][][][]float64
	switch g.Type {
	case "Polygon":
		var p [][][]float64
		if err := json.Unmarshal(g.Coordinates, &p); err != nil {
			return nil, fmt.Errorf("polygon coordinates: %w", err)
		}
		polys = [][][][]float64{p}
	case "MultiPolygon":
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return nil, fmt.Errorf("multipolygon coordinates: %w", err)
		}
	default:
		return nil, fmt.Errorf("geometry type %q, want Polygon or MultiPolygon", g.Type)
	}

	out := make([][][][]float64, 0, len(polys))
	for _, poly := range polys {
		if len(poly) == 0 {
			continue
		}
		// A polygon with no exterior left has nothing for its holes to punch
		// through, so the exterior decides the whole polygon's fate.
		exterior := reduceRing(poly[0], tolDeg, true)
		if exterior == nil {
			continue
		}
		rings := [][][]float64{exterior}
		for _, hole := range poly[1:] {
			if r := reduceRing(hole, tolDeg, false); r != nil {
				rings = append(rings, r)
			}
		}
		out = append(out, rings)
	}
	if len(out) == 0 {
		return nil, nil
	}

	var body any = out
	kind := "MultiPolygon"
	if len(out) == 1 {
		body, kind = out[0], "Polygon"
	}
	coords, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(geometry{Type: kind, Coordinates: coords})
}

// reduceRing normalizes, simplifies and re-closes one ring, or returns nil if
// it is not worth keeping. `ccw` asks for the RFC 7946 winding of its role.
func reduceRing(ring [][]float64, tolDeg float64, ccw bool) [][]float64 {
	pts := normalizeRing(ring)
	if pts == nil {
		return nil
	}
	// A shape smaller than the tolerance square cannot survive simplification
	// as anything but a sliver, and the upstream files carry thousands of such
	// specks. Dropping them here is most of the size win.
	if ringArea(pts) < tolDeg*tolDeg {
		return nil
	}
	for _, tol := range []float64{tolDeg, tolDeg / 4, tolDeg / 16, 0} {
		out := simplifyClosed(pts, tol)
		if out == nil {
			continue
		}
		// Orient BEFORE testing. selfIntersection's crossing test is strict
		// (d > 0), so a ring that merely touches itself registers as crossing
		// in one winding and not the other. Testing the ring geo.go will
		// actually see is the only way the two checks agree; testing the other
		// one lets a pinched ring through to fail the bake instead.
		out = unloop(orientRing(out, ccw))
		if out == nil {
			continue // finer tolerance, or eventually the original
		}
		return out
	}
	return nil
}

// unloop excises self-intersection loops until the ring is clean, or returns
// nil if it cannot be. Where segment i crosses segment j, the vertices between
// them fold back over the rest of the ring; cutting at the crossing point
// splits the ring into that loop and the remainder, and the larger of the two
// is the shape the source meant.
//
// Dropping the whole ring instead is not an option at this scale. The upstream
// Carolingian Empire outline crosses itself once in 271 vertices, and refusing
// it erased the empire's entire main body - Aachen, Paris and Milan with it.
func unloop(ring [][]float64) [][]float64 {
	// One excision per crossing; the bound stops a pathological ring from
	// spinning here, and such a ring is better dropped than nursed.
	for pass := 0; pass < 32; pass++ {
		i, j := selfIntersection(ring)
		if i < 0 {
			return ring
		}
		x, ok := intersection(ring[i], ring[i+1], ring[j], ring[j+1])
		if !ok {
			return nil
		}
		// loop: the span between the crossing segments, closed through x.
		loop := append(append([][]float64{x}, ring[i+1:j+1]...), x)
		rest := append(append(append([][]float64{}, ring[:i+1]...), x), ring[j+1:]...)
		switch {
		case len(rest) >= 4 && ringArea(rest) >= ringArea(loop):
			ring = rest
		case len(loop) >= 4:
			ring = loop
		default:
			return nil
		}
	}
	return nil
}

// intersection returns the crossing point of segments ab and cd. The caller
// has already established that they properly cross, so the denominator is
// non-zero for anything but a floating-point degeneracy.
func intersection(a, b, c, d []float64) ([]float64, bool) {
	r0, r1 := b[0]-a[0], b[1]-a[1]
	s0, s1 := d[0]-c[0], d[1]-c[1]
	den := r0*s1 - r1*s0
	if den == 0 {
		return nil, false
	}
	t := ((c[0]-a[0])*s1 - (c[1]-a[1])*s0) / den
	scale := math.Pow(10, coordDecimals)
	return []float64{
		math.Round((a[0]+t*r0)*scale) / scale,
		math.Round((a[1]+t*r1)*scale) / scale,
	}, true
}

// normalizeRing drops the closing repeat, truncates positions to [lon,lat],
// clamps the slop that puts an upstream vertex a hair outside the valid range,
// quantizes to coordDecimals, and removes consecutive duplicates. It returns
// nil for a ring with fewer than three distinct vertices.
//
// Quantizing here rather than at encode time is load-bearing: moving a vertex
// by half a unit in the last place can turn a ring that grazes itself into one
// that crosses, and geo.go rejects the bake for it. Rounding first means every
// later check - self-intersection, winding, closure - runs on the bytes that
// actually get written.
func normalizeRing(ring [][]float64) [][]float64 {
	scale := math.Pow(10, coordDecimals)
	out := make([][]float64, 0, len(ring))
	for _, c := range ring {
		if len(c) < 2 || math.IsNaN(c[0]) || math.IsNaN(c[1]) {
			continue
		}
		lon := math.Round(math.Max(-180, math.Min(180, c[0]))*scale) / scale
		lat := math.Round(math.Max(-90, math.Min(90, c[1]))*scale) / scale
		if n := len(out); n > 0 && out[n-1][0] == lon && out[n-1][1] == lat {
			continue
		}
		out = append(out, []float64{lon, lat})
	}
	// The source ring's closing vertex is now a trailing duplicate of the
	// first only if it was exact; drop it either way.
	for len(out) > 1 && out[0][0] == out[len(out)-1][0] && out[0][1] == out[len(out)-1][1] {
		out = out[:len(out)-1]
	}
	out = unpinch(out)
	if len(out) < 3 {
		return nil
	}
	return out
}

// unpinch removes repeated vertices, which a ring is not allowed to have: a
// ring that returns to a point it already visited is two loops joined at a
// pinch, and the crossing test in geo.go reports it as a self-intersection.
// Splitting there and keeping the larger loop is the repair that costs the
// least real territory - Switzerland's outline is pinched at Bern in several
// slices, and dropping the whole ring would erase the country from the map.
//
// Not every pinch is an upstream defect: quantizing to coordDecimals merges
// two vertices that differed in the fifth decimal, which manufactures one.
// Repairing after the rounding is cheaper than keeping precision nobody reads.
func unpinch(pts [][]float64) [][]float64 {
	for len(pts) >= 3 {
		i, j := firstRepeat(pts)
		if i < 0 {
			return pts
		}
		// pts[i] == pts[j], so pts[i:j] is a closed loop on its own and the
		// remainder is another, the two sharing that single vertex.
		inner := pts[i:j]
		outer := append(append([][]float64{}, pts[:i]...), pts[j:]...)
		switch {
		case len(outer) < 3 || (len(inner) >= 3 && ringArea(inner) > ringArea(outer)):
			pts = inner
		default:
			pts = outer
		}
	}
	return nil
}

// firstRepeat returns the indexes of the first vertex that occurs twice, or
// (-1, -1).
func firstRepeat(pts [][]float64) (int, int) {
	seen := make(map[[2]float64]int, len(pts))
	for k, p := range pts {
		key := [2]float64{p[0], p[1]}
		if prev, ok := seen[key]; ok {
			return prev, k
		}
		seen[key] = k
	}
	return -1, -1
}

// simplifyClosed runs Douglas-Peucker over a closed ring given as its distinct
// vertices (no closing repeat) and returns a closed ring, or nil if fewer than
// three vertices survive. A ring has no natural endpoints, so it is cut at the
// vertex farthest from the first one and simplified as two chains: running DP
// straight from ring[0] to ring[0] would measure every vertex against a
// zero-length segment.
func simplifyClosed(pts [][]float64, tol float64) [][]float64 {
	if tol <= 0 {
		return closeRing(pts)
	}
	far, best := 0, -1.0
	for i, p := range pts[1:] {
		if d := sqDist(pts[0], p); d > best {
			far, best = i+1, d
		}
	}
	// The two chains share their endpoints, so marking keeps on one mask over
	// the original indexes joins them without any splicing.
	keep := make([]bool, len(pts))
	keep[0], keep[far] = true, true
	markKeeps(pts, 0, far, tol, keep)
	markWrapped(pts, far, tol, keep)

	out := make([][]float64, 0, len(pts)+1)
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	if len(out) < 3 {
		return nil
	}
	return append(out, []float64{out[0][0], out[0][1]})
}

func closeRing(pts [][]float64) [][]float64 {
	out := make([][]float64, len(pts), len(pts)+1)
	copy(out, pts)
	return append(out, []float64{pts[0][0], pts[0][1]})
}

// markKeeps is Douglas-Peucker over pts[first..last]: keep every vertex
// farther than tol from the chord through the endpoints, then recurse on each
// side of the farthest one. Endpoints are the caller's to mark.
func markKeeps(pts [][]float64, first, last int, tol float64, keep []bool) {
	if last-first < 2 {
		return
	}
	idx, best := 0, 0.0
	for i := first + 1; i < last; i++ {
		if d := perpDistance(pts[i], pts[first], pts[last]); d > best {
			idx, best = i, d
		}
	}
	if best <= tol {
		return
	}
	keep[idx] = true
	markKeeps(pts, first, idx, tol, keep)
	markKeeps(pts, idx, last, tol, keep)
}

// markWrapped is markKeeps over the chain from pts[far] round the end of the
// slice back to pts[0], addressing it modulo len(pts) so the ring's two chains
// share one mask.
func markWrapped(pts [][]float64, far int, tol float64, keep []bool) {
	n := len(pts)
	chain := make([][]float64, 0, n-far+1)
	idx := make([]int, 0, n-far+1)
	for i := far; i < n; i++ {
		chain, idx = append(chain, pts[i]), append(idx, i)
	}
	chain, idx = append(chain, pts[0]), append(idx, 0)

	sub := make([]bool, len(chain))
	markKeeps(chain, 0, len(chain)-1, tol, sub)
	for i, k := range sub {
		if k {
			keep[idx[i]] = true
		}
	}
}

// perpDistance is the distance from p to segment ab, in degrees.
func perpDistance(p, a, b []float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

func sqDist(a, b []float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	return dx*dx + dy*dy
}

// ringArea is the unsigned shoelace area of a ring in square degrees.
func ringArea(pts [][]float64) float64 {
	sum := 0.0
	for i := range pts {
		j := (i + 1) % len(pts)
		sum += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}
	return math.Abs(sum) / 2
}

// orientRing forces RFC 7946 winding: exteriors counterclockwise, holes
// clockwise. Upstream files are inconsistent about this and geo.go treats a
// backwards exterior as a hole.
func orientRing(ring [][]float64, ccw bool) [][]float64 {
	if ringIsCCW(ring) == ccw {
		return ring
	}
	for i, j := 0, len(ring)-1; i < j; i, j = i+1, j-1 {
		ring[i], ring[j] = ring[j], ring[i]
	}
	return ring
}
