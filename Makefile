# Timeline - local dev (spec 11, DEV-1..6).
# Native baker runs (go run) target the compose MinIO for a tight loop;
# `make bake-docker` runs the same job through the baker image for parity.

S3_ENV = S3_ENDPOINT=http://localhost:9000 S3_FORCE_PATH_STYLE=true \
         AWS_ACCESS_KEY_ID=wkadmin AWS_SECRET_ACCESS_KEY=wk-dev-minio AWS_REGION=us-east-1

.PHONY: up down bake bake-full bake-docker fetch-wikidata fetch-geo verify-geo census smoke test vet fmt web-install e2e e2e-static

up:
	docker compose up -d minio minio-init gateway web

down:
	docker compose down

bake:
	$(S3_ENV) go run ./cmd/baker bake --seed data/seed

bake-full:
	$(S3_ENV) go run ./cmd/baker bake --seed data/seed --warm

fetch-wikidata:
	$(S3_ENV) go run ./cmd/baker fetch-wikidata

# The two big map layers are derived from pinned upstreams, not committed.
# Run once after cloning; re-run only when a pin moves. CI caches the result
# on `baker geo-fingerprint`.
fetch-geo:
	go run ./cmd/baker fetch-borders
	go run ./cmd/baker fetch-paleo

# Fails unless both layers tile their whole range - how a partial or stale
# fetch is caught before a bake trusts it.
verify-geo:
	go run ./cmd/baker geo-verify

census:
	$(S3_ENV) go run ./cmd/baker census

bake-docker:
	docker compose run --rm baker bake --seed /seed --geo /geo --goldens /goldens.json

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal

web-install:
	cd web && npm install

# Browser verification against the prod-parity gateway (playwright-verify).
e2e:
	cd web && npx playwright test

# Same suite against the built static artifact (what GitHub Pages serves).
# verify-geo first, so the suite exercises the real layers rather than
# passing against an atlas the fetch left half-empty.
e2e-static: verify-geo
	cd web && VITE_BASE=/timeline/ npm run build
	docker build --tag world-knowledge-baker:e2e .
	docker run --rm --user "$$(id -u):$$(id -g)" --volume "$(CURDIR)/data/seed:/seed:ro" --volume "$(CURDIR)/data/geo:/geo:ro" --volume "$(CURDIR)/data/goldens.json:/goldens.json:ro" --volume "$(CURDIR)/web/dist:/out" world-knowledge-baker:e2e bake --seed /seed --geo /geo --goldens /goldens.json --out /out
	cd web && VITE_BASE=/timeline/ E2E_STATIC=1 E2E_BASE_URL=http://localhost:4173/timeline/ npx playwright test

smoke:
	./scripts/smoke.sh
