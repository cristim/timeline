package ingest

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"

	"wk/internal/model"
)

// The SRC-3 bulk ingest: dump -> parse + filter -> normalize (classification and
// calendar) -> validate -> the normalized model, with an import report that is
// deterministic and reproducible from the same dump.
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

	// defaultMaxRejectRate is the SRC-3 quality gate. The importer only ever
	// tries to normalize items it has already classified and found a time for,
	// so a reject rate above a few percent means the normalizer is wrong, not
	// that Wikidata is messy.
	defaultMaxRejectRate = 0.05
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
	// MaxRejectRate gates the run; zero means the default.
	MaxRejectRate float64
	// ImportanceFloor drops accepted entities below the floor before they
	// reach the model. Zero keeps everything.
	ImportanceFloor float64
}

type WikidataDumpImport struct {
	Entities []*model.Entity
	Rejects  []Reject
	Report   WikidataDumpImportReport
}

// ImportWikidataDump streams a dump into normalized, validated entities.
func ImportWikidataDump(r io.Reader, opts WikidataDumpImportOptions) (*WikidataDumpImport, error) {
	if r == nil {
		return nil, fmt.Errorf("import wikidata dump: nil reader")
	}
	maxRejectRate := opts.MaxRejectRate
	if maxRejectRate == 0 {
		maxRejectRate = defaultMaxRejectRate
	}
	if maxRejectRate < 0 || maxRejectRate > 1 {
		return nil, fmt.Errorf("import wikidata dump: max reject rate %v outside [0,1]", maxRejectRate)
	}
	if opts.ImportanceFloor < 0 || opts.ImportanceFloor > 1 {
		return nil, fmt.Errorf("import wikidata dump: importance floor %v outside [0,1]", opts.ImportanceFloor)
	}
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		return nil, fmt.Errorf("import wikidata dump: %w", err)
	}

	result := &WikidataDumpImport{}
	filterCounts := map[string]int{}
	rejectCounts := map[string]int{}
	typeCounts := map[string]int{}
	seen := map[string]bool{}
	index := 0

	digest := sha256.New()
	tee := io.TeeReader(r, digest)
	stream, compression, err := OpenWikidataDumpStream(tee)
	if err != nil {
		return nil, fmt.Errorf("import wikidata dump: %w", err)
	}

	counters := newDumpCounters()
	scan, err := scanWikidataDumpWithCounters(stream, counters, func(facts wikidataDumpItemFacts) error {
		position := index
		index++

		// SRC-3 entity resolution: wikidata_id is the join key, so a repeated
		// one is a corrupt dump rather than a row to reconcile.
		if seen[facts.QID] {
			return fmt.Errorf("duplicate wikidata id %s", facts.QID)
		}
		seen[facts.QID] = true

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
			rejectCounts[reason]++
			result.Rejects = append(result.Rejects, Reject{
				Source: RejectSourceWikidataDump,
				File:   wikidataSourceName,
				Line:   position,
				Reason: reason,
			})
			return nil
		}
		if entity.Importance < opts.ImportanceFloor {
			// Held in the WARM tier rather than promoted (SRC-5). Counted as a
			// filter so the reject gate keeps its meaning.
			filterCounts[importanceFloorReason(opts.ImportanceFloor)]++
			return nil
		}
		typeCounts[entity.Type]++
		result.Entities = append(result.Entities, entity)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("import wikidata dump: %w", err)
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return nil, fmt.Errorf("import wikidata dump: drain input: %w", err)
	}
	if err := model.AssignSlugs(result.Entities); err != nil {
		return nil, fmt.Errorf("import wikidata dump: %w", err)
	}

	filtered := totalCount(filterCounts)
	rejected := totalCount(rejectCounts)
	considered := scan.Items - filtered
	result.Report = WikidataDumpImportReport{
		SchemaVersion:  WikidataDumpImportReportSchemaVersion,
		Source:         wikidataSourceName,
		SourceURL:      wikidataSourceURL,
		License:        wikidataLicense,
		InputSHA256:    hex.EncodeToString(digest.Sum(nil)),
		Compression:    compression,
		Items:          scan.Items,
		Properties:     scan.Properties,
		Filtered:       filtered,
		Accepted:       len(result.Entities),
		Rejected:       rejected,
		FilterReasons:  countsByReason(filterCounts, scan.Items),
		RejectReasons:  countsByReason(rejectCounts, considered),
		RejectRate:     coverageRatio(rejected, considered),
		MaxRejectRate:  maxRejectRate,
		AcceptedByType: typeCountRows(typeCounts),
		SkippedClaims:  summarizeDumpSkips(counters.skips),
	}
	if result.Report.Accepted+result.Report.Rejected+result.Report.Filtered != scan.Items {
		return nil, fmt.Errorf("import wikidata dump: %d accepted + %d rejected + %d filtered != %d items",
			result.Report.Accepted, result.Report.Rejected, result.Report.Filtered, scan.Items)
	}
	if result.Report.RejectRate > maxRejectRate {
		return nil, fmt.Errorf("import wikidata dump: reject rate %.4f exceeds the gate of %.4f (%d of %d normalizable items)",
			result.Report.RejectRate, maxRejectRate, rejected, considered)
	}
	return result, nil
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

func importanceFloorReason(floor float64) string {
	return fmt.Sprintf("below importance floor %.2f", floor)
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
	slices.SortFunc(rows, func(a, b WikidataDumpCountByReason) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return cmp.Compare(a.Reason, b.Reason)
	})
	return rows
}

func typeCountRows(counts map[string]int) []WikidataDumpTypeCount {
	rows := make([]WikidataDumpTypeCount, 0, len(counts))
	for entityType, count := range counts {
		rows = append(rows, WikidataDumpTypeCount{Type: entityType, Count: count})
	}
	slices.SortFunc(rows, func(a, b WikidataDumpTypeCount) int { return cmp.Compare(a.Type, b.Type) })
	return rows
}
