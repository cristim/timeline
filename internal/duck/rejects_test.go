package duck

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriteRejectsDeterministicAndDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	input := []RejectRow{
		{Source: "warm", File: "warm:events", Line: 7, Reason: "invalid JSON"},
		{Source: "seed", File: "b.ndjson", Line: 2, Reason: "bad date"},
		{Source: "seed", File: "a.ndjson", Line: 9, Reason: "bad date"},
	}
	want := slices.Clone(input)

	first, firstPath := writeRejectsFixture(t, input)
	second, secondPath := writeRejectsFixture(t, []RejectRow{input[1], input[2], input[0]})
	if first.Name != "reject.parquet" || second.Name != "reject.parquet" {
		t.Fatalf("unexpected file names: %#v %#v", first, second)
	}
	if first.Rows != len(input) || second.Rows != len(input) {
		t.Fatalf("rows = %d and %d, want %d", first.Rows, second.Rows, len(input))
	}

	firstBody, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read first parquet: %v", err)
	}
	secondBody, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read second parquet: %v", err)
	}
	if sha256.Sum256(firstBody) != sha256.Sum256(secondBody) {
		t.Fatal("same rejects produced different parquet bytes")
	}
	if !slices.Equal(input, want) {
		t.Fatalf("WriteRejects mutated caller slice\n got: %#v\nwant: %#v", input, want)
	}
}

func TestWriteRejectsWritesActualRowsInSortedOrder(t *testing.T) {
	t.Parallel()

	rows := []RejectRow{
		{Source: "warm", File: "warm:events", Line: 7, Reason: "invalid JSON"},
		{Source: "seed", File: "b.ndjson", Line: 2, Reason: "bad date"},
		{Source: "seed", File: "a.ndjson", Line: 9, Reason: "bad date"},
	}
	file, path := writeRejectsFixture(t, rows)
	if file.Rows != len(rows) {
		t.Fatalf("Rows = %d, want %d", file.Rows, len(rows))
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type rejectRow struct {
		source string
		file   string
		line   int64
		reason string
	}
	got := []rejectRow{}
	queryRows, err := db.QueryContext(context.Background(), `SELECT source, file, line, reason FROM read_parquet(?)`, path)
	if err != nil {
		t.Fatalf("query reject parquet: %v", err)
	}
	defer queryRows.Close()
	for queryRows.Next() {
		var row rejectRow
		if err := queryRows.Scan(&row.source, &row.file, &row.line, &row.reason); err != nil {
			t.Fatalf("scan reject row: %v", err)
		}
		got = append(got, row)
	}
	if err := queryRows.Err(); err != nil {
		t.Fatalf("read reject rows: %v", err)
	}
	want := []rejectRow{
		{source: "seed", file: "a.ndjson", line: 9, reason: "bad date"},
		{source: "seed", file: "b.ndjson", line: 2, reason: "bad date"},
		{source: "warm", file: "warm:events", line: 7, reason: "invalid JSON"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows\n got: %#v\nwant: %#v", got, want)
	}
}

func TestWriteRejectsPreservesDuplicates(t *testing.T) {
	t.Parallel()

	file, path := writeRejectsFixture(t, []RejectRow{
		{Source: "seed", File: "a.ndjson", Line: 9, Reason: "bad date"},
		{Source: "seed", File: "a.ndjson", Line: 9, Reason: "bad date"},
	})
	if file.Rows != 2 {
		t.Fatalf("Rows = %d, want 2", file.Rows)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM read_parquet(?)`, path).Scan(&count); err != nil {
		t.Fatalf("count reject rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("stored rows = %d, want 2", count)
	}
}

func TestWriteRejectsAcceptsEmptyInput(t *testing.T) {
	t.Parallel()

	file, path := writeRejectsFixture(t, nil)
	if file.Rows != 0 {
		t.Fatalf("Rows = %d, want 0", file.Rows)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM read_parquet(?)`, path).Scan(&count); err != nil {
		t.Fatalf("count empty reject rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("stored rows = %d, want 0", count)
	}
}

func TestWriteRejectsEmptyOutputIsByteDeterministic(t *testing.T) {
	t.Parallel()

	first, firstPath := writeRejectsFixture(t, nil)
	second, secondPath := writeRejectsFixture(t, []RejectRow{})
	if first.Rows != 0 || second.Rows != 0 {
		t.Fatalf("rows = %d and %d, want 0", first.Rows, second.Rows)
	}

	firstBody, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read first empty parquet: %v", err)
	}
	secondBody, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read second empty parquet: %v", err)
	}
	if sha256.Sum256(firstBody) != sha256.Sum256(secondBody) {
		t.Fatal("empty rejects produced different parquet bytes")
	}
}

func TestWriteRejectsRejectsPreexistingParquet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "reject.parquet")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed preexisting parquet: %v", err)
	}

	_, err := WriteRejects(context.Background(), dir, []RejectRow{{Source: "seed", File: "a.ndjson", Line: 1, Reason: "bad date"}})
	if err == nil {
		t.Fatal("expected preexisting parquet to fail")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "already exists")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want os.ErrExist", err)
	}
}

func writeRejectsFixture(t *testing.T, rows []RejectRow) (ModelFile, string) {
	t.Helper()

	dir := t.TempDir()
	file, err := WriteRejects(context.Background(), dir, rows)
	if err != nil {
		t.Fatalf("WriteRejects: %v", err)
	}
	path := filepath.Join(dir, file.Name)
	if file.Path != path {
		t.Fatalf("Path = %q, want %q", file.Path, path)
	}
	return file, path
}
