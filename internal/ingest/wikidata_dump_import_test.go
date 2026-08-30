package ingest

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"wk/internal/model"
)

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

func TestImportWikidataDumpAppliesTheImportanceFloor(t *testing.T) {
	t.Parallel()

	all := importCensusFixture(t, WikidataDumpImportOptions{})
	floored := importCensusFixture(t, WikidataDumpImportOptions{ImportanceFloor: 0.32})
	if floored.Report.Accepted >= all.Report.Accepted {
		t.Fatalf("floor kept %d of %d entities, want fewer", floored.Report.Accepted, all.Report.Accepted)
	}
	for _, entity := range floored.Entities {
		if entity.Importance < 0.32 {
			t.Fatalf("entity %q importance %v is below the floor", entity.SeedID, entity.Importance)
		}
	}
	held := 0
	for _, row := range floored.Report.FilterReasons {
		if strings.HasPrefix(row.Reason, "below importance floor") {
			held = row.Count
		}
	}
	if held != all.Report.Accepted-floored.Report.Accepted {
		t.Fatalf("held %d, want %d", held, all.Report.Accepted-floored.Report.Accepted)
	}
}

func TestImportWikidataDumpFailsLoudly(t *testing.T) {
	t.Parallel()

	if _, err := ImportWikidataDump(nil, WikidataDumpImportOptions{}); err == nil ||
		!strings.Contains(err.Error(), "nil reader") {
		t.Fatalf("nil reader error = %v", err)
	}

	for _, opts := range []WikidataDumpImportOptions{{MaxRejectRate: 1.5}, {MaxRejectRate: -1}} {
		if _, err := ImportWikidataDump(strings.NewReader("[]"), opts); err == nil ||
			!strings.Contains(err.Error(), "max reject rate") {
			t.Fatalf("max reject rate %v error = %v", opts.MaxRejectRate, err)
		}
	}
	if _, err := ImportWikidataDump(strings.NewReader("[]"), WikidataDumpImportOptions{ImportanceFloor: 2}); err == nil ||
		!strings.Contains(err.Error(), "importance floor") {
		t.Fatalf("importance floor error = %v", err)
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
	imported, err := ImportWikidataDump(strings.NewReader(input), WikidataDumpImportOptions{MaxRejectRate: 1})
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
