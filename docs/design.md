# Design

## Stack

| Need | Choice | Notes |
|---|---|---|
| Replication protocol | github.com/jackc/pglogrepl | Existence checked 11 Nov 2026 |
| Normal queries | github.com/jackc/pgx/v5 | pglogrepl builds on its pgconn package |
| SQL parser | github.com/pganalyze/pg_query_go/v6 | Needs cgo; on Windows use WSL |
| State store | github.com/cockroachdb/pebble | Indexed batches and snapshots |
| Decimals | a decimal library such as shopspring/decimal | Not re-checked; confirm before adding |
| Property tests | standard library: testing/quick, go test -fuzz | No dependency |
| Metrics, tracing | Prometheus Go client, OpenTelemetry Go SDK | Added in M10; confirm module paths then |
| Postgres | Docker image postgres:17 or newer | wal_level=logical |

## Repo layout

```text
deltaview/
  CLAUDE.md  docker-compose.yml  Makefile  views.sql
  db/init.sql               demo tables, publication
  cmd/deltaview/            the engine
  cmd/dvtest/               differential tester
  cmd/dvbench/              benchmarks
  internal/types/           Value, Row, canonical encoding
  internal/zset/            Z-set
  internal/cdc/             replication client, pgoutput decoding, snapshot
  internal/sql/             parser adapter, binder, logical plan
  internal/expr/            expression evaluation
  internal/ops/             Filter, Project, Join, Aggregate, Distinct
  internal/circuit/         wires operators, runs one step
  internal/state/           Store interface, memory and Pebble versions
  internal/engine/          apply loop, checkpoint, recovery
  internal/serve/           HTTP and SSE
  internal/obs/             metrics and tracing
```

## Key types

```go
// internal/types
type Row []Value   // Value = null | int64 | decimal | float64 | text | bool | timestamp

// Encode returns a canonical, order-preserving byte encoding.
// Equal rows give equal bytes. Byte order equals value order.
func Encode(r Row) []byte

// internal/zset
type ZSet struct{ /* encoded row -> (row, weight) */ }
func (z *ZSet) Add(r Row, w int64)   // drops the entry when the weight reaches 0
func (z *ZSet) Merge(o *ZSet)
func (z *ZSet) Negate() *ZSet

// internal/cdc
type TxnBatch struct {
    EndLSN     uint64                 // end of this transaction in the WAL
    CommitTime time.Time
    Deltas     map[string]*zset.ZSet  // table name -> delta
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

Tx must read its own writes inside a step. Pebble's indexed batch does this.

## Patterns

| Pattern | Where | Why |
|---|---|---|
| Strategy | state.Store (memory or Pebble); aggregate functions | Swap implementations without touching operators |
| Composite | A circuit is a tree of operators with one Step interface | The engine runs any view the same way |
| Adapter | pgoutput to TxnBatch; pg_query AST to our plan | Keeps third-party shapes out of the core |
| Builder | Planner builds a circuit from a logical plan | Construction separate from execution |
| Observer | Subscription hub pushes deltas to clients | Many readers, one writer |
| Single-writer | The apply loop owns all state | No locks, deterministic replay |

## State key layout

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

- Unambiguous: parts of a composite key are self-delimiting, so ("ab","c") and ("a","bc") differ.
- Order-preserving: under x/OP/GROUPKEY/ the first key is the minimum. MIN is one seek.

## Supported SQL (v1)

- Select list: columns, simple arithmetic, COUNT(*), COUNT(col), SUM, AVG, MIN, MAX
- FROM one table, or INNER JOIN on column equality
- WHERE with AND, OR, NOT, comparisons, IS NULL, IN (list)
- GROUP BY columns

| Rejected | Why |
|---|---|
| ORDER BY, LIMIT | Incremental top-k is its own problem. Clients sort |
| Outer joins | Need rules for rows that gain or lose their last match |
| Subqueries, window functions | Each needs its own incremental rule |
| now(), random() | The answer changes with no row change, so there is no delta |
| Schema changes | Logical replication carries no DDL. Detect and stop |

## HTTP API

```text
GET /views                    list of views
GET /views/NAME               rows plus the LSN they are valid at
GET /views/NAME/subscribe     SSE: a snapshot event with its LSN, then delta events
GET /status                   applied LSN, heartbeat sequence, slot lag
GET /metrics                  Prometheus metrics
```
