package ingest

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"wk/internal/model"
)

const (
	CensusReportSchemaVersion = 1
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

type CensusCenturyRow struct {
	CenturyStartYear float64         `json:"century_start_year"`
	Total            CensusStats     `json:"total"`
	Types            []CensusTypeRow `json:"types"`
}

type CensusReport struct {
	SchemaVersion int                `json:"schema_version"`
	CoverageBasis string             `json:"coverage_basis"`
	ImportReport  ImportReport       `json:"import_report"`
	Total         CensusStats        `json:"total"`
	Types         []CensusTypeRow    `json:"types"`
	Centuries     []CensusCenturyRow `json:"centuries"`
}

type censusStatsAccumulator struct {
	count               int
	hasDate             int
	hasCoordinates      int
	hasEnglishWikipedia int
	hasAll              int
	precisionCounts     map[string]int
}

type censusCenturyAccumulator struct {
	total  *censusStatsAccumulator
	byType map[string]*censusStatsAccumulator
}

func BuildCensusReport(result *Result, warmSource WarmSource, warmSHA256 string) (CensusReport, error) {
	importReport, err := BuildImportReport(result, warmSource, warmSHA256)
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
	byCentury := map[float64]*censusCenturyAccumulator{}

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

		centuryStart := centuryStartYear(censusYear(entity))
		century := byCentury[centuryStart]
		if century == nil {
			century = &censusCenturyAccumulator{
				total:  newCensusStatsAccumulator(),
				byType: map[string]*censusStatsAccumulator{},
			}
			byCentury[centuryStart] = century
		}
		century.total.add(entity)
		centuryTypeStats := century.byType[entity.Type]
		if centuryTypeStats == nil {
			centuryTypeStats = newCensusStatsAccumulator()
			century.byType[entity.Type] = centuryTypeStats
		}
		centuryTypeStats.add(entity)
	}

	return CensusReport{
		SchemaVersion: CensusReportSchemaVersion,
		CoverageBasis: CensusCoverageBasis,
		ImportReport:  importReport,
		Total:         total.snapshot(),
		Types:         buildCensusTypeRows(byType),
		Centuries:     buildCenturyRows(byCentury),
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

func buildCenturyRows(byCentury map[float64]*censusCenturyAccumulator) []CensusCenturyRow {
	starts := make([]float64, 0, len(byCentury))
	for start := range byCentury {
		starts = append(starts, start)
	}
	slices.Sort(starts)

	rows := make([]CensusCenturyRow, 0, len(starts))
	for _, start := range starts {
		century := byCentury[start]
		rows = append(rows, CensusCenturyRow{
			CenturyStartYear: start,
			Total:            century.total.snapshot(),
			Types:            buildCensusTypeRows(century.byType),
		})
	}
	return rows
}

func censusYear(entity *model.Entity) float64 {
	modelYear := normalizeSignedZero(model.SecondsToYear(entity.T0))
	if modelYear == math.Trunc(modelYear) {
		return modelYear
	}
	if !usesCivilYear(entity.Precision) {
		return modelYear
	}

	roundedSecond := math.Round(entity.T0)
	if roundedSecond < math.MinInt64 || roundedSecond > math.MaxInt64 {
		return modelYear
	}

	civilYear := time.Unix(int64(roundedSecond), 0).UTC().Year()
	if civilYear < 1 || civilYear > 9999 {
		return modelYear
	}
	return normalizeSignedZero(float64(civilYear))
}

func usesCivilYear(precision string) bool {
	switch precision {
	case "year", "month", "day", "hour", "minute", "second":
		return true
	default:
		return false
	}
}

func centuryStartYear(year float64) float64 {
	return normalizeSignedZero(math.Floor(year/100) * 100)
}

func normalizeSignedZero(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}
