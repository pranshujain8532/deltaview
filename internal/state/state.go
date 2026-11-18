package state

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Iterator walks keys in byte order (or reverse byte order).
type Iterator interface {
	// Next advances to the next entry. The first call moves to the first one.
	Next() bool
	Key() []byte
	Value() []byte
	Close() error
}

// Reader is read access to a consistent state: a snapshot, or a Tx that
// also sees its own uncommitted writes.
type Reader interface {
	Get(key []byte) ([]byte, bool, error)
	// Scan iterates all keys starting with prefix in ascending order.
	Scan(prefix []byte) Iterator
	// ScanReverse iterates all keys starting with prefix in descending order.
	// MAX uses it to find the largest value of a group in one seek.
	ScanReverse(prefix []byte) Iterator
}

// Snapshot is a Reader pinned at one commit. HTTP handlers read views from
// a snapshot so they never see a half-applied transaction.
type Snapshot interface {
	Reader
	Close() error
}

// Tx collects the writes of one step. Reads see the Tx's own writes.
// Nothing is visible to snapshots until Commit.
type Tx interface {
	Reader
	Set(key, val []byte)
	Delete(key []byte)
	// Commit applies every write atomically. With sync it returns only after
	// the data is on stable storage: the durability point of the engine.
	Commit(sync bool) error
	// Discard drops the Tx without applying it.
	Discard()
}

// Store is the strategy interface the engine runs on: memory for tests and
// Pebble for durable runs. Only the apply loop calls Begin.
type Store interface {
	Begin() Tx
	Snapshot() Snapshot
	// Wipe deletes every key. Used before a fresh initial snapshot.
	Wipe() error
	// SizeBytes estimates the space the state uses.
	SizeBytes() int64
	Close() error
}

// ErrCorruptWeight is returned for a stored weight that is not a varint.
var ErrCorruptWeight = errors.New("state: corrupt weight")

// Meta keys. They live under m/ so they can never collide with operator keys.
var (
	KeyAppliedLSN   = []byte("m/applied_lsn")
	KeySnapshotDone = []byte("m/snapshot_done")
	KeyHeartbeat    = []byte("m/heartbeat")
	KeyViewsHash    = []byte("m/views_hash")
)

// SchemaKey is where a source table's column list is stored.
func SchemaKey(table string) []byte { return []byte("m/schema/" + table) }

// Key concatenates a readable prefix ("j/", "a/", ...) and encoded parts.
// Every part must be self-delimiting (an encoded value or row) so different
// part boundaries can never produce the same key.
func Key(prefix string, parts ...[]byte) []byte {
	n := len(prefix)
	for _, p := range parts {
		n += len(p)
	}
	k := make([]byte, 0, n)
	k = append(k, prefix...)
	for _, p := range parts {
		k = append(k, p...)
	}
	return k
}

// PrefixEnd returns the smallest key greater than every key with prefix,
// or nil when there is none (prefix is all 0xff).
func PrefixEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

// EncodeInt encodes a weight, count or LSN.
func EncodeInt(v int64) []byte { return binary.AppendVarint(nil, v) }

// DecodeInt decodes EncodeInt.
func DecodeInt(b []byte) (int64, error) {
	v, n := binary.Varint(b)
	if n <= 0 || n != len(b) {
		return 0, ErrCorruptWeight
	}
	return v, nil
}

// GetInt reads an integer key, returning 0 when it is absent.
func GetInt(r Reader, key []byte) (int64, error) {
	b, ok, err := r.Get(key)
	if err != nil || !ok {
		return 0, err
	}
	v, err := DecodeInt(b)
	if err != nil {
		return 0, fmt.Errorf("%w at %q", err, key)
	}
	return v, nil
}

// AddWeight adds delta to the integer at key and deletes the key when the
// result is zero, so state never holds zero weights. It returns old and new.
func AddWeight(tx Tx, key []byte, delta int64) (old, now int64, err error) {
	old, err = GetInt(tx, key)
	if err != nil {
		return 0, 0, err
	}
	now = old + delta
	if now == 0 {
		tx.Delete(key)
	} else {
		tx.Set(key, EncodeInt(now))
	}
	return old, now, nil
}

// GetLSN reads an LSN meta key (0 when absent).
func GetLSN(r Reader, key []byte) (uint64, error) {
	v, err := GetInt(r, key)
	return uint64(v), err
}

// Collect drains an iterator into key/value pairs. Operators collect before
// writing so they never mutate the range they are iterating.
func Collect(it Iterator) ([]KV, error) {
	var out []KV
	for it.Next() {
		out = append(out, KV{Key: append([]byte(nil), it.Key()...), Value: append([]byte(nil), it.Value()...)})
	}
	return out, it.Close()
}

// KV is one key-value pair.
type KV struct {
	Key, Value []byte
}
