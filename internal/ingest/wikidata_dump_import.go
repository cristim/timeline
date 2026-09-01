package ingest

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"wk/internal/model"
)

// The SRC-3 bulk ingest: dump -> parse + filter -> normalize (classification and
// calendar) -> validate -> the normalized model, with an import report that is
// deterministic and reproducible from the same dump.
//
// ImportWikidataDump keeps accepted entities and seen QIDs in memory for
// compatibility with seed-sized callers. ImportWikidataDumpToSink lets the
// dump-scale caller keep that state outside the Go heap.
//
// Filtering and rejection are kept apart on purpose. An item we deliberately do
// not want (a Wikimedia housekeeping page, a class the curated table does not
// cover) is FILTERED and counted by reason. An item we wanted but could not
// normalize is REJECTED, and it is the reject rate that gates the run
// (ROAD-3 step 2: "reject rates within gates"). Folding the two together would
// let a filter that matches half of Wikidata hide behind a healthy-looking
// number.

const (
	WikidataDumpImportReportSchemaVersion = 1

	// Wikidata is CC0 in full (SRC-1 tier 1), so provenance is a property of
	// the import rather than of each row.
	wikidataSourceName = "wikidata"
	wikidataLicense    = "CC0-1.0"
	wikidataSourceURL  = "https://www.wikidata.org/"

	// RejectSourceWikidataDump labels reject rows from this importer.
	RejectSourceWikidataDump RejectSource = "wikidata-dump"

	// DefaultMaxRejectRate is the SRC-3 quality gate. The importer only ever
	// tries to normalize items it has already classified and found a time for,
	// so a reject rate above a few percent means the normalizer is wrong, not
	// that Wikidata is messy.
	DefaultMaxRejectRate = 0.05
)

type WikidataDumpFilterReason string

const (
	FilterExcludedClass  WikidataDumpFilterReason = "excluded wikidata class"
	FilterUnclassified   WikidataDumpFilterReason = "unclassified wikidata class"
	FilterNoEnglishLabel WikidataDumpFilterReason = "no english label"
	FilterNoUsableTime   WikidataDumpFilterReason = "no usable time claim"
)

type WikidataDumpCountByReason struct {
	Reason string  `json:"reason"`
	Count  int     `json:"count"`
	Rate   float64 `json:"rate"`
}

type WikidataDumpImportReport struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	SourceURL     string `json:"source_url"`
	License       string `json:"license"`

	InputSHA256 string          `json:"input_sha256"`
	Compression DumpCompression `json:"compression"`

	Items      int `json:"items"`
	Properties int `json:"properties"`
	Filtered   int `json:"filtered"`
	Accepted   int `json:"accepted"`
	Rejected   int `json:"rejected"`

	FilterReasons []WikidataDumpCountByReason `json:"filter_reasons"`
	RejectReasons []WikidataDumpCountByReason `json:"reject_reasons"`
	RejectRate    float64                     `json:"reject_rate"`
	MaxRejectRate float64                     `json:"max_reject_rate"`

	AcceptedByType []WikidataDumpTypeCount `json:"accepted_by_type"`
	SkippedClaims  []WikidataDumpSkipCount `json:"skipped_claims"`
}

type WikidataDumpTypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type WikidataDumpImportOptions struct {
	// MaxRejectRate gates the run. Nil takes DefaultMaxRejectRate; an explicit
	// 0 means "no reject at all is acceptable", which a sentinel zero could
	// not express.
	MaxRejectRate *float64
}

type WikidataDumpImport struct {
	Entities []*model.Entity
	Rejects  []Reject
	Report   WikidataDumpImportReport
}

// WikidataDumpSink receives normalized rows while the dump is scanned.
// MarkSeenQID must reject duplicates and is called before filtering.
// AcceptEntity receives entities before global slug assignment.
type WikidataDumpSink interface {
	MarkSeenQID(qid string) error
	AcceptEntity(entity *model.Entity) error
	Reject(reject Reject) error
}

type memoryWikidataDumpSink struct {
	seen     map[string]bool
	entities []*model.Entity
	rejects  []Reject
}

func (s *memoryWikidataDumpSink) MarkSeenQID(qid string) error {
	if s.seen[qid] {
		return fmt.Errorf("duplicate wikidata id %s", qid)
	}
	s.seen[qid] = true
	return nil
}

func (s *memoryWikidataDumpSink) AcceptEntity(entity *model.Entity) error {
	s.entities = append(s.entities, entity)
	return nil
}

func (s *memoryWikidataDumpSink) Reject(reject Reject) error {
	s.rejects = append(s.rejects, reject)
	return nil
}

// ImportWikidataDump preserves the in-memory compatibility API for tests and
// seed-sized callers. Dump-scale callers use ImportWikidataDumpToSink.
func ImportWikidataDump(r io.Reader, opts WikidataDumpImportOptions) (*WikidataDumpImport, error) {
	sink := &memoryWikidataDumpSink{seen: make(map[string]bool)}
	report, err := importWikidataDumpToSink(r, opts, sink)
	if err != nil {
		return nil, err
	}
	if err := model.AssignSlugs(sink.entities); err != nil {
		return nil, fmt.Errorf("import wikidata dump: %w", err)
	}
	if err := validateWikidataDumpRejectRate(report); err != nil {
		return nil, err
	}
	return &WikidataDumpImport{Entities: sink.entities, Rejects: sink.rejects, Report: report}, nil
}

// ImportWikidataDumpToSink streams normalized, validated rows to sink.
func ImportWikidataDumpToSink(r io.Reader, opts WikidataDumpImportOptions, sink WikidataDumpSink) (WikidataDumpImportReport, error) {
	report, err := importWikidataDumpToSink(r, opts, sink)
	if err != nil {
		return WikidataDumpImportReport{}, err
	}
	if err := validateWikidataDumpRejectRate(report); err != nil {
		return WikidataDumpImportReport{}, err
	}
	return report, nil
}

func importWikidataDumpToSink(r io.Reader, opts WikidataDumpImportOptions, sink WikidataDumpSink) (WikidataDumpImportReport, error) {
	if r == nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: nil reader")
	}
	if sink == nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: nil sink")
	}
	maxRejectRate := DefaultMaxRejectRate
	if opts.MaxRejectRate != nil {
		maxRejectRate = *opts.MaxRejectRate
	}
	if maxRejectRate < 0 || maxRejectRate > 1 {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: max reject rate %v outside [0,1]", maxRejectRate)
	}
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: %w", err)
	}

	filterCounts := map[string]int{}
	rejectCounts := map[string]int{}
	typeCounts := map[string]int{}
	accepted := 0
	index := 0

	digest := sha256.New()
	tee := io.TeeReader(r, digest)
	stream, compression, err := OpenWikidataDumpStream(tee)
	if err != nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: %w", err)
	}

	counters := newDumpCounters()
	scan, err := scanWikidataDump(stream, counters, func(facts wikidataDumpItemFacts) error {
		position := index
		index++

		// SRC-3 entity resolution: wikidata_id is the join key, so a repeated
		// one is a corrupt dump rather than a row to reconcile.
		if err := sink.MarkSeenQID(facts.QID); err != nil {
			return err
		}

		classification := taxonomy.Classify(facts.InstanceOfQIDs, facts.SubclassOfQIDs)
		switch classification.Outcome {
		case ClassificationExcluded:
			filterCounts[string(FilterExcludedClass)]++
			return nil
		case ClassificationUnclassified:
			filterCounts[string(FilterUnclassified)]++
			return nil
		}
		if facts.EnglishLabel == "" {
			// An unlabeled item has nothing to render, so it is filtered
			// rather than rejected.
			counters.skip(SkipLabelMissingForItem)
			filterCounts[string(FilterNoEnglishLabel)]++
			return nil
		}
		resolved, ok := resolveWikidataItemTime(counters, facts.TimeClaims)
		if !ok {
			counters.skip(SkipUnusableTimeForItem)
			filterCounts[string(FilterNoUsableTime)]++
			return nil
		}

		entity := normalizedDumpEntity(facts, classification, resolved)
		if reason := ValidateEntity(entity); reason != "" {
			reject := Reject{
				Source: RejectSourceWikidataDump,
				File:   wikidataSourceName,
				Line:   position,
				Reason: reason,
			}
			if err := sink.Reject(reject); err != nil {
				return err
			}
			rejectCounts[reason]++
			return nil
		}
		if err := sink.AcceptEntity(entity); err != nil {
			return err
		}
		typeCounts[entity.Type]++
		accepted++
		return nil
	})
	if err != nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: %w", err)
	}
	// Drain the DECODED stream first: a corrupt compression trailer after the
	// JSON array closes only surfaces as a decoder error, and reading the raw
	// bytes straight past it would swallow that. Then drain the raw side so the
	// digest covers every byte the caller handed us.
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: drain decoded input: %w", err)
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: drain input: %w", err)
	}

	filtered := totalCount(filterCounts)
	rejected := totalCount(rejectCounts)
	considered := scan.Items - filtered
	report := WikidataDumpImportReport{
		SchemaVersion:  WikidataDumpImportReportSchemaVersion,
		Source:         wikidataSourceName,
		SourceURL:      wikidataSourceURL,
		License:        wikidataLicense,
		InputSHA256:    hex.EncodeToString(digest.Sum(nil)),
		Compression:    compression,
		Items:          scan.Items,
		Properties:     scan.Properties,
		Filtered:       filtered,
		Accepted:       accepted,
		Rejected:       rejected,
		FilterReasons:  countsByReason(filterCounts, scan.Items),
		RejectReasons:  countsByReason(rejectCounts, considered),
		RejectRate:     coverageRatio(rejected, considered),
		MaxRejectRate:  maxRejectRate,
		AcceptedByType: typeCountRows(typeCounts),
		SkippedClaims:  summarizeDumpSkips(counters.skips),
	}
	if report.Accepted+report.Rejected+report.Filtered != scan.Items {
		return WikidataDumpImportReport{}, fmt.Errorf("import wikidata dump: %d accepted + %d rejected + %d filtered != %d items",
			report.Accepted, report.Rejected, report.Filtered, scan.Items)
	}
	return report, nil
}

func validateWikidataDumpRejectRate(report WikidataDumpImportReport) error {
	if report.RejectRate <= report.MaxRejectRate {
		return nil
	}
	considered := report.Items - report.Filtered
	return fmt.Errorf("import wikidata dump: reject rate %.4f exceeds the gate of %.4f (%d of %d normalizable items)",
		report.RejectRate, report.MaxRejectRate, report.Rejected, considered)
}

// ValidateWikidataDumpImportReport checks the deterministic report contract.
func ValidateWikidataDumpImportReport(report WikidataDumpImportReport) error {
	if report.SchemaVersion != WikidataDumpImportReportSchemaVersion || report.Source != wikidataSourceName || report.SourceURL != wikidataSourceURL || report.License != wikidataLicense {
		return fmt.Errorf("invalid Wikidata import report provenance or schema")
	}
	decoded, err := hex.DecodeString(report.InputSHA256)
	if err != nil || len(decoded) != sha256.Size || report.InputSHA256 != strings.ToLower(report.InputSHA256) {
		return fmt.Errorf("invalid Wikidata import report input SHA-256")
	}
	if report.Compression != DumpCompressionNone && report.Compression != DumpCompressionGzip && report.Compression != DumpCompressionBzip2 {
		return fmt.Errorf("invalid Wikidata import report compression %q", report.Compression)
	}
	if report.Items < 0 || report.Properties < 0 || report.Filtered < 0 || report.Accepted < 0 || report.Rejected < 0 || report.Filtered > report.Items || report.Accepted > report.Items-report.Filtered || report.Rejected != report.Items-report.Filtered-report.Accepted {
		return fmt.Errorf("invalid Wikidata import report counters")
	}
	filterReasons := map[string]bool{string(FilterExcludedClass): true, string(FilterUnclassified): true, string(FilterNoEnglishLabel): true, string(FilterNoUsableTime): true}
	filterRemaining := report.Filtered
	seenReasons := make(map[string]bool, len(report.FilterReasons))
	for i, row := range report.FilterReasons {
		if !filterReasons[row.Reason] || seenReasons[row.Reason] || i > 0 && compareCountByReason(report.FilterReasons[i-1], row) >= 0 || row.Count <= 0 || row.Count > filterRemaining || !validReportRate(row.Rate, coverageRatio(row.Count, report.Items)) {
			return fmt.Errorf("invalid or unordered Wikidata filter reason row")
		}
		seenReasons[row.Reason] = true
		filterRemaining -= row.Count
	}
	if filterRemaining != 0 {
		return fmt.Errorf("Wikidata filter reasons leave %d of %d items unaccounted for", filterRemaining, report.Filtered)
	}
	considered := report.Accepted + report.Rejected
	rejectRemaining := report.Rejected
	seenReasons = make(map[string]bool, len(report.RejectReasons))
	for i, row := range report.RejectReasons {
		if row.Reason == "" || seenReasons[row.Reason] || i > 0 && compareCountByReason(report.RejectReasons[i-1], row) >= 0 || row.Count <= 0 || row.Count > rejectRemaining || !validReportRate(row.Rate, coverageRatio(row.Count, considered)) {
			return fmt.Errorf("invalid or unordered Wikidata reject reason row")
		}
		seenReasons[row.Reason] = true
		rejectRemaining -= row.Count
	}
	if rejectRemaining != 0 || !validReportRate(report.RejectRate, coverageRatio(report.Rejected, considered)) || math.IsNaN(report.MaxRejectRate) || math.IsInf(report.MaxRejectRate, 0) || report.MaxRejectRate < 0 || report.MaxRejectRate > 1 || report.RejectRate > report.MaxRejectRate {
		return fmt.Errorf("invalid Wikidata reject totals or rates")
	}
	typeRemaining := report.Accepted
	previous := ""
	for _, row := range report.AcceptedByType {
		if !model.EntityTypes[row.Type] || row.Type <= previous || row.Count <= 0 || row.Count > typeRemaining {
			return fmt.Errorf("invalid or unordered Wikidata accepted type row")
		}
		previous = row.Type
		typeRemaining -= row.Count
	}
	if typeRemaining != 0 {
		return fmt.Errorf("Wikidata accepted types leave %d of %d items unaccounted for", typeRemaining, report.Accepted)
	}
	skipReasons := map[WikidataDumpSkipReason]bool{SkipClaimGroupNotArray: true, SkipStatementNotObject: true, SkipStatementRank: true, SkipSnakShape: true, SkipEntityValueInvalid: true, SkipTimeValueInvalid: true, SkipCoordinateValue: true, SkipCoordinateNotOnEarth: true, SkipCoordinateOutOfRange: true, SkipLabelMissingForItem: true, SkipUnusableTimeForItem: true, SkipUnconvertibleTimeFact: true}
	previous = ""
	for _, row := range report.SkippedClaims {
		if !skipReasons[row.Reason] || string(row.Reason) <= previous || row.Count <= 0 {
			return fmt.Errorf("invalid or unordered Wikidata skipped claim row")
		}
		previous = string(row.Reason)
	}
	return nil
}

func validReportRate(got, want float64) bool {
	return !math.IsNaN(got) && !math.IsInf(got, 0) && got >= 0 && got <= 1 && got == want
}

// normalizedDumpEntity is the DM-2 row for one classified, dated item.
func normalizedDumpEntity(
	facts wikidataDumpItemFacts,
	classification WikidataClassification,
	resolved WikidataItemTime,
) *model.Entity {
	return &model.Entity{
		SeedID:      wikidataSeedID(facts.QID),
		Type:        classification.Type,
		Name:        facts.EnglishLabel,
		Description: facts.EnglishDescription,
		T0:          resolved.T0,
		T1:          resolved.T1,
		Precision:   resolved.Precision,
		// Everything here is a sourced third-party statement, never our
		// observation (DM-4 temporal_status).
		Status:     "documented",
		Categories: slices.Clone(classification.Categories),
		Importance: importanceFromSitelinks(facts.SitelinkCount),
		Point:      slices.Clone(facts.Point),
		Wikidata:   facts.QID,
		Wikipedia:  facts.EnglishWikipedia,
	}
}

func wikidataSeedID(qid string) string {
	return "wd-" + strings.ToLower(qid)
}

// ValidWikidataEntityIdentity checks the identity pair emitted by dump import.
func ValidWikidataEntityIdentity(seedID, qid string) bool {
	return validNumericEntityID(qid, 'Q') && seedID == wikidataSeedID(qid)
}

func totalCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func countsByReason(counts map[string]int, total int) []WikidataDumpCountByReason {
	rows := make([]WikidataDumpCountByReason, 0, len(counts))
	for reason, count := range counts {
		rows = append(rows, WikidataDumpCountByReason{
			Reason: reason,
			Count:  count,
			Rate:   coverageRatio(count, total),
		})
	}
	slices.SortFunc(rows, compareCountByReason)
	return rows
}

func compareCountByReason(a, b WikidataDumpCountByReason) int {
	if a.Count != b.Count {
		return cmp.Compare(b.Count, a.Count)
	}
	return cmp.Compare(a.Reason, b.Reason)
}

func typeCountRows(counts map[string]int) []WikidataDumpTypeCount {
	rows := make([]WikidataDumpTypeCount, 0, len(counts))
	for entityType, count := range counts {
		rows = append(rows, WikidataDumpTypeCount{Type: entityType, Count: count})
	}
	slices.SortFunc(rows, func(a, b WikidataDumpTypeCount) int { return cmp.Compare(a.Type, b.Type) })
	return rows
}
