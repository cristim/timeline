package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"golang.org/x/sync/errgroup"
)

// Deep-time paleogeography: present-day coastlines reconstructed onto their
// past plate positions by the GPlates Web Service. Derived, not committed:
// `make fetch-geo` writes these into data/geo/paleo and CI caches them.
//
// LICENSING: the reconstruction model's data is CC-BY 4.0 and wants the paper
// cited, which PaleoSource and data/geo/paleo/NOTICE.md do.
const (
	paleoEndpoint = "https://gws.gplates.org/reconstruct/coastlines/"
	// MERDITH2021 reaches 1000 Ma. The service default (ZAHIROVIC2022) stops
	// at 410 Ma and cannot answer for the Cambrian at all.
	PaleoModel  = "MERDITH2021"
	PaleoSource = "GPlates Web Service · Merdith et al. 2021 (CC-BY 4.0), simplified"

	// Coastlines at this scale are read on a globe, so the byte budget is
	// tighter than the political layer's; the shapes are continents.
	paleoSliceBudget = 220 << 10
)

var paleoTolerances = []float64{0.08, 0.14, 0.22, 0.35, 0.55, 0.9}

// PaleoSlices are the reconstruction times in Ma, densest toward the present
// where plate motion resolves into recognisable geography. 540 Ma is the base
// of the Cambrian; the youngest is 1 Ma rather than 0 so the layer's coverage
// ends before the political layer's begins (see PaleoWindows).
var PaleoSlices = []int{
	540, 500, 470, 440, 420, 400, 380, 360, 340, 320,
	300, 280, 260, 250, 240, 220, 200, 180, 160, 140,
	120, 100, 90, 80, 70, 65, 60, 50, 40, 30,
	20, 15, 10, 5, 2, 1,
}

// PaleoYear is the signed year a reconstruction time in Ma occupies on the
// timeline. The 1970 epoch offset is 8 orders of magnitude below the slice
// spacing, so it is dropped: 250 Ma is year -250000000 exactly, which keeps
// the artifact keys readable and the arithmetic exact in float64.
func PaleoYear(ma int) int { return -ma * 1_000_000 }

// PaleoSlice is one fetched, simplified reconstruction ready to be written to
// data/geo/paleo/<year>.geojson.
type PaleoSlice struct {
	Ma   int
	Year int
	Body []byte
	Tol  float64
}

// gwsCollection is what /reconstruct/coastlines/ returns: bare Polygon
// features with no properties at all.
type gwsCollection struct {
	Features []struct {
		Geometry json.RawMessage `json:"geometry"`
	} `json:"features"`
}

// FetchPaleo reconstructs every slice in PaleoSlices and returns them in
// ascending year (descending Ma) order with coverage windows already tiled up
// to politicalStart, the first year the political layer speaks for.
func FetchPaleo(ctx context.Context, cli *http.Client, politicalStart int, log func(string)) ([]PaleoSlice, error) {
	mas := append([]int(nil), PaleoSlices...)
	sort.Sort(sort.Reverse(sort.IntSlice(mas))) // oldest first = ascending year
	if PaleoYear(mas[len(mas)-1]) >= politicalStart {
		return nil, fmt.Errorf("youngest paleo slice %d Ma (year %d) is not older than the political layer's start (%d)",
			mas[len(mas)-1], PaleoYear(mas[len(mas)-1]), politicalStart)
	}

	out := make([]PaleoSlice, len(mas))
	g, gctx := errgroup.WithContext(ctx)
	// A shared academic service with no published quota and 5-12s responses:
	// three at a time is enough to finish in minutes without leaning on it.
	g.SetLimit(3)
	for i, ma := range mas {
		i, ma := i, ma
		g.Go(func() error {
			tFrom := PaleoYear(ma)
			tTo := politicalStart - 1
			if i+1 < len(mas) {
				tTo = PaleoYear(mas[i+1]) - 1
			}
			s, err := fetchPaleoSlice(gctx, cli, ma, tFrom, tTo)
			if err != nil {
				return err
			}
			out[i] = s
			log(fmt.Sprintf("%5d Ma  %6.1f KB  tol %.3f  (covers %d..%d)",
				ma, float64(len(s.Body))/1024, s.Tol, tFrom, tTo))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func fetchPaleoSlice(ctx context.Context, cli *http.Client, ma, tFrom, tTo int) (PaleoSlice, error) {
	// wrap=true splits polygons at the antimeridian. Without it a ring that
	// straddles the seam runs the long way round and paints a band across the
	// whole map.
	url := fmt.Sprintf("%s?time=%d&model=%s&wrap=true", paleoEndpoint, ma, PaleoModel)
	var fc gwsCollection
	if err := getJSON(ctx, cli, url, &fc); err != nil {
		return PaleoSlice{}, err
	}
	if len(fc.Features) == 0 {
		return PaleoSlice{}, fmt.Errorf("%d Ma: no coastline features", ma)
	}

	year := PaleoYear(ma)
	label := fmt.Sprintf("≈ %d Ma · GPlates/%s", ma, PaleoModel)
	for _, tol := range paleoTolerances {
		feats := make([]curatedFeature, 0, len(fc.Features))
		for _, f := range fc.Features {
			if len(f.Geometry) == 0 {
				continue
			}
			g, err := simplifyPolygons(f.Geometry, tol)
			if err != nil {
				return PaleoSlice{}, fmt.Errorf("%d Ma: %w", ma, err)
			}
			if g == nil {
				continue
			}
			// The service returns unnamed landmasses; naming them "land" is
			// the honest label, and the client draws them as one mass anyway.
			feats = append(feats, curatedFeature{
				Type:       "Feature",
				Properties: curatedFeatureProps{Name: "land", Representation: "estimated"},
				Geometry:   g,
			})
		}
		if len(feats) == 0 {
			return PaleoSlice{}, fmt.Errorf("%d Ma: every landmass simplified away at tolerance %v", ma, tol)
		}
		body, err := encodeCuratedSlice(year, tFrom, tTo, label, PaleoSource, feats)
		if err != nil {
			return PaleoSlice{}, err
		}
		if len(body) <= paleoSliceBudget || tol == paleoTolerances[len(paleoTolerances)-1] {
			return PaleoSlice{Ma: ma, Year: year, Body: body, Tol: tol}, nil
		}
	}
	panic("unreachable: the tolerance loop always returns on its last step")
}
