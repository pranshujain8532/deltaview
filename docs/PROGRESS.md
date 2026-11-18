# Progress

Keep this file under 60 lines. Move old detail out; git history keeps it.

Active milestone: none (M0 to M10 done; stretch goals 1 and 2 done)
Status: complete, all checks green

## Done
- M0 skeleton, Compose Postgres (wal_level=logical), CI
- M1 CDC reader: pgoutput decoding, txn assembly, keepalives, TOAST, slots
- M2 Value/Row encoding and Z-sets, property and fuzz tests
- M3 filter/project, circuit, apply loop, HTTP; differential tester
- M4 aggregates: COUNT/SUM/AVG with NULL rules, MIN/MAX multisets, global aggregates
- M5 SQL front end (pg_query_go), binder, planner, evaluator tested against Postgres
- M6 inner equi-joins (multi-way, self-join), arrangements
- M7 initial snapshot from the slot's exported snapshot
- M8 Pebble store, exactly-once apply loop, fail points, crash test, lost-slot re-snapshot
- M9 GET views from snapshots, SSE subscriptions registered on the apply loop
- M10 Prometheus metrics, OpenTelemetry spans, dvbench, demo page, README
- Stretch: HAVING, DISTINCT, group commit (--batch-max)

## Next step
Stretch goals 3 to 5 (CREATE VIEW at runtime, Postgres wire protocol, MCP server).

## Open problems
- SUM/AVG over float8 accumulate rounding error under deletes (numeric is exact).
- Numeric display scale is not kept (150.00 is served as 150).
- A transaction is held in memory until its commit arrives (proto_version 1).

## Suggested entries for docs/decisions.md
- Text compared byte by byte; Postgres runs with the C collation | linguistic collations | MIN/MAX/< on text must match Postgres exactly
- Schema change mid-stream stops the engine; at startup a changed fingerprint (views + schemas) re-snapshots | always stop | the restart is the safe recovery and needs no manual step
- Acknowledge only the durable applied LSN, never a keepalive's WAL end | advance on idle keepalives | simpler proof; the heartbeat table keeps the slot moving
- Join key columns compared by kind (int2/int4/int8 share one encoding) | per-type encodings | equal keys of different int widths must match

## Measured numbers
See README "Measured numbers" and bench/results/*.csv (machine and date in each file).
- difftest: 50 seeds x 1,000 txns, 14 views, 101 checks per seed: 0 mismatches
- crash test: 200 kill -9 cycles (31,389 writer txns) then clean check; each fail point 30/30
- expression tests vs Postgres: 112 expressions x 6 rows, all equal
