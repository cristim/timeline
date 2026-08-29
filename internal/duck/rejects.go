package duck

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

//go:embed sql/rejects.sql
var rejectSQLFiles embed.FS

type RejectRow struct {
	Source string
	File   string
	Line   int
	Reason string
}

func WriteRejects(ctx context.Context, dir string, rows []RejectRow) (ModelFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ModelFile{}, fmt.Errorf("create reject directory: %w", err)
	}

	path := filepath.Join(dir, "reject.parquet")
	if _, err := os.Stat(path); err == nil {
		return ModelFile{}, fmt.Errorf("reject file %s: %w", path, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ModelFile{}, fmt.Errorf("check reject file %s: %w", path, err)
	}

	db, conn, err := openConnection(ctx)
	if err != nil {
		return ModelFile{}, err
	}
	defer db.Close()
	defer conn.Close()

	schema, err := rejectSQLFiles.ReadFile("sql/rejects.sql")
	if err != nil {
		return ModelFile{}, fmt.Errorf("read reject schema: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return ModelFile{}, fmt.Errorf("begin reject transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
		return ModelFile{}, fmt.Errorf("create reject schema: %w", err)
	}
	if err := insertRejects(ctx, tx, rows); err != nil {
		return ModelFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelFile{}, fmt.Errorf("commit rejects: %w", err)
	}
	committed = true

	if _, err := conn.ExecContext(ctx,
		`COPY (SELECT source, file, line, reason FROM reject ORDER BY source, file, line, reason) TO ? (FORMAT PARQUET, COMPRESSION ZSTD)`,
		path,
	); err != nil {
		return ModelFile{}, fmt.Errorf("write reject.parquet: %w", err)
	}

	return ModelFile{Name: "reject.parquet", Path: path, Rows: len(rows)}, nil
}

func insertRejects(ctx context.Context, tx *sql.Tx, rows []RejectRow) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO reject VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare reject insert: %w", err)
	}
	defer stmt.Close()

	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, func(a, b RejectRow) int {
		switch {
		case a.Source != b.Source:
			return cmp.Compare(a.Source, b.Source)
		case a.File != b.File:
			return cmp.Compare(a.File, b.File)
		case a.Line != b.Line:
			return cmp.Compare(a.Line, b.Line)
		default:
			return cmp.Compare(a.Reason, b.Reason)
		}
	})

	for i, row := range sorted {
		if _, err := stmt.ExecContext(ctx, row.Source, row.File, row.Line, row.Reason); err != nil {
			return fmt.Errorf("insert reject %d: %w", i, err)
		}
	}
	return nil
}
