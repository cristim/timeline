# Everything Timeline - local dev (spec 11, DEV-1..6).
# Native baker runs (go run) target the compose MinIO for a tight loop;
# `make bake-docker` runs the same job through the baker image for parity.

S3_ENV = S3_ENDPOINT=http://localhost:9000 S3_FORCE_PATH_STYLE=true \
         AWS_ACCESS_KEY_ID=wkadmin AWS_SECRET_ACCESS_KEY=wk-dev-minio AWS_REGION=us-east-1

.PHONY: up down bake bake-docker publish smoke test vet fmt web-install

up:
	docker compose up -d minio minio-init gateway web

down:
	docker compose down

bake:
	$(S3_ENV) go run ./cmd/baker bake --seed data/seed

bake-docker:
	docker compose run --rm baker bake --seed /seed

publish:
	$(S3_ENV) go run ./cmd/baker publish

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal

web-install:
	cd web && npm install

smoke:
	./scripts/smoke.sh
