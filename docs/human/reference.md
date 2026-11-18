# Glossary and references

## Glossary
| Term | Meaning |
|---|---|
| WAL | Postgres's log of every change, written before the change is applied |
| LSN | A position in the WAL; only grows |
| Logical decoding | Turning WAL records back into row changes |
| pgoutput | The built-in format for logical decoding output |
| Publication | The list of tables whose changes are published |
| Replication slot | Server-side bookmark that pins WAL for one consumer |
| Replica identity | What Postgres sends as the old row on update and delete |
| CDC | Change data capture: reading a database's changes as a stream |
| Z-set | Map from row to integer weight |
| Delta | A Z-set describing a change |
| IVM | Incremental view maintenance |
| Linear operator | One where op(A + B) = op(A) + op(B); needs no state |
| Arrangement | An index of a Z-set by key, used by joins |
| Circuit | The operators of one view, wired together |
| Applied LSN | The last transaction whose effects are durably in the state store |
| Exactly-once | Each change affects the result once, even across crashes and re-delivery |
| Backpressure | Slowing the producer when the consumer cannot keep up |
| Differential testing | Checking a system against a trusted reference on random inputs |

## Repos (read for ideas, write your own code)
- feldera/feldera: the DBSP engine. Read crates/dbsp for Z-sets and operators.
- sraoss/pg_ivm: trigger-based IVM inside Postgres. Your comparison baseline.
- MaterializeInc/materialize: source-available under BSL, not open source. Study only.
- risingwavelabs/risingwave: a streaming database with a Postgres CDC source.
- jackc/pglogrepl: example/pglogrepl_demo shows the full replication loop.
- pganalyze/pg_query_go: the Postgres parser for Go.
- cockroachdb/pebble: the key-value store.

## Reading
- DBSP paper (VLDB 2023): https://docs.feldera.com/vldb23.pdf
- Feldera, "Universal IVM": https://www.feldera.com/blog/universal-ivm
- PostgreSQL streaming replication protocol: https://www.postgresql.org/docs/current/protocol-replication.html
- PostgreSQL manual: chapters "Logical Replication" and "Logical Decoding"
- Claude Code docs: https://code.claude.com/docs/en/memory , /sub-agents , /context-window , /model-config
