package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"golang.org/x/sync/errgroup"

	"wk/internal/model"
)

// Continuous political borders (SRC-1). The upstream is
// aourednik/historical-basemaps: world polity polygons in year slices from
// 123000 BC to 2010, pinned to one commit so a re-fetch is reproducible.
//
// The slices are derived, not committed: `make fetch-geo` writes them into
// data/geo/borders and CI caches them on the fingerprint of the pins below.
//
// LICENSING: the upstream repo is GPL-3.0 and the files written here are a
// modified version of it, so they stay GPL-3.0 and carry their own notice in
// data/geo/borders/NOTICE.md. See it before moving or relicensing them.
const (
	bordersRepo   = "aourednik/historical-basemaps"
	BordersCommit = "62d8f1a03a71f2d3ff17f2d166f7553f256bce68"
	// BordersSource is the attribution string baked into every slice and shown
	// by the client. It names the work, the pin and the licence.
	BordersSource = "aourednik/historical-basemaps @ 62d8f1a (GPL-3.0), simplified"

	// The final slice is 2010 and nothing has redrawn a border since that this
	// dataset would show, so it speaks for the present too. Fixed rather than
	// "now" to keep a re-fetch byte-identical.
	bordersLastTTo = 2035

	// Per-slice byte budget. Slices over it are re-simplified at a coarser
	// tolerance until they fit. Set so that every slice but one keeps the
	// finest tolerance - at 2 km the whole set is 16 MB against 72 MB of
	// upstream - and exists for upstream outliers (1492 is 4 MB of tiny
	// American polities) rather than to shave the ordinary slices.
	borderSliceBudget = 600 << 10
)

// borderTolerances are the Douglas-Peucker tolerances tried in order, in
// degrees. 0.02 deg is ~2 km, well under a pixel at the zoom this layer is
// read at; the coarser steps exist for the few oversized slices.
var borderTolerances = []float64{0.02, 0.035, 0.06, 0.1, 0.16, 0.25}

// worldSliceName matches geojson/world_<year>.geojson and geojson/world_bc<year>.geojson.
// places.geojson and anything else in the directory is not a year slice.
var worldSliceName = regexp.MustCompile(`^geojson/world_(bc)?(\d+)\.geojson$`)

// upstreamProps are the fields historical-basemaps puts on every polity.
type upstreamProps struct {
	Name     string `json:"NAME"`
	Subjecto string `json:"SUBJECTO"`
}

type upstreamFeature struct {
	Properties upstreamProps   `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type upstreamCollection struct {
	Features []upstreamFeature `json:"features"`
}

// BorderSlice is one fetched, simplified year slice ready to be written to
// data/geo/borders/<year>.geojson.
type BorderSlice struct {
	Year int
	Path string // upstream path, for the report
	Body []byte
	Tol  float64
}

// FetchBorders downloads every world slice at the pinned commit, simplifies it
// and returns the slices in ascending year order with their coverage windows
// already tiled: each slice speaks until the year before the next one.
func FetchBorders(ctx context.Context, cli *http.Client, log func(string)) ([]BorderSlice, error) {
	paths, err := listWorldSlices(ctx, cli)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no world_*.geojson slices at %s: upstream layout changed", BordersCommit[:7])
	}
	years := make([]int, 0, len(paths))
	for y := range paths {
		years = append(years, y)
	}
	sort.Ints(years)
	log(fmt.Sprintf("%d world slices, %d..%d", len(years), years[0], years[len(years)-1]))

	slices := make([]BorderSlice, len(years))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6) // polite to raw.githubusercontent, and plenty
	for i, year := range years {
		i, year := i, year
		g.Go(func() error {
			tFrom := year
			tTo := bordersLastTTo
			if i+1 < len(years) {
				tTo = years[i+1] - 1
			}
			s, err := fetchBorderSlice(gctx, cli, year, paths[year], tFrom, tTo)
			if err != nil {
				return err
			}
			slices[i] = s
			log(fmt.Sprintf("%8d  %6.1f KB  tol %.3f  (covers %d..%d)",
				year, float64(len(s.Body))/1024, s.Tol, tFrom, tTo))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return slices, nil
}

// listWorldSlices reads the repository tree at the pinned commit and returns
// the year slices by year. Discovering rather than hardcoding keeps the list
// honest, and pinning the commit keeps it reproducible.
func listWorldSlices(ctx context.Context, cli *http.Client) (map[int]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", bordersRepo, BordersCommit)
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := getJSON(ctx, cli, url, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated {
		return nil, fmt.Errorf("github truncated the tree listing for %s", BordersCommit[:7])
	}
	out := map[int]string{}
	for _, e := range tree.Tree {
		m := worldSliceName.FindStringSubmatch(e.Path)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, fmt.Errorf("slice %s: %w", e.Path, err)
		}
		// BC years are negative; the dataset has no year 0, so BC n maps to -n
		// rather than to astronomical numbering. The one-year offset that
		// leaves is far inside the uncertainty of every slice here.
		if m[1] == "bc" {
			n = -n
		}
		if prev, dup := out[n]; dup {
			return nil, fmt.Errorf("year %d claimed by both %s and %s", n, prev, e.Path)
		}
		out[n] = e.Path
	}
	return out, nil
}

func fetchBorderSlice(ctx context.Context, cli *http.Client, year int, path string, tFrom, tTo int) (BorderSlice, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", bordersRepo, BordersCommit, path)
	var fc upstreamCollection
	if err := getJSON(ctx, cli, url, &fc); err != nil {
		return BorderSlice{}, err
	}
	if len(fc.Features) == 0 {
		return BorderSlice{}, fmt.Errorf("%s: no features", path)
	}

	label := fmt.Sprintf("world borders · %s · historical-basemaps", FormatYear(year))
	for _, tol := range borderTolerances {
		feats := make([]curatedFeature, 0, len(fc.Features))
		for _, f := range fc.Features {
			name := f.Properties.Name
			if name == "" {
				name = f.Properties.Subjecto
			}
			// An unnamed polygon is a region the source could not attribute;
			// drawing it as an anonymous blob would claim more than it knows.
			if name == "" || len(f.Geometry) == 0 {
				continue
			}
			g, err := simplifyPolygons(f.Geometry, tol)
			if err != nil {
				return BorderSlice{}, fmt.Errorf("%s (%s): %w", path, name, err)
			}
			if g == nil {
				continue // simplified away entirely: too small to draw
			}
			feats = append(feats, curatedFeature{
				Type:       "Feature",
				Properties: curatedFeatureProps{Name: name, Representation: "estimated"},
				Geometry:   g,
			})
		}
		if len(feats) == 0 {
			return BorderSlice{}, fmt.Errorf("%s: every feature simplified away at tolerance %v", path, tol)
		}
		body, err := encodeCuratedSlice(year, tFrom, tTo, label, BordersSource, feats)
		if err != nil {
			return BorderSlice{}, err
		}
		if len(body) <= borderSliceBudget || tol == borderTolerances[len(borderTolerances)-1] {
			return BorderSlice{Year: year, Path: path, Body: body, Tol: tol}, nil
		}
	}
	panic("unreachable: the tolerance loop always returns on its last step")
}

// AreaCoverage summarises one fetched layer directory. Loading it is the
// check: loadAreaSlices rejects a layer whose windows do not tile, which is
// how a partial or stale fetch is caught before a bake trusts it.
type AreaCoverage struct {
	Slices int
	TFrom  int
	TTo    int
}

// VerifyAreaLayer loads a layer directory and reports its coverage.
func VerifyAreaLayer(dir string) (AreaCoverage, error) {
	slices, err := loadAreaSlices(dir, map[string]*model.Entity{})
	if err != nil {
		return AreaCoverage{}, err
	}
	return AreaCoverage{
		Slices: len(slices),
		TFrom:  slices[0].TFrom,
		TTo:    slices[len(slices)-1].TTo,
	}, nil
}

// EarliestBorderYear is the first year the political layer speaks for: the
// minimum t_from across the written slices. The paleo layer stops one year
// short of it, so the two tile with neither a gap nor an overlap.
func EarliestBorderYear(dir string) (int, error) {
	paths, err := geojsonPaths(dir)
	if err != nil {
		return 0, err
	}
	earliest := 0
	found := false
	for _, path := range paths {
		var f borderFile
		if err := readJSON(path, &f); err != nil {
			return 0, err
		}
		if f.Properties.TFrom == nil {
			return 0, fmt.Errorf("%s: properties.t_from is missing", path)
		}
		if !found || *f.Properties.TFrom < earliest {
			earliest, found = *f.Properties.TFrom, true
		}
	}
	if !found {
		return 0, fmt.Errorf("no border slices in %s", dir)
	}
	return earliest, nil
}
