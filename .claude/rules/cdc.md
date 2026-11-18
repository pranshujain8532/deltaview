---
paths:
  - "internal/cdc/**"
---
# CDC rules

- Connect with replication=database. Plugin pgoutput, proto_version 1, publication deltaview_pub, slot deltaview_slot.
- A Relation message gives a table's columns and types. It is re-sent after a schema change. If it differs from the stored schema, stop with a clear error.
- Values arrive as text. Convert by column type. numeric becomes a decimal. Unknown type is an error.
- An Update must carry the old tuple (tables use REPLICA IDENTITY FULL). Missing old tuple is an error.
- An unchanged-TOAST marker in the new tuple means: copy that value from the old tuple.
- Begin..Commit becomes one TxnBatch. EndLSN is the transaction end LSN from the Commit message.
- Insert = +new. Delete = -old. Update = -old and +new. Truncate is an error in v1.
- Answer keepalives. Send a status update at least every 10 seconds.
- The flushed LSN reported to Postgres is the last durably committed EndLSN, nothing newer.
- dv_heartbeat is special: record its latest seq for /status. It is never a view input.
