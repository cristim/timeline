package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"wk/internal/model"
)

type recordingWikidataDumpSink struct {
	seen     map[string]bool
	marked   []string
	entities []*model.Entity
	rejects  []Reject
}

func (s *recordingWikidataDumpSink) MarkSeenQID(qid string) error {
	s.marked = append(s.marked, qid)
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	if s.seen[qid] {
		return fmt.Errorf("duplicate wikidata id %s", qid)
	}
	s.seen[qid] = true
	return nil
}

func (s *recordingWikidataDumpSink) AcceptEntity(entity *model.Entity) error {
	s.entities = append(s.entities, entity)
	return nil
}

func (s *recordingWikidataDumpSink) Reject(reject Reject) error {
	s.rejects = append(s.rejects, reject)
	return nil
}

func importCensusFixture(t *testing.T, opts WikidataDumpImportOptions) *WikidataDumpImport {
	t.Helper()
	body, err := os.ReadFile("testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	imported, err := ImportWikidataDump(strings.NewReader(string(body)), opts)
	if err != nil {
		t.Fatalf("ImportWikidataDump: %v", err)
	}
	return imported
}

func TestImportWikidataDumpNormalizesClassifiedDatedItems(t *testing.T) {
	t.Parallel()

	imported := importCensusFixture(t, WikidataDumpImportOptions{})
	bySeedID := map[string]*model.Entity{}
	for _, entity := range imported.Entities {
		bySeedID[entity.SeedID] = entity
	}

	waterloo, ok := bySeedID["wd-q2001"]
	if !ok {
		t.Fatalf("Waterloo missing from %d entities", len(imported.Entities))
	}
	if waterloo.Type != "event" || !reflect.DeepEqual(waterloo.Categories, []string{"war"}) {
		t.Fatalf("Waterloo = type %q categories %v, want event/[war]", waterloo.Type, waterloo.Categories)
	}
	if waterloo.Precision != "day" {
		t.Fatalf("Waterloo precision = %q, want day", waterloo.Precision)
	}
	if waterloo.Name != "Battle of Waterloo" || waterloo.Wikidata != "Q2001" {
		t.Fatalf("Waterloo identity = %#v", waterloo)
	}
	if waterloo.Wikipedia != "Battle of Waterloo" {
		t.Fatalf("Waterloo wikipedia = %q", waterloo.Wikipedia)
	}
	if len(waterloo.Point) != 2 || waterloo.Point[0] != 4.41 || waterloo.Point[1] != 50.68 {
		t.Fatalf("Waterloo point = %v, want [lon,lat]", waterloo.Point)
	}
	if waterloo.Status != "documented" {
		t.Fatalf("Waterloo status = %q, want documented", waterloo.Status)
	}
	if waterloo.Slug == "" {
		t.Fatal("Waterloo has no slug")
	}

	// A span keeps both ends; a point-in-time keeps its precision window and
	// never collapses into a fake instant (DM-5).
	war := bySeedID["wd-q2002"]
	if war == nil || !(war.T1 > war.T0) {
		t.Fatalf("World War I span = %#v", war)
	}
	pandemic := bySeedID["wd-q2012"]
	if pandemic == nil || pandemic.T1 != pandemic.T0+86400 {
		t.Fatalf("pandemic window = %#v, want one day", pandemic)
	}

	// The Julian BCE person converts through the calendar, not around it.
	caesar := bySeedID["wd-q2003"]
	if caesar == nil {
		t.Fatal("Caesar missing")
	}
	if got := censusYearForEntity(caesar); got != -99 {
		t.Fatalf("Caesar year = %v, want -99 (100 BCE)", got)
	}
	if caesar.T1 <= caesar.T0 {
		t.Fatalf("Caesar span = [%v,%v]", caesar.T0, caesar.T1)
	}
}

// Every scanned item ends up accepted, filtered by a named reason, or rejected
// by a named reason. Nothing disappears.
func TestImportWikidataDumpAccountsForEveryItem(t *testing.T) {
	t.Parallel()

	imported := importCensusFixture(t, WikidataDumpImportOptions{})
	report := imported.Report
	if report.Items != 13 {
		t.Fatalf("items = %d, want 13", report.Items)
	}
	if report.Accepted+report.Filtered+report.Rejected != report.Items {
		t.Fatalf("%d + %d + %d != %d", report.Accepted, report.Filtered, report.Rejected, report.Items)
	}
	if report.Accepted != len(imported.Entities) {
		t.Fatalf("accepted = %d, entities = %d", report.Accepted, len(imported.Entities))
	}

	filters := map[string]int{}
	for _, row := range report.FilterReasons {
		filters[row.Reason] = row.Count
		if row.Rate <= 0 {
			t.Fatalf("filter reason %q has no rate", row.Reason)
		}
	}
	want := map[string]int{
		string(FilterExcludedClass): 1, // the disambiguation page
		string(FilterUnclassified):  1, // the protein domain
		string(FilterNoUsableTime):  1, // the undated taxon
	}
	if !reflect.DeepEqual(filters, want) {
		t.Fatalf("filter reasons = %#v, want %#v", filters, want)
	}
	if report.Rejected != 0 || len(imported.Rejects) != 0 {
		t.Fatalf("rejects = %d %#v, want none", report.Rejected, imported.Rejects)
	}
	if report.Source != "wikidata" || report.License != "CC0-1.0" {
		t.Fatalf("provenance = %#v", report)
	}
}

func TestImportWikidataDumpIsDeterministic(t *testing.T) {
	t.Parallel()

	first := importCensusFixture(t, WikidataDumpImportOptions{})
	second := importCensusFixture(t, WikidataDumpImportOptions{})
	if !reflect.DeepEqual(first.Report, second.Report) {
		t.Fatal("repeated import produced a different report")
	}
	firstJSON, err := json.Marshal(first.Report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	secondJSON, err := json.Marshal(second.Report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("report JSON differs\n%s\n%s", firstJSON, secondJSON)
	}
	if len(first.Entities) != len(second.Entities) {
		t.Fatalf("entity counts differ: %d vs %d", len(first.Entities), len(second.Entities))
	}
	for i := range first.Entities {
		if first.Entities[i].SeedID != second.Entities[i].SeedID {
			t.Fatalf("entity %d order differs: %q vs %q", i, first.Entities[i].SeedID, second.Entities[i].SeedID)
		}
	}
}

func TestImportWikidataDumpToSinkPreservesCompatibility(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	compatibility, err := ImportWikidataDump(strings.NewReader(string(body)), WikidataDumpImportOptions{})
	if err != nil {
		t.Fatalf("ImportWikidataDump: %v", err)
	}
	sink := new(recordingWikidataDumpSink)
	report, err := ImportWikidataDumpToSink(strings.NewReader(string(body)), WikidataDumpImportOptions{}, sink)
	if err != nil {
		t.Fatalf("ImportWikidataDumpToSink: %v", err)
	}
	if !reflect.DeepEqual(report, compatibility.Report) {
		t.Fatalf("streamed report differs\n got: %#v\nwant: %#v", report, compatibility.Report)
	}
	if len(sink.marked) != report.Items {
		t.Fatalf("marked %d QIDs for %d items", len(sink.marked), report.Items)
	}
	if len(sink.entities) != report.Accepted || len(sink.rejects) != report.Rejected {
		t.Fatalf("sink received %d entities and %d rejects, report has %d and %d",
			len(sink.entities), len(sink.rejects), report.Accepted, report.Rejected)
	}
	for _, entity := range sink.entities {
		if entity.Slug != "" {
			t.Fatalf("sink entity %q already has slug %q", entity.SeedID, entity.Slug)
		}
	}
	if err := model.AssignSlugs(sink.entities); err != nil {
		t.Fatalf("AssignSlugs: %v", err)
	}
	if !reflect.DeepEqual(sink.entities, compatibility.Entities) {
		t.Fatal("streamed entities differ from the compatibility import")
	}
}

func TestImportWikidataDumpToSinkMarksDuplicatesBeforeFiltering(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q9001","type":"item","labels":{"en":{"value":"First"}},"claims":{}},
{"id":"Q9001","type":"item","labels":{"en":{"value":"Second"}},"claims":{}}
]`
	sink := new(recordingWikidataDumpSink)
	_, err := ImportWikidataDumpToSink(strings.NewReader(input), WikidataDumpImportOptions{}, sink)
	if err == nil || !strings.Contains(err.Error(), "duplicate wikidata id Q9001") {
		t.Fatalf("duplicate id error = %v", err)
	}
	if !reflect.DeepEqual(sink.marked, []string{"Q9001", "Q9001"}) {
		t.Fatalf("marked QIDs = %v", sink.marked)
	}
	if len(sink.entities) != 0 || len(sink.rejects) != 0 {
		t.Fatalf("duplicate filtered input delivered %d entities and %d rejects",
			len(sink.entities), len(sink.rejects))
	}
}

func TestImportWikidataDumpToSinkStreamsRejects(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q9301","type":"item","labels":{"en":{"value":"Impossible"}},"claims":{
"P31":[{"mainsnak":{"snaktype":"value","property":"P31","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"item","id":"Q178561"},"type":"wikibase-entityid"}},"rank":"normal"}],
"P585":[{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"-100000000000-00-00T00:00:00Z","timezone":0,"before":0,"after":0,"precision":6,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}]
}}
]`
	allowAll := 1.0
	sink := new(recordingWikidataDumpSink)
	report, err := ImportWikidataDumpToSink(strings.NewReader(input), WikidataDumpImportOptions{MaxRejectRate: &allowAll}, sink)
	if err != nil {
		t.Fatalf("ImportWikidataDumpToSink: %v", err)
	}
	if report.Rejected != 1 || len(sink.rejects) != 1 {
		t.Fatalf("report rejected %d and sink received %d", report.Rejected, len(sink.rejects))
	}
	if sink.rejects[0].Source != RejectSourceWikidataDump || sink.rejects[0].Line != 0 {
		t.Fatalf("reject = %#v", sink.rejects[0])
	}
}

// The compressed fixtures must import identically to the plain one.
func TestImportWikidataDumpReadsCompressedInput(t *testing.T) {
	t.Parallel()

	plain, err := os.Open("testdata/wikidata-dump-mini.json")
	if err != nil {
		t.Fatalf("open plain: %v", err)
	}
	defer plain.Close()
	plainImport, err := ImportWikidataDump(plain, WikidataDumpImportOptions{})
	if err != nil {
		t.Fatalf("plain ImportWikidataDump: %v", err)
	}

	compressed, err := os.Open("testdata/wikidata-dump-mini.json.bz2")
	if err != nil {
		t.Fatalf("open bz2: %v", err)
	}
	defer compressed.Close()
	compressedImport, err := ImportWikidataDump(compressed, WikidataDumpImportOptions{})
	if err != nil {
		t.Fatalf("bz2 ImportWikidataDump: %v", err)
	}
	if compressedImport.Report.Compression != DumpCompressionBzip2 {
		t.Fatalf("compression = %q", compressedImport.Report.Compression)
	}
	if compressedImport.Report.Items != plainImport.Report.Items ||
		compressedImport.Report.Accepted != plainImport.Report.Accepted {
		t.Fatalf("compressed import differs: %#v vs %#v", compressedImport.Report, plainImport.Report)
	}
}

func TestImportWikidataDumpFailsLoudly(t *testing.T) {
	t.Parallel()

	if _, err := ImportWikidataDump(nil, WikidataDumpImportOptions{}); err == nil ||
		!strings.Contains(err.Error(), "nil reader") {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := ImportWikidataDumpToSink(strings.NewReader("[]"), WikidataDumpImportOptions{}, nil); err == nil ||
		!strings.Contains(err.Error(), "nil sink") {
		t.Fatalf("nil sink error = %v", err)
	}

	for _, rate := range []float64{1.5, -1} {
		if _, err := ImportWikidataDump(strings.NewReader("[]"), WikidataDumpImportOptions{MaxRejectRate: &rate}); err == nil ||
			!strings.Contains(err.Error(), "max reject rate") {
			t.Fatalf("max reject rate %v error = %v", rate, err)
		}
	}

	// SRC-3 entity resolution joins on wikidata_id, so a repeat is fatal.
	duplicate := `[
{"id":"Q9001","type":"item","labels":{"en":{"value":"One"}},"claims":{"P31":[{"mainsnak":{"snaktype":"value","property":"P31","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"item","id":"Q178561"},"type":"wikibase-entityid"}},"rank":"normal"}]}},
{"id":"Q9001","type":"item","labels":{"en":{"value":"Two"}},"claims":{}}
]`
	if _, err := ImportWikidataDump(strings.NewReader(duplicate), WikidataDumpImportOptions{}); err == nil ||
		!strings.Contains(err.Error(), "duplicate wikidata id Q9001") {
		t.Fatalf("duplicate id error = %v", err)
	}
}

// A normalizer that starts producing invalid rows has to stop the run, not
// quietly ship a smaller dataset (ROAD-3 step 2: reject rates within gates).
func TestImportWikidataDumpEnforcesTheRejectGate(t *testing.T) {
	t.Parallel()

	// A day-precision claim older than the supported calendar range converts,
	// but lands beyond the windowed-time guard for its precision.
	input := `[
{"id":"Q9101","type":"item","labels":{"en":{"value":"Impossible"}},"claims":{
"P31":[{"mainsnak":{"snaktype":"value","property":"P31","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"item","id":"Q178561"},"type":"wikibase-entityid"}},"rank":"normal"}],
"P585":[{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"-100000000000-00-00T00:00:00Z","timezone":0,"before":0,"after":0,"precision":6,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}]
}}
]`
	_, err := ImportWikidataDump(strings.NewReader(input), WikidataDumpImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "reject rate") {
		t.Fatalf("ImportWikidataDump error = %v, want the reject gate", err)
	}

	// Raising the gate lets the same run through, with the reject recorded.
	allowAll := 1.0
	imported, err := ImportWikidataDump(strings.NewReader(input), WikidataDumpImportOptions{MaxRejectRate: &allowAll})
	if err != nil {
		t.Fatalf("ImportWikidataDump with a raised gate: %v", err)
	}
	if imported.Report.Rejected != 1 || len(imported.Rejects) != 1 {
		t.Fatalf("rejects = %d %#v, want 1", imported.Report.Rejected, imported.Rejects)
	}
	if imported.Rejects[0].Source != RejectSourceWikidataDump {
		t.Fatalf("reject source = %q", imported.Rejects[0].Source)
	}
	if imported.Report.RejectRate != 1 {
		t.Fatalf("reject rate = %v, want 1", imported.Report.RejectRate)
	}
}

// An explicit zero gate means "no reject is acceptable", which a sentinel zero
// standing for "use the default" could not express.
func TestImportWikidataDumpHonoursAnExplicitZeroGate(t *testing.T) {
	t.Parallel()

	input := `[
{"id":"Q9201","type":"item","labels":{"en":{"value":"Impossible"}},"claims":{
"P31":[{"mainsnak":{"snaktype":"value","property":"P31","datatype":"wikibase-item","datavalue":{"value":{"entity-type":"item","id":"Q178561"},"type":"wikibase-entityid"}},"rank":"normal"}],
"P585":[{"mainsnak":{"snaktype":"value","property":"P585","datatype":"time","datavalue":{"value":{"time":"-100000000000-00-00T00:00:00Z","timezone":0,"before":0,"after":0,"precision":6,"calendarmodel":"http://www.wikidata.org/entity/Q1985727"},"type":"time"}},"rank":"normal"}]
}}
]`
	zero := 0.0
	if _, err := ImportWikidataDump(strings.NewReader(input), WikidataDumpImportOptions{MaxRejectRate: &zero}); err == nil ||
		!strings.Contains(err.Error(), "reject rate") {
		t.Fatalf("ImportWikidataDump error = %v, want the zero gate to bite", err)
	}
	// And a clean dump still passes the same zero gate.
	body, err := os.ReadFile("testdata/wikidata-dump-census.json")
	if err != nil {
		t.Fatalf("read census fixture: %v", err)
	}
	if _, err := ImportWikidataDump(strings.NewReader(string(body)), WikidataDumpImportOptions{MaxRejectRate: &zero}); err != nil {
		t.Fatalf("a dump with no rejects failed a zero gate: %v", err)
	}
}
