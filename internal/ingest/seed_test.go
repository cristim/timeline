package ingest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if res.SeedParsed != len(res.Entities) {
		t.Fatalf("SeedParsed = %d, want %d", res.SeedParsed, len(res.Entities))
	}
	if res.SeedAccepted != len(res.Entities) {
		t.Fatalf("SeedAccepted = %d, want %d", res.SeedAccepted, len(res.Entities))
	}
	if res.SeedInputSHA256 == "" {
		t.Fatal("SeedInputSHA256 is empty")
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

func TestLoadSeedTracksSeedCountersAndSource(t *testing.T) {
	t.Parallel()

	dir := writeSeedFixture(t,
		"seed-abc12345",
		map[string]string{
			"part1.ndjson": "\n# ignored comment\n" +
				`{"id":"entity-1","type":"event","name":"Valid","t0":"1900-01-01","precision":"day","status":"documented","categories":["war"],"importance":0.5}` + "\n" +
				`{"id":"entity-2","type":"event","name":"Broken","t0":"not-a-date","precision":"day","status":"documented","categories":["war"],"importance":0.5}` + "\n",
		},
	)

	res, err := LoadSeed(dir)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if res.SeedParsed != 2 {
		t.Fatalf("SeedParsed = %d, want 2", res.SeedParsed)
	}
	if res.SeedAccepted != 1 {
		t.Fatalf("SeedAccepted = %d, want 1", res.SeedAccepted)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %d, want 1", len(res.Entities))
	}
	if len(res.Rejects) != 1 {
		t.Fatalf("rejects = %d, want 1", len(res.Rejects))
	}
	reject := res.Rejects[0]
	if reject.Source != RejectSourceSeed {
		t.Fatalf("reject source = %q, want %q", reject.Source, RejectSourceSeed)
	}
	if reject.File != "part1.ndjson" || reject.Line != 4 {
		t.Fatalf("reject location = %s:%d, want part1.ndjson:4", reject.File, reject.Line)
	}
	if !strings.Contains(reject.Reason, "unparseable") {
		t.Fatalf("reject reason = %q, want unparseable parse error", reject.Reason)
	}
}

func TestLoadSeedDifferentFullInputsChangeSeedInputSHA256(t *testing.T) {
	t.Parallel()

	first := writeSeedFixture(t,
		"seed-deadbeef",
		map[string]string{
			"a.ndjson": `{"id":"entity-1","type":"event","name":"One","t0":"1900-01-01","precision":"day","status":"documented","categories":["war"],"importance":0.5}` + "\n",
		},
	)
	second := writeSeedFixture(t,
		"seed-deadbeef",
		map[string]string{
			"a.ndjson": `{"id":"entity-1","type":"event","name":"One updated","t0":"1900-01-01","precision":"day","status":"documented","categories":["war"],"importance":0.5}` + "\n",
		},
	)

	firstRes, err := LoadSeed(first)
	if err != nil {
		t.Fatalf("LoadSeed(first): %v", err)
	}
	secondRes, err := LoadSeed(second)
	if err != nil {
		t.Fatalf("LoadSeed(second): %v", err)
	}
	if firstRes.SeedVersion != secondRes.SeedVersion {
		t.Fatalf("seed versions differ: %q vs %q", firstRes.SeedVersion, secondRes.SeedVersion)
	}
	if firstRes.SeedInputSHA256 == secondRes.SeedInputSHA256 {
		t.Fatalf("SeedInputSHA256 matched for different manifest inputs: %q", firstRes.SeedInputSHA256)
	}
}

func writeSeedFixture(t *testing.T, seedVersion string, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	manifest := SeedManifest{
		SeedVersion: seedVersion,
		Files:       map[string]SeedFileMD{},
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write seed file %s: %v", name, err)
		}
		sum := sha256.Sum256([]byte(body))
		manifest.Files[name] = SeedFileMD{
			Count:  countSeedRecords(t, body),
			SHA256: fmt.Sprintf("%x", sum[:]),
		}
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func countSeedRecords(t *testing.T, body string) int {
	t.Helper()

	count := 0
	for _, line := range strings.Split(body, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		count++
	}
	return count
}
