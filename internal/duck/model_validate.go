package duck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"wk/internal/ingest"
	"wk/internal/model"
)

func validateModelPaths(ctx context.Context, paths map[string]string, report *ingest.WikidataDumpImportReport) (_ map[string]int64, err error) {
	db, conn, err := openConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, conn.Close(), db.Close()) }()

	counts := make(map[string]int64, len(fileDefinitions)+1)
	if counts["entity.parquet"], err = validateEntityRows(ctx, conn, paths["entity.parquet"], report != nil); err != nil {
		return nil, err
	}
	if err := validateUniqueEntityColumn(ctx, conn, paths["entity.parquet"], "seed_id"); err != nil {
		return nil, err
	}
	if err := validateUniqueEntityColumn(ctx, conn, paths["entity.parquet"], "slug"); err != nil {
		return nil, err
	}
	if counts["entity_category.parquet"], err = validateCategoryRows(ctx, conn, paths["entity_category.parquet"]); err != nil {
		return nil, err
	}
	if err := validateChildOwners(ctx, conn, paths["entity.parquet"], paths["entity_category.parquet"], "category"); err != nil {
		return nil, err
	}
	if err := validateChildCounts(ctx, conn, paths["entity.parquet"], paths["entity_category.parquet"], "category", "category_count"); err != nil {
		return nil, err
	}
	if counts["relationship.parquet"], err = validateRelationshipRows(ctx, conn, paths["relationship.parquet"]); err != nil {
		return nil, err
	}
	if err := validateChildOwners(ctx, conn, paths["entity.parquet"], paths["relationship.parquet"], "relationship"); err != nil {
		return nil, err
	}
	if err := validateChildCounts(ctx, conn, paths["entity.parquet"], paths["relationship.parquet"], "relationship", "relationship_count"); err != nil {
		return nil, err
	}
	if err := validateRelationshipTargets(ctx, conn, paths["entity.parquet"], paths["relationship.parquet"]); err != nil {
		return nil, err
	}
	if counts["claim.parquet"], err = validateClaimRows(ctx, conn, paths["claim.parquet"]); err != nil {
		return nil, err
	}
	if err := validateChildOwners(ctx, conn, paths["entity.parquet"], paths["claim.parquet"], "claim"); err != nil {
		return nil, err
	}
	if err := validateChildCounts(ctx, conn, paths["entity.parquet"], paths["claim.parquet"], "claim", "claim_count"); err != nil {
		return nil, err
	}
	if rejectPath := paths["reject.parquet"]; rejectPath != "" {
		if counts["reject.parquet"], err = validateRejectRows(ctx, conn, rejectPath, report); err != nil {
			return nil, err
		}
		if report != nil {
			if err := validateReportModelBindings(ctx, conn, paths, *report); err != nil {
				return nil, err
			}
		}
	}
	return counts, nil
}

func validateReportModelBindings(ctx context.Context, conn *sql.Conn, paths map[string]string, report ingest.WikidataDumpImportReport) error {
	typeRows, err := conn.QueryContext(ctx, `SELECT type, count(*) FROM read_parquet(?) GROUP BY type ORDER BY type`, paths["entity.parquet"])
	if err != nil {
		return fmt.Errorf("query committed entity types: %w", err)
	}
	typeIndex := 0
	for typeRows.Next() {
		var entityType string
		var count int
		if err := typeRows.Scan(&entityType, &count); err != nil {
			return errors.Join(fmt.Errorf("scan committed entity types: %w", err), typeRows.Close())
		}
		if typeIndex >= len(report.AcceptedByType) || report.AcceptedByType[typeIndex].Type != entityType || report.AcceptedByType[typeIndex].Count != count {
			return errors.Join(fmt.Errorf("committed entity type %q count %d does not match import report", entityType, count), typeRows.Close())
		}
		typeIndex++
	}
	if err := typeRows.Err(); err != nil {
		return errors.Join(fmt.Errorf("read committed entity types: %w", err), typeRows.Close())
	}
	if err := typeRows.Close(); err != nil {
		return fmt.Errorf("close committed entity types: %w", err)
	}
	if typeIndex != len(report.AcceptedByType) {
		return fmt.Errorf("committed entity types omit import report rows")
	}

	reasonRows, err := conn.QueryContext(ctx, `SELECT reason, count(*) AS count FROM read_parquet(?) GROUP BY reason ORDER BY count DESC, reason`, paths["reject.parquet"])
	if err != nil {
		return fmt.Errorf("query committed reject reasons: %w", err)
	}
	reasonIndex := 0
	for reasonRows.Next() {
		var reason string
		var count int
		if err := reasonRows.Scan(&reason, &count); err != nil {
			return errors.Join(fmt.Errorf("scan committed reject reasons: %w", err), reasonRows.Close())
		}
		if reasonIndex >= len(report.RejectReasons) || report.RejectReasons[reasonIndex].Reason != reason || report.RejectReasons[reasonIndex].Count != count {
			return errors.Join(fmt.Errorf("committed reject reason %q count %d does not match import report", reason, count), reasonRows.Close())
		}
		reasonIndex++
	}
	if err := reasonRows.Err(); err != nil {
		return errors.Join(fmt.Errorf("read committed reject reasons: %w", err), reasonRows.Close())
	}
	if err := reasonRows.Close(); err != nil {
		return fmt.Errorf("close committed reject reasons: %w", err)
	}
	if reasonIndex != len(report.RejectReasons) {
		return fmt.Errorf("committed reject reasons omit import report rows")
	}
	return nil
}

func validateEntityRows(ctx context.Context, conn *sql.Conn, path string, requireWikidataIdentity bool) (_ int64, err error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT input_order, seed_id, slug, type, name, description, t0, t1, precision,
		       status, importance, has_point, point_lon, point_lat, wikidata, wikipedia, media_thumb,
		       category_count, relationship_count, claim_count
		FROM read_parquet(?) ORDER BY input_order`, path)
	if err != nil {
		return 0, fmt.Errorf("query entities: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var count int64
	for rows.Next() {
		var inputOrder int64
		var hasPoint bool
		var lon, lat sql.NullFloat64
		var child childCounts
		entity := new(model.Entity)
		if err := rows.Scan(
			&inputOrder, &entity.SeedID, &entity.Slug, &entity.Type, &entity.Name, &entity.Description,
			&entity.T0, &entity.T1, &entity.Precision, &entity.Status, &entity.Importance,
			&hasPoint, &lon, &lat, &entity.Wikidata, &entity.Wikipedia, &entity.MediaThumb,
			&child.categories, &child.relationships, &child.claims,
		); err != nil {
			return count, fmt.Errorf("scan entity: %w", err)
		}
		if inputOrder != count {
			return count, fmt.Errorf("entity input_order %d, want %d", inputOrder, count)
		}
		if entity.Slug == "" {
			return count, fmt.Errorf("entity %q has empty slug", entity.SeedID)
		}
		if requireWikidataIdentity && !ingest.ValidWikidataEntityIdentity(entity.SeedID, entity.Wikidata) {
			return count, fmt.Errorf("entity %q has invalid Wikidata identity %q", entity.SeedID, entity.Wikidata)
		}
		if child.categories == 0 {
			return count, fmt.Errorf("validate committed entity %q: no categories", entity.SeedID)
		}
		if child.categories < 0 || child.relationships < 0 || child.claims < 0 {
			return count, fmt.Errorf("entity %q has invalid child count", entity.SeedID)
		}
		switch {
		case hasPoint && lon.Valid && lat.Valid:
			entity.Point = []float64{lon.Float64, lat.Float64}
		case !hasPoint && !lon.Valid && !lat.Valid:
		default:
			return count, fmt.Errorf("entity %q has inconsistent point columns", entity.SeedID)
		}
		if reason := ingest.ValidateEntityScalar(entity); reason != "" {
			return count, fmt.Errorf("validate committed entity %q: %s", entity.SeedID, reason)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("read entities: %w", err)
	}
	return count, nil
}

func validateUniqueEntityColumn(ctx context.Context, conn *sql.Conn, path, column string) error {
	query := fmt.Sprintf(`SELECT %s FROM read_parquet(?) GROUP BY %s HAVING count(*) > 1 LIMIT 1`, column, column)
	var value string
	err := conn.QueryRowContext(ctx, query, path).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("validate unique entity %s: %w", column, err)
	}
	if column == "slug" {
		return fmt.Errorf("duplicate entity slug %q", value)
	}
	return fmt.Errorf("duplicate entity %q", value)
}

func validateCategoryRows(ctx context.Context, conn *sql.Conn, path string) (_ int64, err error) {
	rows, err := conn.QueryContext(ctx, `SELECT seed_id, category_order, category FROM read_parquet(?) ORDER BY seed_id, category_order`, path)
	if err != nil {
		return 0, fmt.Errorf("query categories: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var count, order int64
	var previous string
	for rows.Next() {
		var seedID, category string
		var gotOrder int64
		if err := rows.Scan(&seedID, &gotOrder, &category); err != nil {
			return count, fmt.Errorf("scan category: %w", err)
		}
		if seedID != previous {
			previous, order = seedID, 0
		}
		if gotOrder != order {
			return count, fmt.Errorf("category %q order %d, want %d", seedID, gotOrder, order)
		}
		if reason := ingest.ValidateEntityCategory(category); reason != "" {
			return count, fmt.Errorf("validate category %q[%d]: %s", seedID, gotOrder, reason)
		}
		order++
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("read categories: %w", err)
	}
	return count, nil
}

func validateRelationshipRows(ctx context.Context, conn *sql.Conn, path string) (_ int64, err error) {
	rows, err := conn.QueryContext(ctx, `SELECT seed_id, relationship_order, type, target_seed_id FROM read_parquet(?) ORDER BY seed_id, relationship_order`, path)
	if err != nil {
		return 0, fmt.Errorf("query relationships: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var count, order int64
	var previous string
	for rows.Next() {
		var seedID string
		var gotOrder int64
		relationship := model.SeedRel{}
		if err := rows.Scan(&seedID, &gotOrder, &relationship.Type, &relationship.Target); err != nil {
			return count, fmt.Errorf("scan relationship: %w", err)
		}
		if seedID != previous {
			previous, order = seedID, 0
		}
		if gotOrder != order {
			return count, fmt.Errorf("relationship %q order %d, want %d", seedID, gotOrder, order)
		}
		if reason := ingest.ValidateEntityRelationship(relationship); reason != "" {
			return count, fmt.Errorf("validate relationship %q[%d]: %s", seedID, gotOrder, reason)
		}
		order++
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("read relationships: %w", err)
	}
	return count, nil
}

func validateClaimRows(ctx context.Context, conn *sql.Conn, path string) (_ int64, err error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT seed_id, claim_order, property, value, min, max, unit, value_type,
		       method, source, published_at, confidence
		FROM read_parquet(?) ORDER BY seed_id, claim_order`, path)
	if err != nil {
		return 0, fmt.Errorf("query claims: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var count, order int64
	var previous string
	for rows.Next() {
		var seedID string
		var gotOrder int64
		var value, minimum, maximum sql.NullFloat64
		claim := model.SeedProp{}
		if err := rows.Scan(
			&seedID, &gotOrder, &claim.Property, &value, &minimum, &maximum, &claim.Unit,
			&claim.ValueType, &claim.Method, &claim.Source, &claim.PublishedAt, &claim.Confidence,
		); err != nil {
			return count, fmt.Errorf("scan claim: %w", err)
		}
		if seedID != previous {
			previous, order = seedID, 0
		}
		if gotOrder != order {
			return count, fmt.Errorf("claim %q order %d, want %d", seedID, gotOrder, order)
		}
		claim.Value = floatPointer(value)
		claim.Min = floatPointer(minimum)
		claim.Max = floatPointer(maximum)
		if !validClaimShape(value.Valid, minimum.Valid, maximum.Valid) {
			return count, fmt.Errorf("claim %q[%d] has invalid value shape", seedID, gotOrder)
		}
		if reason := ingest.ValidateEntityProperty(claim); reason != "" {
			return count, fmt.Errorf("validate claim %q[%d]: %s", seedID, gotOrder, reason)
		}
		order++
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("read claims: %w", err)
	}
	return count, nil
}

func validateRejectRows(ctx context.Context, conn *sql.Conn, path string, report *ingest.WikidataDumpImportReport) (_ int64, err error) {
	rows, err := conn.QueryContext(ctx, `SELECT source, file, line, reason FROM read_parquet(?)`, path)
	if err != nil {
		return 0, fmt.Errorf("query rejects: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var count int64
	var previousSource, previousFile, previousReason string
	var previousLine int64
	for rows.Next() {
		var source, file, reason string
		var line int64
		if err := rows.Scan(&source, &file, &line, &reason); err != nil {
			return count, fmt.Errorf("scan reject: %w", err)
		}
		if source == "" || file == "" || reason == "" || line < 0 {
			return count, fmt.Errorf("reject row %d has invalid source, file, line, or reason", count)
		}
		if report != nil && (source != string(ingest.RejectSourceWikidataDump) || file != report.Source) {
			return count, fmt.Errorf("reject row %d has source/file %q/%q, want %q/%q", count, source, file, ingest.RejectSourceWikidataDump, report.Source)
		}
		if report != nil && (line >= int64(report.Items) || count > 0 && line <= previousLine) {
			return count, fmt.Errorf("reject row %d has duplicate, unordered, or out-of-range input line %d", count, line)
		}
		if count > 0 && rejectTupleLess(source, file, line, reason, previousSource, previousFile, previousLine, previousReason) {
			return count, fmt.Errorf("reject row %d is out of deterministic order", count)
		}
		previousSource, previousFile, previousLine, previousReason = source, file, line, reason
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("read rejects: %w", err)
	}
	return count, nil
}

func rejectTupleLess(source, file string, line int64, reason, previousSource, previousFile string, previousLine int64, previousReason string) bool {
	switch {
	case source != previousSource:
		return source < previousSource
	case file != previousFile:
		return file < previousFile
	case line != previousLine:
		return line < previousLine
	default:
		return reason < previousReason
	}
}

func validateChildCounts(ctx context.Context, conn *sql.Conn, entityPath, childPath, table, countColumn string) error {
	query := fmt.Sprintf(`
		WITH entities AS (SELECT seed_id, %s AS expected FROM read_parquet(?)),
		     children AS (SELECT seed_id, count(*) AS actual FROM read_parquet(?) GROUP BY seed_id)
		SELECT entities.seed_id, entities.expected, coalesce(children.actual, 0)
		FROM entities LEFT JOIN children USING (seed_id)
		WHERE entities.expected != coalesce(children.actual, 0)
		LIMIT 1`, countColumn)
	var seedID string
	var expected, actual int64
	err := conn.QueryRowContext(ctx, query, entityPath, childPath).Scan(&seedID, &expected, &actual)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("validate %s counts: %w", table, err)
	}
	return fmt.Errorf("entity %q %s count %d, want %d", seedID, table, actual, expected)
}

func validateChildOwners(ctx context.Context, conn *sql.Conn, entityPath, childPath, table string) error {
	var seedID string
	err := conn.QueryRowContext(ctx, `
		SELECT children.seed_id
		FROM read_parquet(?) children
		LEFT JOIN read_parquet(?) entities USING (seed_id)
		WHERE entities.seed_id IS NULL LIMIT 1`, childPath, entityPath).Scan(&seedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("validate %s owners: %w", table, err)
	}
	return fmt.Errorf("%s owner %q is not an entity", table, seedID)
}

func validateRelationshipTargets(ctx context.Context, conn *sql.Conn, entityPath, relationshipPath string) error {
	var seedID, target string
	var order int64
	err := conn.QueryRowContext(ctx, `
		SELECT relationships.seed_id, relationships.relationship_order, relationships.target_seed_id
		FROM read_parquet(?) relationships
		LEFT JOIN read_parquet(?) entities ON entities.seed_id = relationships.target_seed_id
		WHERE entities.seed_id IS NULL LIMIT 1`, relationshipPath, entityPath).Scan(&seedID, &order, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("validate relationship targets: %w", err)
	}
	return fmt.Errorf("relationship %q[%d] targets missing entity %q", seedID, order, target)
}
