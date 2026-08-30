package ingest

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
)

// The ROAD-2 census: "process a Wikidata dump and report, per century and per
// type: how many battles, wars, political events, disasters, scientific events,
// people, species, products exist; how many have dates + coordinates +
// Wikipedia sitelinks; distribution of time precision."
//
// Per type answers it in the project's vocabulary; per class answers it in
// Wikidata's, which is the form the question is actually asked in (a battle and
// a war are both `event` to us). Both are reported per time slice and in total.
const (
	wikidataDumpCoverageReportSchemaVersion = 2
	wikidataDumpCoverageBasis               = "wikidata-items-after-statement-validation-and-type-classification"

	// maxReportedUnclassifiedClasses bounds the tail of unmatched classes the
	// report carries. It is the list the curated table grows from, so it is
	// ranked by count, and the distinct total is reported alongside it so the
	// truncation is never mistaken for the whole picture.
	maxReportedUnclassifiedClasses = 100
)

type WikidataDumpCoverageRatios struct {
	Date             float64 `json:"date"`
	Coordinates      float64 `json:"coordinates"`
	EnglishWikipedia float64 `json:"english_wikipedia"`
	AnySitelink      float64 `json:"any_sitelink"`
	All              float64 `json:"all"`
}

type WikidataDumpCoverageStats struct {
	Count               int                        `json:"count"`
	HasEnglishLabel     int                        `json:"has_english_label"`
	HasDate             int                        `json:"has_date"`
	HasCoordinates      int                        `json:"has_coordinates"`
	HasEnglishWikipedia int                        `json:"has_english_wikipedia"`
	HasAnySitelink      int                        `json:"has_any_sitelink"`
	HasAll              int                        `json:"has_all"`
	TotalSitelinks      int                        `json:"total_sitelinks"`
	Ratios              WikidataDumpCoverageRatios `json:"ratios"`
}

type WikidataDumpTypeRow struct {
	Type  string                    `json:"type"`
	Stats WikidataDumpCoverageStats `json:"stats"`
}

// WikidataDumpClassRow reports one curated Wikidata class, which is the level
// ROAD-2 asks its question at.
type WikidataDumpClassRow struct {
	ClassQID   string                    `json:"class_qid"`
	ClassLabel string                    `json:"class_label"`
	Type       string                    `json:"type"`
	Stats      WikidataDumpCoverageStats `json:"stats"`
}

// WikidataDumpBucketRow is one time slice: a century through recorded history,
// a coarser span in deep time.
type WikidataDumpBucketRow struct {
	StartYear float64                   `json:"start_year"`
	SpanYears float64                   `json:"span_years"`
	Total     WikidataDumpCoverageStats `json:"total"`
	Types     []WikidataDumpTypeRow     `json:"types"`
	Classes   []WikidataDumpClassRow    `json:"classes"`
}

type WikidataDumpPrecisionCount struct {
	Precision string `json:"precision"`
	Count     int    `json:"count"`
}

// WikidataDumpCalendarCount splits resolved times by the calendar they were
// stated in and the era they fall in. Julian and Gregorian values are not
// interchangeable, and a census that cannot tell BCE from CE cannot be checked.
type WikidataDumpCalendarCount struct {
	CalendarModel string `json:"calendar_model"`
	Era           string `json:"era"`
	Count         int    `json:"count"`
}

type WikidataDumpUnclassifiedClassCount struct {
	ClassQID string `json:"class_qid"`
	Count    int    `json:"count"`
}

type WikidataDumpTimeClaimCount struct {
	Property  string `json:"property"`
	Precision int    `json:"precision"`
	Count     int    `json:"count"`
}

// WikidataDumpSkipCount reports one reason the scanner declined a claim. Every
// drop path in the scanner lands here; a dump-format change that starts
// discarding claims shows up as a count instead of as silence.
type WikidataDumpSkipCount struct {
	Reason WikidataDumpSkipReason `json:"reason"`
	Count  int                    `json:"count"`
}

type WikidataDumpCoverageReport struct {
	SchemaVersion int             `json:"schema_version"`
	CoverageBasis string          `json:"coverage_basis"`
	InputSHA256   string          `json:"input_sha256"`
	Compression   DumpCompression `json:"compression"`

	Items      WikidataDumpCoverageStats `json:"items"`
	Properties int                       `json:"properties"`

	Types   []WikidataDumpTypeRow   `json:"types"`
	Classes []WikidataDumpClassRow  `json:"classes"`
	Buckets []WikidataDumpBucketRow `json:"buckets"`

	TimePrecision    []WikidataDumpPrecisionCount `json:"time_precision"`
	Calendars        []WikidataDumpCalendarCount  `json:"calendars"`
	ItemsWithoutTime int                          `json:"items_without_time"`

	UnclassifiedClasses      []WikidataDumpUnclassifiedClassCount `json:"unclassified_classes"`
	UnclassifiedClassesTotal int                                  `json:"unclassified_classes_total"`

	TimeClaims    []WikidataDumpTimeClaimCount `json:"time_claims"`
	SkippedClaims []WikidataDumpSkipCount      `json:"skipped_claims"`
}

const (
	eraBCE = "BCE"
	eraCE  = "CE"
)

type wikidataDumpTimeClaimKey struct {
	property  string
	precision int
}

type wikidataDumpCalendarKey struct {
	calendarModel string
	era           string
}

type dumpStatsAccumulator struct {
	count               int
	hasEnglishLabel     int
	hasDate             int
	hasCoordinates      int
	hasEnglishWikipedia int
	hasAnySitelink      int
	hasAll              int
	totalSitelinks      int
}

func (a *dumpStatsAccumulator) add(facts wikidataDumpItemFacts, hasDate bool) {
	a.count++
	if facts.HasEnglishLabel {
		a.hasEnglishLabel++
	}
	if hasDate {
		a.hasDate++
	}
	if facts.HasCoordinates {
		a.hasCoordinates++
	}
	if facts.HasEnglishWikipedia {
		a.hasEnglishWikipedia++
	}
	if facts.SitelinkCount != 0 {
		a.hasAnySitelink++
	}
	if hasDate && facts.HasCoordinates && facts.HasEnglishWikipedia {
		a.hasAll++
	}
	a.totalSitelinks += facts.SitelinkCount
}

func (a *dumpStatsAccumulator) snapshot() WikidataDumpCoverageStats {
	return WikidataDumpCoverageStats{
		Count:               a.count,
		HasEnglishLabel:     a.hasEnglishLabel,
		HasDate:             a.hasDate,
		HasCoordinates:      a.hasCoordinates,
		HasEnglishWikipedia: a.hasEnglishWikipedia,
		HasAnySitelink:      a.hasAnySitelink,
		HasAll:              a.hasAll,
		TotalSitelinks:      a.totalSitelinks,
		Ratios: WikidataDumpCoverageRatios{
			Date:             coverageRatio(a.hasDate, a.count),
			Coordinates:      coverageRatio(a.hasCoordinates, a.count),
			EnglishWikipedia: coverageRatio(a.hasEnglishWikipedia, a.count),
			AnySitelink:      coverageRatio(a.hasAnySitelink, a.count),
			All:              coverageRatio(a.hasAll, a.count),
		},
	}
}

// coverageRatio rounds to six places so repeated runs serialize identically.
func coverageRatio(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(part)/float64(total)*1e6) / 1e6
}

type dumpClassAccumulator struct {
	class WikidataClass
	stats *dumpStatsAccumulator
}

type dumpBucketAccumulator struct {
	total   *dumpStatsAccumulator
	byType  map[string]*dumpStatsAccumulator
	byClass map[string]*dumpClassAccumulator
}

type dumpCensusAccumulator struct {
	total     *dumpStatsAccumulator
	byType    map[string]*dumpStatsAccumulator
	byClass   map[string]*dumpClassAccumulator
	byBucket  map[censusBucketKey]*dumpBucketAccumulator
	precision map[string]int
	calendars map[wikidataDumpCalendarKey]int
	unmatched map[string]int
	noTime    int
}

func newDumpCensusAccumulator() *dumpCensusAccumulator {
	return &dumpCensusAccumulator{
		total:     &dumpStatsAccumulator{},
		byType:    map[string]*dumpStatsAccumulator{},
		byClass:   map[string]*dumpClassAccumulator{},
		byBucket:  map[censusBucketKey]*dumpBucketAccumulator{},
		precision: map[string]int{},
		calendars: map[wikidataDumpCalendarKey]int{},
		unmatched: map[string]int{},
	}
}

func BuildWikidataDumpCoverageReport(r io.Reader) (WikidataDumpCoverageReport, error) {
	if r == nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: nil reader")
	}
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: %w", err)
	}

	digest := sha256.New()
	report := WikidataDumpCoverageReport{
		SchemaVersion: wikidataDumpCoverageReportSchemaVersion,
		CoverageBasis: wikidataDumpCoverageBasis,
	}
	census := newDumpCensusAccumulator()
	timeClaimCounts := map[wikidataDumpTimeClaimKey]int{}

	// The digest identifies the input artifact as supplied, compressed or not,
	// so a report can be traced back to the exact dump file it read.
	tee := io.TeeReader(r, digest)
	stream, compression, err := OpenWikidataDumpStream(tee)
	if err != nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: %w", err)
	}
	report.Compression = compression

	counters := newDumpCounters()
	scan, err := scanWikidataDump(stream, counters, func(facts wikidataDumpItemFacts) error {
		classification := taxonomy.Classify(facts.InstanceOfQIDs, facts.SubclassOfQIDs)
		resolved, hasTime := resolveWikidataItemTime(counters, facts.TimeClaims)
		census.add(facts, classification, resolved, hasTime)

		for _, claim := range facts.TimeClaims {
			timeClaimCounts[wikidataDumpTimeClaimKey{property: claim.Property, precision: claim.Precision}]++
		}
		return nil
	})
	if err != nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: %w", err)
	}

	// A compressed container can end before its file does; drain so the digest
	// covers every byte the caller handed us.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: drain input: %w", err)
	}

	report.InputSHA256 = hex.EncodeToString(digest.Sum(nil))
	report.Properties = scan.Properties
	report.SkippedClaims = summarizeDumpSkips(counters.skips)
	report.TimeClaims = summarizeTimeClaims(timeClaimCounts)
	census.fill(&report)
	return report, nil
}

func (c *dumpCensusAccumulator) add(
	facts wikidataDumpItemFacts,
	classification WikidataClassification,
	resolved WikidataItemTime,
	hasTime bool,
) {
	c.total.add(facts, hasTime)
	statsFor(c.byType, classification.CensusType()).add(facts, hasTime)

	if classification.Outcome == ClassificationTyped {
		classStatsFor(c.byClass, classification.Class).stats.add(facts, hasTime)
	}
	if classification.Outcome == ClassificationUnclassified {
		for _, qid := range facts.InstanceOfQIDs {
			c.unmatched[qid]++
		}
	}

	if !hasTime {
		c.noTime++
		return
	}

	c.precision[resolved.Precision]++
	c.calendars[wikidataDumpCalendarKey{calendarModel: resolved.CalendarModel, era: eraFor(resolved.Year)}]++

	// Attribution uses the year the source stated, in the calendar it stated it
	// in, so the era split and the time slice always agree.
	key := censusBucketKeyFor(resolved.Year)
	bucket := c.byBucket[key]
	if bucket == nil {
		bucket = &dumpBucketAccumulator{
			total:   &dumpStatsAccumulator{},
			byType:  map[string]*dumpStatsAccumulator{},
			byClass: map[string]*dumpClassAccumulator{},
		}
		c.byBucket[key] = bucket
	}
	bucket.total.add(facts, hasTime)
	statsFor(bucket.byType, classification.CensusType()).add(facts, hasTime)
	if classification.Outcome == ClassificationTyped {
		classStatsFor(bucket.byClass, classification.Class).stats.add(facts, hasTime)
	}
}

func (c *dumpCensusAccumulator) fill(report *WikidataDumpCoverageReport) {
	report.Items = c.total.snapshot()
	report.Types = buildDumpTypeRows(c.byType)
	report.Classes = buildDumpClassRows(c.byClass)
	report.ItemsWithoutTime = c.noTime

	keys := make([]censusBucketKey, 0, len(c.byBucket))
	for key := range c.byBucket {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b censusBucketKey) int {
		if lessCensusBucketKey(a, b) {
			return -1
		}
		if lessCensusBucketKey(b, a) {
			return 1
		}
		return 0
	})
	report.Buckets = make([]WikidataDumpBucketRow, 0, len(keys))
	for _, key := range keys {
		bucket := c.byBucket[key]
		report.Buckets = append(report.Buckets, WikidataDumpBucketRow{
			StartYear: key.StartYear,
			SpanYears: key.SpanYears,
			Total:     bucket.total.snapshot(),
			Types:     buildDumpTypeRows(bucket.byType),
			Classes:   buildDumpClassRows(bucket.byClass),
		})
	}

	report.TimePrecision = make([]WikidataDumpPrecisionCount, 0, len(c.precision))
	for precision, count := range c.precision {
		report.TimePrecision = append(report.TimePrecision, WikidataDumpPrecisionCount{Precision: precision, Count: count})
	}
	slices.SortFunc(report.TimePrecision, func(a, b WikidataDumpPrecisionCount) int {
		return cmp.Compare(a.Precision, b.Precision)
	})

	report.Calendars = make([]WikidataDumpCalendarCount, 0, len(c.calendars))
	for key, count := range c.calendars {
		report.Calendars = append(report.Calendars, WikidataDumpCalendarCount{
			CalendarModel: key.calendarModel, Era: key.era, Count: count,
		})
	}
	slices.SortFunc(report.Calendars, func(a, b WikidataDumpCalendarCount) int {
		if a.CalendarModel != b.CalendarModel {
			return cmp.Compare(a.CalendarModel, b.CalendarModel)
		}
		return cmp.Compare(a.Era, b.Era)
	})

	report.UnclassifiedClassesTotal = len(c.unmatched)
	report.UnclassifiedClasses = make([]WikidataDumpUnclassifiedClassCount, 0, len(c.unmatched))
	for qid, count := range c.unmatched {
		report.UnclassifiedClasses = append(report.UnclassifiedClasses, WikidataDumpUnclassifiedClassCount{ClassQID: qid, Count: count})
	}
	slices.SortFunc(report.UnclassifiedClasses, func(a, b WikidataDumpUnclassifiedClassCount) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return cmp.Compare(a.ClassQID, b.ClassQID)
	})
	if len(report.UnclassifiedClasses) > maxReportedUnclassifiedClasses {
		report.UnclassifiedClasses = report.UnclassifiedClasses[:maxReportedUnclassifiedClasses]
	}
}

func statsFor(byKey map[string]*dumpStatsAccumulator, key string) *dumpStatsAccumulator {
	stats := byKey[key]
	if stats == nil {
		stats = &dumpStatsAccumulator{}
		byKey[key] = stats
	}
	return stats
}

func classStatsFor(byClass map[string]*dumpClassAccumulator, class WikidataClass) *dumpClassAccumulator {
	entry := byClass[class.QID]
	if entry == nil {
		entry = &dumpClassAccumulator{class: class, stats: &dumpStatsAccumulator{}}
		byClass[class.QID] = entry
	}
	return entry
}

func buildDumpTypeRows(byType map[string]*dumpStatsAccumulator) []WikidataDumpTypeRow {
	rows := make([]WikidataDumpTypeRow, 0, len(byType))
	for entityType, stats := range byType {
		rows = append(rows, WikidataDumpTypeRow{Type: entityType, Stats: stats.snapshot()})
	}
	slices.SortFunc(rows, func(a, b WikidataDumpTypeRow) int { return cmp.Compare(a.Type, b.Type) })
	return rows
}

func buildDumpClassRows(byClass map[string]*dumpClassAccumulator) []WikidataDumpClassRow {
	rows := make([]WikidataDumpClassRow, 0, len(byClass))
	for _, entry := range byClass {
		rows = append(rows, WikidataDumpClassRow{
			ClassQID:   entry.class.QID,
			ClassLabel: entry.class.Label,
			Type:       entry.class.Type,
			Stats:      entry.stats.snapshot(),
		})
	}
	slices.SortFunc(rows, func(a, b WikidataDumpClassRow) int { return cmp.Compare(a.ClassQID, b.ClassQID) })
	return rows
}

func eraFor(year float64) string {
	if year < 1 {
		return eraBCE
	}
	return eraCE
}

func summarizeTimeClaims(counts map[wikidataDumpTimeClaimKey]int) []WikidataDumpTimeClaimCount {
	out := make([]WikidataDumpTimeClaimCount, 0, len(counts))
	for key, count := range counts {
		out = append(out, WikidataDumpTimeClaimCount{Property: key.property, Precision: key.precision, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Property != out[j].Property {
			return out[i].Property < out[j].Property
		}
		return out[i].Precision < out[j].Precision
	})
	return out
}

func summarizeDumpSkips(skips map[WikidataDumpSkipReason]int) []WikidataDumpSkipCount {
	out := make([]WikidataDumpSkipCount, 0, len(skips))
	for reason, count := range skips {
		out = append(out, WikidataDumpSkipCount{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}
