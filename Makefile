# Everything Timeline - local dev (spec 11, DEV-1..6).
# Native baker runs (go run) target the compose MinIO for a tight loop;
# `make bake-docker` runs the same job through the baker image for parity.

S3_ENV = S3_ENDPOINT=http://localhost:9000 S3_FORCE_PATH_STYLE=true \
         AWS_ACCESS_KEY_ID=wkadmin AWS_SECRET_ACCESS_KEY=wk-dev-minio AWS_REGION=us-east-1

.PHONY: up down bake bake-full bake-docker fetch-wikidata census smoke test vet fmt web-install e2e e2e-static

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

census:
	$(S3_ENV) go run ./cmd/baker census

bake-docker:
	docker compose run --rm baker bake --seed /seed

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
e2e-static:
	cd web && VITE_BASE=/everything-timeline/ npm run build
	go run ./cmd/baker bake --seed data/seed --out web/dist
	cd web && E2E_STATIC=1 E2E_BASE_URL=http://localhost:4173/everything-timeline/ npx playwright test

smoke:
	./scripts/smoke.sh
