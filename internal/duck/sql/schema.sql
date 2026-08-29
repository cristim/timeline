CREATE TABLE entity (
  input_order BIGINT PRIMARY KEY,
  seed_id VARCHAR NOT NULL UNIQUE,
  slug VARCHAR NOT NULL UNIQUE,
  type VARCHAR NOT NULL,
  name VARCHAR NOT NULL,
  description VARCHAR NOT NULL,
  t0 DOUBLE NOT NULL,
  t1 DOUBLE NOT NULL,
  precision VARCHAR NOT NULL,
  status VARCHAR NOT NULL,
  importance DOUBLE NOT NULL,
  has_point BOOLEAN NOT NULL,
  point_lon DOUBLE,
  point_lat DOUBLE,
  wikidata VARCHAR NOT NULL,
  wikipedia VARCHAR NOT NULL,
  media_thumb VARCHAR NOT NULL,
  category_count BIGINT NOT NULL CHECK (category_count >= 0),
  relationship_count BIGINT NOT NULL CHECK (relationship_count >= 0),
  claim_count BIGINT NOT NULL CHECK (claim_count >= 0),
  CHECK (
    (has_point AND point_lon IS NOT NULL AND point_lat IS NOT NULL) OR
    (NOT has_point AND point_lon IS NULL AND point_lat IS NULL)
  )
);

CREATE TABLE entity_category (
  seed_id VARCHAR NOT NULL REFERENCES entity(seed_id),
  category_order BIGINT NOT NULL,
  category VARCHAR NOT NULL,
  PRIMARY KEY (seed_id, category_order)
);

CREATE TABLE relationship (
  seed_id VARCHAR NOT NULL REFERENCES entity(seed_id),
  relationship_order BIGINT NOT NULL,
  type VARCHAR NOT NULL,
  target_seed_id VARCHAR NOT NULL REFERENCES entity(seed_id),
  PRIMARY KEY (seed_id, relationship_order)
);

CREATE TABLE claim (
  seed_id VARCHAR NOT NULL REFERENCES entity(seed_id),
  claim_order BIGINT NOT NULL,
  property VARCHAR NOT NULL,
  value DOUBLE,
  min DOUBLE,
  max DOUBLE,
  unit VARCHAR NOT NULL,
  value_type VARCHAR NOT NULL,
  method VARCHAR NOT NULL,
  source VARCHAR NOT NULL,
  published_at VARCHAR NOT NULL,
  confidence DOUBLE NOT NULL,
  PRIMARY KEY (seed_id, claim_order),
  CHECK (value IS NOT NULL OR (min IS NOT NULL AND max IS NOT NULL))
);
