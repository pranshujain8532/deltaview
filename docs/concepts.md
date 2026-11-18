# Concepts

## 1. The WAL and logical replication

- Postgres writes every change to the Write-Ahead Log (WAL) before changing table files.
- Logical decoding turns WAL records back into row changes. pgoutput is the built-in format.
- A publication is the list of tables you want changes for.
- A replication slot is a server-side bookmark. It remembers what you confirmed and forces
  Postgres to keep all WAL after that. A slow or dead consumer can fill the Postgres disk.
- An LSN is a position in the WAL. It only grows. It is the clock of the whole system.
- Changes arrive one whole transaction at a time, in commit order. Uncommitted and rolled-back
  work is never sent.
- Replica identity controls what is sent as the OLD row. DeltaView needs the full old row, so
  tables use REPLICA IDENTITY FULL.

## 2. Z-sets

A Z-set maps a row to an integer weight. +1 = added, -1 = removed. An update is -old and +new.
A table is a Z-set with positive weights. Adding Z-sets adds weights; weight 0 disappears.

```text
orders          = { (1, paid, 100): +1,  (2, paid, 50): +1 }
change          = { (2, paid, 50): -1,   (2, paid, 70): +1 }     order 2: 50 -> 70
orders + change = { (1, paid, 100): +1,  (2, paid, 70): +1 }
```

Inserts, deletes and updates become one thing. Every operator needs one rule: input delta in,
output delta out.

## 3. The core equation

```text
View = Q(Table)        dView = Q(Table + dT) - Q(Table)
```

The project is computing dView without computing Q(Table) again.

## 4. Linear operators: filter, project

filter(A + B) = filter(A) + filter(B), so d_out = filter(d_in). No state.
With WHERE status = 'paid', the delta above passes unchanged; a 'pending' row is dropped.
Project maps each row and keeps its weight. Equal output rows add their weights.

## 5. Aggregation

Not linear: a group's output depends on all its rows. Keep state per group.
For COUNT and SUM the state is (count, sum). For each touched group:

1. Read the old output row (if count > 0).
2. count += weight, sum += weight * value, for every delta row of the group.
3. Compute the new output row (if count > 0).
4. Emit { old: -1, new: +1 }.

```text
Delhi state (2, 150), new paid order of 70:
emit { (Delhi, 2, 150): -1,  (Delhi, 3, 220): +1 }
```

Count reaches 0: emit only the -1.

MIN and MAX: if the current minimum is deleted you need the next one. Keep an ordered multiset
per group (value -> count). Cost: memory grows with distinct values per group.

NULL rules: SUM ignores NULL; SUM of only NULLs is NULL. COUNT(col) ignores NULL; COUNT(*)
does not. GROUP BY puts NULL keys in one group. No GROUP BY means exactly one output row,
even on an empty table.

## 6. Joins

Each matching pair (a, b) gives an output row with weight wa * wb.

```text
(A + dA) JOIN (B + dB) = A JOIN B + dA JOIN B + A JOIN dB + dA JOIN dB
d_out                  =            dA JOIN B + A JOIN dB + dA JOIN dB
```

Keep an index of each side by join key (an arrangement). A join stores both inputs.

```text
1. out  = dA JOIN B_old
2. A    = A + dA
3. out += A_new JOIN dB        covers A JOIN dB and dA JOIN dB
4. B    = B + dB
```

```text
customers B = { (7, Delhi): +1 }     orders A = empty
Txn 1: insert order (1, cust 7, 100)     dA = { (1,7,100): +1 }
  step 1 -> { (1,7,100,Delhi): +1 }
Txn 2: customer 7 moves to Mumbai        dB = { (7,Delhi): -1, (7,Mumbai): +1 }
  step 3 -> { (1,7,100,Delhi): -1, (1,7,100,Mumbai): +1 }
```

The aggregate downstream moves 100 from Delhi to Mumbai with no special code.

## 7. Exactly-once

The stream is at-least-once: after a crash Postgres re-sends what you had not confirmed.
Applying a delta twice corrupts counts. Fix: save operator state and the last applied LSN in
one atomic write. On restart, resume from the saved LSN and skip anything at or before it.

```text
at-least-once delivery + atomic (state, position) + skip duplicates = effectively exactly-once
```

Confirm an LSN to Postgres only after it is durably saved.

## 8. Initial snapshot

- Creating the slot returns a consistent point (an LSN) and an exported snapshot name.
- A transaction pinned to that snapshot sees exactly what committed before the point.
- Stream from the point for everything after it.
- Feed snapshot rows through the circuit as +1 deltas.
