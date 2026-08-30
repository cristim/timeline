package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"wk/internal/model"
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
	Timezone      int
	Before        int
	After         int
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
	Timezone      json.RawMessage `json:"timezone"`
	Before        json.RawMessage `json:"before"`
	After         json.RawMessage `json:"after"`
}

type wikidataCoordinateValue struct {
	Latitude  json.RawMessage `json:"latitude"`
	Longitude json.RawMessage `json:"longitude"`
	Globe     string          `json:"globe"`
}

// Every path below that declines a claim increments a named counter instead of
// dropping it silently (SRC-3 "parsed / accepted / rejected per reason"): a
// dump-format drift that discards millions of claims has to be visible in the
// report rather than showing up as a suspiciously small dataset.
type WikidataDumpSkipReason string

const (
	SkipClaimGroupNotArray    WikidataDumpSkipReason = "claim_group_not_an_array"
	SkipStatementNotObject    WikidataDumpSkipReason = "statement_not_an_object"
	SkipStatementRank         WikidataDumpSkipReason = "statement_rank_not_used"
	SkipSnakShape             WikidataDumpSkipReason = "snak_shape_unusable"
	SkipEntityValueInvalid    WikidataDumpSkipReason = "entity_id_value_invalid"
	SkipTimeValueInvalid      WikidataDumpSkipReason = "time_value_invalid"
	SkipCoordinateValue       WikidataDumpSkipReason = "coordinate_value_invalid"
	SkipCoordinateNotOnEarth  WikidataDumpSkipReason = "coordinate_not_on_earth"
	SkipCoordinateOutOfRange  WikidataDumpSkipReason = "coordinate_out_of_range"
	SkipLabelMissingForItem   WikidataDumpSkipReason = "item_without_english_label"
	SkipUnusableTimeForItem   WikidataDumpSkipReason = "item_without_usable_time"
	SkipUnconvertibleTimeFact WikidataDumpSkipReason = "time_value_unconvertible"
)

// defaultMaxDumpRecordBytes bounds one JSON record. Wikidata's largest real
// items are a few MB; a record beyond this is corruption or an attack, and
// decoding it unbounded would buffer the whole thing in memory.
const defaultMaxDumpRecordBytes = 32 << 20

// dumpReadAheadSlack is how far past the record limit the decoder is allowed to
// buffer. json.Decoder grows its buffer geometrically and reads ahead into the
// spare capacity, so a guard set exactly at the record limit would fire on a
// stream of small records. Peak memory stays bounded at roughly
// 2*limit + slack, which is the point of the guard.
const dumpReadAheadSlack = 64 << 10

var (
	// errDumpBufferOverrun stops a single unterminated record from being read
	// into memory without end.
	errDumpBufferOverrun = errors.New("wikidata dump reader buffered past the record size limit")
	// errDumpRecordTooLarge names the oversized record once it is decodable.
	errDumpRecordTooLarge = errors.New("wikidata dump record exceeds the per-record size limit")
)

type dumpCounters struct {
	skips map[WikidataDumpSkipReason]int
}

func newDumpCounters() *dumpCounters {
	return &dumpCounters{skips: map[WikidataDumpSkipReason]int{}}
}

func (c *dumpCounters) skip(reason WikidataDumpSkipReason) {
	c.skips[reason]++
}

type wikidataDumpScanStats struct {
	Items      int
	Properties int
	Skips      map[WikidataDumpSkipReason]int
}

// recordBoundedReader trips as soon as the decoder has pulled more bytes than
// the limit without committing a record, which keeps peak memory bounded
// instead of detecting the problem after the allocation.
type recordBoundedReader struct {
	src       io.Reader
	limit     int64
	read      int64
	committed func() int64
	err       error
}

// Read reports the overrun with no bytes attached and stays failed. Handing
// back data alongside the error would let json.Decoder finish the oversized
// value and discard the error: it defers read errors until a scan pass fails
// to complete a value.
func (r *recordBoundedReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.src.Read(p)
	r.read += int64(n)
	if r.read-r.committed() > r.limit {
		r.err = fmt.Errorf("%w of %d bytes, near byte offset %d", errDumpBufferOverrun, r.limit, r.committed())
		return 0, r.err
	}
	return n, err
}

func scanWikidataDump(r io.Reader, visit func(wikidataDumpItemFacts) error) (wikidataDumpScanStats, error) {
	return scanWikidataDumpLimited(r, defaultMaxDumpRecordBytes, visit)
}

func scanWikidataDumpLimited(r io.Reader, maxRecordBytes int64, visit func(wikidataDumpItemFacts) error) (wikidataDumpScanStats, error) {
	counters := newDumpCounters()
	stats := wikidataDumpScanStats{Skips: counters.skips}
	if r == nil {
		return stats, fmt.Errorf("scan wikidata dump: nil reader")
	}
	if visit == nil {
		return stats, fmt.Errorf("scan wikidata dump: nil visitor")
	}
	if maxRecordBytes <= 0 {
		return stats, fmt.Errorf("scan wikidata dump: record size limit must be positive, got %d", maxRecordBytes)
	}

	var dec *json.Decoder
	bounded := &recordBoundedReader{src: r, limit: 2*maxRecordBytes + dumpReadAheadSlack, committed: func() int64 {
		if dec == nil {
			return 0
		}
		return dec.InputOffset()
	}}
	dec = json.NewDecoder(bounded)

	tok, err := dec.Token()
	if err != nil {
		return stats, fmt.Errorf("scan wikidata dump: read root: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return stats, fmt.Errorf("scan wikidata dump: root is not an array")
	}

	entityIndex := 0
	for dec.More() {
		recordStart := dec.InputOffset()
		var entity wikidataDumpEntity
		if err := dec.Decode(&entity); err != nil {
			return stats, fmt.Errorf("wikidata dump entity %d: decode: %w", entityIndex, err)
		}
		if size := dec.InputOffset() - recordStart; size > maxRecordBytes {
			return stats, fmt.Errorf("wikidata dump entity %d (%s): %w of %d bytes: %d bytes",
				entityIndex, entity.ID, errDumpRecordTooLarge, maxRecordBytes, size)
		}

		switch entity.Type {
		case "item":
			facts, err := buildWikidataDumpFacts(counters, entity)
			if err != nil {
				return stats, fmt.Errorf("wikidata dump entity %d: %w", entityIndex, err)
			}
			stats.Items++
			if err := visit(facts); err != nil {
				return stats, fmt.Errorf("wikidata dump entity %d: visit %s: %w", entityIndex, facts.QID, err)
			}
		case "property":
			if !validNumericEntityID(entity.ID, 'P') {
				return stats, fmt.Errorf("wikidata dump entity %d: invalid property id %q", entityIndex, entity.ID)
			}
			stats.Properties++
		case "":
			return stats, fmt.Errorf("wikidata dump entity %d: missing entity type", entityIndex)
		default:
			return stats, fmt.Errorf("wikidata dump entity %d: unknown entity type %q", entityIndex, entity.Type)
		}
		entityIndex++
	}

	tok, err = dec.Token()
	if err != nil {
		return stats, fmt.Errorf("scan wikidata dump: read closing array: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != ']' {
		return stats, fmt.Errorf("scan wikidata dump: missing closing array delimiter")
	}
	if tok, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return stats, fmt.Errorf("scan wikidata dump: trailing JSON: %w", err)
		}
		return stats, fmt.Errorf("scan wikidata dump: trailing JSON after array: %v", tok)
	}
	return stats, nil
}

func buildWikidataDumpFacts(counters *dumpCounters, entity wikidataDumpEntity) (wikidataDumpItemFacts, error) {
	if !validNumericEntityID(entity.ID, 'Q') {
		return wikidataDumpItemFacts{}, fmt.Errorf("invalid item id %q", entity.ID)
	}

	facts := wikidataDumpItemFacts{
		QID:                 entity.ID,
		HasEnglishLabel:     entity.Labels["en"].Value != "",
		InstanceOfQIDs:      sortedWikidataDumpQIDs(counters, entity, "P31"),
		SubclassOfQIDs:      sortedWikidataDumpQIDs(counters, entity, "P279"),
		SitelinkCount:       len(entity.Sitelinks),
		HasEnglishWikipedia: entity.Sitelinks["enwiki"].Title != "",
	}

	for _, property := range wikidataDumpDateProperties {
		for _, stmt := range wikidataDumpStatements(counters, entity.Claims[property]) {
			if fact, ok := extractTimeFact(counters, property, stmt); ok {
				facts.TimeClaims = append(facts.TimeClaims, fact)
			}
		}
	}

	for _, stmt := range wikidataDumpStatements(counters, entity.Claims["P625"]) {
		if extractCoordinatePresence(counters, stmt) {
			facts.HasCoordinates = true
			break
		}
	}

	return facts, nil
}

func sortedWikidataDumpQIDs(counters *dumpCounters, entity wikidataDumpEntity, property string) []string {
	qids := map[string]bool{}
	for _, stmt := range wikidataDumpStatements(counters, entity.Claims[property]) {
		qid, ok := extractEntityQID(counters, property, stmt)
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

func wikidataDumpStatements(counters *dumpCounters, raw json.RawMessage) []wikidataDumpStatement {
	if len(raw) == 0 {
		return nil
	}
	var rawStatements []json.RawMessage
	if err := json.Unmarshal(raw, &rawStatements); err != nil {
		counters.skip(SkipClaimGroupNotArray)
		return nil
	}
	stmts := make([]wikidataDumpStatement, 0, len(rawStatements))
	for _, rawStatement := range rawStatements {
		var stmt wikidataDumpStatement
		if err := json.Unmarshal(rawStatement, &stmt); err != nil {
			counters.skip(SkipStatementNotObject)
			continue
		}
		stmts = append(stmts, stmt)
	}
	return stmts
}

func extractEntityQID(counters *dumpCounters, property string, stmt wikidataDumpStatement) (string, bool) {
	if !usableStatement(counters, stmt, stmt.MainSnak, property, "wikibase-item", "wikibase-entityid") {
		return "", false
	}
	var value wikidataEntityIDValue
	if err := json.Unmarshal(stmt.MainSnak.DataValue.Value, &value); err != nil {
		counters.skip(SkipEntityValueInvalid)
		return "", false
	}
	if value.EntityType != "item" || !validNumericEntityID(value.ID, 'Q') {
		counters.skip(SkipEntityValueInvalid)
		return "", false
	}
	return value.ID, true
}

func extractTimeFact(counters *dumpCounters, property string, stmt wikidataDumpStatement) (wikidataDumpTimeFact, bool) {
	if !usableStatement(counters, stmt, stmt.MainSnak, property, "time", "time") {
		return wikidataDumpTimeFact{}, false
	}
	var value wikidataTimeValue
	if err := json.Unmarshal(stmt.MainSnak.DataValue.Value, &value); err != nil {
		counters.skip(SkipTimeValueInvalid)
		return wikidataDumpTimeFact{}, false
	}
	precision, ok := parseFlexibleInt(value.Precision)
	if !ok || precision < 0 || precision > 14 || value.Time == "" || value.CalendarModel == "" {
		counters.skip(SkipTimeValueInvalid)
		return wikidataDumpTimeFact{}, false
	}
	// timezone/before/after are optional in practice but signed when present;
	// an unparseable one would silently shift or narrow the window.
	timezone, ok := parseOptionalSignedInt(value.Timezone)
	if !ok {
		counters.skip(SkipTimeValueInvalid)
		return wikidataDumpTimeFact{}, false
	}
	before, ok := parseOptionalSignedInt(value.Before)
	if !ok {
		counters.skip(SkipTimeValueInvalid)
		return wikidataDumpTimeFact{}, false
	}
	after, ok := parseOptionalSignedInt(value.After)
	if !ok {
		counters.skip(SkipTimeValueInvalid)
		return wikidataDumpTimeFact{}, false
	}
	return wikidataDumpTimeFact{
		Property:      property,
		Time:          value.Time,
		Precision:     precision,
		CalendarModel: value.CalendarModel,
		Timezone:      timezone,
		Before:        before,
		After:         after,
	}, true
}

func extractCoordinate(counters *dumpCounters, stmt wikidataDumpStatement) ([]float64, bool) {
	if !usableStatement(counters, stmt, stmt.MainSnak, "P625", "globe-coordinate", "globecoordinate") {
		return nil, false
	}
	var value wikidataCoordinateValue
	if err := json.Unmarshal(stmt.MainSnak.DataValue.Value, &value); err != nil {
		counters.skip(SkipCoordinateValue)
		return nil, false
	}
	if value.Globe != earthGlobeURI {
		// Coordinates on Mars or the Moon are valid Wikidata, just not ours.
		counters.skip(SkipCoordinateNotOnEarth)
		return nil, false
	}
	lat, latOK := parseFlexibleFloat(value.Latitude)
	lon, lonOK := parseFlexibleFloat(value.Longitude)
	if !latOK || !lonOK {
		counters.skip(SkipCoordinateValue)
		return nil, false
	}
	if !model.ValidLonLat(lon, lat) {
		counters.skip(SkipCoordinateOutOfRange)
		return nil, false
	}
	return []float64{lon, lat}, true
}

func extractCoordinatePresence(counters *dumpCounters, stmt wikidataDumpStatement) bool {
	_, ok := extractCoordinate(counters, stmt)
	return ok
}

// usableStatement folds the rank and snak-shape gates together so each rejection
// lands on its own counter.
func usableStatement(counters *dumpCounters, stmt wikidataDumpStatement, snak wikidataDumpSnak, property, datatype, dataValueType string) bool {
	if stmt.Rank != "normal" && stmt.Rank != "preferred" {
		counters.skip(SkipStatementRank)
		return false
	}
	if snak.SnakType != "value" || snak.Property != property || snak.Datatype != datatype ||
		snak.DataValue == nil || snak.DataValue.Type != dataValueType || len(snak.DataValue.Value) == 0 {
		counters.skip(SkipSnakShape)
		return false
	}
	return true
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

// parseFlexibleInt accepts every JSON spelling of a whole number a dump can
// carry: bare, quoted, and the "11.0" form a JSON round-trip through a float
// type produces. Rejecting that form would silently drop every time claim from
// a re-serialized dump.
func parseFlexibleInt(raw json.RawMessage) (int, bool) {
	f, ok := parseFlexibleFloat(raw)
	if !ok || f != math.Trunc(f) || math.Abs(f) > math.MaxInt32 {
		return 0, false
	}
	return int(f), true
}

// parseOptionalSignedInt treats an absent field as zero and anything present
// but unparseable as a hard no.
func parseOptionalSignedInt(raw json.RawMessage) (int, bool) {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return 0, true
	}
	return parseFlexibleInt(raw)
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
