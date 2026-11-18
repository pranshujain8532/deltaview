# DeltaView: Implementation Plan and Learning Guide

DeltaView keeps SQL views over Postgres up to date by processing only the rows that changed.
This file holds the build plan, the architecture, the Claude Code prompts, and the explanations
you need to defend every part in an interview.

Contents

1. How to use this guide
2. The project in one minute
3. Concepts you must own
4. Architecture
5. Design decisions and trade-offs
6. Tech stack and repo layout
7. Low-level design
8. Working with Claude Code
9. Milestones M0 to M10 (with prompts)
10. Testing strategy
11. Benchmarks and resume bullets
12. Interview prep
13. Timeline
14. Glossary
15. References

---

## 1. How to use this guide

Build one milestone at a time, in order. For each milestone:

1. Read its "Why it exists" and "How it works" notes before touching code.
2. Paste the prompt into Claude Code. Make it plan first and wait for your OK.
3. Read every diff. If you cannot explain a line, ask Claude Code to explain it, then rewrite the explanation in your own words in `docs/notes.md`.
4. Run the "Definition of done" checks yourself.
5. Answer the "Explain it back" questions aloud, without notes. These are your interview answers.

Three rules that protect you:

- **The differential tester is the judge.** It compares DeltaView's answer with Postgres's own answer for the same query. If they differ, DeltaView is wrong.
- **Never let Claude Code weaken, skip or delete a test to make it pass.**
- **Keep a decision log** in `docs/decisions.md`: one line per choice (what, why, what you rejected). Interviewers ask "why did you do it this way" more than anything else.

---

## 2. The project in one minute

**One-line pitch:** DeltaView is a materialized view that updates itself by processing only the changed rows, instead of re-running the whole query.

**The problem.** Take this view:

```sql
CREATE MATERIALIZED VIEW revenue_by_city AS
SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
FROM orders o JOIN customers c ON o.customer_id = c.id
WHERE o.status = 'paid'
GROUP BY c.city;
```

With 10 million orders, `REFRESH MATERIALIZED VIEW` re-reads all 10 million rows even when one order changed. So teams either show stale numbers, or hand-write update logic in triggers and application code. That hand-written logic usually breaks on deletes, on updates that move a row between groups, and on joins where both sides change.

**What DeltaView does.**

1. Listens to Postgres's change stream: every committed insert, update and delete.
2. Turns each transaction into a small "delta".
3. Pushes the delta through the view's query plan. Each operator knows how to turn an input delta into an output delta.
4. Stores the up-to-date result and streams the changes to subscribers.

The work done is proportional to the size of the change, not the size of the table.

**What it is not.** It is not a database (Postgres stays the source of truth). It does not support all of SQL. It is a single process, not a distributed system. Say these limits yourself in interviews before you are asked.

**Real systems built on the same idea:** Materialize, Feldera, RisingWave (separate engines), and pg_ivm (inside Postgres, using triggers). The theory comes from the DBSP paper (VLDB 2023).

---

## 3. Concepts you must own

Each of these is something an interviewer can ask you to explain on a whiteboard.

### 3.1 The WAL and logical replication

- Postgres writes every change to the **Write-Ahead Log (WAL)** before changing table files. That is how it survives crashes.
- **Logical decoding** turns WAL records back into row-level changes such as "row X inserted into orders".
- **pgoutput** is the built-in plugin that formats those changes. There is nothing to install.
- A **publication** is the list of tables you want changes for.
- A **replication slot** is a bookmark stored on the server. It remembers how far you have confirmed, and it forces Postgres to keep all WAL after that point. This is why a slow or dead consumer can fill the Postgres disk.
- An **LSN** (Log Sequence Number) is a position in the WAL. It only grows. Treat it as the clock of the whole system.
- Changes arrive **one whole transaction at a time, in commit order**: Begin, the changes, Commit. You never see uncommitted or rolled-back work.
- **Replica identity** controls what Postgres sends as the OLD row on update and delete. The default sends only the primary key. DeltaView must subtract the full old row, so its tables use `REPLICA IDENTITY FULL`.

### 3.2 Z-sets: the one data structure

A Z-set is a map from a row to an integer weight.

- Weight +1 means the row was added. Weight -1 means it was removed.
- An update is two entries: -1 for the old row, +1 for the new row.
- A table is a Z-set where every weight is positive. Weight 2 means the row appears twice.
- Adding two Z-sets adds the weights row by row. A row whose weight reaches 0 disappears.

Worked example:

```text
orders (table)    = { (1, paid, 100): +1,  (2, paid, 50): +1 }
change (delta)    = { (2, paid, 50): -1,   (2, paid, 70): +1 }      order 2: 50 -> 70
orders + change   = { (1, paid, 100): +1,  (2, paid, 70): +1 }
```

Why this matters: inserts, deletes and updates all become the same thing, a delta. Every operator needs only one rule: given an input delta, produce the output delta.

### 3.3 The core equation

```text
View = Q(Table)
When the table changes by dT:   dView = Q(Table + dT) - Q(Table)
```

The whole project is computing `dView` without ever computing `Q(Table)` again.

### 3.4 Linear operators: filter and project

Filter looks at each row on its own, so `filter(A + B) = filter(A) + filter(B)`. That means `d_out = filter(d_in)`. No memory is needed.

Example with `WHERE status = 'paid'`: the delta `{(2,paid,50): -1, (2,paid,70): +1}` passes through unchanged. A delta row with status `pending` is dropped.

Project works the same way: transform each row, keep its weight. If two input rows map to the same output row, their weights add.

### 3.5 Aggregation (GROUP BY)

Aggregation is not linear: the output for a group depends on all its rows. So the operator keeps a small state per group.

For COUNT and SUM the state is `(count, sum)`. For each group the delta touches:

1. Read the group's old output row (if count > 0).
2. Apply the delta: `count += weight`, `sum += weight * value`.
3. Compute the new output row (if count > 0).
4. Emit `{old row: -1, new row: +1}`.

Example: Delhi has state (count 2, sum 150). A new paid order of 70 arrives.

```text
emit { (Delhi, 2, 150): -1,  (Delhi, 3, 220): +1 }
```

When a group's count reaches 0, emit only the -1. The group is gone.

**MIN and MAX are harder.** If the current minimum is deleted you need the next smallest value. So keep an ordered multiset per group (value -> how many times). Cost: memory grows with the number of distinct values per group.

**NULL rules to match Postgres:**

- `SUM` ignores NULLs. `SUM` over only NULLs is NULL, not 0. Keep a non-null count per aggregate.
- `COUNT(col)` ignores NULLs. `COUNT(*)` does not.
- `GROUP BY` puts all NULL keys in one group.
- A query with aggregates but no GROUP BY always returns exactly one row, even on an empty table.

### 3.6 Joins

A join multiplies: each matching pair (row a, row b) produces an output row with weight `wa * wb`.

```text
(A + dA) JOIN (B + dB) = A JOIN B  +  dA JOIN B  +  A JOIN dB  +  dA JOIN dB
                         ^ old output
so:   d_out = dA JOIN B  +  A JOIN dB  +  dA JOIN dB
```

Each term is cheap only if matches can be found fast. So the join keeps an index of each side by join key. This index is called an **arrangement**. The cost: a join stores a copy of both of its inputs.

Implementation trick (three terms, two lookups):

```text
1. out  = dA JOIN B_old
2. A    = A + dA
3. out += A_new JOIN dB        covers both  A JOIN dB  and  dA JOIN dB
4. B    = B + dB
```

Worked example:

```text
customers B = { (7, Delhi): +1 }       orders A = empty

Txn 1: insert order (1, cust 7, 100)   dA = { (1,7,100): +1 }
  step 1 -> out = { (1,7,100,Delhi): +1 }

Txn 2: customer 7 moves to Mumbai      dB = { (7,Delhi): -1, (7,Mumbai): +1 }
  step 3 -> out = { (1,7,100,Delhi): -1, (1,7,100,Mumbai): +1 }
```

The aggregate downstream then moves 100 from Delhi to Mumbai. Nobody wrote special "customer changed city" code. That is the point of the algebra.

### 3.7 Exactly-once processing

The stream is **at-least-once**: after a crash, Postgres re-sends whatever you had not confirmed. Applying a delta twice corrupts every count and sum.

The fix: save the operator state and the "last applied LSN" in **one atomic write**. Then after any crash:

- State and LSN always agree, because both were saved or neither was.
- On restart, resume from the saved LSN and skip any transaction at or before it.

```text
at-least-once delivery + atomic (state, position) + skip duplicates = effectively exactly-once
```

This is the same pattern as storing Kafka offsets in the same database transaction as the results.

**The one rule you must never break:** tell Postgres "confirmed up to LSN X" only after X is durably saved. Confirm early, then crash, and those changes are lost forever.

### 3.8 Initial snapshot

Tables have rows before DeltaView starts. You need the existing rows plus every later change, with no gap and no double count.

- Creating the replication slot returns a **consistent point** (an LSN) and an **exported snapshot name**.
- Read the tables in a transaction pinned to that snapshot. It sees exactly what was committed before the consistent point.
- Then stream from the consistent point. Everything after it arrives through the stream.
- Feed the snapshot rows through the circuit as one large +1 delta.

---

## 4. Architecture

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

### What each part does and why it exists

| # | Component | Job | Why it is needed |
|---|---|---|---|
| 0 | SQL front end | Turns view SQL into a plan, then into a circuit of operators | Users write SQL, not operator graphs |
| 1 | CDC reader | Speaks the replication protocol, decodes pgoutput messages | It is the only input to the system |
| 2 | Txn assembler | Groups Begin..Commit into one batch of Z-set deltas | Views must never show half a transaction |
| 3 | Apply loop | Runs one transaction through every circuit, then commits | A single writer means no locks and a deterministic order |
| 4 | Circuit | The incremental operators wired together | This is where the work becomes proportional to the change |
| 5 | State store | Holds operator state, view contents and the applied LSN | Survives restarts; the atomic batch gives exactly-once |
| 6 | Ack | Tells Postgres which WAL it may delete | Without it the Postgres disk fills up |
| 7 | Serving | Read a view, or subscribe to its changes | A view nobody can read is useless |
| 8 | Observability | Lag, latency, sizes | You cannot benchmark or debug what you cannot see |
| 9 | Differential tester | Proves the view equals the real query result | Incremental logic is easy to get subtly wrong |

### Life of one change

1. An app runs `UPDATE orders SET status = 'paid' WHERE id = 42` and commits.
2. Postgres writes the change to the WAL. At commit, logical decoding emits Begin, Update (old row and new row), Commit.
3. The CDC reader decodes the messages. The assembler builds a batch: `orders -> {old: -1, new: +1}`, with the commit LSN.
4. The batch waits in a bounded queue. If the queue is full the reader stops reading, and Postgres keeps the WAL. That is backpressure.
5. The apply loop takes the batch and opens one state transaction.
6. Filter: the old row (`pending`) is dropped. The new row (`paid`) passes with weight +1.
7. Join: looks up customer 7 in the customer arrangement and emits the joined row with weight +1. The order arrangement is updated.
8. Aggregate: reads Delhi's state, adds the amount, emits `-old row, +new row`.
9. The view store applies that output delta. `applied_lsn` is written in the same batch.
10. The batch commits with fsync. Only now is the LSN acknowledged to Postgres.
11. Subscribers receive `{lsn, changes}`. Slow subscribers are dropped, never waited for.

### Concurrency model

- One goroutine reads from Postgres.
- One goroutine (the apply loop) owns all state. Nothing else writes.
- HTTP handlers read from consistent store snapshots, so they never see a half-applied transaction.

---

## 5. Design decisions and trade-offs

Copy these into `docs/decisions.md` and extend them as you build.

| Decision | Chosen | Alternative | Why |
|---|---|---|---|
| Where to compute | Separate process reading the WAL | Triggers inside Postgres (pg_ivm does this) | No extra work inside the user's write transaction; works on managed Postgres with no extension. Cost: the view lags by milliseconds |
| Change format | pgoutput | wal2json, or Debezium plus Kafka | Built in, nothing to install. Debezium adds two more systems to run |
| Getting old row values | `REPLICA IDENTITY FULL` | Keep our own copy of each table keyed by primary key | Simpler. Cost: more WAL per update and delete. The alternative saves WAL but doubles state |
| Unit of work | One Postgres transaction = one step | Row at a time, or several transactions per step | The view always equals the query at some real commit point. Multi-transaction batching is a later throughput optimisation |
| Threading | Single apply goroutine | Parallel operators | No locks, deterministic, easy to test. Limit: one CPU core |
| State | Embedded key-value store (Pebble) | Memory only, or store state back in Postgres | An atomic local batch gives exactly-once cheaply. Memory only means a full re-snapshot on every restart |
| Checkpointing | State and LSN in one atomic batch per transaction | Periodic snapshots with barriers (Flink style) | One process and one store, so one batch is enough. Barriers are only needed when operators run on different machines |
| SQL parsing | The real Postgres parser via pg_query_go | Hand-written parser | Same grammar as Postgres. Cost: needs cgo |
| SQL scope | A fixed, documented subset | "All of SQL" | Every feature needs a proven incremental rule. Unsupported SQL is rejected with a clear error |
| Slow subscribers | Drop them and make them resync | Block the pipeline, or buffer without limit | One slow client must not stall every view or eat all memory |

---

## 6. Tech stack and repo layout

**Language: Go.** Reasons: a library built for Postgres logical replication, simple concurrency, and code that is easy to review line by line. Interviewers at product companies do not care which language a project uses.

| Need | Choice | Notes |
|---|---|---|
| Replication protocol | `github.com/jackc/pglogrepl` | Verified to exist on 11 Nov 2026 |
| Normal Postgres queries | `github.com/jackc/pgx/v5` | pglogrepl is built on its `pgconn` package |
| SQL parser | `github.com/pganalyze/pg_query_go/v6` | Verified. Needs cgo; on Windows use WSL |
| State store | `github.com/cockroachdb/pebble` | Verified. Supports indexed batches and snapshots |
| Decimal numbers | a decimal library such as `shopspring/decimal` | Not re-verified this session; confirm before adding |
| Property tests | Go standard library: `testing/quick` and `go test -fuzz` | No dependency |
| Metrics, tracing | Prometheus Go client, OpenTelemetry Go SDK | Add in M10; confirm current module paths then |
| Postgres | Docker image `postgres:17` or newer | `wal_level=logical` |

Do not pin versions from memory. Let Claude Code run `go get MODULE@latest` and record what it got.

**If you prefer Java:** the PostgreSQL JDBC driver has a replication API, Apache Calcite can parse SQL, and RocksDB has Java bindings. The design in this guide does not change.

```text
deltaview/
  CLAUDE.md                 rules for Claude Code
  docker-compose.yml        Postgres with wal_level=logical
  db/init.sql               demo tables, publication
  Makefile
  cmd/deltaview/            the engine
  cmd/dvtest/               differential tester
  cmd/dvbench/              benchmarks
  internal/types/           Value, Row, canonical encoding
  internal/zset/            Z-set
  internal/cdc/             replication client, pgoutput decoding, snapshot
  internal/sql/             parser adapter, binder, logical plan
  internal/expr/            expression evaluation (three-valued logic)
  internal/ops/             Filter, Project, Join, Aggregate, Distinct
  internal/circuit/         wires operators, runs one step
  internal/state/           Store interface, memory and Pebble versions
  internal/engine/          apply loop, checkpoint, recovery
  internal/serve/           HTTP and SSE
  internal/obs/             metrics and tracing
  docs/PLAN.md  docs/decisions.md  docs/notes.md
```

---

## 7. Low-level design

### Key types

```go
// internal/types
type Row []Value            // Value = null | int64 | decimal | float64 | text | bool | timestamp

// Encode returns a canonical, order-preserving byte encoding.
// Equal rows give equal bytes. Byte order equals value order.
func Encode(r Row) []byte

// internal/zset
type ZSet struct{ /* map: encoded row -> (row, weight) */ }
func (z *ZSet) Add(r Row, w int64)   // drops the entry when the weight reaches 0
func (z *ZSet) Merge(o *ZSet)
func (z *ZSet) Negate() *ZSet

// internal/cdc
type TxnBatch struct {
    EndLSN     uint64               // end of this transaction in the WAL
    CommitTime time.Time
    Deltas     map[string]*zset.ZSet // table name -> delta
}

// internal/ops
type Operator interface {
    // Step takes one input delta per input port and returns the output delta.
    // All state reads and writes go through tx so they commit atomically.
    Step(tx state.Tx, in []*zset.ZSet) (*zset.ZSet, error)
}

// internal/state
type Store interface {
    Begin() Tx          // one per Postgres transaction
    Snapshot() Reader   // consistent read-only view for queries
}
type Tx interface {
    Get(key []byte) ([]byte, bool, error)
    Set(key, val []byte)
    Delete(key []byte)
    Scan(prefix []byte) Iterator
    Commit(sync bool) error
}
```

`Tx` must read its own writes inside a step. Pebble's indexed batch does this.

### Design patterns and where they appear

| Pattern | Where | Why |
|---|---|---|
| Strategy | `state.Store` (memory or Pebble); aggregate functions | Swap the implementation without touching operators |
| Composite | A circuit is a tree of operators with one `Step` interface | The engine runs any view the same way |
| Adapter | pgoutput messages to `TxnBatch`; pg_query AST to our plan | Keeps third-party shapes out of the core |
| Builder | Planner builds a circuit from a logical plan | Construction is separate from execution |
| Observer | Subscription hub pushes view deltas to clients | Many readers, one writer |
| Single-writer | The apply loop owns all state | No locks, deterministic replay |

### State key layout

```text
m/applied_lsn               -> uint64        end LSN of the last committed transaction
m/snapshot_done             -> bool          initial snapshot finished
m/schema/TABLE              -> column list   to detect schema changes
j/OP/L/JOINKEY/ROW          -> weight        left arrangement of a join
j/OP/R/JOINKEY/ROW          -> weight        right arrangement
a/OP/GROUPKEY               -> agg state     row count, sums, non-null counts
x/OP/GROUPKEY/VALUE         -> count         ordered multiset for MIN and MAX
d/OP/ROW                    -> weight        input weights for DISTINCT
v/VIEW/ROW                  -> weight        the materialized view itself
```

Two encoding rules that cause real bugs if ignored:

- **Unambiguous:** the parts of a composite key must be self-delimiting. Otherwise `("ab","c")` and `("a","bc")` collide.
- **Order-preserving:** under `x/OP/GROUPKEY/`, the first key must be the minimum value. Then MIN is one seek.

### Supported SQL (version 1)

- Select list: columns, simple arithmetic, `COUNT(*)`, `COUNT(col)`, `SUM`, `AVG`, `MIN`, `MAX`
- `FROM` one table, or `INNER JOIN` on column equality
- `WHERE` with `AND`, `OR`, `NOT`, comparisons, `IS NULL`, `IN (list)`
- `GROUP BY` columns

Rejected with a clear error, and why:

| Feature | Why not in v1 |
|---|---|
| `ORDER BY`, `LIMIT` | Incremental top-k is its own problem. Clients sort |
| Outer joins | Need extra rules for rows that gain or lose their last match |
| Subqueries, window functions | Each needs a separate incremental rule |
| `now()`, `random()` | The answer changes when no row changed, so there is no delta to react to |
| Table schema changes | Logical replication does not carry DDL. Detect and stop with a clear message |

### HTTP API

```text
GET /views                    list of views
GET /views/NAME               rows plus the LSN they are valid at
GET /views/NAME/subscribe     SSE: first a snapshot event with its LSN, then delta events
GET /status                   applied LSN, heartbeat sequence, slot lag
GET /metrics                  Prometheus metrics
```

---

## 8. Working with Claude Code

### CLAUDE.md (put this in the repo root)

```markdown
# DeltaView

Incremental view maintenance engine over Postgres logical replication.
Read docs/PLAN.md before any task.

## Rules
- Work on one milestone at a time. Do not build ahead.
- Standard library first. Ask before adding any dependency.
- Write or update tests before the implementation.
- Never delete, skip or loosen a test to make it pass. If a test looks wrong, stop and tell me.
- All operator state goes through state.Tx. Only the apply loop writes state. No locks inside operators.
- Never acknowledge an LSN to Postgres before the batch containing it is committed with sync.
- Numeric columns are decimals, never float64.
- Unsupported SQL must be rejected with an error naming the feature. Never guess.
- Wrap errors with context. No panics outside main and tests.
- Every exported type and function gets a comment that says why it exists.
- When unsure how Postgres behaves, write a small test against the Docker Postgres. Do not guess.
- After each task run `make check` and report the real output.

## Commands
make up | make down | make check | make difftest SEED=n | make bench
```

### Standard opening (start every milestone prompt with this)

```text
Read CLAUDE.md and docs/PLAN.md. We are doing milestone <ID> only. Do not build ahead.
First give me a short plan: the files you will create or change, the tests you will write,
and any Postgres behaviour you are unsure about. Wait for my OK before writing code.
```

### Standard closing (end every milestone prompt with this)

```text
When done: run make check and paste the real output. List every assumption you made.
List anything you skipped or stubbed. Then explain the main code path in plain words,
as if I must defend it in an interview.
```

### Reusable prompts

Explain-back (use after every milestone):

```text
Quiz me on milestone <ID>. Ask one question at a time, the way a senior engineer would in an
interview. After each answer, tell me what was wrong or missing, then ask a harder follow-up.
Cover: why this component exists, what breaks without it, and the trade-offs we chose.
```

Adversarial review (use before moving on):

```text
Act as a reviewer who believes this milestone has a correctness bug. Do not change code.
Read the diff and list concrete inputs or crash timings that could produce a wrong view.
For each, say whether an existing test covers it. Propose new tests for the uncovered ones.
```

Failing differential test:

```text
make difftest SEED=<n> fails. Do not change the tester or any expected values.
Reproduce it, shrink it to the smallest transaction sequence that still fails,
explain the root cause in terms of the delta rules in docs/PLAN.md section 3,
then fix the operator and add the shrunk case as a regression test.
```

---

## 9. Milestones

### M0. Project skeleton (1 to 2 days)

**Why it exists.** Every later step needs a Postgres that is set up for logical replication, started the same way every time.

**How it works.** `wal_level=logical` makes Postgres record enough detail in the WAL to rebuild row changes. The default level does not.

**Prompt.**

```text
[standard opening, milestone M0]
Create the Go module and the folder layout from docs/PLAN.md section 6, with empty packages
and doc comments. Add docker-compose.yml running Postgres 17 with wal_level=logical,
max_replication_slots=4 and max_wal_senders=4. Add db/init.sql that creates:
  customers(id bigint primary key, name text, city text)
  orders(id bigint primary key, customer_id bigint, status text, amount numeric(12,2),
         created_at timestamptz)
  dv_heartbeat(id int primary key, seq bigint) with one row
Set REPLICA IDENTITY FULL on all three tables and create publication deltaview_pub for them.
Add the Makefile targets from CLAUDE.md and a GitHub Actions workflow that runs make check.
No engine code yet.
[standard closing]
```

**Definition of done.** `make up` works. `SHOW wal_level` prints `logical`. CI is green.

**Explain it back.**

- Why `wal_level=logical`? Lower levels do not record enough to decode row changes.
- What does `REPLICA IDENTITY FULL` cost? Every update and delete writes the whole old row to the WAL, so more WAL.

### M1. CDC reader (week 1)

**Why it exists.** It is the only input. If a change is decoded wrongly, everything after it is wrong.

**How it works.**

1. Connect with `replication=database`.
2. Create the slot with pgoutput if it does not exist.
3. Start replication from an LSN, with protocol version 1 and the publication name.
4. The server sends Relation (a table's columns; sent before the table's first change and after a schema change), Begin, Insert, Update, Delete, Commit, and keepalives.
5. The client must send status updates. If it stays silent, the server disconnects it after `wal_sender_timeout` (60 seconds by default).

Details that matter:

- Values arrive as text. Convert them using the column type from the Relation message.
- Update carries the old row and the new row. Large values that did not change arrive as an "unchanged" marker in the new row. Copy them from the old row.
- Assembly: Insert gives `+new`. Delete gives `-old`. Update gives `-old, +new`. Begin to Commit becomes one `TxnBatch`.
- Truncate: for now, stop with a clear error.

**Prompt.**

```text
[standard opening, milestone M1]
Build internal/cdc using github.com/jackc/pglogrepl. It must: connect in replication mode,
create slot deltaview_slot with pgoutput if missing, start replication for publication
deltaview_pub with proto_version 1, decode Relation, Begin, Insert, Update, Delete, Commit,
and answer keepalives. Assemble each transaction into a TxnBatch as defined in section 7:
insert = +new, delete = -old, update = -old and +new, using the full old tuple.
Handle the unchanged-TOAST marker by copying the value from the old tuple.
Decode int2/int4/int8, numeric (as decimal), float8, text, varchar, bool, timestamptz, date.
Fail loudly on Truncate, on an Update without an old tuple, and on unknown types.
For now acknowledge the end LSN of each transaction after handing it to a callback;
milestone M8 will change this. Add cmd/deltaview flag --print-changes that prints batches.
Integration tests against Docker Postgres: multi-row insert; update; delete; NULLs; numeric;
timestamps; a 100 KB text column updated without changing it; one transaction touching two
tables; a rolled-back transaction (must produce nothing); restart resumes without loss.
[standard closing]
```

**Definition of done.** All integration tests pass. You watched live changes print while typing SQL in `psql`.

**What AI tends to get wrong here.**

- Not answering keepalives, so the connection drops after a minute of quiet.
- Accepting an Update with no old row.
- Ignoring the unchanged-TOAST marker and writing NULL.
- Parsing `numeric` as a float.

**Explain it back.**

- What is a replication slot, and what is its danger? A server-side bookmark; it pins WAL, so a dead consumer fills the disk.
- Why do changes arrive per transaction? Postgres decodes at commit and groups each transaction's changes together.
- What happens to a rolled-back transaction? It is never sent.

### M2. Types and Z-sets (week 1 to 2)

**Why it exists.** Every operator speaks Z-sets. A bug here poisons everything above it.

**How it works.** The laws your tests must prove:

```text
a + (-a) = empty          a + b = b + a          (a + b) + c = a + (b + c)
no entry with weight 0 is ever stored
decode(encode(row)) = row         encode order = value order
```

**Prompt.**

```text
[standard opening, milestone M2]
Build internal/types (Value, Row, canonical order-preserving Encode and Decode) and
internal/zset (Add, Merge, Negate, iteration in key order). Requirements: equal rows always
encode to identical bytes; numeric 1.0 and 1.00 are equal; text '1' and integer 1 are
different; NULL is distinct from the text 'NULL'; composite encodings are self-delimiting.
Write property tests with testing/quick and a fuzz test for encode/decode round trips,
ordering, and the Z-set laws listed in docs/PLAN.md M2.
[standard closing]
```

**Definition of done.** Property tests run thousands of random cases with no failure.

**What AI tends to get wrong here.** Using `fmt.Sprint(row)` as the map key (ambiguous), and forgetting that decimals with different scale can be equal.

**Explain it back.**

- Why can a weight be greater than 1? SQL tables are bags; duplicates are allowed.
- Why a canonical encoding? Rows are map keys and store keys. Two encodings of the same row would count as two rows.

### M3. First view end to end, and the differential tester (week 2 to 3)

**Why it exists.** A thin slice through the whole system proves the pieces connect. The tester then guards every later milestone.

**How the tester works.**

1. It is the only writer. It runs a random transaction on Postgres using a seeded random generator.
2. It then bumps `dv_heartbeat.seq` to N in its own transaction.
3. It polls `GET /status` until DeltaView reports heartbeat N. Commit order is preserved, so the earlier transaction has been applied.
4. It runs the view's `SELECT` on Postgres and reads the view from DeltaView.
5. It compares them as **multisets** (rows with counts). On mismatch it prints the seed and the transaction history.

**Prompt A (the slice).**

```text
[standard opening, milestone M3 part A]
Build Filter and Project operators, the circuit runner, an in-memory state.Store, and the
apply loop. Hard-code one view in Go for now:
  SELECT id, amount FROM orders WHERE status = 'paid'
Apply each TxnBatch to the circuit, apply the output delta to the view store, and serve
GET /views/paid_orders and GET /status (applied LSN and the latest dv_heartbeat seq).
The engine treats dv_heartbeat specially: it only records the latest seq.
[standard closing]
```

**Prompt B (the tester).**

```text
[standard opening, milestone M3 part B]
Build cmd/dvtest as described in docs/PLAN.md M3. Random transactions must include inserts,
updates, deletes, NULL values, duplicate non-key values, multi-row statements, updates that
flip rows in and out of the WHERE condition, and multi-statement transactions.
Compare as multisets, with decimals compared by value. Take the views to check from a list of
(name, SQL) pairs. Flags: --seed, --txns, --check-every. On mismatch print the seed, the
transactions since the last good check, and the rows that differ.
[standard closing]
```

**Definition of done.**

- `make difftest` with 50 seeds of 1,000 transactions each: zero mismatches.
- **Mutation check:** break the filter on purpose (for example, ignore deletes). The tester must fail. If it does not, the tester is too weak.

**Explain it back.**

- How do you know the engine is correct? It is compared against Postgres's own answer after random workloads, and I confirmed the tester catches planted bugs.
- How does the tester know the engine has caught up? The heartbeat row: once its change is visible, everything committed before it has been applied.
- Why multisets, not sets? Sets hide duplicate-row bugs.

### M4. Aggregation (week 3 to 5)

**Why it exists.** Aggregates are what most real views are for.

**How it works.** See section 3.5. Process a delta group by group: collect all delta rows of a group, apply them all, then emit one `-old, +new` pair for that group.

**Prompt.**

```text
[standard opening, milestone M4]
Part 1: build the Aggregate operator for GROUP BY with COUNT(*), COUNT(col), SUM and AVG,
following docs/PLAN.md section 3.5, including every NULL rule listed there and the rule that
an aggregate query without GROUP BY always outputs exactly one row. State goes through
state.Tx using the a/ key layout. Emit at most one -old and one +new row per group per step.
Part 2: add MIN and MAX using an ordered multiset per group under the x/ prefix.
Add these views to the tester: orders per status; SUM and AVG of amount per customer_id;
MIN and MAX amount per status; a global COUNT and SUM with no GROUP BY.
Extend the tester workload with updates that move rows between groups, deletes that empty a
group, and NULL amounts. Compare AVG after rounding both sides to 6 decimal places.
[standard closing]
```

**Definition of done.** 50 seeds pass. Mutation check: make MIN ignore deletes; the tester must fail.

**What AI tends to get wrong here.** SUM of an all-NULL group returning 0; groups not removed at count 0; AVG computed in floats; a missing row for an empty global aggregate.

**Explain it back.**

- Why is aggregation not linear? A group's output depends on all its rows together, not on each row alone.
- What happens when the current MIN is deleted? The multiset gives the next smallest value in one seek.
- What does MIN cost? Memory proportional to the distinct values in each group.

### M5. SQL front end (week 5 to 6)

**Why it exists.** Views should be written as SQL, not as Go code.

**How it works.**

```text
SQL text -> parse (pg_query_go) -> bind (names to column positions and types,
using table schemas read from Postgres) -> logical plan -> circuit of operators
```

Expressions use SQL's three-valued logic: `NULL = NULL` is unknown, not true. `WHERE` keeps a row only when the condition is true.

**Prompt.**

```text
[standard opening, milestone M5]
Build internal/sql and internal/expr. Read view definitions from views.sql
(CREATE MATERIALIZED VIEW name AS SELECT ...). Parse with pg_query_go, bind names against
table schemas read from the Postgres catalog, build a logical plan
(Scan, Filter, Project, Aggregate), and build the circuit from it.
Support exactly the SQL listed in docs/PLAN.md section 7 except joins (next milestone).
Reject everything else with an error that names the unsupported feature.
Expression evaluation must follow Postgres three-valued logic and Postgres integer division.
Replace the hard-coded views with views.sql and point the tester at the same file.
Unit-test the evaluator against Postgres: for a table of expressions and input rows,
assert that our result equals the result of running the same expression in Postgres.
[standard closing]
```

**Definition of done.** The tester passes with at least 10 views loaded from `views.sql`. A view using `ORDER BY` is rejected with a clear message.

**Explain it back.**

- Logical plan versus circuit? The plan says what to compute; the circuit is the stateful operators that compute it step by step.
- Why reject unsupported SQL instead of trying? A wrong answer that looks right is worse than an error.

### M6. Joins (week 6 to 8)

**Why it exists.** Joins are where hand-written incremental logic usually fails, and where this project earns its depth.

**How it works.** See section 3.6. State is two arrangements under the `j/` prefix.

**Prompt.**

```text
[standard opening, milestone M6]
Build the inner equi-join operator exactly as in docs/PLAN.md section 3.6:
out = dA join B_old; A += dA; out += A_new join dB; B += dB. Output weight is the product
of the input weights. Rows with a NULL join key never match and are not stored.
Arrangements live under j/OP/L and j/OP/R, keyed by encoded join key then encoded row.
Extend the binder and planner for FROM a JOIN b ON a.x = b.y, with WHERE, GROUP BY and
aggregates on top. Add tester views: orders joined to customers; revenue_by_city from
section 2. Extend the workload: both tables changed in one transaction; a customer's city
updated; a customer deleted while orders remain; duplicate join keys on both sides;
NULL customer_id; an order's customer_id updated.
[standard closing]
```

**Definition of done.** 50 seeds pass. Mutation checks: remove the `dA join dB` contribution; then double it. The tester must catch both.

**What AI tends to get wrong here.** Using the old index on both sides (drops `dA join dB`), using the new index on both sides (counts it twice), matching NULL keys, forgetting to multiply weights, and encoding int4 and int8 keys differently so equal keys never match.

**Explain it back.**

- Derive the join delta from `(A + dA) JOIN (B + dB)`.
- What does a join cost in state? A copy of both inputs, indexed by key.
- What about a hot key? If one customer has a million orders, changing that customer emits a million output changes. That cost is inherent, not a bug.

**Resume-ready checkpoint.** After M6 you have a correct engine with filters, aggregates and joins, proven by a differential tester. If time runs short, stop here and jump to benchmarks.

### M7. Initial snapshot (week 8 to 9)

**Why it exists.** Real tables already contain rows when DeltaView first starts.

**How it works.**

1. On the replication connection, create the slot and ask it to export a snapshot. You get a consistent point and a snapshot name.
2. Keep that connection idle. The snapshot is valid only until the next command on it.
3. On a normal connection: `BEGIN ISOLATION LEVEL REPEATABLE READ; SET TRANSACTION SNAPSHOT 'name';` then read each table.
4. Feed the rows through the circuits in chunks, as +1 deltas.
5. Write `snapshot_done` and `applied_lsn = consistent point` in one batch.
6. Start replication from the consistent point.

If the engine starts and `snapshot_done` is missing, the previous snapshot was interrupted: wipe state, drop the slot, start again.

**Prompt.**

```text
[standard opening, milestone M7]
Implement the initial snapshot exactly as in docs/PLAN.md M7, using the exported snapshot
from slot creation and a second connection pinned to it with SET TRANSACTION SNAPSHOT.
Stream table rows in chunks of 10,000 without loading a table into memory.
Handle an interrupted snapshot by wiping state, dropping the slot and restarting.
Test: load 1 million orders, start a background writer that never stops, start the engine,
wait for catch-up, pause the writer, run the differential check. Repeat 20 times.
[standard closing]
```

**Definition of done.** 20 of 20 runs pass with the writer active during the snapshot.

**Explain it back.**

- Why is there no gap and no double count? The snapshot sees exactly the commits before the consistent point; the stream delivers exactly the commits after it.
- What if the engine dies mid-snapshot? It starts the snapshot again from nothing.

### M8. Durable state and exactly-once (week 9 to 11)

**Why it exists.** Without it, every restart means a full re-snapshot, and any crash risks double-applying changes.

**How it works.**

```text
for each TxnBatch:
    if batch.EndLSN <= applied_lsn: skip          (a re-sent duplicate)
    tx = store.Begin()
    run every circuit with tx
    tx.Set(m/applied_lsn, batch.EndLSN)
    tx.Commit(sync = true)                        (the durability point)
    acknowledge batch.EndLSN to Postgres          (only now)
```

Crash analysis you must be able to give from memory:

| Crash happens | State on disk | After restart |
|---|---|---|
| Before the commit | Old state, old LSN | Postgres re-sends the transaction; it is applied once |
| After the commit, before the ack | New state, new LSN | Postgres re-sends it; it is skipped because its LSN is not newer |
| After the ack | New state, new LSN | Nothing is re-sent |

**Prompt.**

```text
[standard opening, milestone M8]
Add a Pebble implementation of state.Store using one indexed batch per transaction,
committed with sync. Move all operator state and view contents to it. Implement the apply
loop exactly as in docs/PLAN.md M8: skip duplicates, commit, then acknowledge.
On startup read applied_lsn and start replication from it.
Add fail points selected by an environment variable that exit the process: before commit,
after commit before ack, after ack. Add a crash-test script that runs the tester workload,
kills the engine with kill -9 at random times, restarts it, and at the end runs the full
differential check. Also detect a lost slot (wal_status = lost) and trigger a re-snapshot.
[standard closing]
```

**Definition of done.** 200 random kill cycles under load, then a clean differential check. Each of the three fail points passes on its own.

**What AI tends to get wrong here.** Acknowledging before the commit; committing without sync; operators caching state in struct fields that drifts from the store after a failed batch.

**Explain it back.**

- Walk through the three crash points in the table.
- Why fsync? Without it the operating system may hold the write in memory; a power cut loses it after you already acknowledged.
- What if the slot is lost? Postgres has deleted WAL you needed. The only correct recovery is a new snapshot.

### M9. Serving and subscriptions (week 11 to 12)

**Why it exists.** A live view is only useful if clients can read it and follow its changes.

**How it works.**

- `GET /views/NAME` reads from a store snapshot, so it sees one consistent LSN.
- Subscribe: the request is handed to the apply loop. Between two transactions the loop takes a snapshot and registers the subscriber in one step. The client gets the snapshot with its LSN, then every delta after it.
- Each subscriber has a bounded buffer. If it overflows, the server closes the stream with a "resync" event.
- Every delta carries its LSN, so a reconnecting client can ignore anything it already has.

**Prompt.**

```text
[standard opening, milestone M9]
Build internal/serve as in docs/PLAN.md M9 and section 7: GET /views, GET /views/NAME from a
store snapshot, and SSE subscribe with snapshot-then-deltas registered atomically inside the
apply loop. Bounded per-subscriber buffer; on overflow send a resync event and close.
The apply loop must never block on a subscriber.
Test: a client that applies the snapshot and all deltas must equal GET /views/NAME at the
same LSN, under a concurrent write load. Test that a stalled client is dropped and that the
pipeline's throughput is unaffected.
[standard closing]
```

**Definition of done.** The subscriber-equals-view test passes under load. A stalled client is dropped.

**Explain it back.**

- How does a subscriber never miss or repeat a delta? The snapshot and the registration happen at one LSN; deltas carry LSNs.
- Is delivery to subscribers exactly-once? No. It is at-least-once with LSNs, and clients drop duplicates. Say this plainly.

### M10. Observability, benchmarks, demo (week 12 to 14)

**Prompt.**

```text
[standard opening, milestone M10]
Add Prometheus metrics: replication slot lag in bytes, end-to-end latency from Postgres
commit time to view commit, transactions and rows applied per second, output delta size,
state size on disk, subscriber count and drops. Add OpenTelemetry spans around each apply
step and each operator. Build cmd/dvbench to run the benchmarks in docs/PLAN.md section 11
and write results as CSV. Build a small demo page that subscribes over SSE and shows
revenue_by_city updating live while a load generator writes. Write the README: the pitch,
the architecture diagram, measured numbers, how to run the demo, and an honest limitations list.
[standard closing]
```

**Definition of done.** Benchmarks run with one command. The demo runs with `docker compose up`.

### Stretch goals, in order of value

1. `HAVING` (a filter after the aggregate) and `DISTINCT`.
2. Batching several transactions per step for throughput.
3. `CREATE VIEW` at runtime with backfill.
4. Postgres wire protocol so `psql` can query views.
5. An MCP server exposing "list views, query view, subscribe" to AI agents. Add it only if you want an AI angle; the project stands without it.

---

## 10. Testing strategy

| Layer | What it checks | Bugs it catches |
|---|---|---|
| Property tests | Z-set laws, encoding round trip and order | Broken foundations |
| Operator unit tests | Hand-worked deltas from section 3 | Wrong delta rules |
| Expression tests vs Postgres | Our evaluator equals Postgres on the same inputs | NULL logic, division, casts |
| Differential tester | View equals the real query after random workloads | Almost everything |
| Mutation checks | Planted bugs must make the tester fail | A tester that is too weak |
| Crash tests | Kill at random and at fail points, then differential check | Lost or double-applied changes |
| Snapshot test | Snapshot while writes continue | Gaps and double counts at the handoff |

---

## 11. Benchmarks and resume bullets

Measure these and report real numbers only. Record the machine, Postgres version and data size.

| Benchmark | How | What it shows |
|---|---|---|
| Refresh versus incremental | Tables of 100 thousand, 1 million, 10 million rows; apply 1,000 changes; time `REFRESH MATERIALIZED VIEW` and DeltaView | Refresh grows with table size; DeltaView stays roughly flat |
| End-to-end latency | Postgres commit time to view commit; p50 and p99 | Freshness |
| Throughput | Changes applied per second, per view shape | Capacity of one core |
| Group commit effect | fsync per transaction versus per batch | The latency and throughput trade-off |
| State size | Store size versus source table size | The cost of join arrangements |
| Recovery time | Restart with N transactions pending | Operational behaviour |
| Versus pg_ivm | Write throughput on Postgres with each approach | Trigger cost on the write path versus asynchronous lag |

Resume bullets (fill in the placeholders):

- Built **DeltaView**, an incremental view maintenance engine over Postgres logical replication (Go, [N]k LOC) that keeps SQL views with joins and aggregates fresh by processing only changed rows.
- Implemented Z-set based incremental filter, join and aggregate operators; applied [N] changes to a view over [M] million rows in [X] ms versus [Y] s for `REFRESH MATERIALIZED VIEW`.
- Achieved effectively exactly-once processing by committing operator state and WAL position atomically; passed [N] random kill -9 cycles with zero divergence.
- Verified correctness with a differential tester comparing against Postgres across [N] million random transactions and [K] planted-bug checks; p99 commit-to-view latency [Z] ms.

---

## 12. Interview prep

### The 60-second pitch

"Materialized views in Postgres are refreshed by re-running the whole query. I built an engine that reads Postgres's replication stream, turns each transaction into a delta, and pushes it through incremental operators, so the cost depends on the change size, not the table size. It supports filters, joins and aggregates, recovers from crashes with exactly-once semantics, and I check it against Postgres itself with a randomized differential tester."

### Question bank

| Question | Answer in brief |
|---|---|
| What is IVM in one sentence? | A materialized view that updates by processing only changed rows |
| Why not `REFRESH MATERIALIZED VIEW`? | It re-runs the whole query. `CONCURRENTLY` avoids blocking readers but still recomputes everything |
| Why not triggers? | They run inside the user's write transaction and slow it down. pg_ivm does this and gets an always-current view; I traded a few milliseconds of lag for zero write-path cost |
| Is the view consistent? | It always equals the query at some real commit point, the applied LSN. It lags slightly but is never torn |
| Derive the join delta | Expand `(A + dA) JOIN (B + dB)` and subtract `A JOIN B` |
| MIN after its value is deleted? | Ordered multiset per group; the next key is the new minimum |
| Crash after commit, before ack? | Postgres re-sends; the transaction's LSN is not newer than the saved one, so it is skipped |
| Crash before commit? | Nothing was saved; the re-sent transaction is applied once |
| Postgres disk is filling. Why? | The slot pins WAL while the consumer is behind or dead. Monitor slot lag; cap with `max_slot_wal_keep_size`; re-snapshot if the slot is lost |
| How does the initial load avoid gaps? | Exported snapshot at the slot's consistent point, then stream from that point |
| Why Z-sets instead of insert and delete events? | One algebra for all changes; operators compose; duplicates and updates need no special cases |
| How big is the state? | Aggregates: one entry per group. Joins: both inputs. It can exceed the source tables. Mitigate by storing only needed columns |
| How would you scale past one core? | Hash-partition by key, exchange operators between partitions, barrier-based checkpoints. Skewed keys become the hard part |
| A transaction changes 10 million rows? | It arrives as one burst at commit and my batch holds it in memory. That is a real limit; the fix is the streaming protocol version or chunked steps |
| A column is added to a table? | DDL is not replicated. I detect the new schema message and stop with a clear error. Online schema change is not supported |
| Why a single apply thread? | No locks, a deterministic order, simple recovery. The limit is one core |
| How do you know it is correct? | Differential tests against Postgres, property tests, crash tests, and planted bugs to prove the tester works |
| Do subscribers get exactly-once? | No. At-least-once with an LSN on each delta, so clients deduplicate |
| Why no `ORDER BY ... LIMIT`? | Incremental top-k needs its own state design. Clients sort |
| Why no `now()` in views? | The answer would change with no row change, so there is no delta to trigger an update |
| How does this compare with Materialize or Feldera? | Same core idea. They are distributed or far more complete engines; mine is a small single-node version I can explain end to end |
| What did AI write and what did you do? | Claude Code wrote most of the code. I chose the design, wrote the invariants and tests first, reviewed every diff, and can describe specific bugs the tester caught |
| What would you change? | Name two from the limitations below and how you would fix them |

### Limitations to state yourself

- Single node, single apply thread.
- A fixed SQL subset; no outer joins, subqueries or windows.
- Needs `REPLICA IDENTITY FULL`, which increases WAL volume.
- Very large transactions are held in memory.
- No online schema changes.
- Views are defined at startup; adding one needs a re-snapshot (until the stretch goal).

### Keep a bug diary

Each time the tester catches a bug, write three lines in `docs/notes.md`: the symptom, the root cause, the fix. Two or three of these stories are the strongest material you will bring to an interview.

---

## 13. Timeline

Assumes about 2 hours a day on the project, with DSA practice continuing every day.

| Weeks | Milestones | You can now say |
|---|---|---|
| 1 | M0, M1 | "I read Postgres's replication stream" |
| 2 to 3 | M2, M3 | "I have an end-to-end view and a tester that proves it" |
| 3 to 5 | M4 | "Aggregates update incrementally, including MIN and MAX" |
| 5 to 6 | M5 | "Views are written in SQL" |
| 6 to 8 | M6 | "Joins work when both sides change". **Resume-ready** |
| 8 to 9 | M7 | "It starts on a live database with no gap" |
| 9 to 11 | M8 | "It survives kill -9 with exactly-once semantics" |
| 11 to 12 | M9 | "Clients subscribe to live changes" |
| 12 to 14 | M10 | "Here are the numbers and the demo" |

If interviews arrive early, skip from M6 to a small version of M10 (the refresh-versus-incremental benchmark and the README), then return to M7 and M8.

---

## 14. Glossary

| Term | Meaning |
|---|---|
| WAL | Postgres's log of every change, written before the change is applied |
| LSN | A position in the WAL; only grows |
| Logical decoding | Turning WAL records back into row changes |
| pgoutput | The built-in format for logical decoding output |
| Publication | The list of tables whose changes are published |
| Replication slot | Server-side bookmark that pins WAL for one consumer |
| Replica identity | What Postgres sends as the old row on update and delete |
| CDC | Change data capture: reading a database's changes as a stream |
| Z-set | Map from row to integer weight |
| Delta | A Z-set describing a change |
| IVM | Incremental view maintenance |
| Linear operator | One where `op(A + B) = op(A) + op(B)`; needs no state |
| Arrangement | An index of a Z-set by key, used by joins |
| Circuit | The operators of one view, wired together |
| Applied LSN | The last transaction whose effects are durably in the state store |
| Exactly-once | Each change affects the result once, even across crashes and re-delivery |
| Backpressure | Slowing the producer when the consumer cannot keep up |
| Differential testing | Checking a system against a trusted reference on random inputs |

---

## 15. References

Repos (existence checked in this conversation; read for ideas, write your own code):

- `feldera/feldera`: the DBSP engine. Read `crates/dbsp` for Z-sets and operators.
- `sraoss/pg_ivm`: trigger-based IVM inside Postgres. Your comparison baseline.
- `MaterializeInc/materialize`: source-available under BSL, not open source. Study only; do not copy code.
- `risingwavelabs/risingwave`: a streaming database with a Postgres CDC source.
- `jackc/pglogrepl`: its `example/pglogrepl_demo` shows the full replication loop.
- `pganalyze/pg_query_go`: the Postgres parser for Go.
- `cockroachdb/pebble`: the key-value store.

Reading:

- DBSP paper (VLDB 2023): https://docs.feldera.com/vldb23.pdf
- Feldera, "Universal IVM": https://www.feldera.com/blog/universal-ivm
- PostgreSQL streaming replication protocol: https://www.postgresql.org/docs/current/protocol-replication.html
- PostgreSQL manual, chapters "Logical Replication" and "Logical Decoding" (message formats, replica identity, replication slots)