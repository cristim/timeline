// Package ingest reads and validates source data into the normalized model.
// Validation is fail-loud (SRC-3): bad records land in a reject list with
// file/line/reason; they never silently enter the dataset.
package ingest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"wk/internal/model"
)

type RejectSource string

const (
	RejectSourceSeed RejectSource = "seed"
	RejectSourceWarm RejectSource = "warm"
)

// SeedManifest is data/seed/manifest.json (DEV-5).
type SeedManifest struct {
	SeedVersion string                `json:"seed_version"`
	Files       map[string]SeedFileMD `json:"files"`
}

type SeedFileMD struct {
	Count  int    `json:"count"`
	SHA256 string `json:"sha256"`
}

type Reject struct {
	Source RejectSource `json:"source"`
	File   string       `json:"file"`
	Line   int          `json:"line"`
	Reason string       `json:"reason"`
}

type Result struct {
	SeedVersion           string
	SeedInputSHA256       string
	Entities              []*model.Entity
	Rejects               []Reject
	SeedParsed            int
	SeedAccepted          int
	WarmParsed            int
	WarmAccepted          int
	WarmDuplicatesSkipped int
}

// LoadSeed reads every file listed in the seed manifest, verifies checksums
// and counts, validates each NDJSON line, and assigns slugs (DM-2a).
func LoadSeed(dir string) (*Result, error) {
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read seed manifest: %w", err)
	}
	var sm SeedManifest
	if err := json.Unmarshal(mb, &sm); err != nil {
		return nil, fmt.Errorf("parse seed manifest: %w", err)
	}
	if sm.SeedVersion == "" {
		return nil, fmt.Errorf("seed manifest missing seed_version")
	}

	res := &Result{SeedVersion: sm.SeedVersion}
	files := make([]string, 0, len(sm.Files))
	for f := range sm.Files {
		files = append(files, f)
	}
	// Deterministic ingest order (SRC-3: same input -> same output).
	sort.Strings(files)

	for _, name := range files {
		md := sm.Files[name]
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read seed file %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != md.SHA256 {
			return nil, fmt.Errorf("seed file %s: sha256 mismatch (manifest %s, actual %s)", name, md.SHA256, got)
		}
		n, accepted, err := res.loadFile(name, body)
		if err != nil {
			return nil, err
		}
		if n != md.Count {
			return nil, fmt.Errorf("seed file %s: %d entities parsed, manifest says %d", name, n, md.Count)
		}
		res.SeedParsed += n
		res.SeedAccepted += accepted
	}
	res.SeedInputSHA256, err = seedInputSHA256(sm)
	if err != nil {
		return nil, err
	}

	if err := model.AssignSlugs(res.Entities); err != nil {
		return nil, err
	}
	if err := res.checkRelTargets(); err != nil {
		return nil, err
	}
	return res, nil
}

func (r *Result) loadFile(name string, body []byte) (int, int, error) {
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	count := 0
	accepted := 0
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		count++
		var se model.SeedEntity
		if err := json.Unmarshal([]byte(text), &se); err != nil {
			r.Rejects = append(r.Rejects, Reject{Source: RejectSourceSeed, File: name, Line: line, Reason: "invalid JSON: " + err.Error()})
			continue
		}
		e, reason := Validate(&se)
		if reason != "" {
			r.Rejects = append(r.Rejects, Reject{Source: RejectSourceSeed, File: name, Line: line, Reason: reason})
			continue
		}
		r.Entities = append(r.Entities, e)
		accepted++
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("scan %s: %w", name, err)
	}
	return count, accepted, nil
}

// Validate normalizes one seed record or returns a reject reason.
func Validate(se *model.SeedEntity) (*model.Entity, string) {
	entity := &model.Entity{
		SeedID: se.ID, Type: se.Type, Name: se.Name, Description: se.Description,
		Precision: se.Precision, Status: se.Status,
		Categories: se.Categories, Importance: se.Importance, Point: se.Point,
		Wikidata: se.Wikidata, Wikipedia: se.Wikipedia, MediaThumb: se.MediaThumb,
		Rel: se.Rel, Props: se.Props,
	}
	if reason := validateEntityFields(entity); reason != "" {
		return nil, reason
	}

	t0, err := model.ParseSeedTime(se.T0)
	if err != nil {
		return nil, "t0: " + err.Error()
	}
	t1 := t0
	if len(se.T1) > 0 {
		t1, err = model.ParseSeedTime(se.T1)
		if err != nil {
			return nil, "t1: " + err.Error()
		}
	}
	entity.T0, entity.T1 = t0, t1
	if reason := validateEntityTimes(entity); reason != "" {
		return nil, reason
	}
	return entity, ""
}

// ValidateEntity applies the same gate to an entity normalized outside the
// seed path (the Wikidata dump importer), so bulk rows meet exactly the
// vocabulary and range rules curated rows do.
func ValidateEntity(entity *model.Entity) string {
	if reason := validateEntityFields(entity); reason != "" {
		return reason
	}
	return validateEntityTimes(entity)
}

// ValidateEntityScalar applies the entity checks that do not depend on child
// rows. Relational model readers use it while streaming one table at a time.
func ValidateEntityScalar(entity *model.Entity) string {
	if reason := validateEntityScalarFields(entity); reason != "" {
		return reason
	}
	if _, ok := model.FinestBucketFor(entity.Precision); !ok {
		return fmt.Sprintf("unknown time precision %q", entity.Precision)
	}
	if entity.Point != nil {
		if len(entity.Point) != 2 || !model.ValidLonLat(entity.Point[0], entity.Point[1]) {
			return fmt.Sprintf("point %v not a valid [lon,lat]", entity.Point)
		}
	}
	return validateEntityTimes(entity)
}

// ValidateEntityCategory checks one category against the ingest vocabulary.
func ValidateEntityCategory(category string) string {
	if !model.Categories[category] {
		return fmt.Sprintf("unknown category %q", category)
	}
	return ""
}

// ValidateEntityRelationship checks one relationship without resolving its target.
func ValidateEntityRelationship(relationship model.SeedRel) string {
	if !model.RelationshipTypes[relationship.Type] {
		return fmt.Sprintf("unknown relationship type %q", relationship.Type)
	}
	if relationship.Target == "" {
		return fmt.Sprintf("relationship %q with empty target", relationship.Type)
	}
	return ""
}

// ValidateEntityProperty checks one normalized property claim.
func ValidateEntityProperty(property model.SeedProp) string {
	switch {
	case property.Property == "":
		return "property claim with empty property name"
	case property.Source == "":
		return fmt.Sprintf("property %q missing source (DM-5)", property.Property)
	case property.PublishedAt == "":
		return fmt.Sprintf("property %q missing published_at (DM-5)", property.Property)
	case !model.ValueTypes[property.ValueType]:
		return fmt.Sprintf("property %q unknown value_type %q", property.Property, property.ValueType)
	case property.Value == nil && (property.Min == nil || property.Max == nil):
		return fmt.Sprintf("property %q needs value or min+max", property.Property)
	case math.IsNaN(property.Confidence) || math.IsInf(property.Confidence, 0) || property.Confidence < 0 || property.Confidence > 1:
		return fmt.Sprintf("property %q has confidence %v outside [0,1]", property.Property, property.Confidence)
	}
	for _, value := range []*float64{property.Value, property.Min, property.Max} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return fmt.Sprintf("property %q has nonfinite value", property.Property)
		}
	}
	if property.Min != nil && property.Max != nil && *property.Max < *property.Min {
		return fmt.Sprintf("property %q has reversed range", property.Property)
	}
	return ""
}

func validateEntityFields(entity *model.Entity) string {
	if reason := validateEntityScalarFields(entity); reason != "" {
		return reason
	}
	if len(entity.Categories) == 0 {
		return "no categories"
	}
	if _, ok := model.FinestBucketFor(entity.Precision); !ok {
		return fmt.Sprintf("unknown time precision %q", entity.Precision)
	}
	for _, category := range entity.Categories {
		if reason := ValidateEntityCategory(category); reason != "" {
			return reason
		}
	}
	for _, relationship := range entity.Rel {
		if reason := ValidateEntityRelationship(relationship); reason != "" {
			return reason
		}
	}
	for _, property := range entity.Props {
		if reason := ValidateEntityProperty(property); reason != "" {
			return reason
		}
	}
	if entity.Point != nil {
		if len(entity.Point) != 2 || !model.ValidLonLat(entity.Point[0], entity.Point[1]) {
			return fmt.Sprintf("point %v not a valid [lon,lat]", entity.Point)
		}
	}
	return ""
}

func validateEntityScalarFields(entity *model.Entity) string {
	switch {
	case entity.SeedID == "":
		return "missing id"
	case entity.Name == "":
		return "missing name"
	case !model.EntityTypes[entity.Type]:
		return fmt.Sprintf("unknown entity type %q", entity.Type)
	case !model.TemporalStatuses[entity.Status]:
		return fmt.Sprintf("unknown temporal status %q", entity.Status)
	case math.IsNaN(entity.Importance) || math.IsInf(entity.Importance, 0) || entity.Importance <= 0 || entity.Importance > 1:
		return fmt.Sprintf("importance %v outside (0,1]", entity.Importance)
	}
	return ""
}

func validateEntityTimes(entity *model.Entity) string {
	if math.IsNaN(entity.T0) || math.IsNaN(entity.T1) || math.IsInf(entity.T0, 0) || math.IsInf(entity.T1, 0) || entity.T1 < entity.T0 {
		return fmt.Sprintf("invalid time range t0=%v t1=%v", entity.T0, entity.T1)
	}
	// Far past/future must carry correspondingly coarse precision, or the
	// windowed-bucket int64 guard (model.MaxWindowedTime) breaks.
	if fb, _ := model.FinestBucketFor(entity.Precision); fb > 2 &&
		(math.Abs(entity.T0) > model.MaxWindowedTime || math.Abs(entity.T1) > model.MaxWindowedTime) {
		return fmt.Sprintf("time beyond +/-1e18s requires billion_year precision, got %q", entity.Precision)
	}
	return ""
}

func (r *Result) checkRelTargets() error {
	ids := map[string]bool{}
	for _, e := range r.Entities {
		ids[e.SeedID] = true
	}
	for _, e := range r.Entities {
		for _, rel := range e.Rel {
			if !ids[rel.Target] {
				return fmt.Errorf("entity %q: relationship %s -> unknown target %q", e.SeedID, rel.Type, rel.Target)
			}
		}
	}
	return nil
}

func seedInputSHA256(manifest SeedManifest) (string, error) {
	files := make([]string, 0, len(manifest.Files))
	for name := range manifest.Files {
		files = append(files, name)
	}
	sort.Strings(files)

	type seedInputFile struct {
		File   string `json:"file"`
		Count  int    `json:"count"`
		SHA256 string `json:"sha256"`
	}

	entries := make([]seedInputFile, 0, len(files))
	for _, name := range files {
		md := manifest.Files[name]
		entries = append(entries, seedInputFile{
			File:   name,
			Count:  md.Count,
			SHA256: md.SHA256,
		})
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal seed input digest: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
