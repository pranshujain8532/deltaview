// Package cdc reads Postgres logical replication (pgoutput) and assembles each
// committed transaction into a TxnBatch of per-table Z-set deltas. It is the
// only input to the engine. It also reads the initial snapshot.
package cdc
