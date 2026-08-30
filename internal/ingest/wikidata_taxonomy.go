package ingest

import (
	"fmt"

	"wk/internal/model"
)

// Wikidata class -> project taxonomy, the SRC-3 "classify" stage. The table is
// curated rather than derived: a wrong row silently mislabels every item of
// that class, so each QID below was read back from the live Wikidata label
// before it was written here, and the Label field keeps that check auditable.
//
// Rows are in specificity order. An item instantiating several curated classes
// takes the first one listed, which is why "battle" precedes "armed conflict"
// and "car model" precedes "product".
//
// The table maps directly; it does not walk the P279 subclass tree upward from
// an item's P31 classes, so an item whose only class is an unlisted descendant
// of a listed one lands in the unclassified bucket instead of being folded into
// its ancestor. That bucket is reported per class (ROAD-2), which is how the
// table grows from evidence rather than from guesswork.

type ClassificationOutcome string

const (
	// ClassificationTyped: the item instantiates a curated class.
	ClassificationTyped ClassificationOutcome = "typed"
	// ClassificationExcluded: the item instantiates a class we know is not a
	// timeline entity (Wikimedia housekeeping pages, name records).
	ClassificationExcluded ClassificationOutcome = "excluded"
	// ClassificationUnclassified: no curated class matched. Never silently
	// dropped - counted, and reported per unmatched class.
	ClassificationUnclassified ClassificationOutcome = "unclassified"
)

// Pseudo-type names the census reports alongside the real model.EntityTypes.
// Neither is a member of that vocabulary, so they cannot collide with one.
const (
	CensusTypeExcluded     = "excluded"
	CensusTypeUnclassified = "unclassified"
)

type WikidataClass struct {
	QID        string
	Label      string // live English label at the time of curation
	Type       string // model.EntityTypes member; empty for excluded classes
	Categories []string
}

type WikidataClassification struct {
	Outcome    ClassificationOutcome
	Class      WikidataClass
	Categories []string
	Type       string
}

// wikidataClasses is the accept list, most specific first.
var wikidataClasses = []WikidataClass{
	// Armed conflict.
	{QID: "Q178561", Label: "battle", Type: "event", Categories: []string{"war"}},
	{QID: "Q188055", Label: "siege", Type: "event", Categories: []string{"war"}},
	{QID: "Q3199915", Label: "massacre", Type: "event", Categories: []string{"war"}},
	{QID: "Q645883", Label: "military operation", Type: "event", Categories: []string{"war"}},
	{QID: "Q198", Label: "war", Type: "event", Categories: []string{"war"}},
	{QID: "Q350604", Label: "armed conflict", Type: "event", Categories: []string{"war"}},
	{QID: "Q180684", Label: "conflict", Type: "event", Categories: []string{"war"}},

	// Political events and instruments.
	{QID: "Q45382", Label: "coup d'etat", Type: "event", Categories: []string{"politics"}},
	{QID: "Q10931", Label: "revolution", Type: "event", Categories: []string{"politics"}},
	{QID: "Q124734", Label: "rebellion", Type: "event", Categories: []string{"politics"}},
	{QID: "Q3882219", Label: "assassination", Type: "event", Categories: []string{"politics"}},
	{QID: "Q124757", Label: "riot", Type: "event", Categories: []string{"politics"}},
	{QID: "Q273120", Label: "protest", Type: "event", Categories: []string{"politics"}},
	{QID: "Q40231", Label: "public election", Type: "event", Categories: []string{"politics"}},
	{QID: "Q625298", Label: "peace treaty", Type: "event", Categories: []string{"politics"}},
	{QID: "Q131569", Label: "treaty", Type: "event", Categories: []string{"politics"}},
	{QID: "Q2334719", Label: "legal case", Type: "event", Categories: []string{"politics"}},

	// Disasters. Q3839081 covers man-made ones too, so it stays a plain event.
	{QID: "Q7944", Label: "earthquake", Type: "natural_event", Categories: []string{"disaster", "earth"}},
	{QID: "Q7692360", Label: "volcanic eruption", Type: "natural_event", Categories: []string{"disaster", "earth"}},
	{QID: "Q8068", Label: "flood", Type: "natural_event", Categories: []string{"disaster", "earth"}},
	{QID: "Q12184", Label: "pandemic", Type: "natural_event", Categories: []string{"disaster", "life"}},
	{QID: "Q44512", Label: "epidemic", Type: "natural_event", Categories: []string{"disaster", "life"}},
	{QID: "Q8065", Label: "natural disaster", Type: "natural_event", Categories: []string{"disaster"}},
	{QID: "Q3839081", Label: "disaster", Type: "event", Categories: []string{"disaster"}},

	// Scientific events and outputs.
	{QID: "Q12772819", Label: "discovery", Type: "discovery", Categories: []string{"science"}},
	{QID: "Q101965", Label: "experiment", Type: "event", Categories: []string{"science"}},
	{QID: "Q1298668", Label: "science project", Type: "event", Categories: []string{"science"}},
	{QID: "Q752783", Label: "human spaceflight", Type: "event", Categories: []string{"exploration", "science"}},
	{QID: "Q2133344", Label: "space mission", Type: "event", Categories: []string{"exploration", "science"}},
	{QID: "Q13442814", Label: "scholarly article", Type: "paper", Categories: []string{"science"}},
	{QID: "Q17737", Label: "theory", Type: "theory", Categories: []string{"science"}},
	{QID: "Q11862829", Label: "academic discipline", Type: "idea", Categories: []string{"science"}},
	{QID: "Q11173", Label: "chemical compound", Type: "chemical", Categories: []string{"science"}},

	// People and life.
	{QID: "Q5", Label: "human", Type: "person", Categories: []string{"culture"}},
	{QID: "Q23038290", Label: "fossil taxon", Type: "species", Categories: []string{"life"}},
	{QID: "Q16521", Label: "taxon", Type: "species", Categories: []string{"life"}},
	{QID: "Q12136", Label: "disease", Type: "disease", Categories: []string{"life"}},

	// Products and technology.
	{QID: "Q3231690", Label: "car model", Type: "product", Categories: []string{"technology", "economy"}},
	{QID: "Q40218", Label: "spacecraft", Type: "product", Categories: []string{"technology", "exploration"}},
	{QID: "Q11446", Label: "ship", Type: "product", Categories: []string{"technology"}},
	{QID: "Q42889", Label: "vehicle", Type: "product", Categories: []string{"technology"}},
	{QID: "Q7397", Label: "software", Type: "product", Categories: []string{"technology"}},
	{QID: "Q11019", Label: "machine", Type: "product", Categories: []string{"technology"}},
	{QID: "Q1183543", Label: "device", Type: "product", Categories: []string{"technology"}},
	{QID: "Q39546", Label: "physical tool", Type: "product", Categories: []string{"technology"}},
	{QID: "Q3099911", Label: "scientific instrument", Type: "object", Categories: []string{"science", "technology"}},
	{QID: "Q253623", Label: "patent", Type: "patent", Categories: []string{"technology"}},
	{QID: "Q11016", Label: "technology", Type: "technology", Categories: []string{"technology"}},
	{QID: "Q2424752", Label: "product", Type: "product", Categories: []string{"economy"}},

	// Culture.
	{QID: "Q7889", Label: "video game", Type: "game", Categories: []string{"culture"}},
	{QID: "Q11424", Label: "film", Type: "film", Categories: []string{"culture"}},
	{QID: "Q5398426", Label: "television series", Type: "film", Categories: []string{"culture"}},
	{QID: "Q482994", Label: "album", Type: "artwork", Categories: []string{"culture"}},
	{QID: "Q207628", Label: "composed musical work", Type: "artwork", Categories: []string{"culture"}},
	{QID: "Q838948", Label: "work of art", Type: "artwork", Categories: []string{"culture"}},
	{QID: "Q571", Label: "book", Type: "book", Categories: []string{"culture"}},
	{QID: "Q17537576", Label: "creative work", Type: "artwork", Categories: []string{"culture"}},
	{QID: "Q9174", Label: "religion", Type: "religion", Categories: []string{"religion"}},
	{QID: "Q34770", Label: "language", Type: "language", Categories: []string{"culture"}},
	{QID: "Q41710", Label: "ethnic group", Type: "civilization", Categories: []string{"culture"}},

	// States, organizations, places.
	{QID: "Q3624078", Label: "sovereign state", Type: "state", Categories: []string{"politics"}},
	{QID: "Q3024240", Label: "historical country", Type: "state", Categories: []string{"politics"}},
	{QID: "Q48349", Label: "empire", Type: "state", Categories: []string{"politics"}},
	{QID: "Q6256", Label: "country", Type: "state", Categories: []string{"politics"}},
	{QID: "Q7275", Label: "state", Type: "state", Categories: []string{"politics"}},
	{QID: "Q164950", Label: "dynasty", Type: "organization", Categories: []string{"politics"}},
	{QID: "Q4830453", Label: "business", Type: "organization", Categories: []string{"economy"}},
	{QID: "Q6881511", Label: "enterprise", Type: "organization", Categories: []string{"economy"}},
	{QID: "Q43229", Label: "organization", Type: "organization", Categories: []string{"economy"}},
	{QID: "Q515", Label: "city", Type: "place", Categories: []string{"culture"}},
	{QID: "Q486972", Label: "human settlement", Type: "place", Categories: []string{"culture"}},
	{QID: "Q839954", Label: "archaeological site", Type: "place", Categories: []string{"culture"}},
	{QID: "Q41176", Label: "building", Type: "structure", Categories: []string{"culture"}},
	{QID: "Q811979", Label: "architectural structure", Type: "structure", Categories: []string{"culture"}},
	{QID: "Q7748", Label: "law", Type: "law", Categories: []string{"politics"}},
	{QID: "Q820655", Label: "statute", Type: "law", Categories: []string{"politics"}},

	// Astronomy.
	{QID: "Q634", Label: "planet", Type: "astronomical_object", Categories: []string{"universe"}},
	{QID: "Q523", Label: "star", Type: "astronomical_object", Categories: []string{"universe"}},
	{QID: "Q6999", Label: "astronomical object", Type: "astronomical_object", Categories: []string{"universe"}},
}

// wikidataExcludedClasses are classes that exist in the dump but are never
// timeline entities. They are counted apart from the unclassified bucket so
// that bucket keeps meaning "a class the table should probably grow to cover".
var wikidataExcludedClasses = []WikidataClass{
	{QID: "Q4167410", Label: "Wikimedia disambiguation page"},
	{QID: "Q13406463", Label: "Wikimedia list article"},
	{QID: "Q4167836", Label: "Wikimedia category"},
	{QID: "Q17524420", Label: "aspect of history"},
	{QID: "Q205892", Label: "calendar date"},
	{QID: "Q101352", Label: "family name"},
}

type WikidataTaxonomy struct {
	rank     map[string]int
	accepted map[string]WikidataClass
	excluded map[string]WikidataClass
}

// NewWikidataTaxonomy validates the curated table against the closed model
// vocabularies and indexes it. A table typo is a startup error, not a run that
// writes 100M mislabelled rows.
func NewWikidataTaxonomy() (*WikidataTaxonomy, error) {
	t := &WikidataTaxonomy{
		rank:     make(map[string]int, len(wikidataClasses)),
		accepted: make(map[string]WikidataClass, len(wikidataClasses)),
		excluded: make(map[string]WikidataClass, len(wikidataExcludedClasses)),
	}
	for i, class := range wikidataClasses {
		if err := validateCuratedClass(class, true); err != nil {
			return nil, fmt.Errorf("wikidata taxonomy: %w", err)
		}
		if _, exists := t.accepted[class.QID]; exists {
			return nil, fmt.Errorf("wikidata taxonomy: duplicate class %s", class.QID)
		}
		t.accepted[class.QID] = class
		t.rank[class.QID] = i
	}
	for _, class := range wikidataExcludedClasses {
		if err := validateCuratedClass(class, false); err != nil {
			return nil, fmt.Errorf("wikidata taxonomy: %w", err)
		}
		if _, exists := t.accepted[class.QID]; exists {
			return nil, fmt.Errorf("wikidata taxonomy: class %s is both accepted and excluded", class.QID)
		}
		if _, exists := t.excluded[class.QID]; exists {
			return nil, fmt.Errorf("wikidata taxonomy: duplicate excluded class %s", class.QID)
		}
		t.excluded[class.QID] = class
	}
	return t, nil
}

func validateCuratedClass(class WikidataClass, accepted bool) error {
	if !validNumericEntityID(class.QID, 'Q') {
		return fmt.Errorf("invalid class id %q", class.QID)
	}
	if class.Label == "" {
		return fmt.Errorf("class %s has no label", class.QID)
	}
	if !accepted {
		if class.Type != "" || len(class.Categories) != 0 {
			return fmt.Errorf("excluded class %s must carry no type or categories", class.QID)
		}
		return nil
	}
	if !model.EntityTypes[class.Type] {
		return fmt.Errorf("class %s has unknown entity type %q", class.QID, class.Type)
	}
	if len(class.Categories) == 0 {
		return fmt.Errorf("class %s has no categories", class.QID)
	}
	seen := map[string]bool{}
	for _, category := range class.Categories {
		if !model.Categories[category] {
			return fmt.Errorf("class %s has unknown category %q", class.QID, category)
		}
		if seen[category] {
			return fmt.Errorf("class %s repeats category %q", class.QID, category)
		}
		seen[category] = true
	}
	return nil
}

// Classify resolves an item's P31 values, then its P279 values, against the
// curated table. Exclusion wins over acceptance: an item that is both a
// scholarly article and a Wikimedia list article is housekeeping.
func (t *WikidataTaxonomy) Classify(instanceOf, subclassOf []string) WikidataClassification {
	for _, qids := range [][]string{instanceOf, subclassOf} {
		for _, qid := range qids {
			if class, ok := t.excluded[qid]; ok {
				return WikidataClassification{Outcome: ClassificationExcluded, Class: class}
			}
		}
	}
	// P31 first, then P279: an item that instantiates a curated class beats one
	// that merely subclasses a different curated class.
	for _, qids := range [][]string{instanceOf, subclassOf} {
		best := -1
		for _, qid := range qids {
			rank, ok := t.rank[qid]
			if ok && (best < 0 || rank < best) {
				best = rank
			}
		}
		if best >= 0 {
			class := wikidataClasses[best]
			return WikidataClassification{
				Outcome:    ClassificationTyped,
				Class:      class,
				Type:       class.Type,
				Categories: class.Categories,
			}
		}
	}
	return WikidataClassification{Outcome: ClassificationUnclassified}
}

// CensusType is the row key the census reports this outcome under.
func (c WikidataClassification) CensusType() string {
	switch c.Outcome {
	case ClassificationTyped:
		return c.Type
	case ClassificationExcluded:
		return CensusTypeExcluded
	default:
		return CensusTypeUnclassified
	}
}
