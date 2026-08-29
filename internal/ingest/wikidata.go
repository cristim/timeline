package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wk/internal/model"
)

// Wikidata bulk import (SRC-1 tier 1, M5). The first bounded slice uses the
// SPARQL endpoint with year-sliced queries instead of the full dump: the same
// normalize/validate/rank path applies either way, and the dump-based streamer
// can replace the fetch layer without touching anything downstream.

const (
	sparqlEndpoint = "https://query.wikidata.org/sparql"
	// Etiquette per Wikimedia UA policy; also used for rate pacing.
	userAgent = "EverythingTimeline-dev/0.1 (https://github.com/cristim; dev build)"
)

// eventClass is one Wikidata class we import as war/event entities.
type eventClass struct {
	QID      string
	Category string
	Name     string
}

var EventClasses = []eventClass{
	{QID: "Q178561", Category: "war", Name: "battle"},
	{QID: "Q198", Category: "war", Name: "war"},
	{QID: "Q188055", Category: "war", Name: "siege"},
}

// yearSlices keep each SPARQL result set small enough to avoid endpoint
// timeouts; boundaries follow event density, not equal spans.
var yearSlices = [][2]int{
	{-4000, 1000}, {1000, 1500}, {1500, 1700}, {1700, 1800},
	{1800, 1860}, {1860, 1900}, {1900, 1920}, {1920, 1945},
	{1945, 1980}, {1980, 2030},
}

type sparqlResponse struct {
	Results struct {
		Bindings []map[string]struct {
			Value string `json:"value"`
		} `json:"bindings"`
	} `json:"results"`
}

// WikidataRecord is the normalized form of one imported entity, kept alongside
// the SeedEntity fields so it flows through the standard Validate path.
type WikidataRecord struct {
	Seed      model.SeedEntity
	PartOfQID string // resolved to a rel only if the target was also imported
}

type FetchStats struct {
	Pages    int
	Rows     int
	Distinct int
}

// FetchWikidata runs the sliced queries and returns normalized records plus
// the raw response pages (stored by the caller for provenance, SRC-2).
func FetchWikidata(ctx context.Context, client *http.Client, progress func(string)) ([]WikidataRecord, [][]byte, *FetchStats, error) {
	stats := &FetchStats{}
	var raw [][]byte
	byQID := map[string]*WikidataRecord{}
	order := []string{} // deterministic output order = first-seen order

	for _, class := range EventClasses {
		for _, slice := range yearSlices {
			q := buildQuery(class.QID, slice[0], slice[1])
			body, err := runQuery(ctx, client, q)
			if err != nil {
				return nil, nil, stats, fmt.Errorf("query %s %d..%d: %w", class.Name, slice[0], slice[1], err)
			}
			raw = append(raw, body)
			stats.Pages++

			var resp sparqlResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, nil, stats, fmt.Errorf("parse %s %d..%d: %w", class.Name, slice[0], slice[1], err)
			}
			added := 0
			for _, b := range resp.Results.Bindings {
				stats.Rows++
				rec, qid, ok := normalizeBinding(b, class)
				if !ok {
					continue
				}
				if _, seen := byQID[qid]; seen {
					continue // multiple coords/dates produce duplicate rows
				}
				byQID[qid] = rec
				order = append(order, qid)
				added++
			}
			progress(fmt.Sprintf("%s %d..%d: %d rows, %d new (total %d)",
				class.Name, slice[0], slice[1], len(resp.Results.Bindings), added, len(order)))
			// Pace requests: WDQS asks heavy clients to stay well under limits.
			time.Sleep(500 * time.Millisecond)
		}
	}

	out := make([]WikidataRecord, 0, len(order))
	for _, qid := range order {
		out = append(out, *byQID[qid])
	}
	stats.Distinct = len(out)
	return out, raw, stats, nil
}

func buildQuery(classQID string, y0, y1 int) string {
	return fmt.Sprintf(`SELECT ?item ?itemLabel ?time ?end ?coord ?sitelinks ?article ?partOf WHERE {
  ?item wdt:P31 wd:%s ; wdt:P625 ?coord ; wikibase:sitelinks ?sitelinks .
  OPTIONAL { ?item wdt:P580 ?start . }
  OPTIONAL { ?item wdt:P585 ?pit . }
  BIND(COALESCE(?start, ?pit) AS ?time)
  FILTER(BOUND(?time))
  FILTER(YEAR(?time) >= %d && YEAR(?time) < %d)
  OPTIONAL { ?item wdt:P582 ?end . }
  OPTIONAL { ?item wdt:P361 ?partOf . }
  OPTIONAL { ?article schema:about ?item ; schema:isPartOf <https://en.wikipedia.org/> . }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "en" . }
}`, classQID, y0, y1)
}

func runQuery(ctx context.Context, client *http.Client, query string) ([]byte, error) {
	form := url.Values{"query": {query}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sparqlEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/sparql-results+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WDQS status %d: %.200s", resp.StatusCode, body)
	}
	return body, nil
}

var qidRe = regexp.MustCompile(`Q\d+$`)

func normalizeBinding(b map[string]struct {
	Value string `json:"value"`
}, class eventClass) (*WikidataRecord, string, bool) {
	qid := qidRe.FindString(b["item"].Value)
	label := b["itemLabel"].Value
	if qid == "" || label == "" || label == qid {
		return nil, "", false // unlabeled items are not worth rendering
	}
	t0, precision, ok := parseWikidataTime(b["time"].Value)
	if !ok {
		return nil, "", false
	}
	seed := model.SeedEntity{
		ID:         "wd-" + strings.ToLower(qid),
		Type:       "event",
		Name:       label,
		Precision:  precision,
		Status:     "documented",
		Categories: []string{class.Category},
		Wikidata:   qid,
		Wikipedia:  b["article"].Value,
	}
	seed.T0 = jsonTime(t0)
	if end, endPrec, ok := parseWikidataTime(b["end"].Value); ok && end >= t0 {
		seed.T1 = jsonTime(end)
		if endPrec == "year" {
			seed.Precision = "year"
		}
	}
	if lon, lat, ok := parsePointWKT(b["coord"].Value); ok {
		seed.Point = []float64{lon, lat}
	}
	sitelinks, _ := strconv.Atoi(b["sitelinks"].Value)
	seed.Importance = importanceFromSitelinks(sitelinks)
	return &WikidataRecord{
		Seed:      seed,
		PartOfQID: qidRe.FindString(b["partOf"].Value),
	}, qid, true
}

// importanceFromSitelinks maps Wikipedia language coverage to the ZOOM-2
// importance prior for imported entities. Capped at 0.90 so curated seed
// values keep outranking bulk imports at the coarsest buckets.
func importanceFromSitelinks(n int) float64 {
	imp := 0.22 + 0.09*math.Log1p(float64(n))
	return math.Min(0.90, math.Max(0.22, math.Round(imp*100)/100))
}

// parseWikidataTime handles WDQS xsd:dateTime values including negative years
// ("-0052-03-15T00:00:00Z"), which time.Parse rejects.
func parseWikidataTime(v string) (float64, string, bool) {
	if v == "" {
		return 0, "", false
	}
	neg := strings.HasPrefix(v, "-")
	s := strings.TrimPrefix(v, "-")
	parts := strings.SplitN(strings.TrimSuffix(s, "Z"), "T", 2)
	date := strings.Split(parts[0], "-")
	if len(date) != 3 {
		return 0, "", false
	}
	year, err1 := strconv.Atoi(date[0])
	month, err2 := strconv.Atoi(date[1])
	day, err3 := strconv.Atoi(date[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, "", false
	}
	if neg {
		year = -year
	}
	// Recent in-range dates keep exact calendar seconds; older ones use the
	// average-year approximation, far below their stated precision.
	if year >= 1 && year <= 9999 {
		t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		precision := "day"
		if month == 1 && day == 1 {
			precision = "year" // truthy WDQS output loses precision; Jan 1 is
		} //                       overwhelmingly a year-precision statement
		return float64(t.Unix()), precision, true
	}
	frac := (float64(month-1) + float64(day-1)/30.0) / 12.0
	return model.YearToSeconds(float64(year) + frac), "year", true
}

var pointRe = regexp.MustCompile(`Point\(([-0-9.eE+]+) ([-0-9.eE+]+)\)`)

func parsePointWKT(v string) (lon, lat float64, ok bool) {
	m := pointRe.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, false
	}
	lon, err1 := strconv.ParseFloat(m[1], 64)
	lat, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return 0, 0, false
	}
	return lon, lat, true
}

func jsonTime(t float64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"y": %.9f}`, model.SecondsToYear(t)))
}
