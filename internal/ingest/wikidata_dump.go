package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

const earthGlobeURI = "http://www.wikidata.org/entity/Q2"

var wikidataDumpDateProperties = []string{
	"P569", "P570", "P571", "P574", "P575", "P576", "P577", "P580", "P582", "P585",
}

type wikidataDumpTimeFact struct {
	Property      string
	Time          string
	Precision     int
	CalendarModel string
}

type wikidataDumpItemFacts struct {
	QID                 string
	HasEnglishLabel     bool
	InstanceOfQIDs      []string
	SubclassOfQIDs      []string
	TimeClaims          []wikidataDumpTimeFact
	HasCoordinates      bool
	HasEnglishWikipedia bool
	SitelinkCount       int
}

type wikidataDumpEntity struct {
	ID        string                          `json:"id"`
	Type      string                          `json:"type"`
	Labels    map[string]wikidataDumpLabel    `json:"labels"`
	Claims    map[string]json.RawMessage      `json:"claims"`
	Sitelinks map[string]wikidataDumpSitelink `json:"sitelinks"`
}

type wikidataDumpLabel struct {
	Value string `json:"value"`
}

type wikidataDumpSitelink struct {
	Title string `json:"title"`
}

type wikidataDumpStatement struct {
	MainSnak wikidataDumpSnak `json:"mainsnak"`
	Rank     string           `json:"rank"`
}

type wikidataDumpSnak struct {
	SnakType  string                 `json:"snaktype"`
	Property  string                 `json:"property"`
	Datatype  string                 `json:"datatype"`
	DataValue *wikidataDumpDataValue `json:"datavalue"`
}

type wikidataDumpDataValue struct {
	Value json.RawMessage `json:"value"`
	Type  string          `json:"type"`
}

type wikidataEntityIDValue struct {
	EntityType string `json:"entity-type"`
	ID         string `json:"id"`
}

type wikidataTimeValue struct {
	Time          string          `json:"time"`
	Precision     json.RawMessage `json:"precision"`
	CalendarModel string          `json:"calendarmodel"`
}

type wikidataCoordinateValue struct {
	Latitude  json.RawMessage `json:"latitude"`
	Longitude json.RawMessage `json:"longitude"`
	Globe     string          `json:"globe"`
}

func scanWikidataDump(r io.Reader, visit func(wikidataDumpItemFacts) error) error {
	if r == nil {
		return fmt.Errorf("scan wikidata dump: nil reader")
	}
	if visit == nil {
		return fmt.Errorf("scan wikidata dump: nil visitor")
	}

	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("scan wikidata dump: read root: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return fmt.Errorf("scan wikidata dump: root is not an array")
	}

	entityIndex := 0
	for dec.More() {
		var entity wikidataDumpEntity
		if err := dec.Decode(&entity); err != nil {
			return fmt.Errorf("wikidata dump entity %d: decode: %w", entityIndex, err)
		}

		switch entity.Type {
		case "item":
			facts, err := buildWikidataDumpFacts(entity)
			if err != nil {
				return fmt.Errorf("wikidata dump entity %d: %w", entityIndex, err)
			}
			if err := visit(facts); err != nil {
				return fmt.Errorf("wikidata dump entity %d: visit %s: %w", entityIndex, facts.QID, err)
			}
		case "property":
			if !validNumericEntityID(entity.ID, 'P') {
				return fmt.Errorf("wikidata dump entity %d: invalid property id %q", entityIndex, entity.ID)
			}
		case "":
			return fmt.Errorf("wikidata dump entity %d: missing entity type", entityIndex)
		default:
			return fmt.Errorf("wikidata dump entity %d: unknown entity type %q", entityIndex, entity.Type)
		}
		entityIndex++
	}

	tok, err = dec.Token()
	if err != nil {
		return fmt.Errorf("scan wikidata dump: read closing array: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != ']' {
		return fmt.Errorf("scan wikidata dump: missing closing array delimiter")
	}
	if tok, err := dec.Token(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("scan wikidata dump: trailing JSON: %w", err)
		}
		return fmt.Errorf("scan wikidata dump: trailing JSON after array: %v", tok)
	}
	return nil
}

func buildWikidataDumpFacts(entity wikidataDumpEntity) (wikidataDumpItemFacts, error) {
	if !validNumericEntityID(entity.ID, 'Q') {
		return wikidataDumpItemFacts{}, fmt.Errorf("invalid item id %q", entity.ID)
	}

	facts := wikidataDumpItemFacts{
		QID:                 entity.ID,
		HasEnglishLabel:     entity.Labels["en"].Value != "",
		InstanceOfQIDs:      sortedWikidataDumpQIDs(entity, "P31"),
		SubclassOfQIDs:      sortedWikidataDumpQIDs(entity, "P279"),
		SitelinkCount:       len(entity.Sitelinks),
		HasEnglishWikipedia: entity.Sitelinks["enwiki"].Title != "",
	}

	for _, property := range wikidataDumpDateProperties {
		for _, stmt := range wikidataDumpStatements(entity.Claims[property]) {
			if fact, ok := extractTimeFact(property, stmt); ok {
				facts.TimeClaims = append(facts.TimeClaims, fact)
			}
		}
	}

	for _, stmt := range wikidataDumpStatements(entity.Claims["P625"]) {
		if extractCoordinatePresence(stmt) {
			facts.HasCoordinates = true
			break
		}
	}

	return facts, nil
}

func sortedWikidataDumpQIDs(entity wikidataDumpEntity, property string) []string {
	qids := map[string]bool{}
	for _, stmt := range wikidataDumpStatements(entity.Claims[property]) {
		qid, ok := extractEntityQID(property, stmt)
		if ok {
			qids[qid] = true
		}
	}
	out := make([]string, 0, len(qids))
	for qid := range qids {
		out = append(out, qid)
	}
	slices.Sort(out)
	return out
}

func wikidataDumpStatements(raw json.RawMessage) []wikidataDumpStatement {
	if len(raw) == 0 {
		return nil
	}
	var rawStatements []json.RawMessage
	if err := json.Unmarshal(raw, &rawStatements); err != nil {
		return nil
	}
	stmts := make([]wikidataDumpStatement, 0, len(rawStatements))
	for _, rawStatement := range rawStatements {
		var stmt wikidataDumpStatement
		if err := json.Unmarshal(rawStatement, &stmt); err != nil {
			continue
		}
		stmts = append(stmts, stmt)
	}
	return stmts
}

func extractEntityQID(property string, stmt wikidataDumpStatement) (string, bool) {
	if !shouldUseStatement(stmt) || !validMainSnak(stmt.MainSnak, property, "wikibase-item", "wikibase-entityid") {
		return "", false
	}
	var value wikidataEntityIDValue
	if err := json.Unmarshal(stmt.MainSnak.DataValue.Value, &value); err != nil {
		return "", false
	}
	if value.EntityType != "item" || !validNumericEntityID(value.ID, 'Q') {
		return "", false
	}
	return value.ID, true
}

func extractTimeFact(property string, stmt wikidataDumpStatement) (wikidataDumpTimeFact, bool) {
	if !shouldUseStatement(stmt) || !validMainSnak(stmt.MainSnak, property, "time", "time") {
		return wikidataDumpTimeFact{}, false
	}
	var value wikidataTimeValue
	if err := json.Unmarshal(stmt.MainSnak.DataValue.Value, &value); err != nil {
		return wikidataDumpTimeFact{}, false
	}
	precision, ok := parseFlexibleInt(value.Precision)
	if !ok || precision < 0 || precision > 14 || value.Time == "" || value.CalendarModel == "" {
		return wikidataDumpTimeFact{}, false
	}
	return wikidataDumpTimeFact{
		Property:      property,
		Time:          value.Time,
		Precision:     precision,
		CalendarModel: value.CalendarModel,
	}, true
}

func extractCoordinatePresence(stmt wikidataDumpStatement) bool {
	if !shouldUseStatement(stmt) || !validMainSnak(stmt.MainSnak, "P625", "globe-coordinate", "globecoordinate") {
		return false
	}
	var value wikidataCoordinateValue
	if err := json.Unmarshal(stmt.MainSnak.DataValue.Value, &value); err != nil {
		return false
	}
	if value.Globe != earthGlobeURI {
		return false
	}
	lat, ok := parseFlexibleFloat(value.Latitude)
	if !ok || lat < -90 || lat > 90 {
		return false
	}
	lon, ok := parseFlexibleFloat(value.Longitude)
	if !ok || lon < -180 || lon > 180 {
		return false
	}
	return true
}

func validMainSnak(snak wikidataDumpSnak, property, datatype, dataValueType string) bool {
	return snak.SnakType == "value" &&
		snak.Property == property &&
		snak.Datatype == datatype &&
		snak.DataValue != nil &&
		snak.DataValue.Type == dataValueType &&
		len(snak.DataValue.Value) != 0
}

func shouldUseStatement(stmt wikidataDumpStatement) bool {
	return stmt.Rank == "normal" || stmt.Rank == "preferred"
}

func validNumericEntityID(id string, prefix byte) bool {
	if len(id) < 2 || id[0] != prefix {
		return false
	}
	if id[1] < '1' || id[1] > '9' {
		return false
	}
	for i := 2; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func parseFlexibleInt(raw json.RawMessage) (int, bool) {
	s, ok := rawNumberString(raw)
	if !ok || !allDecimalDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseFlexibleFloat(raw json.RawMessage) (float64, bool) {
	s, ok := rawNumberString(raw)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func rawNumberString(raw json.RawMessage) (string, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", false
	}
	if text[0] != '"' {
		return text, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func allDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
