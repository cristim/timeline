package duck

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"wk/internal/ingest"
	"wk/internal/model"
)

//go:embed sql/wikidata_stage.sql
var wikidataStageSQL embed.FS

const importReportFileName = "import.json"

// WikidataDumpStagingSink keeps dump-scale import state in private DuckDB
// scratch space. It is not a readable model until a later promotion step.
type WikidataDumpStagingSink struct {
	ctx       context.Context
	workspace *stagingWorkspace
	connector *duckdb.Connector
	conn      driver.Conn
	tx        driver.Tx

	entityAppender       *duckdb.Appender
	categoryAppender     *duckdb.Appender
	relationshipAppender *duckdb.Appender
	claimAppender        *duckdb.Appender
	rejectAppender       *duckdb.Appender

	entityRows       int
	categoryRows     int
	relationshipRows int
	claimRows        int
	rejectRows       int

	finalized bool
	aborted   bool
}

func NewWikidataDumpStagingSink(ctx context.Context, outputPath string) (_ *WikidataDumpStagingSink, err error) {
	workspace, err := newStagingWorkspace(outputPath)
	if err != nil {
		return nil, err
	}
	s := &WikidataDumpStagingSink{ctx: ctx, workspace: workspace}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Abort())
		}
	}()

	if err := workspace.verifyScratchPathIdentity(); err != nil {
		return nil, err
	}
	s.connector, err = duckdb.NewConnector(workspace.duckDBPath(), nil)
	if err != nil {
		return nil, fmt.Errorf("open staging DuckDB: %w", err)
	}
	s.conn, err = s.connector.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect staging DuckDB: %w", err)
	}
	beginner, ok := s.conn.(driver.ConnBeginTx)
	if !ok {
		return nil, fmt.Errorf("staging DuckDB does not support transactions")
	}
	s.tx, err = beginner.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin staging DuckDB transaction: %w", err)
	}
	schema, err := wikidataStageSQL.ReadFile("sql/wikidata_stage.sql")
	if err != nil {
		return nil, fmt.Errorf("read staging DuckDB schema: %w", err)
	}
	if _, err := driverExec(ctx, s.conn, string(schema)); err != nil {
		return nil, fmt.Errorf("create staging DuckDB schema: %w", err)
	}
	if s.entityAppender, err = duckdb.NewAppenderFromConn(s.conn, "", "entity"); err != nil {
		return nil, fmt.Errorf("open entity appender: %w", err)
	}
	if s.categoryAppender, err = duckdb.NewAppenderFromConn(s.conn, "", "entity_category"); err != nil {
		return nil, fmt.Errorf("open category appender: %w", err)
	}
	if s.relationshipAppender, err = duckdb.NewAppenderFromConn(s.conn, "", "relationship"); err != nil {
		return nil, fmt.Errorf("open relationship appender: %w", err)
	}
	if s.claimAppender, err = duckdb.NewAppenderFromConn(s.conn, "", "claim"); err != nil {
		return nil, fmt.Errorf("open claim appender: %w", err)
	}
	if s.rejectAppender, err = duckdb.NewAppenderFromConn(s.conn, "", "reject"); err != nil {
		return nil, fmt.Errorf("open reject appender: %w", err)
	}
	if err := workspace.recordDuckFiles(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *WikidataDumpStagingSink) MarkSeenQID(qid string) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	result, err := driverExec(s.ctx, s.conn,
		`INSERT INTO seen_qid VALUES (?) ON CONFLICT DO NOTHING`, qid)
	if err != nil {
		return fmt.Errorf("record wikidata id %s: %w", qid, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count recorded wikidata id %s: %w", qid, err)
	}
	if rows == 0 {
		return fmt.Errorf("duplicate wikidata id %s", qid)
	}
	return nil
}

func (s *WikidataDumpStagingSink) AcceptEntity(entity *model.Entity) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	if entity == nil {
		return fmt.Errorf("accept entity: nil entity")
	}
	if entity.SeedID == "" {
		return fmt.Errorf("entity %q has empty seed id", entity.Name)
	}
	if entity.BucketMin != 0 || entity.BucketMax != 0 {
		return fmt.Errorf("entity %q already has bucket bounds [%d,%d]", entity.SeedID, entity.BucketMin, entity.BucketMax)
	}
	baseSlug := model.Slugify(entity.Name)
	if baseSlug == "" {
		return fmt.Errorf("entity %q slugifies to empty", entity.SeedID)
	}
	var lon, lat any
	hasPoint := entity.Point != nil
	if hasPoint {
		if len(entity.Point) != 2 {
			return fmt.Errorf("entity %q has point length %d", entity.SeedID, len(entity.Point))
		}
		lon, lat = entity.Point[0], entity.Point[1]
	}
	if err := s.entityAppender.AppendRow(
		int64(s.entityRows), entity.SeedID, nil, baseSlug, entity.Type, entity.Name, entity.Description,
		entity.T0, entity.T1, entity.Precision, entity.Status, entity.Importance,
		hasPoint, lon, lat, entity.Wikidata, entity.Wikipedia, entity.MediaThumb,
		int64(len(entity.Categories)), int64(len(entity.Rel)), int64(len(entity.Props)),
	); err != nil {
		return fmt.Errorf("append entity %q: %w", entity.SeedID, err)
	}
	for order, category := range entity.Categories {
		if err := s.categoryAppender.AppendRow(entity.SeedID, int64(order), category); err != nil {
			return fmt.Errorf("append category %q[%d]: %w", entity.SeedID, order, err)
		}
		s.categoryRows++
	}
	for order, relationship := range entity.Rel {
		if err := s.relationshipAppender.AppendRow(entity.SeedID, int64(order), relationship.Type, relationship.Target); err != nil {
			return fmt.Errorf("append relationship %q[%d]: %w", entity.SeedID, order, err)
		}
		s.relationshipRows++
	}
	for order, claim := range entity.Props {
		if !validClaimShape(claim.Value != nil, claim.Min != nil, claim.Max != nil) {
			return fmt.Errorf("claim %q[%d] has invalid value shape", entity.SeedID, order)
		}
		if err := s.claimAppender.AppendRow(
			entity.SeedID, int64(order), claim.Property, nullableFloat(claim.Value), nullableFloat(claim.Min),
			nullableFloat(claim.Max), claim.Unit, claim.ValueType, claim.Method, claim.Source,
			claim.PublishedAt, claim.Confidence,
		); err != nil {
			return fmt.Errorf("append claim %q[%d]: %w", entity.SeedID, order, err)
		}
		s.claimRows++
	}
	s.entityRows++
	return nil
}

func (s *WikidataDumpStagingSink) Reject(reject ingest.Reject) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	if err := s.rejectAppender.AppendRow(string(reject.Source), reject.File, int64(reject.Line), reject.Reason); err != nil {
		return fmt.Errorf("append reject %d: %w", s.rejectRows, err)
	}
	s.rejectRows++
	return nil
}

func (s *WikidataDumpStagingSink) finalizeScratch(ctx context.Context, importReport []byte) (err error) {
	if err := s.requireWritable(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Abort())
		}
	}()
	if err := s.closeAppenders(); err != nil {
		return err
	}
	tx := s.tx
	s.tx = nil
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit staged Wikidata rows: %w", err)
	}
	if err := s.closeDuckDB(); err != nil {
		return err
	}
	if err := s.workspace.recordDuckFiles(); err != nil {
		return err
	}
	if err := s.workspace.verifyScratchPathIdentity(); err != nil {
		return err
	}

	db, err := sql.Open("duckdb", s.workspace.duckDBPath())
	if err != nil {
		return fmt.Errorf("reopen staged Wikidata rows: %w", err)
	}
	db.SetMaxOpenConns(2)
	dbClosed := false
	defer func() {
		if !dbClosed {
			if closeErr := db.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close staged Wikidata exports: %w", closeErr))
			}
		}
	}()
	if err := validateStagedReferences(ctx, db); err != nil {
		return err
	}
	if err := assignStagedSlugs(ctx, db); err != nil {
		return err
	}

	exports := s.workspace.exportsPath()
	for _, definition := range fileDefinitions {
		if err := s.workspace.verifyScratchPathIdentity(); err != nil {
			return err
		}
		path := filepath.Join(exports, definition.name)
		projection := "*"
		if definition.table == "entity" {
			projection = `input_order, seed_id, slug, type, name, description, t0, t1, precision,
status, importance, has_point, point_lon, point_lat, wikidata, wikipedia, media_thumb,
category_count, relationship_count, claim_count`
		}
		query := fmt.Sprintf(
			"COPY (SELECT %s FROM %s ORDER BY %s) TO ? (FORMAT PARQUET, COMPRESSION ZSTD)",
			projection, definition.table, definition.orderBy,
		)
		if _, err := db.ExecContext(ctx, query, path); err != nil {
			return fmt.Errorf("write staged %s: %w", definition.name, err)
		}
		if err := s.workspace.recordExportFile(definition.name, nil); err != nil {
			return err
		}
	}
	if err := s.workspace.verifyScratchPathIdentity(); err != nil {
		return err
	}
	rejectPath := filepath.Join(exports, "reject.parquet")
	if _, err := db.ExecContext(ctx,
		`COPY (SELECT source, file, line, reason FROM reject ORDER BY source, file, line, reason) TO ? (FORMAT PARQUET, COMPRESSION ZSTD)`,
		rejectPath,
	); err != nil {
		return fmt.Errorf("write staged reject.parquet: %w", err)
	}
	if err := s.workspace.recordExportFile("reject.parquet", nil); err != nil {
		return err
	}
	if err := s.workspace.verifyScratchPathIdentity(); err != nil {
		return err
	}
	file, err := s.workspace.exportsRoot.OpenFile(importReportFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create staged import report: %w", err)
	}
	if _, writeErr := file.Write(importReport); writeErr != nil {
		err = errors.Join(fmt.Errorf("write staged import report: %w", writeErr), file.Close())
		return err
	}
	if err := s.workspace.recordExportFile(importReportFileName, file); err != nil {
		err = errors.Join(err, file.Close())
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close staged import report: %w", err)
	}
	dbClosed = true
	if err := db.Close(); err != nil {
		return fmt.Errorf("close staged Wikidata exports: %w", err)
	}
	if err := s.workspace.recordDuckFiles(); err != nil {
		return err
	}
	s.finalized = true
	return nil
}

func validateStagedReferences(ctx context.Context, db *sql.DB) error {
	checks := []struct {
		name  string
		query string
	}{
		{name: "category owner", query: `SELECT count(*) FROM entity_category c LEFT JOIN entity e USING (seed_id) WHERE e.seed_id IS NULL`},
		{name: "relationship owner", query: `SELECT count(*) FROM relationship r LEFT JOIN entity e USING (seed_id) WHERE e.seed_id IS NULL`},
		{name: "relationship target", query: `SELECT count(*) FROM relationship r LEFT JOIN entity e ON e.seed_id = r.target_seed_id WHERE e.seed_id IS NULL`},
		{name: "claim owner", query: `SELECT count(*) FROM claim c LEFT JOIN entity e USING (seed_id) WHERE e.seed_id IS NULL`},
	}
	for _, check := range checks {
		var count int64
		if err := db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			return fmt.Errorf("validate staged %s: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("staged model has %d rows with a missing %s", count, check.name)
		}
	}
	return nil
}

func assignStagedSlugs(ctx context.Context, db *sql.DB) (err error) {
	rows, err := db.QueryContext(ctx, `
SELECT base_slug, seed_id, t0, type, wikidata,
       row_number() OVER (PARTITION BY base_slug ORDER BY importance DESC, seed_id) - 1 AS base_rank
FROM entity
ORDER BY base_slug, importance DESC, seed_id`)
	if err != nil {
		return fmt.Errorf("read staged slug order: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close staged slug rows: %w", closeErr))
		}
	}()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin staged slug assignment: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback staged slug assignment: %w", rollbackErr))
			}
		}
	}()
	for rows.Next() {
		var base, seedID, entityType, wikidata string
		var t0 float64
		var rank int64
		if err := rows.Scan(&base, &seedID, &t0, &entityType, &wikidata, &rank); err != nil {
			return fmt.Errorf("scan staged slug row: %w", err)
		}
		candidates := []string{base}
		if rank > 0 {
			candidates = []string{
				fmt.Sprintf("%s-%d", base, int(model.SecondsToYear(t0))),
				fmt.Sprintf("%s-%s", base, entityType),
			}
			if wikidata != "" {
				candidates = append(candidates, fmt.Sprintf("%s-%s", base, strings.ToLower(wikidata)))
			}
		}
		slug := ""
		for _, candidate := range candidates {
			result, err := tx.ExecContext(ctx,
				`INSERT INTO taken_slug VALUES (?, ?) ON CONFLICT DO NOTHING`, candidate, seedID)
			if err != nil {
				return fmt.Errorf("claim staged slug %q: %w", candidate, err)
			}
			claimed, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count staged slug claim %q: %w", candidate, err)
			}
			if claimed == 1 {
				slug = candidate
				break
			}
		}
		if slug == "" {
			return fmt.Errorf("unresolvable slug collision on %q (seed id %q)", base, seedID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entity SET slug = ? WHERE seed_id = ?`, slug, seedID); err != nil {
			return fmt.Errorf("store staged slug %q: %w", slug, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read staged slug rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close staged slug rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit staged slug assignment: %w", err)
	}
	committed = true
	return nil
}

func (s *WikidataDumpStagingSink) Abort() error {
	if s == nil || s.aborted {
		return nil
	}
	s.aborted = true
	var errs []error
	if err := s.closeAppenders(); err != nil {
		errs = append(errs, err)
	}
	if s.tx != nil {
		if err := s.tx.Rollback(); err != nil {
			errs = append(errs, fmt.Errorf("rollback staged Wikidata rows: %w", err))
		}
		s.tx = nil
	}
	if err := s.closeDuckDB(); err != nil {
		errs = append(errs, err)
	}
	if s.workspace != nil {
		if err := s.workspace.recordDuckFiles(); err != nil {
			errs = append(errs, err)
		}
		if err := s.workspace.cleanup(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *WikidataDumpStagingSink) closeAppenders() error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, appender := range []struct {
		name string
		ptr  **duckdb.Appender
	}{
		{name: "entity", ptr: &s.entityAppender},
		{name: "category", ptr: &s.categoryAppender},
		{name: "relationship", ptr: &s.relationshipAppender},
		{name: "claim", ptr: &s.claimAppender},
		{name: "reject", ptr: &s.rejectAppender},
	} {
		if *appender.ptr == nil {
			continue
		}
		if err := (*appender.ptr).Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s appender: %w", appender.name, err))
		}
		*appender.ptr = nil
	}
	return errors.Join(errs...)
}

func (s *WikidataDumpStagingSink) closeDuckDB() error {
	var errs []error
	if s.conn != nil {
		errs = appendCloseError(errs, "close staging DuckDB connection", s.conn.Close())
		s.conn = nil
	}
	if s.connector != nil {
		errs = appendCloseError(errs, "close staging DuckDB", s.connector.Close())
		s.connector = nil
	}
	return errors.Join(errs...)
}

func (s *WikidataDumpStagingSink) requireWritable() error {
	switch {
	case s == nil:
		return fmt.Errorf("nil Wikidata dump staging sink")
	case s.aborted:
		return fmt.Errorf("wikidata dump staging sink is aborted")
	case s.finalized:
		return fmt.Errorf("wikidata dump staging sink is finalized")
	default:
		return nil
	}
}

func driverExec(ctx context.Context, conn driver.Conn, query string, args ...any) (driver.Result, error) {
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		return nil, fmt.Errorf("DuckDB connection does not support context execution")
	}
	named := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return execer.ExecContext(ctx, query, named)
}

var _ ingest.WikidataDumpSink = (*WikidataDumpStagingSink)(nil)
