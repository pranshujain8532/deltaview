package engine

import (
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
)

// Fail point names. Each one exits the process at a precise spot in the
// apply/ack sequence, so the crash test can prove all three crash cases.
const (
	FailBeforeCommit = "before_commit" // old state, old LSN on disk
	FailAfterCommit  = "after_commit"  // new state on disk, LSN not yet acked
	FailAfterAck     = "after_ack"     // new state on disk and acked
)

// Failpoints exits the process when a named point is hit for the Nth time.
// The zero value never fires.
type Failpoints struct {
	name  string
	after int64
	hits  atomic.Int64
	exit  func(code int)
}

// FailpointsFromEnv reads DV_FAILPOINT (a name) and DV_FAILPOINT_AFTER
// (fire on the Nth hit, default 1).
func FailpointsFromEnv() (*Failpoints, error) {
	f := &Failpoints{name: os.Getenv("DV_FAILPOINT"), after: 1, exit: os.Exit}
	switch f.name {
	case "", FailBeforeCommit, FailAfterCommit, FailAfterAck:
	default:
		return nil, fmt.Errorf("unknown DV_FAILPOINT %q", f.name)
	}
	if s := os.Getenv("DV_FAILPOINT_AFTER"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("DV_FAILPOINT_AFTER must be a positive integer, got %q", s)
		}
		f.after = n
	}
	return f, nil
}

// Hit counts a pass through a fail point and exits when it fires.
func (f *Failpoints) Hit(name string) {
	if f == nil || f.name != name {
		return
	}
	if f.hits.Add(1) == f.after {
		fmt.Fprintf(os.Stderr, "failpoint %s fired on hit %d\n", name, f.after)
		f.exit(77)
	}
}
