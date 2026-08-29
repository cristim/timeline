package duck

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"wk/internal/ingest"
	"wk/internal/model"
)

func TestModelRoundTripPreservesIngestedEntities(t *testing.T) {
	t.Parallel()

	zero := 0.0
	combinedValue := 9.3
	combinedMinimum := 8.8
	combinedMaximum := 9.8
	minimum := 8.4
	maximum := 10.2
	entities := []*model.Entity{
		{
			SeedID: "first", Slug: "first", Type: "event", Name: "First",
			Description: "before everything", T0: -3.2e107, T1: -3.2e107,
			Precision: "billion_year", Status: "estimated",
			Categories: []string{"universe", "science"}, Importance: 0.9,
			Wikidata: "Q1", Wikipedia: "https://example.test/first",
			MediaThumb: "https://example.test/first.jpg",
			Rel: []model.SeedRel{
				{Type: "preceded", Target: "second"},
				{Type: "influenced", Target: "second"},
			},
			Props: []model.SeedProp{
				{Property: "population", Value: &zero, Unit: "count", ValueType: "measured", Source: "source zero", PublishedAt: "2000", Confidence: 1},
				{Property: "mass", Min: &minimum, Max: &maximum, Unit: "kg", ValueType: "estimated", Method: "model", Source: "source range", PublishedAt: "2020", Confidence: 0.7},
				{Property: "length", Value: &combinedValue, Min: &combinedMinimum, Max: &combinedMaximum, Unit: "m", ValueType: "measured", Source: "source combined", PublishedAt: "2021", Confidence: 0.8},
			},
		},
		{
			SeedID: "second", Slug: "second", Type: "place", Name: "Second",
			T0: 3.2e107, T1: 3.2e107, Precision: "billion_year", Status: "projected",
			Categories: []string{"future"}, Importance: 0.5, Point: []float64{0, -0.0},
		},
	}

	dir := t.TempDir()
	files, err := WriteModel(context.Background(), dir, entities)
	if err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	wantRows := map[string]int{
		"entity.parquet": 2, "entity_category.parquet": 3,
		"relationship.parquet": 2, "claim.parquet": 3,
	}
	if len(files) != len(wantRows) {
		t.Fatalf("WriteModel returned %d files, want %d", len(files), len(wantRows))
	}
	for _, file := range files {
		if file.Rows != wantRows[file.Name] {
			t.Errorf("%s rows = %d, want %d", file.Name, file.Rows, wantRows[file.Name])
		}
		if file.Path != filepath.Join(dir, file.Name) {
			t.Errorf("%s path = %q", file.Name, file.Path)
		}
	}
	assertModelFileRows(t, files)

	got, err := ReadModel(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !reflect.DeepEqual(got, entities) {
		t.Fatalf("round trip differs\n got: %#v\nwant: %#v", got, entities)
	}
}

func TestModelRoundTripPreservesRealSeedAndBytes(t *testing.T) {
	t.Parallel()

	seed, err := ingest.LoadSeed("../../data/seed")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if len(seed.Rejects) != 0 {
		t.Fatalf("real seed has %d rejects", len(seed.Rejects))
	}

	first, err := WriteModel(context.Background(), t.TempDir(), seed.Entities)
	if err != nil {
		t.Fatalf("first WriteModel: %v", err)
	}
	secondDir := t.TempDir()
	second, err := WriteModel(context.Background(), secondDir, seed.Entities)
	if err != nil {
		t.Fatalf("second WriteModel: %v", err)
	}
	for i := range first {
		firstBody, err := os.ReadFile(first[i].Path)
		if err != nil {
			t.Fatal(err)
		}
		secondBody, err := os.ReadFile(second[i].Path)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(firstBody) != sha256.Sum256(secondBody) {
			t.Errorf("%s is not deterministic", first[i].Name)
		}
	}
	assertModelFileRows(t, second)

	got, err := ReadModel(context.Background(), secondDir)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !reflect.DeepEqual(got, seed.Entities) {
		t.Fatal("real seed changed across Parquet round trip")
	}
}

func TestReadModelRejectsMissingTable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := WriteModel(context.Background(), dir, []*model.Entity{{
		SeedID: "one", Slug: "one", Type: "event", Name: "One", Precision: "year",
		Status: "documented", Categories: []string{"science"}, Importance: 1,
	}}); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "claim.parquet")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadModel(context.Background(), dir); err == nil {
		t.Fatal("ReadModel accepted a missing claim table")
	}
}

func TestWriteModelRejectsPostBucketEntities(t *testing.T) {
	t.Parallel()

	_, err := WriteModel(context.Background(), t.TempDir(), []*model.Entity{{
		SeedID: "one", Slug: "one", Type: "event", Name: "One", Precision: "year",
		Status: "documented", Categories: []string{"science"}, Importance: 1,
		BucketMin: 3, BucketMax: 8,
	}})
	if err == nil {
		t.Fatal("WriteModel accepted a post-bucket entity")
	}
}

func TestReadModelRejectsBrokenTables(t *testing.T) {
	t.Parallel()

	value := 1.0
	entities := []*model.Entity{
		{
			SeedID: "one", Slug: "one", Type: "event", Name: "One", Precision: "year",
			Status: "documented", Categories: []string{"science"}, Importance: 1,
			Rel:   []model.SeedRel{{Type: "preceded", Target: "two"}},
			Props: []model.SeedProp{{Property: "population", Value: &value, ValueType: "measured", Source: "source", PublishedAt: "2000"}},
		},
		{
			SeedID: "two", Slug: "two", Type: "place", Name: "Two", Precision: "year",
			Status: "documented", Categories: []string{"science"}, Importance: 1,
		},
	}
	tests := []struct {
		name      string
		file      string
		selectSQL string
		wantError string
	}{
		{
			name: "duplicate entity slug", file: "entity.parquet",
			selectSQL: `SELECT * REPLACE ('same' AS slug) FROM read_parquet(?)`,
			wantError: "duplicate entity slug",
		},
		{
			name: "inconsistent point columns", file: "entity.parquet",
			selectSQL: `SELECT * REPLACE (true AS has_point, NULL::DOUBLE AS point_lon, NULL::DOUBLE AS point_lat) FROM read_parquet(?)`,
			wantError: "inconsistent point columns",
		},
		{
			name: "orphan category owner", file: "entity_category.parquet",
			selectSQL: `SELECT 'missing' AS seed_id, category_order, category FROM read_parquet(?) LIMIT 1`,
			wantError: "category owner",
		},
		{
			name: "missing category row", file: "entity_category.parquet",
			selectSQL: `SELECT * FROM read_parquet(?) WHERE NOT (seed_id = 'one' AND category_order = 0)`,
			wantError: "category count",
		},
		{
			name: "duplicate claim order", file: "claim.parquet",
			selectSQL: `WITH source AS (SELECT * FROM read_parquet(?)) SELECT * FROM source UNION ALL (SELECT * FROM source LIMIT 1)`,
			wantError: "claim \"one\" order",
		},
		{
			name: "claim with partial range", file: "claim.parquet",
			selectSQL: `SELECT * REPLACE (NULL::DOUBLE AS value, 1::DOUBLE AS min, NULL::DOUBLE AS max) FROM read_parquet(?)`,
			wantError: "invalid value shape",
		},
		{
			name: "claim with no value", file: "claim.parquet",
			selectSQL: `SELECT * REPLACE (NULL::DOUBLE AS value, NULL::DOUBLE AS min, NULL::DOUBLE AS max) FROM read_parquet(?)`,
			wantError: "invalid value shape",
		},
		{
			name: "missing relationship target", file: "relationship.parquet",
			selectSQL: `SELECT seed_id, relationship_order, type, 'missing' AS target_seed_id FROM read_parquet(?)`,
			wantError: "targets missing entity",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if _, err := WriteModel(context.Background(), dir, entities); err != nil {
				t.Fatalf("WriteModel: %v", err)
			}
			rewriteParquet(t, filepath.Join(dir, test.file), test.selectSQL)
			_, err := ReadModel(context.Background(), dir)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ReadModel error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func assertModelFileRows(t *testing.T, files []ModelFile) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, file := range files {
		var rows int
		if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM read_parquet(?)`, file.Path).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", file.Name, err)
		}
		if rows != file.Rows {
			t.Errorf("%s contains %d rows, metadata says %d", file.Name, rows, file.Rows)
		}
	}
}

func rewriteParquet(t *testing.T, path, selectSQL string) {
	t.Helper()
	replacement := path + ".replacement"
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query := "COPY (" + strings.ReplaceAll(selectSQL, "?", "$2") + ") TO $1 (FORMAT PARQUET)"
	if _, err := db.ExecContext(context.Background(), query, replacement, path); err != nil {
		t.Fatalf("rewrite %s: %v", filepath.Base(path), err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}
