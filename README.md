# DeltaView

DeltaView keeps SQL views over Postgres up to date by processing only the rows that changed.

`REFRESH MATERIALIZED VIEW` re-reads every row even when one row changed. DeltaView reads
Postgres's logical replication stream instead, turns each committed transaction into a small
delta, and pushes that delta through the view's query plan. Every operator knows how to turn
an input delta into an output delta, so the work is proportional to the size of the change,
not the size of the table.

```sql
CREATE MATERIALIZED VIEW revenue_by_city AS
SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
FROM orders o JOIN customers c ON o.customer_id = c.id
WHERE o.status = 'paid'
GROUP BY c.city;
```

When a customer moves from Delhi to Mumbai, DeltaView receives `{(7, Delhi): -1, (7, Mumbai): +1}`.
The join turns that into `-old +new` rows for each of the customer's orders, and the aggregate moves
their revenue from Delhi to Mumbai. Nobody wrote "customer changed city" code: it falls out of the
algebra (Z-sets, from the DBSP paper).

It is a single Go process, it does not replace Postgres (Postgres stays the source of truth), and it
supports a fixed, documented subset of SQL. Everything outside the subset is rejected at startup
with an error that names the feature.

## Architecture

```text
                +------------------- Postgres -------------------+
                |  tables -> WAL -> logical decoding (pgoutput)  |
                |  replication slot: remembers our confirmed LSN |
                +-----------------------+------------------------+
                                        |  Relation / Begin / Insert / Update / Delete / Commit
                                        v
+----------------------------- DeltaView (one Go process) -----------------------------+
|  [1] CDC reader  -->  [2] Txn assembler  -->  bounded queue  -->  [3] Apply loop     |
|      decode pgoutput      one Z-set per table                     (single goroutine) |
|      answer keepalives    per transaction                               |            |
|  [0] SQL front end  ---- builds at startup ---->  [4] Circuit, one per view          |
|      parse -> bind -> plan                            Scan -> Filter -> Join         |
|                                                       -> Aggregate -> Project        |
|                                                   [5] State store (Pebble)           |
|                                                       operator state + view rows     |
|                                                       + applied LSN, ONE atomic      |
|                                                       batch per transaction          |
|                                   [6] Ack LSN to Postgres      [7] Serving           |
|                                       only after the commit        GET  /views/NAME  |
|                                                                    SSE  /subscribe   |
|  [8] Metrics and traces: slot lag, apply latency, delta size, state size             |
+--------------------------------------------------------------------------------------+
[9] Differential tester (cmd/dvtest): random transactions into Postgres, then compares
    each view with the same SELECT run directly on Postgres.
```

| Part | Code | What it does |
|---|---|---|
| CDC reader | `internal/cdc` | Replication protocol via pglogrepl, pgoutput decoding, keepalives, slot handling, snapshot reads |
| Z-sets and encoding | `internal/types`, `internal/zset` | Canonical, order-preserving, self-delimiting row encoding; row -> weight maps |
| SQL front end | `internal/sql`, `internal/expr` | Real Postgres parser (pg_query_go), binder, logical plan, evaluator with Postgres semantics |
| Operators | `internal/ops`, `internal/circuit` | Filter, Project, Join, Aggregate, Distinct; one `Step(tx, deltas)` per view |
| State | `internal/state` | Pebble (durable) and memory stores behind one interface |
| Engine | `internal/engine` | Startup, initial snapshot, apply loop, exactly-once, subscriptions, fail points |
| Serving | `internal/serve` | HTTP JSON, server-sent events, the demo page |
| Observability | `internal/obs` | Prometheus metrics, OpenTelemetry spans |

### Exactly-once

The stream is at-least-once: after a crash Postgres re-sends what was not confirmed. The apply
loop makes it effectively exactly-once:

```text
for each transaction:
    if EndLSN <= applied_lsn: skip                 (re-sent duplicate)
    run every circuit inside one state transaction
    write applied_lsn = EndLSN into the same transaction
    commit with fsync                              (the durability point)
    acknowledge EndLSN to Postgres                 (only now)
```

Crash before the commit: nothing was saved and the transaction is re-sent and applied once.
Crash after the commit but before the ack: it is re-sent and skipped. Crash after the ack:
nothing is re-sent. Fail points at all three spots are exercised by `make failpoints`.

## Run it

Requirements: Docker. For development, Go 1.27 and a C compiler (pg_query_go uses cgo; on
Windows use WSL or the Docker image).

**Demo** (Postgres, DeltaView and a load generator):

```sh
docker compose up --build
# open http://localhost:8080 and watch revenue_by_city change live
```

**Development:**

```sh
make up                 # Postgres 17 with wal_level=logical
make build
./bin/deltaview         # reads views.sql, keeps state in ./data, serves :8080
curl localhost:8080/views/revenue_by_city
curl -N localhost:8080/views/revenue_by_city/subscribe
```

Flags: `--views FILE`, `--state-dir DIR`, `--memory`, `--http ADDR`, `--batch-max N` (group commit),
`--reset` (fresh snapshot), `--print-changes`.

### HTTP API

```text
GET /views                    list of views, their SQL and columns
GET /views/NAME               rows plus the LSN they are valid at
GET /views/NAME/subscribe     SSE: a snapshot event with its LSN, then delta events
GET /status                   applied LSN, heartbeat sequence, slot lag
GET /metrics                  Prometheus metrics
GET /                         live demo page (?view=NAME)
```

Every delta carries the LSN it brings the view to. Delivery to subscribers is at-least-once, so
clients drop deltas at or below the LSN they already have. A subscriber that falls behind gets a
`resync` event and is closed; it never slows the pipeline down.

## Supported SQL

- Select list: columns, arithmetic, `CASE`, `COALESCE`, `NULLIF`, casts, `||`, `abs`, `lower`,
  `upper`, `length`, `round`, and `COUNT(*)`, `COUNT(col)`, `SUM`, `AVG`, `MIN`, `MAX`
- `FROM` one table, or `INNER JOIN ... ON` with at least one column equality per join (more joins
  chain; extra non-equality conditions become a filter)
- `WHERE` with `AND`, `OR`, `NOT`, comparisons, `IS [NOT] NULL`, `IN (list)`, `BETWEEN`, `LIKE`,
  `IS [NOT] DISTINCT FROM`, `IS TRUE/FALSE/UNKNOWN`
- `GROUP BY` columns or expressions, `HAVING`, `SELECT DISTINCT`

Rejected with an error that names the feature: `ORDER BY`/`LIMIT` (incremental top-k is its own
problem), outer joins, subqueries, window functions, set operations, CTEs, `now()`/`random()` and
other volatile functions (the answer changes with no row change), `COUNT(DISTINCT ...)`, `FILTER`.

Expressions follow Postgres: three-valued logic, integer division truncating toward zero, overflow
errors, numeric division with Postgres's result scale (`select_div_scale`). 112 expressions are
checked against Postgres itself in `internal/sql/expr_pg_test.go`.

## How correctness is checked

| Layer | What | Command |
|---|---|---|
| Property and fuzz tests | Z-set laws, encode/decode round trip, encoding order | `make check` |
| Operator unit tests | The worked examples from `docs/concepts.md` | `make check` |
| Expressions vs Postgres | Our evaluator against Postgres on the same rows | `make itest` |
| CDC integration | Inserts, updates, NULLs, numerics, timestamps, 100 KB TOAST values, rollbacks, restarts, keepalives | `make itest` |
| Differential tester | Every view equals its SELECT on Postgres after random workloads | `make difftest SEED=n` |
| Mutation checks | Planted bugs must make the tester fail | see `docs/notes.md` |
| Crash test | `kill -9` at random times and at each fail point, then a full check | `make crashtest`, `make failpoints` |
| Snapshot test | Initial snapshot of 1 million rows while a writer keeps committing | `make snapshottest` |

The tester is the only writer to Postgres during a run. After its transactions it bumps
`dv_heartbeat.seq` and waits until DeltaView reports that sequence: changes arrive in commit order,
so everything before the heartbeat has been applied. It then compares each view as a multiset
(rows with counts; sets would hide duplicate-row bugs).

## Measured numbers

MEASURED_NUMBERS_PLACEHOLDER

## Limitations

- Single node, single apply thread: one CPU core applies every view.
- A fixed SQL subset; no outer joins, subqueries, windows or top-k.
- Needs `REPLICA IDENTITY FULL` on source tables, which writes the whole old row to the WAL on
  every update and delete.
- A transaction is held in memory until its commit (pgoutput protocol version 1). A 10-million-row
  transaction is a real memory spike.
- No online schema changes. A changed table stops the stream; the next start notices the new schema
  and takes a fresh snapshot.
- Views are defined at startup. Changing `views.sql` triggers a fresh snapshot.
- Joins store both inputs. The state can be larger than the source tables.
- Text is compared byte by byte, so Postgres must use the C collation for MIN/MAX/`<` on text to agree.
- `SUM`/`AVG` over `float8` accumulate rounding error under deletes (infinities and NaN are
  tracked exactly); `numeric` is exact. `real` (float4) is not supported. Numeric display scale is
  not kept (`150.00` is served as `150`).
- Source tables must be ordinary tables (not partitioned, no inheritance) and fully published:
  every column, no row filter, inserts, updates, deletes and truncates. Checked at startup.
- DDL that keeps the column names and types (for example `ALTER COLUMN ... TYPE` to the same type
  with `USING`, which rewrites the table without logging row changes) is not detected while running.
- The acknowledged LSN only moves when a transaction on a published table commits. On a quiet
  database, update `dv_heartbeat` periodically so the slot does not pin WAL.

## Repository layout

```text
cmd/deltaview  the engine          cmd/dvtest  differential tester     cmd/dvbench  benchmarks
internal/      types zset cdc sql expr ops circuit state engine serve obs
views.sql      the views           db/init.sql demo schema and publication
scripts/       difftest, crash and snapshot tests
docs/          concepts, architecture, design, decisions, milestones, notes
```

Further reading: [docs/concepts.md](docs/concepts.md) (the delta rules with worked examples),
[docs/design.md](docs/design.md), [docs/decisions.md](docs/decisions.md),
[docs/notes.md](docs/notes.md) (bug diary).
