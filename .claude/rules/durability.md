---
paths:
  - "internal/engine/**"
  - "internal/state/**"
---
# Durability rules

Apply loop, in this order, for each TxnBatch:
1. If batch.EndLSN <= applied_lsn, skip it (a re-sent duplicate).
2. tx = store.Begin()
3. Run every circuit with tx.
4. tx.Set(m/applied_lsn, batch.EndLSN)
5. tx.Commit(sync = true). This is the durability point.
6. Acknowledge batch.EndLSN to Postgres. Only now.
7. Publish output deltas to subscribers. Never block on a subscriber.

Crash cases that must hold:
- Crash before step 5: old state, old LSN. The transaction is re-sent and applied once.
- Crash between 5 and 6: new state, new LSN. The transaction is re-sent and skipped.
- Crash after 6: nothing is re-sent.

Other rules:
- Composite keys are self-delimiting and order-preserving (docs/design.md).
- If m/snapshot_done is missing at startup, wipe state, drop the slot, snapshot again.
- If the slot is lost (wal_status = lost), re-snapshot. There is no other correct recovery.
