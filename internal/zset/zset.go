package zset

import (
	"sort"
	"strconv"
	"strings"

	"github.com/pranshujain8532/deltaview/internal/types"
)

// Entry is one row of a Z-set with its weight. Key is the canonical encoding
// of Row, kept so callers that need storage keys do not encode twice.
type Entry struct {
	Key    string
	Row    types.Row
	Weight int64
}

type entry struct {
	row types.Row
	w   int64
}

// ZSet maps rows to non-zero integer weights. It is keyed by the canonical row
// encoding, never by a printed form, so equal rows always land on one entry.
// A nil *ZSet is a valid empty Z-set for every read method.
type ZSet struct {
	m map[string]*entry
}

// New returns an empty Z-set.
func New() *ZSet { return &ZSet{m: make(map[string]*entry)} }

// Add adds w to the weight of r. An entry whose weight reaches 0 is removed,
// so no zero weight is ever stored.
func (z *ZSet) Add(r types.Row, w int64) {
	z.AddKeyed(string(types.Encode(r)), r, w)
}

// AddKeyed is Add for callers that already hold the row's encoding.
func (z *ZSet) AddKeyed(key string, r types.Row, w int64) {
	if w == 0 {
		return
	}
	if z.m == nil {
		z.m = make(map[string]*entry)
	}
	if e, ok := z.m[key]; ok {
		e.w += w
		if e.w == 0 {
			delete(z.m, key)
		}
		return
	}
	z.m[key] = &entry{row: r, w: w}
}

// Merge adds every entry of o into z (z = z + o).
func (z *ZSet) Merge(o *ZSet) {
	if o == nil {
		return
	}
	for k, e := range o.m {
		z.AddKeyed(k, e.row, e.w)
	}
}

// Negate returns -z, a new Z-set with every weight negated.
func (z *ZSet) Negate() *ZSet {
	out := New()
	if z == nil {
		return out
	}
	for k, e := range z.m {
		out.m[k] = &entry{row: e.row, w: -e.w}
	}
	return out
}

// Clone returns an independent copy of z.
func (z *ZSet) Clone() *ZSet {
	out := New()
	out.Merge(z)
	return out
}

// Len is the number of distinct rows with a non-zero weight.
func (z *ZSet) Len() int {
	if z == nil {
		return 0
	}
	return len(z.m)
}

// IsEmpty reports whether z has no entries.
func (z *ZSet) IsEmpty() bool { return z.Len() == 0 }

// Weight returns the weight of r (0 when absent).
func (z *ZSet) Weight(r types.Row) int64 {
	if z == nil {
		return 0
	}
	if e, ok := z.m[string(types.Encode(r))]; ok {
		return e.w
	}
	return 0
}

// Entries returns all entries sorted by encoded key, which is value order.
// Sorting makes every operator deterministic regardless of map order.
func (z *ZSet) Entries() []Entry {
	if z == nil {
		return nil
	}
	out := make([]Entry, 0, len(z.m))
	for k, e := range z.m {
		out = append(out, Entry{Key: k, Row: e.row, Weight: e.w})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Each calls fn for every entry in unspecified order. Use it where order does
// not matter, such as building another Z-set.
func (z *ZSet) Each(fn func(key string, r types.Row, w int64)) {
	if z == nil {
		return
	}
	for k, e := range z.m {
		fn(k, e.row, e.w)
	}
}

// Equal reports whether two Z-sets hold the same rows with the same weights.
func (z *ZSet) Equal(o *ZSet) bool {
	if z.Len() != o.Len() {
		return false
	}
	for k, e := range z.m {
		oe, ok := o.m[k]
		if !ok || oe.w != e.w {
			return false
		}
	}
	return true
}

// String renders z as {row: weight, ...} in key order, for tests and logs.
func (z *ZSet) String() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, e := range z.Entries() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(e.Row.String())
		b.WriteString(": ")
		if e.Weight > 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.FormatInt(e.Weight, 10))
	}
	b.WriteByte('}')
	return b.String()
}
