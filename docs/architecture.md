# Architecture

```text
                +------------------- Postgres -------------------+
                |  tables -> WAL -> logical decoding (pgoutput)  |
                |  replication slot: remembers our confirmed LSN |
                +-----------------------+------------------------+
                                        |  Relation / Begin / Insert / Update / Delete / Commit
                                        v
+----------------------------- DeltaView (one Go process) -----------------------------+
|                                                                                      |
|  [1] CDC reader  -->  [2] Txn assembler  -->  bounded queue  -->  [3] Apply loop     |
|      decode pgoutput      one Z-set per table                     (single goroutine) |
|      answer keepalives    per transaction                               |            |
|                                                                         v            |
|  [0] SQL front end  ---- builds at startup ---->  [4] Circuit, one per view          |
|      parse -> bind -> plan                            Scan -> Filter -> Join         |
|                                                       -> Aggregate -> Project        |
|                                                                         |            |
|                                                                         v            |
|                                                   [5] State store (Pebble)           |
|                                                       operator state + view rows     |
|                                                       + applied LSN, ONE atomic      |
|                                                       batch per transaction          |
|                                                          |                |          |
|                                                          v                v          |
|                                   [6] Ack LSN to Postgres      [7] Serving           |
|                                       only after the commit        GET  /views/NAME  |
|                                                                    SSE  /subscribe   |
|  [8] Metrics and traces: slot lag, apply latency, delta size, state size             |
+--------------------------------------------------------------------------------------+

[9] Differential tester (separate program): random transactions into Postgres,
    then compares each view with the same SELECT run directly on Postgres.
```

## Components

| # | Component | Job | Why it is needed |
|---|---|---|---|
| 0 | SQL front end | View SQL to plan to circuit | Users write SQL, not operator graphs |
| 1 | CDC reader | Replication protocol, pgoutput decoding | The only input |
| 2 | Txn assembler | Begin..Commit into one batch of deltas | Views never show half a transaction |
| 3 | Apply loop | Runs one transaction through all circuits, commits | Single writer: no locks, fixed order |
| 4 | Circuit | Incremental operators wired together | Work proportional to the change |
| 5 | State store | Operator state, view rows, applied LSN | Survives restarts; atomic batch gives exactly-once |
| 6 | Ack | Tells Postgres which WAL it may delete | Otherwise the Postgres disk fills |
| 7 | Serving | Read a view, subscribe to changes | A view nobody can read is useless |
| 8 | Observability | Lag, latency, sizes | Needed to benchmark and debug |
| 9 | Differential tester | Proves the view equals the real query | Incremental logic is easy to get subtly wrong |

## Life of one change

1. An app runs `UPDATE orders SET status = 'paid' WHERE id = 42` and commits.
2. At commit, logical decoding emits Begin, Update (old and new row), Commit.
3. The assembler builds a batch: orders -> {old: -1, new: +1}, with the end LSN.
4. The batch waits in a bounded queue. Full queue: the reader stops reading and Postgres keeps the WAL. That is backpressure.
5. The apply loop opens one state transaction.
6. Filter drops the old row (pending) and passes the new row (paid) with +1.
7. Join looks up customer 7 in the customer arrangement and emits the joined row. The order arrangement is updated.
8. Aggregate reads Delhi's state, adds the amount, emits -old and +new.
9. The view store applies that delta. applied_lsn is written in the same batch.
10. The batch commits with fsync. Only now is the LSN acknowledged.
11. Subscribers receive {lsn, changes}. Slow subscribers are dropped, never waited for.

## Concurrency

- One goroutine reads from Postgres.
- One goroutine, the apply loop, owns all state. Nothing else writes.
- HTTP handlers read from consistent store snapshots.
