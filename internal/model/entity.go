package model

import "encoding/json"

// Closed vocabularies (DM-1, DM-4, DM-8): unknown values are ingestion errors,
// never silently coerced.

var EntityTypes = stringSet(
	"event", "person", "species", "object", "place", "organization", "state",
	"civilization", "idea", "theory", "discovery", "invention", "patent",
	"paper", "book", "artwork", "film", "game", "product", "technology",
	"standard", "law", "religion", "language", "disease", "chemical",
	"material", "structure", "astronomical_object", "natural_event",
	"future_event", "record", "time_series",
)

var TemporalStatuses = stringSet(
	"observed", "documented", "estimated", "projected", "model_dependent",
	"speculative", "legendary", "disputed",
)

var RelationshipTypes = stringSet(
	"part_of", "caused_by", "resulted_in", "invented_by", "based_on",
	"built_upon", "replaced", "influenced", "influenced_by", "occurred_at",
	"participant", "contemporary_with", "spread_to", "disproved_by",
	"confirmed_by", "discovered_by", "used_by", "created_by", "preceded",
	"succeeded", "supports", "parent_taxon", "depicts", "about",
)

// Categories are the topic facets used for chunk files and filtering (API-1).
var Categories = stringSet(
	"universe", "earth", "life", "war", "politics", "science", "technology",
	"culture", "exploration", "economy", "religion", "disaster", "future",
)

var ValueTypes = stringSet("measured", "estimated", "reconstructed", "projected")

func stringSet(ss ...string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// SeedEntity is one NDJSON line in data/seed/*.ndjson (DEV-5): the
// curator-authored form, validated and normalized by ingest into Entity.
type SeedEntity struct {
	ID          string          `json:"id"` // desired slug base (DM-2a validates)
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	T0          json.RawMessage `json:"t0"`
	T1          json.RawMessage `json:"t1,omitempty"` // absent = moment (t1 = t0)
	Precision   string          `json:"precision"`
	Status      string          `json:"status"`
	Categories  []string        `json:"categories"`
	Importance  float64         `json:"importance"` // curator prior 0..1; dump-scale signals replace this at M5
	Point       []float64       `json:"point,omitempty"`
	Wikidata    string          `json:"wikidata,omitempty"`
	Wikipedia   string          `json:"wikipedia,omitempty"`
	MediaThumb  string          `json:"media_thumb,omitempty"`
	Rel         []SeedRel       `json:"rel,omitempty"`
	Props       []SeedProp      `json:"props,omitempty"`
}

type SeedRel struct {
	Type   string `json:"type"`
	Target string `json:"target"` // seed id of the target entity
}

// SeedProp is one property claim (DM-5/DM-6): a sourced statement, possibly a
// range, never a bare unexplained number.
type SeedProp struct {
	Property    string   `json:"property"`
	Value       *float64 `json:"value,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	ValueType   string   `json:"value_type"`
	Method      string   `json:"method,omitempty"`
	Source      string   `json:"source"`
	PublishedAt string   `json:"published_at"` // powers the knowledge-date slider (VIS-6)
	Confidence  float64  `json:"confidence,omitempty"`
}

// Entity is the validated, normalized in-memory model row (DM-2).
type Entity struct {
	SeedID      string
	Slug        string
	Type        string
	Name        string
	Description string
	T0, T1      float64 // seconds since 1970
	Precision   string
	Status      string
	Categories  []string
	Importance  float64
	Point       []float64 // [lon, lat] render anchor, optional
	Wikidata    string
	Wikipedia   string
	MediaThumb  string
	Rel         []SeedRel
	Props       []SeedProp

	BucketMin int // coarsest bucket where it renders (ZOOM-3)
	BucketMax int // finest bucket, bounded by precision
}
