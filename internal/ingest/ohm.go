package ingest

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmgeojson"

	"wk/internal/model"
)

const (
	ohmManifestSchema = 1
	ohmSource         = "OpenHistoricalMap"
	ohmDefaultLicense = "CC0-1.0"
	ohmAttribution    = "Map data courtesy of the OpenHistoricalMap project, in the public domain unless otherwise noted."
)

var ohmDatePattern = regexp.MustCompile(`^(-?\d{4,})(?:-(\d{2})(?:-(\d{2}))?)?$`)

type ohmManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Source        string           `json:"source"`
	Endpoint      string           `json:"endpoint"`
	Query         string           `json:"query"`
	RetrievedAt   string           `json:"retrieved_at"`
	Payload       ohmPayloadPin    `json:"payload"`
	TargetYears   []int            `json:"target_years"`
	Relations     []ohmRelationPin `json:"relations"`
}

type ohmPayloadPin struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type ohmRelationPin struct {
	ID      int64 `json:"id"`
	Version int   `json:"version"`
}

type ohmRelation struct {
	id          int64
	version     int
	name        string
	startYear   *int
	endYear     *int
	license     string
	attribution string
	accepted    bool
	geometry    json.RawMessage
}

type ohmDate struct {
	year        int
	earliestDay int
	latestDay   int
}

type parsedOHM struct {
	manifest  ohmManifest
	data      *osm.OSM
	relations []*ohmRelation
	summary   *OHMImportSummary
}

// LoadOHMSummary validates a configured OHM source before import-report
// publication. Full geometry loading later validates its coverage against the
// political layer.
func LoadOHMSummary(geoDir string) (*OHMImportSummary, error) {
	dir := filepath.Join(geoDir, "ohm")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	parsed, err := parseOHMMetadata(dir)
	if err != nil {
		return nil, err
	}
	return parsed.summary, nil
}

func parseOHMMetadata(dir string) (*parsedOHM, error) {
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read OHM manifest: %w", err)
	}
	var manifest ohmManifest
	if err := decodeStrict(manifestBody, &manifest); err != nil {
		return nil, fmt.Errorf("parse OHM manifest: %w", err)
	}
	if err := validateOHMManifest(manifest); err != nil {
		return nil, err
	}

	payloadPath := filepath.Join(dir, manifest.Payload.File)
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		return nil, fmt.Errorf("read OHM payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	actualSHA := hex.EncodeToString(digest[:])
	if actualSHA != manifest.Payload.SHA256 {
		return nil, fmt.Errorf("OHM payload sha256 %s, want %s", actualSHA, manifest.Payload.SHA256)
	}

	var data osm.OSM
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, fmt.Errorf("parse OHM payload: %w", err)
	}
	relations, summary, err := normalizeOHMRelations(&data, manifest)
	if err != nil {
		return nil, err
	}
	return &parsedOHM{manifest: manifest, data: &data, relations: relations, summary: summary}, nil
}

func loadOHM(dir string, politicalTTo int) ([]model.BorderLayer, *OHMImportSummary, error) {
	parsed, err := parseOHMMetadata(dir)
	if err != nil {
		return nil, nil, err
	}
	if err := attachOHMGeometry(parsed.data, parsed.relations); err != nil {
		return nil, nil, err
	}
	manifest := parsed.manifest
	if manifest.TargetYears[len(manifest.TargetYears)-1] > politicalTTo {
		return nil, nil, fmt.Errorf("OHM target years must end by political coverage %d", politicalTTo)
	}
	layers := make([]model.BorderLayer, 0, len(manifest.TargetYears))
	for i, year := range manifest.TargetYears {
		tTo := politicalTTo
		if i+1 < len(manifest.TargetYears) {
			tTo = manifest.TargetYears[i+1] - 1
		}
		layer := model.BorderLayer{
			Year: year, TFrom: year, TTo: tTo,
			Label:  fmt.Sprintf("London administrative boundaries · %d · OpenHistoricalMap", year),
			Source: fmt.Sprintf("OpenHistoricalMap snapshot %s", manifest.Payload.SHA256[:12]),
		}
		for _, relation := range parsed.relations {
			if !relation.accepted || !activeInYear(relation, year) {
				continue
			}
			layer.Features = append(layer.Features, model.BorderFeature{
				Name: relation.name, Representation: "estimated", Geometry: relation.geometry,
				Source: ohmSource, SourceID: fmt.Sprintf("relation/%d@%d", relation.id, relation.version),
				License: relation.license, Attribution: relation.attribution,
				SourceURL:   fmt.Sprintf("https://www.openhistoricalmap.org/relation/%d", relation.id),
				RetrievedAt: manifest.RetrievedAt,
			})
		}
		slices.SortFunc(layer.Features, func(a, b model.BorderFeature) int {
			if a.Name != b.Name {
				return strings.Compare(a.Name, b.Name)
			}
			return strings.Compare(a.SourceID, b.SourceID)
		})
		if len(layer.Features) == 0 {
			return nil, nil, fmt.Errorf("OHM target year %d has no accepted active relations", year)
		}
		layers = append(layers, layer)
	}
	return layers, parsed.summary, nil
}

func VerifyOHM(dir string, politicalTTo int) (AreaCoverage, *OHMImportSummary, error) {
	layers, summary, err := loadOHM(dir, politicalTTo)
	if err != nil {
		return AreaCoverage{}, nil, err
	}
	return AreaCoverage{
		Slices: len(layers), TFrom: layers[0].TFrom, TTo: layers[len(layers)-1].TTo,
	}, summary, nil
}

func decodeStrict(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func validateOHMManifest(manifest ohmManifest) error {
	if manifest.SchemaVersion != ohmManifestSchema {
		return fmt.Errorf("OHM manifest schema_version %d, want %d", manifest.SchemaVersion, ohmManifestSchema)
	}
	if manifest.Source != ohmSource || manifest.Endpoint == "" || manifest.Query == "" {
		return fmt.Errorf("OHM manifest needs source %q, endpoint and query", ohmSource)
	}
	if _, err := time.Parse(time.RFC3339, manifest.RetrievedAt); err != nil {
		return fmt.Errorf("OHM manifest retrieved_at %q: %w", manifest.RetrievedAt, err)
	}
	if manifest.Payload.File == "" || filepath.Base(manifest.Payload.File) != manifest.Payload.File {
		return fmt.Errorf("OHM manifest payload file %q must be one base name", manifest.Payload.File)
	}
	if !isSHA256Hex(manifest.Payload.SHA256) {
		return fmt.Errorf("OHM manifest payload sha256 %q is invalid", manifest.Payload.SHA256)
	}
	if len(manifest.TargetYears) == 0 {
		return fmt.Errorf("OHM target years must be non-empty")
	}
	for i, year := range manifest.TargetYears {
		if i > 0 && year <= manifest.TargetYears[i-1] {
			return fmt.Errorf("OHM target years are not strictly ascending at %d", year)
		}
	}
	if len(manifest.Relations) == 0 {
		return fmt.Errorf("OHM manifest has no relations")
	}
	seen := map[int64]bool{}
	for _, relation := range manifest.Relations {
		if relation.ID <= 0 || relation.Version <= 0 || seen[relation.ID] {
			return fmt.Errorf("OHM manifest has invalid or duplicate relation pin %#v", relation)
		}
		seen[relation.ID] = true
	}
	return nil
}

func normalizeOHMRelations(data *osm.OSM, manifest ohmManifest) ([]*ohmRelation, *OHMImportSummary, error) {
	pins := make(map[int64]int, len(manifest.Relations))
	for _, pin := range manifest.Relations {
		pins[pin.ID] = pin.Version
	}
	byID := make(map[int64]*ohmRelation, len(manifest.Relations))
	licenseCounts := map[string]int{}
	exceptions := []LicenseException{}
	for _, source := range data.Relations {
		id := int64(source.ID)
		wantVersion, declared := pins[id]
		if !declared {
			return nil, nil, fmt.Errorf("OHM payload has undeclared relation %d", id)
		}
		if source.Version != wantVersion {
			return nil, nil, fmt.Errorf("OHM relation %d version %d, want %d", id, source.Version, wantVersion)
		}
		if byID[id] != nil {
			return nil, nil, fmt.Errorf("OHM payload repeats relation %d", id)
		}
		tags := source.Tags.Map()
		if tags["type"] != "boundary" || tags["boundary"] == "" {
			return nil, nil, fmt.Errorf("OHM relation %d is not a boundary", id)
		}
		name := strings.TrimSpace(tags["name"])
		if name == "" {
			return nil, nil, fmt.Errorf("OHM relation %d has no name", id)
		}
		startDate, err := parseOHMDate(tags["start_date"])
		if err != nil {
			return nil, nil, fmt.Errorf("OHM relation %d start_date: %w", id, err)
		}
		endDate, err := parseOHMDate(tags["end_date"])
		if err != nil {
			return nil, nil, fmt.Errorf("OHM relation %d end_date: %w", id, err)
		}
		if startDate != nil && endDate != nil &&
			(startDate.year > endDate.year ||
				(startDate.year == endDate.year && startDate.earliestDay > endDate.latestDay)) {
			return nil, nil, fmt.Errorf("OHM relation %d start_date %q is after end_date %q", id, tags["start_date"], tags["end_date"])
		}
		license, explicit, accepted, reason, err := resolveOHMLicense(tags)
		if err != nil {
			return nil, nil, fmt.Errorf("OHM relation %d: %w", id, err)
		}
		attribution := strings.TrimSpace(tags["attribution"])
		if attribution == "" {
			attribution = ohmAttribution
		}
		sourceID := fmt.Sprintf("relation/%d@%d", id, source.Version)
		if explicit {
			action := LicenseExcluded
			if accepted {
				action = LicenseAccepted
			}
			exceptions = append(exceptions, LicenseException{
				SourceID: sourceID, License: license, Attribution: attribution,
				Action: action,
				Reason: reason,
			})
		}
		licenseCounts[license]++
		byID[id] = &ohmRelation{
			id: id, version: source.Version, name: name,
			startYear: ohmDateYear(startDate), endYear: ohmDateYear(endDate),
			license: license, attribution: attribution, accepted: accepted,
		}
	}
	if len(byID) != len(pins) {
		for id := range pins {
			if byID[id] == nil {
				return nil, nil, fmt.Errorf("OHM payload is missing relation %d", id)
			}
		}
	}
	relations := make([]*ohmRelation, 0, len(byID))
	accepted := 0
	for _, relation := range byID {
		relations = append(relations, relation)
		if relation.accepted {
			accepted++
		}
	}
	slices.SortFunc(relations, func(a, b *ohmRelation) int { return cmp.Compare(a.id, b.id) })
	licenses := make([]LicenseCount, 0, len(licenseCounts))
	for license, count := range licenseCounts {
		licenses = append(licenses, LicenseCount{License: license, Count: count})
	}
	slices.SortFunc(licenses, func(a, b LicenseCount) int { return strings.Compare(a.License, b.License) })
	slices.SortFunc(exceptions, func(a, b LicenseException) int { return strings.Compare(a.SourceID, b.SourceID) })
	return relations, &OHMImportSummary{
		Source: ohmSource, InputSHA256: manifest.Payload.SHA256, RetrievedAt: manifest.RetrievedAt,
		Parsed: len(relations), Accepted: accepted, Excluded: len(relations) - accepted,
		Licenses: licenses, LicenseExceptions: exceptions,
	}, nil
}

func attachOHMGeometry(data *osm.OSM, relations []*ohmRelation) error {
	collection, err := osmgeojson.Convert(data, osmgeojson.NoRelationMembership(true))
	if err != nil {
		return fmt.Errorf("convert OHM geometry: %w", err)
	}
	byFeatureID := make(map[string]*ohmRelation, len(relations))
	for _, relation := range relations {
		byFeatureID[fmt.Sprintf("relation/%d", relation.id)] = relation
	}
	for _, feature := range collection.Features {
		relation := byFeatureID[fmt.Sprint(feature.ID)]
		if relation == nil {
			continue
		}
		if len(relation.geometry) != 0 {
			return fmt.Errorf("OHM converter repeats relation/%d", relation.id)
		}
		geometry, err := json.Marshal(geojson.NewGeometry(feature.Geometry))
		if err != nil {
			return fmt.Errorf("marshal OHM relation/%d geometry: %w", relation.id, err)
		}
		if err := checkPolygons(fmt.Sprintf("OHM relation/%d", relation.id), geometry); err != nil {
			return err
		}
		relation.geometry = geometry
	}
	for _, relation := range relations {
		if len(relation.geometry) == 0 {
			return fmt.Errorf("OHM relation/%d has no valid polygon geometry", relation.id)
		}
	}
	return nil
}

func parseOHMYear(value string) (*int, error) {
	date, err := parseOHMDate(value)
	if err != nil || date == nil {
		return nil, err
	}
	return &date.year, nil
}

func parseOHMDate(value string) (*ohmDate, error) {
	if value == "" {
		return nil, nil
	}
	match := ohmDatePattern.FindStringSubmatch(value)
	if match == nil {
		return nil, fmt.Errorf("%q is not YYYY, YYYY-MM or YYYY-MM-DD", value)
	}
	year, err := strconv.Atoi(match[1])
	if err != nil {
		return nil, fmt.Errorf("year %q: %w", match[1], err)
	}
	date := &ohmDate{year: year, earliestDay: 1, latestDay: time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC).YearDay()}
	if match[2] == "" {
		return date, nil
	}
	monthNumber, _ := strconv.Atoi(match[2])
	if monthNumber < 1 || monthNumber > 12 {
		return nil, fmt.Errorf("%q has invalid month", value)
	}
	month := time.Month(monthNumber)
	date.earliestDay = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).YearDay()
	date.latestDay = time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).YearDay()
	if match[3] != "" {
		day, _ := strconv.Atoi(match[3])
		parsed := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
		if day < 1 || parsed.Month() != month || parsed.Day() != day {
			return nil, fmt.Errorf("%q has invalid day", value)
		}
		date.earliestDay = parsed.YearDay()
		date.latestDay = parsed.YearDay()
	}
	return date, nil
}

func ohmDateYear(date *ohmDate) *int {
	if date == nil {
		return nil
	}
	return &date.year
}

func resolveOHMLicense(tags map[string]string) (license string, explicit, accepted bool, reason string, err error) {
	licenseTag, hasLicense := tags["license"]
	licenceTag, hasLicence := tags["licence"]
	if hasLicense && hasLicence && strings.TrimSpace(licenseTag) != strings.TrimSpace(licenceTag) {
		return "", false, false, "", fmt.Errorf("conflicting license %q and licence %q", licenseTag, licenceTag)
	}
	value := licenseTag
	if !hasLicense {
		value = licenceTag
	}
	explicit = hasLicense || hasLicence
	value = strings.TrimSpace(value)
	if !explicit {
		return ohmDefaultLicense, false, true, "", nil
	}
	if value == "" {
		return "", true, false, "", fmt.Errorf("empty explicit license")
	}
	switch strings.ToUpper(value) {
	case "CC0", "CC0-1.0", "PUBLIC DOMAIN", "PUBLIC-DOMAIN":
		return ohmDefaultLicense, true, true, "", nil
	case "ODBL-1.0", "CC-BY-3.0", "CC-BY-4.0", "CC-BY-SA-3.0", "CC-BY-SA-4.0":
		return value, true, false, "licence is not enabled for serving artifacts", nil
	default:
		return "", true, false, "", fmt.Errorf("unknown explicit license %q", value)
	}
}

func activeInYear(relation *ohmRelation, year int) bool {
	return (relation.startYear == nil || *relation.startYear <= year) &&
		(relation.endYear == nil || *relation.endYear >= year)
}
