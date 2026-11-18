# Overview

**Pitch:** DeltaView is a materialized view that updates itself by processing only the changed
rows, instead of re-running the whole query.

## The problem

```sql
CREATE MATERIALIZED VIEW revenue_by_city AS
SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
FROM orders o JOIN customers c ON o.customer_id = c.id
WHERE o.status = 'paid'
GROUP BY c.city;
```

With 10 million orders, `REFRESH MATERIALIZED VIEW` re-reads all of them even when one order
changed. Teams then show stale numbers, or hand-write update logic in triggers and app code.
That logic usually breaks on deletes, on rows that move between groups, and on joins where
both sides change.

## What DeltaView does

1. Listens to Postgres's change stream: every committed insert, update and delete.
2. Turns each transaction into a small delta.
3. Pushes the delta through the view's query plan. Each operator turns an input delta into an output delta.
4. Stores the up-to-date result and streams changes to subscribers.

The work is proportional to the size of the change, not the size of the table.

## What it is not

- Not a database. Postgres stays the source of truth.
- Not all of SQL. A fixed subset (docs/design.md).
- Not distributed. One process.

Same idea in production systems: Materialize, Feldera, RisingWave, and pg_ivm (inside Postgres,
with triggers). The theory is the DBSP paper (VLDB 2023).
