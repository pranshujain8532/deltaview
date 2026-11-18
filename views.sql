-- Views maintained by DeltaView. Each one must use the SQL subset in
-- docs/design.md; anything else is rejected at startup with an error that
-- names the feature. The differential tester checks every view in this file.

-- Filter and project.
CREATE MATERIALIZED VIEW paid_orders AS
SELECT id, amount FROM orders WHERE status = 'paid';

-- COUNT per group; groups appear and disappear.
CREATE MATERIALIZED VIEW orders_per_status AS
SELECT status, COUNT(*) AS orders FROM orders GROUP BY status;

-- SUM and AVG with NULL amounts and NULL group keys.
CREATE MATERIALIZED VIEW amount_by_customer AS
SELECT customer_id, SUM(amount) AS total, AVG(amount) AS avg_amount, COUNT(amount) AS priced
FROM orders GROUP BY customer_id;

-- MIN and MAX survive deletes of the current extreme.
CREATE MATERIALIZED VIEW amount_range_by_status AS
SELECT status, MIN(amount) AS min_amount, MAX(amount) AS max_amount
FROM orders GROUP BY status;

-- No GROUP BY: always exactly one row, even with no orders.
CREATE MATERIALIZED VIEW order_totals AS
SELECT COUNT(*) AS orders, SUM(amount) AS total, MIN(created_at) AS first_order
FROM orders;

-- A global aggregate over a filter that is often empty.
CREATE MATERIALIZED VIEW refunded_totals AS
SELECT COUNT(*) AS refunds, SUM(amount) AS total, AVG(amount) AS avg_amount, MAX(amount) AS biggest
FROM orders WHERE status = 'refunded';

-- Expressions: arithmetic, numeric division, CASE, COALESCE, IN, OR, IS NULL.
CREATE MATERIALIZED VIEW order_flags AS
SELECT id, customer_id, amount * 2 AS doubled, amount / 3 AS third, id % 7 AS bucket,
       CASE WHEN amount > 100 THEN 'big' WHEN amount IS NULL THEN 'unknown' ELSE 'small' END AS size,
       COALESCE(status, 'none') AS status
FROM orders
WHERE amount IS NULL OR customer_id IS NULL OR status IN ('shipped', 'refunded') OR amount > 150;

-- DISTINCT.
CREATE MATERIALIZED VIEW status_customer_pairs AS
SELECT DISTINCT status, customer_id FROM orders;

-- HAVING: a filter after the aggregate.
CREATE MATERIALIZED VIEW busy_customers AS
SELECT customer_id, COUNT(*) AS orders FROM orders GROUP BY customer_id HAVING COUNT(*) > 3;

-- MAX over text (C collation).
CREATE MATERIALIZED VIEW customers_per_city AS
SELECT city, COUNT(*) AS customers, MAX(name) AS last_name FROM customers GROUP BY city;

-- Join: both sides change, keys move, customers vanish under live orders.
CREATE MATERIALIZED VIEW orders_with_customers AS
SELECT o.id, o.amount, o.status, c.name, c.city
FROM orders o JOIN customers c ON o.customer_id = c.id;

-- The view from docs/overview.md.
CREATE MATERIALIZED VIEW revenue_by_city AS
SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
FROM orders o JOIN customers c ON o.customer_id = c.id
WHERE o.status = 'paid'
GROUP BY c.city;

-- Three-way join on a text key with duplicates on both sides.
CREATE MATERIALIZED VIEW revenue_by_region AS
SELECT r.region, COUNT(*) AS orders, SUM(o.amount) AS revenue, MIN(o.amount) AS smallest
FROM orders o JOIN customers c ON o.customer_id = c.id JOIN regions r ON c.city = r.city
GROUP BY r.region;

-- Self-join: one transaction changes both inputs at once.
CREATE MATERIALIZED VIEW order_pairs_per_customer AS
SELECT a.customer_id, COUNT(*) AS pairs
FROM orders a JOIN orders b ON a.customer_id = b.customer_id AND a.id < b.id
GROUP BY a.customer_id;
