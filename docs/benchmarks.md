# Benchmarks

Report measured numbers only. Record machine, Postgres version and data size with each.

| Benchmark | How | What it shows |
|---|---|---|
| Refresh vs incremental | Tables of 100 thousand, 1 million, 10 million rows; apply 1,000 changes; time REFRESH MATERIALIZED VIEW and DeltaView | Refresh grows with table size; DeltaView stays roughly flat |
| End-to-end latency | Postgres commit time to view commit; p50, p99 | Freshness |
| Throughput | Changes applied per second, per view shape | Capacity of one core |
| Group commit | fsync per transaction vs per batch | Latency and throughput trade-off |
| State size | Store size vs source table size | Cost of join arrangements |
| Recovery time | Restart with N transactions pending | Operational behaviour |
| vs pg_ivm | Write throughput on Postgres with each | Trigger cost on writes vs asynchronous lag |

cmd/dvbench runs each with one command and writes CSV to bench/results/.
