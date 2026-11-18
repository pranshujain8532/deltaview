// Every number on the site comes from a real run (see README "Measured numbers").
export const GITHUB = 'https://github.com/pranshujain8532/deltaview'

export const refreshVsIncremental = [
  { rows: 100_000, refresh: 19.7, deltaview: 67.8 },
  { rows: 500_000, refresh: 43.5, deltaview: 59.1 },
  { rows: 2_000_000, refresh: 127.8, deltaview: 62.5 },
]

export const throughput = [
  { shape: 'Filter', sql: 'WHERE status = ...', txns: 52442 },
  { shape: 'Join', sql: 'orders JOIN customers', txns: 14227 },
  { shape: 'Aggregate', sql: 'GROUP BY customer_id', txns: 13085 },
  { shape: 'Join + aggregate', sql: 'revenue_by_city', txns: 11542 },
]

export const proof = [
  { value: 0, label: 'mismatches', detail: '50 seeds × 1,000 random transactions, 14 views, compared with Postgres every 10 transactions' },
  { value: 70700, label: 'view comparisons', detail: 'each one a multiset diff against the same SELECT run on Postgres' },
  { value: 200, label: 'kill -9 cycles', detail: 'while a writer committed 31,389 transactions, then one clean check' },
  { value: 112, label: 'expressions', detail: 'evaluated by DeltaView and by Postgres on the same rows: identical' },
  { value: 6, label: 'planted bugs caught', detail: 'filter, MIN, both join terms, integer division, ack-before-commit' },
  { value: 9, label: 'review findings fixed', detail: 'from an adversarial review, each with a regression test' },
]

export const stack = ['Go 1.27', 'PostgreSQL 17', 'pgoutput', 'pglogrepl', 'pg_query_go', 'Pebble', 'Prometheus', 'OpenTelemetry', 'Server-sent events', 'Docker']
