package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
)

const (
	wikidataDumpCoverageReportSchemaVersion = 1
	wikidataDumpCoverageBasis               = "wikidata-item-facts-after-statement-validation-before-type-classification"
)

type WikidataDumpCoverageStats struct {
	Count               int `json:"count"`
	HasEnglishLabel     int `json:"has_english_label"`
	HasDate             int `json:"has_date"`
	HasCoordinates      int `json:"has_coordinates"`
	HasEnglishWikipedia int `json:"has_english_wikipedia"`
	HasAnySitelink      int `json:"has_any_sitelink"`
	HasAll              int `json:"has_all"`
	TotalSitelinks      int `json:"total_sitelinks"`
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
	Items         WikidataDumpCoverageStats    `json:"items"`
	Properties    int                          `json:"properties"`
	TimeClaims    []WikidataDumpTimeClaimCount `json:"time_claims"`
	SkippedClaims []WikidataDumpSkipCount      `json:"skipped_claims"`
}

type wikidataDumpTimeClaimKey struct {
	property  string
	precision int
}

func BuildWikidataDumpCoverageReport(r io.Reader) (WikidataDumpCoverageReport, error) {
	if r == nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: nil reader")
	}

	digest := sha256.New()
	report := WikidataDumpCoverageReport{
		SchemaVersion: wikidataDumpCoverageReportSchemaVersion,
		CoverageBasis: wikidataDumpCoverageBasis,
		TimeClaims:    []WikidataDumpTimeClaimCount{},
		SkippedClaims: []WikidataDumpSkipCount{},
	}
	timeClaimCounts := map[wikidataDumpTimeClaimKey]int{}

	// The digest identifies the input artifact as supplied, compressed or not,
	// so a report can be traced back to the exact dump file it read.
	tee := io.TeeReader(r, digest)
	stream, compression, err := OpenWikidataDumpStream(tee)
	if err != nil {
		return WikidataDumpCoverageReport{}, fmt.Errorf("build wikidata dump coverage report: %w", err)
	}
	report.Compression = compression

	scan, err := scanWikidataDump(stream, func(facts wikidataDumpItemFacts) error {
		hasDate := len(facts.TimeClaims) != 0
		report.Items.Count++
		if facts.HasEnglishLabel {
			report.Items.HasEnglishLabel++
		}
		if hasDate {
			report.Items.HasDate++
		}
		if facts.HasCoordinates {
			report.Items.HasCoordinates++
		}
		if facts.HasEnglishWikipedia {
			report.Items.HasEnglishWikipedia++
		}
		if facts.SitelinkCount != 0 {
			report.Items.HasAnySitelink++
		}
		if hasDate && facts.HasCoordinates && facts.HasEnglishWikipedia {
			report.Items.HasAll++
		}
		report.Items.TotalSitelinks += facts.SitelinkCount

		for _, claim := range facts.TimeClaims {
			key := wikidataDumpTimeClaimKey{property: claim.Property, precision: claim.Precision}
			timeClaimCounts[key]++
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
	report.SkippedClaims = summarizeDumpSkips(scan.Skips)
	for key, count := range timeClaimCounts {
		report.TimeClaims = append(report.TimeClaims, WikidataDumpTimeClaimCount{
			Property:  key.property,
			Precision: key.precision,
			Count:     count,
		})
	}
	sort.Slice(report.TimeClaims, func(i, j int) bool {
		if report.TimeClaims[i].Property != report.TimeClaims[j].Property {
			return report.TimeClaims[i].Property < report.TimeClaims[j].Property
		}
		return report.TimeClaims[i].Precision < report.TimeClaims[j].Precision
	})
	return report, nil
}

func summarizeDumpSkips(skips map[WikidataDumpSkipReason]int) []WikidataDumpSkipCount {
	out := make([]WikidataDumpSkipCount, 0, len(skips))
	for reason, count := range skips {
		out = append(out, WikidataDumpSkipCount{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}
