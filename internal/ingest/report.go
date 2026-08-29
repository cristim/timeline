package ingest

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"
)

type WarmSource string

const (
	WarmSourceNone           WarmSource = "none"
	WarmSourceWarmFile       WarmSource = "warm-file"
	WarmSourceWikidataEvents WarmSource = "wikidata-events"
)

const ImportReportSchemaVersion = 1

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
}

func BuildImportReport(result *Result, warmSource WarmSource, warmSHA256 string) (ImportReport, error) {
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
	}, nil
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
