package state

import (
	"bytes"
	"sort"
	"strings"
	"sync"
)

// Memory is an in-memory Store. It loses everything on exit, so it is used
// for tests and for runs that accept a full re-snapshot on every restart.
type Memory struct {
	mu   sync.RWMutex
	data map[string][]byte
	keys []string // sorted
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{data: make(map[string][]byte)} }

// Begin starts a write transaction.
func (m *Memory) Begin() Tx { return &memTx{m: m, writes: make(map[string]*[]byte)} }

// Snapshot copies the current state. Copying is O(state), which is fine for
// the sizes the memory store is used with.
func (m *Memory) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := &Memory{data: make(map[string][]byte, len(m.data)), keys: append([]string(nil), m.keys...)}
	for k, v := range m.data {
		s.data[k] = v
	}
	return memSnap{s}
}

// Wipe deletes everything.
func (m *Memory) Wipe() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = make(map[string][]byte)
	m.keys = nil
	return nil
}

// SizeBytes sums key and value lengths.
func (m *Memory) SizeBytes() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var n int64
	for k, v := range m.data {
		n += int64(len(k) + len(v))
	}
	return n
}

// Close is a no-op.
func (m *Memory) Close() error { return nil }

// Len is the number of keys, for tests.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.data)
}

func (m *Memory) get(key string) ([]byte, bool) {
	v, ok := m.data[key]
	return v, ok
}

// rangeKeys returns the stored keys with prefix, in order.
func (m *Memory) rangeKeys(prefix string) []string {
	i := sort.SearchStrings(m.keys, prefix)
	j := i
	for j < len(m.keys) && strings.HasPrefix(m.keys[j], prefix) {
		j++
	}
	return m.keys[i:j]
}

func (m *Memory) set(k string, v []byte) {
	if _, ok := m.data[k]; !ok {
		i := sort.SearchStrings(m.keys, k)
		m.keys = append(m.keys, "")
		copy(m.keys[i+1:], m.keys[i:])
		m.keys[i] = k
	}
	m.data[k] = v
}

func (m *Memory) del(k string) {
	if _, ok := m.data[k]; !ok {
		return
	}
	delete(m.data, k)
	i := sort.SearchStrings(m.keys, k)
	m.keys = append(m.keys[:i], m.keys[i+1:]...)
}

type memSnap struct{ m *Memory }

func (s memSnap) Get(key []byte) ([]byte, bool, error) {
	v, ok := s.m.get(string(key))
	return v, ok, nil
}
func (s memSnap) Scan(prefix []byte) Iterator { return s.scan(prefix, false) }
func (s memSnap) ScanReverse(prefix []byte) Iterator {
	return s.scan(prefix, true)
}
func (s memSnap) scan(prefix []byte, rev bool) Iterator {
	keys := s.m.rangeKeys(string(prefix))
	kvs := make([]KV, len(keys))
	for i, k := range keys {
		kvs[i] = KV{Key: []byte(k), Value: s.m.data[k]}
	}
	return newSliceIter(kvs, rev)
}
func (s memSnap) Close() error { return nil }

type memTx struct {
	m      *Memory
	writes map[string]*[]byte // nil pointer = delete
	done   bool
}

func (t *memTx) Get(key []byte) ([]byte, bool, error) {
	if w, ok := t.writes[string(key)]; ok {
		if w == nil {
			return nil, false, nil
		}
		return *w, true, nil
	}
	t.m.mu.RLock()
	defer t.m.mu.RUnlock()
	v, ok := t.m.get(string(key))
	return v, ok, nil
}

func (t *memTx) Set(key, val []byte) {
	v := append([]byte(nil), val...)
	t.writes[string(key)] = &v
}

func (t *memTx) Delete(key []byte) { t.writes[string(key)] = nil }

func (t *memTx) Scan(prefix []byte) Iterator        { return t.scan(prefix, false) }
func (t *memTx) ScanReverse(prefix []byte) Iterator { return t.scan(prefix, true) }

// scan merges committed keys with this Tx's writes.
func (t *memTx) scan(prefix []byte, rev bool) Iterator {
	p := string(prefix)
	merged := make(map[string][]byte)
	t.m.mu.RLock()
	for _, k := range t.m.rangeKeys(p) {
		merged[k] = t.m.data[k]
	}
	t.m.mu.RUnlock()
	for k, w := range t.writes {
		if !strings.HasPrefix(k, p) {
			continue
		}
		if w == nil {
			delete(merged, k)
		} else {
			merged[k] = *w
		}
	}
	kvs := make([]KV, 0, len(merged))
	for k, v := range merged {
		kvs = append(kvs, KV{Key: []byte(k), Value: v})
	}
	sort.Slice(kvs, func(i, j int) bool { return bytes.Compare(kvs[i].Key, kvs[j].Key) < 0 })
	return newSliceIter(kvs, rev)
}

func (t *memTx) Commit(bool) error {
	if t.done {
		return nil
	}
	t.done = true
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	for k, w := range t.writes {
		if w == nil {
			t.m.del(k)
		} else {
			t.m.set(k, *w)
		}
	}
	return nil
}

func (t *memTx) Discard() { t.done = true }

type sliceIter struct {
	kvs []KV
	i   int
	rev bool
}

func newSliceIter(kvs []KV, rev bool) *sliceIter {
	if rev {
		for i, j := 0, len(kvs)-1; i < j; i, j = i+1, j-1 {
			kvs[i], kvs[j] = kvs[j], kvs[i]
		}
	}
	return &sliceIter{kvs: kvs, i: -1}
}

func (s *sliceIter) Next() bool    { s.i++; return s.i < len(s.kvs) }
func (s *sliceIter) Key() []byte   { return s.kvs[s.i].Key }
func (s *sliceIter) Value() []byte { return s.kvs[s.i].Value }
func (s *sliceIter) Close() error  { return nil }
