package ingest

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"
	"time"
)

type WarmSource string

const (
	WarmSourceNone           WarmSource = "none"
	WarmSourceWarmFile       WarmSource = "warm-file"
	WarmSourceWikidataEvents WarmSource = "wikidata-events"
)

const ImportReportSchemaVersion = 2

type ImportCounts struct {
	Seed  int `json:"seed"`
	Warm  int `json:"warm"`
	Total int `json:"total"`
}

type RejectReasonCount struct {
	Source RejectSource `json:"source"`
	Reason string       `json:"reason"`
	Count  int          `json:"count"`
}

type LicenseAction string

const (
	LicenseAccepted LicenseAction = "accepted"
	LicenseExcluded LicenseAction = "excluded"
)

type LicenseCount struct {
	License string `json:"license"`
	Count   int    `json:"count"`
}

type LicenseException struct {
	SourceID    string        `json:"source_id"`
	License     string        `json:"license"`
	Attribution string        `json:"attribution"`
	Action      LicenseAction `json:"action"`
	Reason      string        `json:"reason,omitempty"`
}

// OHMImportSummary records source-relation counts. A relation appearing in
// both target snapshots is counted once.
type OHMImportSummary struct {
	Source            string             `json:"source"`
	InputSHA256       string             `json:"input_sha256"`
	RetrievedAt       string             `json:"retrieved_at"`
	Parsed            int                `json:"parsed"`
	Accepted          int                `json:"accepted"`
	Excluded          int                `json:"excluded"`
	Licenses          []LicenseCount     `json:"licenses"`
	LicenseExceptions []LicenseException `json:"license_exceptions"`
}

type ImportReport struct {
	SchemaVersion         int                 `json:"schema_version"`
	SeedVersion           string              `json:"seed_version"`
	SeedInputSHA256       string              `json:"seed_input_sha256"`
	WarmSource            WarmSource          `json:"warm_source"`
	WarmSHA256            string              `json:"warm_sha256"`
	Parsed                ImportCounts        `json:"parsed"`
	Accepted              ImportCounts        `json:"accepted"`
	Rejected              ImportCounts        `json:"rejected"`
	SkippedWarmDuplicates int                 `json:"skipped_warm_duplicates"`
	RejectReasons         []RejectReasonCount `json:"reject_reasons"`
	OHM                   *OHMImportSummary   `json:"ohm,omitempty"`
}

func BuildImportReport(result *Result, warmSource WarmSource, warmSHA256 string, ohm *OHMImportSummary) (ImportReport, error) {
	if result == nil {
		return ImportReport{}, fmt.Errorf("build import report: nil result")
	}
	if result.SeedVersion == "" {
		return ImportReport{}, fmt.Errorf("build import report: missing seed version")
	}
	if !isSHA256Hex(result.SeedInputSHA256) {
		return ImportReport{}, fmt.Errorf("build import report: invalid seed input sha256 %q", result.SeedInputSHA256)
	}
	if !validWarmSource(warmSource) {
		return ImportReport{}, fmt.Errorf("build import report: unknown warm source %q", warmSource)
	}
	if warmSource == WarmSourceNone {
		if warmSHA256 != "" {
			return ImportReport{}, fmt.Errorf("build import report: warm source %q requires empty warm sha256", warmSource)
		}
		if result.WarmParsed != 0 || result.WarmAccepted != 0 || result.WarmDuplicatesSkipped != 0 {
			return ImportReport{}, fmt.Errorf("build import report: warm source %q requires zero warm counters", warmSource)
		}
	} else if !isSHA256Hex(warmSHA256) {
		return ImportReport{}, fmt.Errorf("build import report: warm source %q requires sha256 digest", warmSource)
	}

	if result.SeedParsed < 0 || result.SeedAccepted < 0 || result.WarmParsed < 0 || result.WarmAccepted < 0 || result.WarmDuplicatesSkipped < 0 {
		return ImportReport{}, fmt.Errorf("build import report: counters must be non-negative")
	}

	seedRejected := 0
	warmRejected := 0
	reasonCounts := map[RejectReasonCount]int{}
	for _, reject := range result.Rejects {
		switch reject.Source {
		case RejectSourceSeed:
			seedRejected++
		case RejectSourceWarm:
			warmRejected++
			if warmSource == WarmSourceNone {
				return ImportReport{}, fmt.Errorf("build import report: warm reject requires warm source")
			}
		default:
			return ImportReport{}, fmt.Errorf("build import report: unknown reject source %q", reject.Source)
		}
		key := RejectReasonCount{Source: reject.Source, Reason: reject.Reason}
		reasonCounts[key]++
	}

	if result.SeedAccepted+seedRejected != result.SeedParsed {
		return ImportReport{}, fmt.Errorf("build import report: seed counters inconsistent")
	}
	if result.WarmAccepted+result.WarmDuplicatesSkipped+warmRejected != result.WarmParsed {
		return ImportReport{}, fmt.Errorf("build import report: warm counters inconsistent")
	}

	rejectReasons := make([]RejectReasonCount, 0, len(reasonCounts))
	for key, count := range reasonCounts {
		rejectReasons = append(rejectReasons, RejectReasonCount{
			Source: key.Source,
			Reason: key.Reason,
			Count:  count,
		})
	}
	slices.SortFunc(rejectReasons, func(a, b RejectReasonCount) int {
		if a.Source != b.Source {
			return cmp.Compare(string(a.Source), string(b.Source))
		}
		return cmp.Compare(a.Reason, b.Reason)
	})

	parsed := ImportCounts{
		Seed:  result.SeedParsed,
		Warm:  result.WarmParsed,
		Total: result.SeedParsed + result.WarmParsed,
	}
	accepted := ImportCounts{
		Seed:  result.SeedAccepted,
		Warm:  result.WarmAccepted,
		Total: result.SeedAccepted + result.WarmAccepted,
	}
	rejected := ImportCounts{
		Seed:  seedRejected,
		Warm:  warmRejected,
		Total: seedRejected + warmRejected,
	}
	normalizedOHM, err := normalizeOHMSummary(ohm)
	if err != nil {
		return ImportReport{}, fmt.Errorf("build import report: %w", err)
	}

	return ImportReport{
		SchemaVersion:         ImportReportSchemaVersion,
		SeedVersion:           result.SeedVersion,
		SeedInputSHA256:       result.SeedInputSHA256,
		WarmSource:            warmSource,
		WarmSHA256:            warmSHA256,
		Parsed:                parsed,
		Accepted:              accepted,
		Rejected:              rejected,
		SkippedWarmDuplicates: result.WarmDuplicatesSkipped,
		RejectReasons:         rejectReasons,
		OHM:                   normalizedOHM,
	}, nil
}

func normalizeOHMSummary(summary *OHMImportSummary) (*OHMImportSummary, error) {
	if summary == nil {
		return nil, nil
	}
	if summary.Source != "OpenHistoricalMap" {
		return nil, fmt.Errorf("OHM summary source %q, want OpenHistoricalMap", summary.Source)
	}
	if !isSHA256Hex(summary.InputSHA256) {
		return nil, fmt.Errorf("OHM summary has invalid input sha256 %q", summary.InputSHA256)
	}
	if _, err := time.Parse(time.RFC3339, summary.RetrievedAt); err != nil {
		return nil, fmt.Errorf("OHM summary has invalid retrieved_at %q", summary.RetrievedAt)
	}
	if summary.Parsed < 0 || summary.Accepted < 0 || summary.Excluded < 0 || summary.Accepted+summary.Excluded != summary.Parsed {
		return nil, fmt.Errorf("OHM summary counters are inconsistent")
	}
	out := *summary
	out.Licenses = slices.Clone(summary.Licenses)
	out.LicenseExceptions = slices.Clone(summary.LicenseExceptions)
	licenseTotal := 0
	licenses := make(map[string]struct{}, len(out.Licenses))
	for _, row := range out.Licenses {
		if row.License == "" || row.Count <= 0 {
			return nil, fmt.Errorf("OHM summary has invalid license count %#v", row)
		}
		if _, exists := licenses[row.License]; exists {
			return nil, fmt.Errorf("OHM summary repeats license %q", row.License)
		}
		licenses[row.License] = struct{}{}
		licenseTotal += row.Count
	}
	if licenseTotal != out.Parsed {
		return nil, fmt.Errorf("OHM summary license counts total %d, want %d", licenseTotal, out.Parsed)
	}
	sourceIDs := make(map[string]struct{}, len(out.LicenseExceptions))
	excluded := 0
	for _, row := range out.LicenseExceptions {
		if row.SourceID == "" || row.License == "" || row.Attribution == "" {
			return nil, fmt.Errorf("OHM summary has incomplete license exception %#v", row)
		}
		if _, exists := sourceIDs[row.SourceID]; exists {
			return nil, fmt.Errorf("OHM summary repeats license exception %q", row.SourceID)
		}
		sourceIDs[row.SourceID] = struct{}{}
		if _, exists := licenses[row.License]; !exists {
			return nil, fmt.Errorf("OHM summary exception %q has uncounted license %q", row.SourceID, row.License)
		}
		if row.Action != LicenseAccepted && row.Action != LicenseExcluded {
			return nil, fmt.Errorf("OHM summary has unknown license action %q", row.Action)
		}
		if row.Action == LicenseExcluded && row.Reason == "" {
			return nil, fmt.Errorf("OHM excluded license exception %q needs a reason", row.SourceID)
		}
		if row.Action == LicenseExcluded {
			excluded++
		}
	}
	if excluded != out.Excluded {
		return nil, fmt.Errorf("OHM summary has %d excluded license exceptions, want %d", excluded, out.Excluded)
	}
	slices.SortFunc(out.Licenses, func(a, b LicenseCount) int {
		return cmp.Compare(a.License, b.License)
	})
	slices.SortFunc(out.LicenseExceptions, func(a, b LicenseException) int {
		if a.SourceID != b.SourceID {
			return cmp.Compare(a.SourceID, b.SourceID)
		}
		return cmp.Compare(string(a.Action), string(b.Action))
	})
	return &out, nil
}

func validWarmSource(source WarmSource) bool {
	switch source {
	case WarmSourceNone, WarmSourceWarmFile, WarmSourceWikidataEvents:
		return true
	default:
		return false
	}
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
