package ingest

import (
	"cmp"
	"fmt"
	"slices"

	"wk/internal/model"
)

const (
	CensusReportSchemaVersion = 2
	CensusCoverageBasis       = "accepted-normalized-entities-after-source-filters"
)

type CensusPrecisionCount struct {
	Precision string `json:"precision"`
	Count     int    `json:"count"`
}

type CensusStats struct {
	Count               int                    `json:"count"`
	HasDate             int                    `json:"has_date"`
	HasCoordinates      int                    `json:"has_coordinates"`
	HasEnglishWikipedia int                    `json:"has_english_wikipedia"`
	HasAll              int                    `json:"has_all"`
	Precision           []CensusPrecisionCount `json:"precision"`
}

type CensusTypeRow struct {
	Type  string      `json:"type"`
	Stats CensusStats `json:"stats"`
}

// CensusBucketRow is one time slice: a century through recorded history, a
// coarser span in deep time (see census_buckets.go).
type CensusBucketRow struct {
	StartYear float64         `json:"start_year"`
	SpanYears float64         `json:"span_years"`
	Total     CensusStats     `json:"total"`
	Types     []CensusTypeRow `json:"types"`
}

type CensusReport struct {
	SchemaVersion int               `json:"schema_version"`
	CoverageBasis string            `json:"coverage_basis"`
	ImportReport  ImportReport      `json:"import_report"`
	Total         CensusStats       `json:"total"`
	Types         []CensusTypeRow   `json:"types"`
	Buckets       []CensusBucketRow `json:"buckets"`
}

type censusStatsAccumulator struct {
	count               int
	hasDate             int
	hasCoordinates      int
	hasEnglishWikipedia int
	hasAll              int
	precisionCounts     map[string]int
}

type censusBucketAccumulator struct {
	total  *censusStatsAccumulator
	byType map[string]*censusStatsAccumulator
}

func BuildCensusReport(result *Result, warmSource WarmSource, warmSHA256 string) (CensusReport, error) {
	importReport, err := BuildImportReport(result, warmSource, warmSHA256, nil)
	if err != nil {
		return CensusReport{}, fmt.Errorf("build census report: %w", err)
	}
	if importReport.Accepted.Total != len(result.Entities) {
		return CensusReport{}, fmt.Errorf(
			"build census report: accepted total %d != entity count %d",
			importReport.Accepted.Total,
			len(result.Entities),
		)
	}

	total := newCensusStatsAccumulator()
	byType := map[string]*censusStatsAccumulator{}
	byBucket := map[censusBucketKey]*censusBucketAccumulator{}

	for idx, entity := range result.Entities {
		if entity == nil {
			return CensusReport{}, fmt.Errorf("build census report: entity %d is nil", idx)
		}

		total.add(entity)

		typeStats := byType[entity.Type]
		if typeStats == nil {
			typeStats = newCensusStatsAccumulator()
			byType[entity.Type] = typeStats
		}
		typeStats.add(entity)

		key := censusBucketKeyFor(censusYearForEntity(entity))
		bucket := byBucket[key]
		if bucket == nil {
			bucket = &censusBucketAccumulator{
				total:  newCensusStatsAccumulator(),
				byType: map[string]*censusStatsAccumulator{},
			}
			byBucket[key] = bucket
		}
		bucket.total.add(entity)
		bucketTypeStats := bucket.byType[entity.Type]
		if bucketTypeStats == nil {
			bucketTypeStats = newCensusStatsAccumulator()
			bucket.byType[entity.Type] = bucketTypeStats
		}
		bucketTypeStats.add(entity)
	}

	return CensusReport{
		SchemaVersion: CensusReportSchemaVersion,
		CoverageBasis: CensusCoverageBasis,
		ImportReport:  importReport,
		Total:         total.snapshot(),
		Types:         buildCensusTypeRows(byType),
		Buckets:       buildCensusBucketRows(byBucket),
	}, nil
}

func newCensusStatsAccumulator() *censusStatsAccumulator {
	return &censusStatsAccumulator{
		precisionCounts: map[string]int{},
	}
}

func (a *censusStatsAccumulator) add(entity *model.Entity) {
	a.count++
	a.hasDate++

	hasCoordinates := len(entity.Point) == 2
	hasEnglishWikipedia := entity.Wikipedia != ""
	if hasCoordinates {
		a.hasCoordinates++
	}
	if hasEnglishWikipedia {
		a.hasEnglishWikipedia++
	}
	if hasCoordinates && hasEnglishWikipedia {
		a.hasAll++
	}

	a.precisionCounts[entity.Precision]++
}

func (a *censusStatsAccumulator) snapshot() CensusStats {
	precision := make([]CensusPrecisionCount, 0, len(a.precisionCounts))
	for name, count := range a.precisionCounts {
		precision = append(precision, CensusPrecisionCount{
			Precision: name,
			Count:     count,
		})
	}
	slices.SortFunc(precision, func(a, b CensusPrecisionCount) int {
		return cmp.Compare(a.Precision, b.Precision)
	})

	return CensusStats{
		Count:               a.count,
		HasDate:             a.hasDate,
		HasCoordinates:      a.hasCoordinates,
		HasEnglishWikipedia: a.hasEnglishWikipedia,
		HasAll:              a.hasAll,
		Precision:           precision,
	}
}

func buildCensusTypeRows(byType map[string]*censusStatsAccumulator) []CensusTypeRow {
	rows := make([]CensusTypeRow, 0, len(byType))
	for entityType, stats := range byType {
		rows = append(rows, CensusTypeRow{
			Type:  entityType,
			Stats: stats.snapshot(),
		})
	}
	slices.SortFunc(rows, func(a, b CensusTypeRow) int {
		return cmp.Compare(a.Type, b.Type)
	})
	return rows
}

func buildCensusBucketRows(byBucket map[censusBucketKey]*censusBucketAccumulator) []CensusBucketRow {
	keys := make([]censusBucketKey, 0, len(byBucket))
	for key := range byBucket {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareCensusBucketKey)

	rows := make([]CensusBucketRow, 0, len(keys))
	for _, key := range keys {
		bucket := byBucket[key]
		rows = append(rows, CensusBucketRow{
			StartYear: key.StartYear,
			SpanYears: key.SpanYears,
			Total:     bucket.total.snapshot(),
			Types:     buildCensusTypeRows(bucket.byType),
		})
	}
	return rows
}
