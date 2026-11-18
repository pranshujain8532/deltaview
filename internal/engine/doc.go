// Package engine runs the apply loop: one goroutine that owns all state,
// applies each transaction to every view, commits, then acknowledges the LSN.
package engine
