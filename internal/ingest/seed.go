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
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

type Result struct {
	SeedVersion string
	Entities    []*model.Entity
	Rejects     []Reject
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
		n, err := res.loadFile(name, body)
		if err != nil {
			return nil, err
		}
		if n != md.Count {
			return nil, fmt.Errorf("seed file %s: %d entities parsed, manifest says %d", name, n, md.Count)
		}
	}

	if err := model.AssignSlugs(res.Entities); err != nil {
		return nil, err
	}
	if err := res.checkRelTargets(); err != nil {
		return nil, err
	}
	return res, nil
}

func (r *Result) loadFile(name string, body []byte) (int, error) {
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	count := 0
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
			r.Rejects = append(r.Rejects, Reject{name, line, "invalid JSON: " + err.Error()})
			continue
		}
		e, reason := Validate(&se)
		if reason != "" {
			r.Rejects = append(r.Rejects, Reject{name, line, reason})
			continue
		}
		r.Entities = append(r.Entities, e)
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", name, err)
	}
	return count, nil
}

// Validate normalizes one seed record or returns a reject reason.
func Validate(se *model.SeedEntity) (*model.Entity, string) {
	switch {
	case se.ID == "":
		return nil, "missing id"
	case se.Name == "":
		return nil, "missing name"
	case !model.EntityTypes[se.Type]:
		return nil, fmt.Sprintf("unknown entity type %q", se.Type)
	case !model.TemporalStatuses[se.Status]:
		return nil, fmt.Sprintf("unknown temporal status %q", se.Status)
	case se.Importance <= 0 || se.Importance > 1:
		return nil, fmt.Sprintf("importance %v outside (0,1]", se.Importance)
	case len(se.Categories) == 0:
		return nil, "no categories"
	}
	if _, ok := model.FinestBucketFor(se.Precision); !ok {
		return nil, fmt.Sprintf("unknown time precision %q", se.Precision)
	}
	for _, c := range se.Categories {
		if !model.Categories[c] {
			return nil, fmt.Sprintf("unknown category %q", c)
		}
	}
	for _, rel := range se.Rel {
		if !model.RelationshipTypes[rel.Type] {
			return nil, fmt.Sprintf("unknown relationship type %q", rel.Type)
		}
		if rel.Target == "" {
			return nil, fmt.Sprintf("relationship %q with empty target", rel.Type)
		}
	}
	for _, p := range se.Props {
		switch {
		case p.Property == "":
			return nil, "property claim with empty property name"
		case p.Source == "":
			return nil, fmt.Sprintf("property %q missing source (DM-5)", p.Property)
		case p.PublishedAt == "":
			return nil, fmt.Sprintf("property %q missing published_at (DM-5)", p.Property)
		case !model.ValueTypes[p.ValueType]:
			return nil, fmt.Sprintf("property %q unknown value_type %q", p.Property, p.ValueType)
		case p.Value == nil && (p.Min == nil || p.Max == nil):
			return nil, fmt.Sprintf("property %q needs value or min+max", p.Property)
		}
	}
	if se.Point != nil {
		if len(se.Point) != 2 || !model.ValidLonLat(se.Point[0], se.Point[1]) {
			return nil, fmt.Sprintf("point %v not a valid [lon,lat]", se.Point)
		}
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
	if math.IsNaN(t0) || math.IsNaN(t1) || t1 < t0 {
		return nil, fmt.Sprintf("invalid time range t0=%v t1=%v", t0, t1)
	}
	// Far past/future must carry correspondingly coarse precision, or the
	// windowed-bucket int64 guard (model.MaxWindowedTime) breaks.
	if fb, _ := model.FinestBucketFor(se.Precision); fb > 2 &&
		(math.Abs(t0) > model.MaxWindowedTime || math.Abs(t1) > model.MaxWindowedTime) {
		return nil, fmt.Sprintf("time beyond +/-1e18s requires billion_year precision, got %q", se.Precision)
	}

	return &model.Entity{
		SeedID: se.ID, Type: se.Type, Name: se.Name, Description: se.Description,
		T0: t0, T1: t1, Precision: se.Precision, Status: se.Status,
		Categories: se.Categories, Importance: se.Importance, Point: se.Point,
		Wikidata: se.Wikidata, Wikipedia: se.Wikipedia, MediaThumb: se.MediaThumb,
		Rel: se.Rel, Props: se.Props,
	}, ""
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
