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

type WikidataDumpCoverageReport struct {
	SchemaVersion int                          `json:"schema_version"`
	CoverageBasis string                       `json:"coverage_basis"`
	InputSHA256   string                       `json:"input_sha256"`
	Items         WikidataDumpCoverageStats    `json:"items"`
	TimeClaims    []WikidataDumpTimeClaimCount `json:"time_claims"`
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
	}
	timeClaimCounts := map[wikidataDumpTimeClaimKey]int{}

	err := scanWikidataDump(io.TeeReader(r, digest), func(facts wikidataDumpItemFacts) error {
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

	report.InputSHA256 = hex.EncodeToString(digest.Sum(nil))
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
