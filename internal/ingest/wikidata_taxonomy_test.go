package ingest

import (
	"strings"
	"testing"

	"wk/internal/model"
)

func TestNewWikidataTaxonomyValidatesTheCuratedTable(t *testing.T) {
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		t.Fatalf("NewWikidataTaxonomy: %v", err)
	}
	if len(taxonomy.accepted) != len(wikidataClasses) {
		t.Fatalf("indexed %d accepted classes, want %d", len(taxonomy.accepted), len(wikidataClasses))
	}
	if len(taxonomy.excluded) != len(wikidataExcludedClasses) {
		t.Fatalf("indexed %d excluded classes, want %d", len(taxonomy.excluded), len(wikidataExcludedClasses))
	}
	for _, class := range wikidataClasses {
		if !model.EntityTypes[class.Type] {
			t.Fatalf("class %s (%s) has non-vocabulary type %q", class.QID, class.Label, class.Type)
		}
		for _, category := range class.Categories {
			if !model.Categories[category] {
				t.Fatalf("class %s (%s) has non-vocabulary category %q", class.QID, class.Label, category)
			}
		}
	}
}

// The census must be able to answer ROAD-2's question list directly, so every
// target class it names has to be reachable in the table.
func TestCuratedTableCoversTheROAD2TargetClasses(t *testing.T) {
	want := map[string]string{
		"battle":            "Q178561",
		"war":               "Q198",
		"political event":   "Q40231",
		"disaster":          "Q8065",
		"scientific event":  "Q101965",
		"person":            "Q5",
		"species":           "Q16521",
		"product":           "Q2424752",
		"scientific output": "Q13442814",
	}
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		t.Fatalf("NewWikidataTaxonomy: %v", err)
	}
	for label, qid := range want {
		if _, ok := taxonomy.accepted[qid]; !ok {
			t.Fatalf("ROAD-2 target %q (%s) is not in the curated table", label, qid)
		}
	}
}

func TestClassifyResolvesInstanceOfBeforeSubclassOf(t *testing.T) {
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		t.Fatalf("NewWikidataTaxonomy: %v", err)
	}

	tests := []struct {
		name        string
		instanceOf  []string
		subclassOf  []string
		wantOutcome ClassificationOutcome
		wantType    string
		wantClass   string
		wantCensus  string
	}{
		{
			name: "battle by P31", instanceOf: []string{"Q178561"},
			wantOutcome: ClassificationTyped, wantType: "event", wantClass: "Q178561", wantCensus: "event",
		},
		{
			name: "most specific curated class wins", instanceOf: []string{"Q180684", "Q178561", "Q198"},
			wantOutcome: ClassificationTyped, wantType: "event", wantClass: "Q178561", wantCensus: "event",
		},
		{
			name: "P279 only when P31 matches nothing", instanceOf: []string{"Q99999999"}, subclassOf: []string{"Q42889"},
			wantOutcome: ClassificationTyped, wantType: "product", wantClass: "Q42889", wantCensus: "product",
		},
		{
			name: "P31 beats P279", instanceOf: []string{"Q5"}, subclassOf: []string{"Q42889"},
			wantOutcome: ClassificationTyped, wantType: "person", wantClass: "Q5", wantCensus: "person",
		},
		{
			name: "exclusion beats acceptance", instanceOf: []string{"Q13442814", "Q13406463"},
			wantOutcome: ClassificationExcluded, wantClass: "Q13406463", wantCensus: CensusTypeExcluded,
		},
		{
			name: "nothing matched", instanceOf: []string{"Q898273"},
			wantOutcome: ClassificationUnclassified, wantCensus: CensusTypeUnclassified,
		},
		{
			name: "no classes at all", wantOutcome: ClassificationUnclassified, wantCensus: CensusTypeUnclassified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taxonomy.Classify(tt.instanceOf, tt.subclassOf)
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if got.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", got.Type, tt.wantType)
			}
			if got.Class.QID != tt.wantClass {
				t.Fatalf("class = %q, want %q", got.Class.QID, tt.wantClass)
			}
			if got.CensusType() != tt.wantCensus {
				t.Fatalf("census type = %q, want %q", got.CensusType(), tt.wantCensus)
			}
			if tt.wantOutcome == ClassificationTyped && len(got.Categories) == 0 {
				t.Fatal("typed classification carries no categories")
			}
		})
	}
}

func TestClassifyIsIndependentOfClaimOrder(t *testing.T) {
	taxonomy, err := NewWikidataTaxonomy()
	if err != nil {
		t.Fatalf("NewWikidataTaxonomy: %v", err)
	}
	forward := taxonomy.Classify([]string{"Q178561", "Q198", "Q350604"}, nil)
	reverse := taxonomy.Classify([]string{"Q350604", "Q198", "Q178561"}, nil)
	if forward.Class.QID != reverse.Class.QID {
		t.Fatalf("order changed the class: %q vs %q", forward.Class.QID, reverse.Class.QID)
	}
}

func TestValidateCuratedClassRejectsBadRows(t *testing.T) {
	tests := []struct {
		name     string
		class    WikidataClass
		accepted bool
		want     string
	}{
		{"bad qid", WikidataClass{QID: "P31", Label: "x", Type: "event", Categories: []string{"war"}}, true, "invalid class id"},
		{"no label", WikidataClass{QID: "Q1", Type: "event", Categories: []string{"war"}}, true, "no label"},
		{"unknown type", WikidataClass{QID: "Q1", Label: "x", Type: "sandwich", Categories: []string{"war"}}, true, "unknown entity type"},
		{"no categories", WikidataClass{QID: "Q1", Label: "x", Type: "event"}, true, "no categories"},
		{"unknown category", WikidataClass{QID: "Q1", Label: "x", Type: "event", Categories: []string{"lunch"}}, true, "unknown category"},
		{"repeated category", WikidataClass{QID: "Q1", Label: "x", Type: "event", Categories: []string{"war", "war"}}, true, "repeats category"},
		{"excluded with type", WikidataClass{QID: "Q1", Label: "x", Type: "event"}, false, "must carry no type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCuratedClass(tt.class, tt.accepted)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateCuratedClass error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}
