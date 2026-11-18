PG_URL ?= postgres://deltaview:deltaview@localhost:5432/deltaview?sslmode=disable
export DV_PG_URL ?= $(PG_URL)

SEED ?= 1

.PHONY: up down psql check itest build fmt

## up: start Postgres with wal_level=logical and wait until it is healthy
up:
	docker compose up -d --wait postgres
	docker compose exec -T postgres psql -U deltaview -d deltaview -c "SHOW wal_level"

## down: stop Postgres and delete its volume
down:
	docker compose down -v

psql:
	docker compose exec postgres psql -U deltaview -d deltaview

## check: formatting, vet and unit tests (no database needed)
check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
	go vet ./...
	go test ./...

build:
	go build -o bin/ ./cmd/...

fmt:
	gofmt -w .

## itest: integration tests against the Docker Postgres (make up first)
itest:
	go test -tags integration -count=1 ./...
