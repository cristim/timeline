package ingest

import (
	"encoding/json"
	"strings"
	"testing"

	"wk/internal/model"
)

func seedLine(t *testing.T, overrides map[string]any) *model.SeedEntity {
	t.Helper()
	base := map[string]any{
		"id": "test-entity", "type": "event", "name": "Test Entity",
		"t0": "1900-01-01", "precision": "day", "status": "documented",
		"categories": []string{"war"}, "importance": 0.5,
	}
	for k, v := range overrides {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var se model.SeedEntity
	if err := json.Unmarshal(b, &se); err != nil {
		t.Fatal(err)
	}
	return &se
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]any
		wantIn    string // substring of the reject reason
	}{
		{"unknown type", map[string]any{"type": "spaceship"}, "unknown entity type"},
		{"unknown status", map[string]any{"status": "vibes"}, "unknown temporal status"},
		{"unknown category", map[string]any{"categories": []string{"sports"}}, "unknown category"},
		{"unknown precision", map[string]any{"precision": "fortnight"}, "unknown time precision"},
		{"importance zero", map[string]any{"importance": 0}, "importance"},
		{"importance high", map[string]any{"importance": 1.5}, "importance"},
		{"no categories", map[string]any{"categories": []string{}}, "no categories"},
		{"bad point", map[string]any{"point": []float64{200, 95}}, "not a valid"},
		{"bad time", map[string]any{"t0": "yesterday"}, "unparseable"},
		{"t1 before t0", map[string]any{"t0": "1950-01-01", "t1": "1900-01-01"}, "invalid time range"},
		{"unknown rel", map[string]any{"rel": []map[string]string{{"type": "friends_with", "target": "x"}}}, "unknown relationship"},
		{"prop missing source", map[string]any{"props": []map[string]any{{"property": "mass", "value": 1.0, "value_type": "measured", "published_at": "2020"}}}, "missing source"},
		{"prop missing published_at", map[string]any{"props": []map[string]any{{"property": "mass", "value": 1.0, "value_type": "measured", "source": "s"}}}, "missing published_at"},
		{"prop no value", map[string]any{"props": []map[string]any{{"property": "mass", "value_type": "measured", "source": "s", "published_at": "2020"}}}, "needs value or min+max"},
		{"deep time fine precision", map[string]any{"t0": map[string]any{"y": -50e9}, "precision": "year"}, "requires billion_year"},
	}
	for _, c := range cases {
		se := seedLine(t, c.overrides)
		_, reason := Validate(se)
		if reason == "" {
			t.Errorf("%s: expected reject, got accept", c.name)
			continue
		}
		if !strings.Contains(reason, c.wantIn) {
			t.Errorf("%s: reason %q does not contain %q", c.name, reason, c.wantIn)
		}
	}
}

func TestValidateAccepts(t *testing.T) {
	se := seedLine(t, map[string]any{
		"t0": map[string]any{"y": -66000000.0}, "t1": nil, "precision": "million_year",
		"status": "estimated",
	})
	e, reason := Validate(se)
	if reason != "" {
		t.Fatalf("unexpected reject: %s", reason)
	}
	if e.T1 != e.T0 {
		t.Errorf("absent t1 should equal t0, got %v vs %v", e.T1, e.T0)
	}
	wantT0 := model.YearToSeconds(-66000000)
	if e.T0 != wantT0 {
		t.Errorf("t0 = %v, want %v", e.T0, wantT0)
	}
}

func TestLoadSeedRealDataset(t *testing.T) {
	// The committed seed must always load clean: counts match the manifest,
	// zero rejects, slugs assigned (DEV-6 M1 done-criteria as a test).
	res, err := LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if len(res.Rejects) != 0 {
		for _, r := range res.Rejects {
			t.Errorf("seed reject %s:%d: %s", r.File, r.Line, r.Reason)
		}
	}
	if len(res.Entities) < 100 {
		t.Errorf("suspiciously small seed: %d entities", len(res.Entities))
	}
	slugs := map[string]bool{}
	for _, e := range res.Entities {
		if e.Slug == "" {
			t.Errorf("entity %q has no slug", e.SeedID)
		}
		if slugs[e.Slug] {
			t.Errorf("duplicate slug %q", e.Slug)
		}
		slugs[e.Slug] = true
	}
}
