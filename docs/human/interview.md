# Interview prep

## The 60-second pitch
"Materialized views in Postgres are refreshed by re-running the whole query. I built an engine
that reads Postgres's replication stream, turns each transaction into a delta, and pushes it
through incremental operators, so the cost depends on the change size, not the table size. It
supports filters, joins and aggregates, recovers from crashes with exactly-once semantics, and
I check it against Postgres itself with a randomized differential tester."

## Question bank
| Question | Answer in brief |
|---|---|
| What is IVM in one sentence? | A materialized view that updates by processing only changed rows |
| Why not REFRESH MATERIALIZED VIEW? | It re-runs the whole query. CONCURRENTLY avoids blocking readers but still recomputes everything |
| Why not triggers? | They run inside the user's write transaction. pg_ivm does this and gets an always-current view; I traded milliseconds of lag for zero write-path cost |
| Is the view consistent? | It always equals the query at some real commit point, the applied LSN. It lags slightly but is never torn |
| Derive the join delta | Expand (A + dA) JOIN (B + dB) and subtract A JOIN B |
| MIN after its value is deleted? | Ordered multiset per group; the next key is the new minimum |
| Crash after commit, before ack? | Postgres re-sends; the LSN is not newer than the saved one, so it is skipped |
| Crash before commit? | Nothing was saved; the re-sent transaction is applied once |
| Postgres disk is filling. Why? | The slot pins WAL while the consumer is behind or dead. Monitor slot lag; cap with max_slot_wal_keep_size; re-snapshot if the slot is lost |
| How does the initial load avoid gaps? | Exported snapshot at the slot's consistent point, then stream from that point |
| Why Z-sets, not insert and delete events? | One algebra for all changes; operators compose; no special cases for duplicates and updates |
| How big is the state? | Aggregates: one entry per group. Joins: both inputs. It can exceed the source. Store only needed columns |
| Scale past one core? | Hash-partition by key, exchange operators, barrier checkpoints. Skewed keys become the hard part |
| A transaction changes 10 million rows? | It arrives as one burst at commit and my batch holds it in memory. A real limit; the fix is the streaming protocol version or chunked steps |
| A column is added? | DDL is not replicated. I detect the new schema message and stop with a clear error |
| Why a single apply thread? | No locks, deterministic order, simple recovery. Limit: one core |
| How do you know it is correct? | Differential tests against Postgres, property tests, crash tests, and planted bugs that prove the tester works |
| Do subscribers get exactly-once? | No. At-least-once with an LSN on each delta; clients deduplicate |
| Why no ORDER BY ... LIMIT? | Incremental top-k needs its own state design. Clients sort |
| Why no now() in views? | The answer would change with no row change, so nothing triggers an update |
| Compared with Materialize or Feldera? | Same core idea. They are far more complete; mine is a small single-node version I can explain end to end |
| What did AI write and what did you do? | Claude Code wrote most of the code. I chose the design, wrote invariants and tests first, reviewed every diff, and can describe bugs the tester caught |
| What would you change? | Pick two limitations below and say how you would fix them |

## Limitations to state yourself
- Single node, single apply thread.
- A fixed SQL subset; no outer joins, subqueries or windows.
- Needs REPLICA IDENTITY FULL, which increases WAL volume.
- Very large transactions are held in memory.
- No online schema changes.
- Views are defined at startup; adding one needs a re-snapshot.

## Bug diary
Each time the tester catches a bug, write three lines in docs/notes.md: symptom, root cause, fix.
Two or three of these stories are your strongest interview material.

## Resume bullets (fill the placeholders with measured numbers)
- Built DeltaView, an incremental view maintenance engine over Postgres logical replication (Go, [N]k LOC) that keeps SQL views with joins and aggregates fresh by processing only changed rows.
- Implemented Z-set based incremental filter, join and aggregate operators; applied [N] changes to a view over [M] million rows in [X] ms versus [Y] s for REFRESH MATERIALIZED VIEW.
- Achieved effectively exactly-once processing by committing operator state and WAL position atomically; passed [N] random kill -9 cycles with zero divergence.
- Verified correctness with a differential tester against Postgres across [N] million random transactions and [K] planted-bug checks; p99 commit-to-view latency [Z] ms.
