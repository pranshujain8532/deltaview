-- Demo schema for DeltaView. Runs once when the Postgres volume is created.

CREATE TABLE customers (
    id   bigint PRIMARY KEY,
    name text,
    city text
);

CREATE TABLE orders (
    id          bigint PRIMARY KEY,
    customer_id bigint,
    status      text,
    amount      numeric(12,2),
    created_at  timestamptz
);

-- The differential tester bumps seq after its own transactions. Because changes
-- arrive in commit order, once DeltaView has seen seq = N it has applied every
-- transaction committed before it.
CREATE TABLE dv_heartbeat (
    id  int PRIMARY KEY,
    seq bigint NOT NULL
);
INSERT INTO dv_heartbeat VALUES (1, 0);

-- DeltaView subtracts the full old row on update and delete, so Postgres must
-- log the whole old row, not only the primary key.
ALTER TABLE customers    REPLICA IDENTITY FULL;
ALTER TABLE orders       REPLICA IDENTITY FULL;
ALTER TABLE dv_heartbeat REPLICA IDENTITY FULL;

CREATE PUBLICATION deltaview_pub FOR TABLE customers, orders, dv_heartbeat;

-- Maps cities to regions. city is not unique, so joins on it have duplicate
-- keys on both sides.
CREATE TABLE regions (
    id     bigint PRIMARY KEY,
    city   text,
    region text
);
ALTER TABLE regions REPLICA IDENTITY FULL;
ALTER PUBLICATION deltaview_pub ADD TABLE regions;
