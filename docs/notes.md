# Notes

Working notes: how the main code paths fit together, and a bug diary.

## How one transaction flows through the code

1. `cdc.Reader.Run` receives XLogData messages and hands the raw pgoutput bytes to
   `cdc.Decoder.Decode`. Relation messages update the table map (and stop the stream if a
   schema no longer matches what the views were bound against). Insert/Update/Delete
   become `+new`, `-old +new` and `-old` in one Z-set per table. Commit closes the
   `TxnBatch` with the transaction's end LSN.
2. The reader pushes the batch into a bounded channel. If the channel is full it keeps
   answering keepalives but reads no more WAL: that is backpressure, and Postgres keeps
   the WAL.
3. `engine.applyLoop` takes the batch (or up to `--batch-max` batches), skips anything at
   or below the applied LSN, and runs `circuit.Step` for every view inside one
   `state.Tx`. Each circuit runs its operators in topological order and finally adds the
   output delta to `v/VIEW/ROW` weights.
4. `m/applied_lsn` and `m/heartbeat` are written into the same Tx, which commits with
   fsync. Only then is the applied LSN stored in the atomic the reader acknowledges from.
5. Output deltas go to subscribers through the hub; a full subscriber buffer drops that
   subscriber with a resync event instead of blocking.

## Invariants the code checks at runtime

- No stored weight is ever negative (view rows, join arrangements, DISTINCT counts, MIN/MAX
  multisets, group row counts). A negative weight can only follow a lost or doubly applied
  change, so the engine stops with `negative weight in state` instead of serving a wrong
  view. The "ack before commit" mutation in the crash test is caught by this check.
- A non-null count never exceeds the group's row count.
- An Update or Delete without the full old row is refused (`REPLICA IDENTITY FULL`).

## Bug diary

Symptom, root cause, fix. Kept honest: most of these were caught by tests before they
reached a commit.

1. **Every view failed to load with "protobuf parse tree version mismatch".**
   Cause: `pg_query.Deparse` needs the parse tree's version number, and a `ParseResult`
   built by hand has version 0. Fix: copy `tree.GetVersion()` into the result that wraps the
   view's SELECT.
2. **Float output disagreed with Postgres for values like 1234567.0.**
   Cause: Go's shortest `%g` switches to exponent notation at 1e6; Postgres prints fixed
   notation for decimal exponents in [-4, 15). Fix: `types.FormatFloat` picks the
   notation by exponent like Postgres does.
3. **The expression-vs-Postgres test reported 60 failures on booleans.**
   Cause: the test harness read `(expr)::text`, and `bool::text` is `true` while the wire
   format is `t`. The evaluator was right; the harness compared different formats. Fix:
   read the raw text wire format, the same format pgoutput uses.
4. **SSE delta events would have started with `{,"lsn"`.**
   Cause: building the JSON by slicing an appended prefix. Caught while reviewing the
   handler before it ran; the SSE integration test now asserts the exact wire format.
5. **`go mod tidy` dropped dependencies that were planned but not yet imported** (pg_query,
   Pebble). Harmless, but it means "go get" must be repeated when a package is first used.

### Found by the adversarial review (bugs the random workload could not reach)

6. **A restored older state store silently skipped changes.** Postgres starts streaming at
   `max(requested, slot.confirmed_flush_lsn)`, so with state older than the slot the gap
   was never applied. Fix: re-snapshot when `confirmed_flush_lsn > applied_lsn`.
   Test: `TestRestoredOlderStateResnapshots`.
7. **Partitioned and inherited tables would freeze.** The snapshot of the parent reads
   every partition, but pgoutput publishes changes under the partition's name, which no
   view reads. Fix: refuse partitioned tables and inheritance trees.
8. **Publication not validated.** A table missing from the publication, a publication
   without `delete`, or a row filter would leave the views drifting with no error. Fix:
   `cdc.CheckPublication` at startup.
9. **`real` was evaluated as float8.** `0.1::real * 3` differs. Fix: reject float4.
10. **Float SUM became NaN forever after deleting an Infinity row** (Inf + -Inf = NaN).
    Fix: count +Inf, -Inf and NaN inputs separately from the finite sum.
11. **Numeric `%` could return the wrong sign** because the quotient was rounded to 16
    places before truncating. Fix: exact `QuoRem`.
12. **`'... +05:30'::timestamp` was shifted to UTC;** Postgres ignores the offset for
    `timestamp without time zone`. Fix: keep the wall-clock time.
13. **Schema detection ignored typmod** (`numeric(12,2)` to `numeric(14,2)`). Fix: compare
    typmod in Relation messages, and typmod and attnum in the startup fingerprint.
14. **`SUM(2)` and `SUM(2.0)` were merged** because aggregates were keyed by printed form.
    Fix: include the argument type in the key.
15. **A TRUNCATE of any published table stopped the engine.** Now only a truncate of a table
    a view reads does.

## Mutation checks (planted bugs the tests must catch)

| Planted bug | Caught by | Where |
|---|---|---|
| Filter ignores deletes | difftest, seed 1 | after 20 transactions |
| MIN ignores deletes | difftest, seed 1 | after 20 transactions |
| Join drops the dA ⋈ dB term | difftest, seed 1 | first check (the snapshot itself changes both sides of the self-join) |
| Join counts dA ⋈ dB twice | difftest, seed 1 | first check |
| Integer division floors instead of truncating | expression test vs Postgres | `-7 / 2` |
| Ack before commit, then crash | crash test (before_commit fail point) | engine stops on a negative weight; the tester fails |
