package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"wk/internal/model"
)

// WarmEvent is one NDJSON line of the normalized Wikidata event set stored in
// wk-warm: the standard seed schema plus unresolved cross-references.
type WarmEvent struct {
	model.SeedEntity
	PartOfQID string `json:"part_of_qid,omitempty"`
}

func EncodeWarmEvents(records []WikidataRecord) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		if err := enc.Encode(WarmEvent{SeedEntity: r.Seed, PartOfQID: r.PartOfQID}); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// MergeWarmEvents validates the warm NDJSON and appends it to a seed result.
// Bulk data is lenient where the curated seed is strict (SRC-3): bad lines and
// duplicates become counted rejects instead of failing the bake, and part_of
// references only materialize when the target exists in the merged set.
func MergeWarmEvents(res *Result, warm []byte) (added, skipped int, err error) {
	qidToSeedID := map[string]string{}
	for _, e := range res.Entities {
		if e.Wikidata != "" {
			qidToSeedID[e.Wikidata] = e.SeedID
		}
	}

	type pending struct {
		entity    *model.Entity
		partOfQID string
	}
	var pendings []pending

	sc := bufio.NewScanner(bytes.NewReader(warm))
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var we WarmEvent
		if err := json.Unmarshal([]byte(text), &we); err != nil {
			res.Rejects = append(res.Rejects, Reject{"warm:events", line, "invalid JSON: " + err.Error()})
			continue
		}
		if _, dup := qidToSeedID[we.Wikidata]; dup && we.Wikidata != "" {
			skipped++ // curated seed entry wins over the bulk import (DM-2a)
			continue
		}
		e, reason := Validate(&we.SeedEntity)
		if reason != "" {
			res.Rejects = append(res.Rejects, Reject{"warm:events", line, reason})
			continue
		}
		qidToSeedID[e.Wikidata] = e.SeedID
		pendings = append(pendings, pending{e, we.PartOfQID})
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("scan warm events: %w", err)
	}

	for _, p := range pendings {
		if p.partOfQID != "" {
			if target, ok := qidToSeedID[p.partOfQID]; ok && target != p.entity.SeedID {
				p.entity.Rel = append(p.entity.Rel, model.SeedRel{Type: "part_of", Target: target})
			}
		}
		res.Entities = append(res.Entities, p.entity)
		added++
	}

	// Slugs must be assigned over the full merged set so collisions resolve
	// deterministically across seed + imports.
	for _, e := range res.Entities {
		e.Slug = ""
	}
	if err := model.AssignSlugs(res.Entities); err != nil {
		return 0, 0, err
	}
	return added, skipped, nil
}
