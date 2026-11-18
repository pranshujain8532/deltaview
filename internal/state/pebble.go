package state

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/cockroachdb/pebble/v2"
)

// Pebble is the durable Store. One indexed batch per transaction: the batch
// reads its own writes during the step and commits atomically, so operator
// state, view rows and the applied LSN are saved together or not at all.
type Pebble struct {
	db *pebble.DB
}

// OpenPebble opens (or creates) a Pebble store in dir.
func OpenPebble(dir string) (*Pebble, error) {
	db, err := pebble.Open(dir, &pebble.Options{Logger: quietLogger{}})
	if err != nil {
		return nil, fmt.Errorf("state: open pebble at %s: %w", dir, err)
	}
	return &Pebble{db: db}, nil
}

// Begin starts an indexed batch.
func (p *Pebble) Begin() Tx { return &pebbleTx{b: p.db.NewIndexedBatch()} }

// Snapshot pins the current committed state for reading.
func (p *Pebble) Snapshot() Snapshot { return pebbleSnap{s: p.db.NewSnapshot()} }

// Wipe deletes every key and syncs.
func (p *Pebble) Wipe() error {
	if err := p.db.DeleteRange([]byte{0x00}, []byte{0xff, 0xff, 0xff, 0xff}, pebble.Sync); err != nil {
		return fmt.Errorf("state: wipe: %w", err)
	}
	return nil
}

// SizeBytes reports disk usage.
func (p *Pebble) SizeBytes() int64 { return int64(p.db.Metrics().DiskSpaceUsage()) }

// Close closes the database.
func (p *Pebble) Close() error { return p.db.Close() }

type pebbleReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
}

func pget(r pebbleReader, key []byte) ([]byte, bool, error) {
	v, closer, err := r.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("state: get: %w", err)
	}
	out := append([]byte(nil), v...)
	return out, true, closer.Close()
}

func pscan(r pebbleReader, prefix []byte, rev bool) Iterator {
	it, err := r.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: PrefixEnd(prefix)})
	if err != nil {
		return &errIter{err: fmt.Errorf("state: iterator: %w", err)}
	}
	return &pebbleIter{it: it, rev: rev}
}

type pebbleTx struct {
	b    *pebble.Batch
	done bool
	err  error
}

func (t *pebbleTx) Get(key []byte) ([]byte, bool, error) { return pget(t.b, key) }
func (t *pebbleTx) Scan(prefix []byte) Iterator          { return pscan(t.b, prefix, false) }
func (t *pebbleTx) ScanReverse(prefix []byte) Iterator   { return pscan(t.b, prefix, true) }

func (t *pebbleTx) Set(key, val []byte) {
	if err := t.b.Set(key, val, nil); err != nil && t.err == nil {
		t.err = err
	}
}

func (t *pebbleTx) Delete(key []byte) {
	if err := t.b.Delete(key, nil); err != nil && t.err == nil {
		t.err = err
	}
}

// Commit writes the batch. With sync, Pebble fsyncs the WAL before
// returning: this is the point after which the LSN may be acknowledged.
func (t *pebbleTx) Commit(sync bool) error {
	if t.done {
		return errors.New("state: commit of a finished transaction")
	}
	t.done = true
	defer t.b.Close()
	if t.err != nil {
		return fmt.Errorf("state: batch write: %w", t.err)
	}
	opts := pebble.NoSync
	if sync {
		opts = pebble.Sync
	}
	if err := t.b.Commit(opts); err != nil {
		return fmt.Errorf("state: commit: %w", err)
	}
	return nil
}

func (t *pebbleTx) Discard() {
	if !t.done {
		t.done = true
		_ = t.b.Close()
	}
}

type pebbleSnap struct{ s *pebble.Snapshot }

func (s pebbleSnap) Get(key []byte) ([]byte, bool, error) { return pget(s.s, key) }
func (s pebbleSnap) Scan(prefix []byte) Iterator          { return pscan(s.s, prefix, false) }
func (s pebbleSnap) ScanReverse(prefix []byte) Iterator   { return pscan(s.s, prefix, true) }
func (s pebbleSnap) Close() error                         { return s.s.Close() }

type pebbleIter struct {
	it      *pebble.Iterator
	rev     bool
	started bool
}

func (p *pebbleIter) Next() bool {
	if !p.started {
		p.started = true
		if p.rev {
			return p.it.Last()
		}
		return p.it.First()
	}
	if p.rev {
		return p.it.Prev()
	}
	return p.it.Next()
}
func (p *pebbleIter) Key() []byte   { return p.it.Key() }
func (p *pebbleIter) Value() []byte { return p.it.Value() }
func (p *pebbleIter) Close() error  { return p.it.Close() }

type errIter struct{ err error }

func (e *errIter) Next() bool    { return false }
func (e *errIter) Key() []byte   { return nil }
func (e *errIter) Value() []byte { return nil }
func (e *errIter) Close() error  { return e.err }

// quietLogger drops Pebble's informational messages (they would interleave
// with the engine's own logs) and keeps errors.
type quietLogger struct{}

func (quietLogger) Infof(string, ...interface{}) {}
func (quietLogger) Errorf(format string, args ...interface{}) {
	log.Printf("pebble: "+format, args...)
}
func (quietLogger) Fatalf(format string, args ...interface{}) {
	log.Printf("pebble: fatal: "+format, args...)
	os.Exit(1)
}
