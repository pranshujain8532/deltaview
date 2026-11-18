---
paths:
  - "internal/ops/**"
  - "internal/zset/**"
  - "internal/circuit/**"
  - "internal/expr/**"
---
# Operator rules

- A delta is a Z-set: row -> integer weight. Insert +1, delete -1, update = -old and +new. Zero weights are never stored.
- Filter and Project: d_out = op(d_in). No state. Project keeps weights; equal output rows add.
- Join: out = dA join B_old; A += dA; out += A_new join dB; B += dB. Output weight = wa * wb.
- NULL join keys never match and are not stored in arrangements.
- Aggregate, per touched group: read the old output row, apply all delta rows of the group, compute the new output row, emit -old and +new. At most one pair per group per step. Remove the group when its row count is 0.
- SUM ignores NULL. SUM of only NULLs is NULL, not 0. COUNT(col) ignores NULL. COUNT(*) does not. AVG = sum / non-null count.
- GROUP BY puts all NULL keys in one group. Aggregates with no GROUP BY always output exactly one row.
- MIN and MAX use an ordered multiset per group (value -> count). Never recompute from the base table.
- Expressions use SQL three-valued logic. WHERE keeps a row only when the condition is TRUE.
- State only through state.Tx. A struct field may cache, but must be rebuildable from the store and dropped if the batch fails.
- Worked examples: docs/concepts.md.
