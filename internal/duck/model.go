// Package duck materializes the baker's relational working model as Parquet.
package duck

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/duckdb/duckdb-go/v2"

	"wk/internal/model"
)

//go:embed sql/schema.sql
var sqlFiles embed.FS

const schemaVersion = 1

type ModelFile struct {
	Name string
	Path string
	Rows int
}

type fileDefinition struct {
	name    string
	table   string
	orderBy string
}

type childCounts struct {
	categories    int64
	relationships int64
	claims        int64
}

var fileDefinitions = []fileDefinition{
	{name: "entity.parquet", table: "entity", orderBy: "input_order"},
	{name: "entity_category.parquet", table: "entity_category", orderBy: "seed_id, category_order"},
	{name: "relationship.parquet", table: "relationship", orderBy: "seed_id, relationship_order"},
	{name: "claim.parquet", table: "claim", orderBy: "seed_id, claim_order"},
}

func SchemaVersion() int { return schemaVersion }

func WriteModel(ctx context.Context, dir string, entities []*model.Entity) ([]ModelFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create model directory: %w", err)
	}
	for _, definition := range fileDefinitions {
		path := filepath.Join(dir, definition.name)
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("model file already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("check model file %s: %w", path, err)
		}
	}

	db, conn, err := openConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer conn.Close()

	schema, err := sqlFiles.ReadFile("sql/schema.sql")
	if err != nil {
		return nil, fmt.Errorf("read model schema: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
		return nil, fmt.Errorf("create model schema: %w", err)
	}

	counts, err := insertModel(ctx, tx, entities)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model: %w", err)
	}
	committed = true

	files := make([]ModelFile, 0, len(fileDefinitions))
	for _, definition := range fileDefinitions {
		path := filepath.Join(dir, definition.name)
		query := fmt.Sprintf(
			"COPY (SELECT * FROM %s ORDER BY %s) TO ? (FORMAT PARQUET, COMPRESSION ZSTD)",
			definition.table, definition.orderBy,
		)
		if _, err := conn.ExecContext(ctx, query, path); err != nil {
			return nil, fmt.Errorf("write %s: %w", definition.name, err)
		}
		files = append(files, ModelFile{Name: definition.name, Path: path, Rows: counts[definition.table]})
	}
	return files, nil
}

func insertModel(ctx context.Context, tx *sql.Tx, entities []*model.Entity) (map[string]int, error) {
	entityStmt, err := tx.PrepareContext(ctx, `INSERT INTO entity VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare entity insert: %w", err)
	}
	defer entityStmt.Close()
	categoryStmt, err := tx.PrepareContext(ctx, `INSERT INTO entity_category VALUES (?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare category insert: %w", err)
	}
	defer categoryStmt.Close()
	relationshipStmt, err := tx.PrepareContext(ctx, `INSERT INTO relationship VALUES (?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare relationship insert: %w", err)
	}
	defer relationshipStmt.Close()
	claimStmt, err := tx.PrepareContext(ctx, `INSERT INTO claim VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare claim insert: %w", err)
	}
	defer claimStmt.Close()

	counts := map[string]int{
		"entity": len(entities), "entity_category": 0, "relationship": 0, "claim": 0,
	}
	for inputOrder, entity := range entities {
		if entity == nil {
			return nil, fmt.Errorf("entity %d is nil", inputOrder)
		}
		if entity.BucketMin != 0 || entity.BucketMax != 0 {
			return nil, fmt.Errorf("entity %q already has bucket bounds [%d,%d]", entity.SeedID, entity.BucketMin, entity.BucketMax)
		}
		var lon, lat any
		hasPoint := entity.Point != nil
		if hasPoint {
			if len(entity.Point) != 2 {
				return nil, fmt.Errorf("entity %q has point length %d", entity.SeedID, len(entity.Point))
			}
			lon, lat = entity.Point[0], entity.Point[1]
		}
		if _, err := entityStmt.ExecContext(ctx,
			inputOrder, entity.SeedID, entity.Slug, entity.Type, entity.Name, entity.Description,
			entity.T0, entity.T1, entity.Precision, entity.Status, entity.Importance,
			hasPoint, lon, lat, entity.Wikidata, entity.Wikipedia, entity.MediaThumb,
			len(entity.Categories), len(entity.Rel), len(entity.Props),
		); err != nil {
			return nil, fmt.Errorf("insert entity %q: %w", entity.SeedID, err)
		}
	}
	for _, entity := range entities {
		for order, category := range entity.Categories {
			if _, err := categoryStmt.ExecContext(ctx, entity.SeedID, order, category); err != nil {
				return nil, fmt.Errorf("insert category %q[%d]: %w", entity.SeedID, order, err)
			}
			counts["entity_category"]++
		}
		for order, relationship := range entity.Rel {
			if _, err := relationshipStmt.ExecContext(ctx, entity.SeedID, order, relationship.Type, relationship.Target); err != nil {
				return nil, fmt.Errorf("insert relationship %q[%d]: %w", entity.SeedID, order, err)
			}
			counts["relationship"]++
		}
		for order, claim := range entity.Props {
			if !validClaimShape(claim.Value != nil, claim.Min != nil, claim.Max != nil) {
				return nil, fmt.Errorf("claim %q[%d] has invalid value shape", entity.SeedID, order)
			}
			if _, err := claimStmt.ExecContext(ctx,
				entity.SeedID, order, claim.Property, nullableFloat(claim.Value), nullableFloat(claim.Min),
				nullableFloat(claim.Max), claim.Unit, claim.ValueType, claim.Method, claim.Source,
				claim.PublishedAt, claim.Confidence,
			); err != nil {
				return nil, fmt.Errorf("insert claim %q[%d]: %w", entity.SeedID, order, err)
			}
			counts["claim"]++
		}
	}
	return counts, nil
}

func ReadModel(ctx context.Context, dir string) ([]*model.Entity, error) {
	for _, definition := range fileDefinitions {
		path := filepath.Join(dir, definition.name)
		if info, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("read model file %s: %w", definition.name, err)
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("read model file %s: not a regular file", definition.name)
		}
	}

	db, conn, err := openConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer conn.Close()

	entities, byID, expected, err := readEntities(ctx, conn, filepath.Join(dir, "entity.parquet"))
	if err != nil {
		return nil, err
	}
	if err := readCategories(ctx, conn, filepath.Join(dir, "entity_category.parquet"), byID); err != nil {
		return nil, err
	}
	if err := readRelationships(ctx, conn, filepath.Join(dir, "relationship.parquet"), byID); err != nil {
		return nil, err
	}
	if err := readClaims(ctx, conn, filepath.Join(dir, "claim.parquet"), byID); err != nil {
		return nil, err
	}
	for _, entity := range entities {
		counts := expected[entity.SeedID]
		if int64(len(entity.Categories)) != counts.categories {
			return nil, fmt.Errorf("entity %q category count %d, want %d", entity.SeedID, len(entity.Categories), counts.categories)
		}
		if int64(len(entity.Rel)) != counts.relationships {
			return nil, fmt.Errorf("entity %q relationship count %d, want %d", entity.SeedID, len(entity.Rel), counts.relationships)
		}
		if int64(len(entity.Props)) != counts.claims {
			return nil, fmt.Errorf("entity %q claim count %d, want %d", entity.SeedID, len(entity.Props), counts.claims)
		}
	}
	return entities, nil
}

func readEntities(ctx context.Context, conn *sql.Conn, path string) ([]*model.Entity, map[string]*model.Entity, map[string]childCounts, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT input_order, seed_id, slug, type, name, description, t0, t1, precision,
		       status, importance, has_point, point_lon, point_lat, wikidata, wikipedia, media_thumb,
		       category_count, relationship_count, claim_count
		FROM read_parquet(?) ORDER BY input_order`, path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query entities: %w", err)
	}
	defer rows.Close()

	var entities []*model.Entity
	byID := make(map[string]*model.Entity)
	expected := make(map[string]childCounts)
	seenSlugs := make(map[string]bool)
	for rows.Next() {
		var inputOrder int64
		var hasPoint bool
		var lon, lat sql.NullFloat64
		var counts childCounts
		entity := new(model.Entity)
		if err := rows.Scan(
			&inputOrder, &entity.SeedID, &entity.Slug, &entity.Type, &entity.Name, &entity.Description,
			&entity.T0, &entity.T1, &entity.Precision, &entity.Status, &entity.Importance,
			&hasPoint, &lon, &lat, &entity.Wikidata, &entity.Wikipedia, &entity.MediaThumb,
			&counts.categories, &counts.relationships, &counts.claims,
		); err != nil {
			return nil, nil, nil, fmt.Errorf("scan entity: %w", err)
		}
		if inputOrder != int64(len(entities)) {
			return nil, nil, nil, fmt.Errorf("entity input_order %d, want %d", inputOrder, len(entities))
		}
		if _, exists := byID[entity.SeedID]; exists {
			return nil, nil, nil, fmt.Errorf("duplicate entity %q", entity.SeedID)
		}
		if seenSlugs[entity.Slug] {
			return nil, nil, nil, fmt.Errorf("duplicate entity slug %q", entity.Slug)
		}
		if counts.categories < 0 || counts.relationships < 0 || counts.claims < 0 {
			return nil, nil, nil, fmt.Errorf("entity %q has negative child count", entity.SeedID)
		}
		switch {
		case hasPoint && lon.Valid && lat.Valid:
			entity.Point = []float64{lon.Float64, lat.Float64}
		case !hasPoint && !lon.Valid && !lat.Valid:
		default:
			return nil, nil, nil, fmt.Errorf("entity %q has inconsistent point columns", entity.SeedID)
		}
		entities = append(entities, entity)
		byID[entity.SeedID] = entity
		expected[entity.SeedID] = counts
		seenSlugs[entity.Slug] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("read entities: %w", err)
	}
	return entities, byID, expected, nil
}

func readCategories(ctx context.Context, conn *sql.Conn, path string, byID map[string]*model.Entity) error {
	rows, err := conn.QueryContext(ctx, `SELECT seed_id, category_order, category FROM read_parquet(?) ORDER BY seed_id, category_order`, path)
	if err != nil {
		return fmt.Errorf("query categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seedID, category string
		var order int64
		if err := rows.Scan(&seedID, &order, &category); err != nil {
			return fmt.Errorf("scan category: %w", err)
		}
		entity, ok := byID[seedID]
		if !ok {
			return fmt.Errorf("category owner %q is not an entity", seedID)
		}
		if order != int64(len(entity.Categories)) {
			return fmt.Errorf("category %q order %d, want %d", seedID, order, len(entity.Categories))
		}
		entity.Categories = append(entity.Categories, category)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read categories: %w", err)
	}
	return nil
}

func readRelationships(ctx context.Context, conn *sql.Conn, path string, byID map[string]*model.Entity) error {
	rows, err := conn.QueryContext(ctx, `SELECT seed_id, relationship_order, type, target_seed_id FROM read_parquet(?) ORDER BY seed_id, relationship_order`, path)
	if err != nil {
		return fmt.Errorf("query relationships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seedID, relationshipType, target string
		var order int64
		if err := rows.Scan(&seedID, &order, &relationshipType, &target); err != nil {
			return fmt.Errorf("scan relationship: %w", err)
		}
		entity, ok := byID[seedID]
		if !ok {
			return fmt.Errorf("relationship owner %q is not an entity", seedID)
		}
		if byID[target] == nil {
			return fmt.Errorf("relationship %q[%d] targets missing entity %q", seedID, order, target)
		}
		if order != int64(len(entity.Rel)) {
			return fmt.Errorf("relationship %q order %d, want %d", seedID, order, len(entity.Rel))
		}
		entity.Rel = append(entity.Rel, model.SeedRel{Type: relationshipType, Target: target})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read relationships: %w", err)
	}
	return nil
}

func readClaims(ctx context.Context, conn *sql.Conn, path string, byID map[string]*model.Entity) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT seed_id, claim_order, property, value, min, max, unit, value_type,
		       method, source, published_at, confidence
		FROM read_parquet(?) ORDER BY seed_id, claim_order`, path)
	if err != nil {
		return fmt.Errorf("query claims: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seedID string
		var order int64
		var value, minimum, maximum sql.NullFloat64
		claim := model.SeedProp{}
		if err := rows.Scan(
			&seedID, &order, &claim.Property, &value, &minimum, &maximum, &claim.Unit,
			&claim.ValueType, &claim.Method, &claim.Source, &claim.PublishedAt, &claim.Confidence,
		); err != nil {
			return fmt.Errorf("scan claim: %w", err)
		}
		entity, ok := byID[seedID]
		if !ok {
			return fmt.Errorf("claim owner %q is not an entity", seedID)
		}
		if order != int64(len(entity.Props)) {
			return fmt.Errorf("claim %q order %d, want %d", seedID, order, len(entity.Props))
		}
		if !validClaimShape(value.Valid, minimum.Valid, maximum.Valid) {
			return fmt.Errorf("claim %q[%d] has invalid value shape", seedID, order)
		}
		claim.Value = floatPointer(value)
		claim.Min = floatPointer(minimum)
		claim.Max = floatPointer(maximum)
		entity.Props = append(entity.Props, claim)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read claims: %w", err)
	}
	return nil
}

func validClaimShape(hasValue, hasMinimum, hasMaximum bool) bool {
	return hasValue || hasMinimum && hasMaximum
}

func openConnection(ctx context.Context) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, nil, fmt.Errorf("open DuckDB: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("connect DuckDB: %w", err)
	}
	return db, conn, nil
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func floatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}
