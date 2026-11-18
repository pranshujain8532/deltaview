# Decisions

Mine to maintain. One line per choice: what, the alternative, why.

| Decision | Chosen | Alternative | Why |
|---|---|---|---|
| Where to compute | Separate process reading the WAL | Triggers in Postgres (pg_ivm) | No work in the user's write transaction; no extension needed. Cost: milliseconds of lag |
| Change format | pgoutput | wal2json; Debezium plus Kafka | Built in. Debezium adds two systems |
| Old row values | REPLICA IDENTITY FULL | Own copy of each table by primary key | Simpler. Cost: more WAL per update and delete |
| Unit of work | One Postgres transaction = one step | Row at a time; several transactions per step | The view always equals the query at a real commit point |
| Threading | Single apply goroutine | Parallel operators | No locks, deterministic. Limit: one core |
| State | Embedded key-value store (Pebble) | Memory only; state in Postgres | Atomic local batch gives exactly-once. Memory only means re-snapshot on restart |
| Checkpointing | State and LSN in one batch per transaction | Barrier snapshots (Flink style) | One process, one store. Barriers are for operators on different machines |
| SQL parsing | Real Postgres parser (pg_query_go) | Hand-written parser | Same grammar as Postgres. Cost: cgo |
| SQL scope | Fixed subset | All of SQL | Every feature needs a proven incremental rule |
| Slow subscribers | Drop and make them resync | Block; unbounded buffer | One slow client must not stall all views |
