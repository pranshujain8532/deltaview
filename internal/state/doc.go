// Package state is the key-value store behind every operator. One Tx per
// Postgres transaction, committed atomically together with the applied LSN.
package state
