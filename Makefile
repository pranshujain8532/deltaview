PG_URL ?= postgres://deltaview:deltaview@localhost:5432/deltaview?sslmode=disable
export DV_PG_URL ?= $(PG_URL)

SEED ?= 1
SEEDS ?= $(SEED)
TXNS ?= 1000
CHECK_EVERY ?= 10
CYCLES ?= 200
ROWS ?= 1000000
RUNS ?= 20

.PHONY: up down psql check itest build fmt difftest difftest50 crashtest failpoints snapshottest bench demo

## up: start Postgres with wal_level=logical and wait until it is healthy
up:
	docker compose up -d --wait postgres
	docker compose exec -T postgres psql -U deltaview -d deltaview -c "SHOW wal_level"

## down: stop everything and delete the Postgres volume
down:
	docker compose down -v

psql:
	docker compose exec postgres psql -U deltaview -d deltaview

## check: formatting, vet and unit tests (no database needed)
check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
	go vet ./...
	go test ./...

## itest: integration tests against the Docker Postgres (make up first)
itest:
	go test -tags integration -count=1 -p 1 ./...

build:
	go build -o bin/ ./cmd/...

fmt:
	gofmt -w .

## difftest: differential test, e.g. make difftest SEED=3 or SEEDS="1 2 3" TXNS=500
difftest:
	bash scripts/difftest.sh "$(SEEDS)" $(TXNS) $(CHECK_EVERY)

## difftest50: the milestone bar, 50 seeds of 1,000 transactions
difftest50:
	bash scripts/difftest.sh "$$(seq -s ' ' 1 50)" 1000 $(CHECK_EVERY)

## crashtest: kill -9 the engine CYCLES times under load, then a full check
crashtest:
	bash scripts/crashtest.sh kill $(CYCLES)

## failpoints: each fail point on its own, then a full check
failpoints:
	bash scripts/crashtest.sh failpoint before_commit 30
	bash scripts/crashtest.sh failpoint after_commit 30
	bash scripts/crashtest.sh failpoint after_ack 30

## snapshottest: initial snapshot of ROWS orders with a live writer, RUNS times
snapshottest:
	bash scripts/snapshottest.sh $(ROWS) $(RUNS)

## bench: run every benchmark in docs/benchmarks.md and write CSV to bench/results
bench:
	go run ./cmd/dvbench

## demo: Postgres, DeltaView and a load generator; open http://localhost:8080
demo:
	docker compose --profile demo up --build
