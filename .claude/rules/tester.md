---
paths:
  - "cmd/dvtest/**"
---
# Differential tester rules

- The tester is the judge. Changing it so a failing run passes is forbidden. If it seems wrong, stop and ask.
- It is the only writer to Postgres during a run. All randomness comes from one seed.
- Sync: after the test transactions, bump dv_heartbeat.seq to N, then poll /status until the engine reports N.
- Compare as multisets (rows with counts). Decimals compare by value. Round AVG to 6 places on both sides.
- On mismatch print: the seed, the transactions since the last good check, the rows that differ.
- The workload must include: inserts, updates, deletes, NULLs, duplicate non-key values, multi-row statements,
  multi-statement transactions, rows moving in and out of WHERE, rows moving between groups, groups emptied,
  both join sides changed in one transaction, join key updates, NULL join keys.
