package ops

import (
	"fmt"

	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Join is an inner equi-join of two inputs. Expanding
//
//	(A + dA) JOIN (B + dB) = A JOIN B + dA JOIN B + A JOIN dB + dA JOIN dB
//
// gives d_out = dA JOIN B + A JOIN dB + dA JOIN dB, computed in four steps
// with two lookups:
//
//	out  = dA JOIN B_old
//	A   += dA
//	out += A_new JOIN dB     (covers A JOIN dB and dA JOIN dB)
//	B   += dB
//
// Each side is kept as an arrangement, an index by join key:
// j/OP/L/JOINKEY/ROW -> weight and j/OP/R/JOINKEY/ROW -> weight.
// A row with a NULL in its key never matches, so it is not stored.
type Join struct {
	ID        []byte
	LeftKeys  []expr.Expr // evaluated on a left row
	RightKeys []expr.Expr // evaluated on a right row
}

// Name implements Operator.
func (j *Join) Name() string { return "join" }

type keyed struct {
	key []byte // encoded join key
	e   zset.Entry
}

// Step implements Operator. in[0] is dA (left), in[1] is dB (right).
func (j *Join) Step(tx state.Tx, in []*zset.ZSet) (*zset.ZSet, error) {
	out := zset.New()
	dA, err := j.withKeys(in[0], j.LeftKeys)
	if err != nil {
		return nil, err
	}
	dB, err := j.withKeys(in[1], j.RightKeys)
	if err != nil {
		return nil, err
	}
	// 1. out = dA JOIN B_old
	if err := j.probe(tx, dA, "R", out, true); err != nil {
		return nil, err
	}
	// 2. A += dA
	if err := j.update(tx, dA, "L"); err != nil {
		return nil, err
	}
	// 3. out += A_new JOIN dB
	if err := j.probe(tx, dB, "L", out, false); err != nil {
		return nil, err
	}
	// 4. B += dB
	if err := j.update(tx, dB, "R"); err != nil {
		return nil, err
	}
	return out, nil
}

// withKeys evaluates the join key of every delta row and drops rows whose
// key contains NULL: NULL = anything is never true.
func (j *Join) withKeys(z *zset.ZSet, keys []expr.Expr) ([]keyed, error) {
	var out []keyed
	for _, e := range z.Entries() {
		k, err := EvalAll(keys, e.Row)
		if err != nil {
			return nil, fmt.Errorf("join key: %w", err)
		}
		if hasNull(k) {
			continue
		}
		out = append(out, keyed{key: types.Encode(k), e: e})
	}
	return out, nil
}

func hasNull(r types.Row) bool {
	for _, v := range r {
		if v.IsNull() {
			return true
		}
	}
	return false
}

func (j *Join) prefix(side string, key []byte) []byte {
	return state.Key("j/", j.ID, []byte(side), key)
}

// probe looks each delta row's key up in the other side's arrangement and
// emits every match with weight w_delta * w_stored. deltaIsLeft decides the
// column order of the output row (left columns first).
func (j *Join) probe(tx state.Tx, delta []keyed, other string, out *zset.ZSet, deltaIsLeft bool) error {
	cache := make(map[string][]zset.Entry)
	for _, d := range delta {
		matches, ok := cache[string(d.key)]
		if !ok {
			var err error
			if matches, err = j.lookup(tx, other, d.key); err != nil {
				return err
			}
			cache[string(d.key)] = matches
		}
		for _, m := range matches {
			if deltaIsLeft {
				out.Add(types.Concat(d.e.Row, m.Row), d.e.Weight*m.Weight)
			} else {
				out.Add(types.Concat(m.Row, d.e.Row), m.Weight*d.e.Weight)
			}
		}
	}
	return nil
}

func (j *Join) lookup(tx state.Tx, side string, key []byte) ([]zset.Entry, error) {
	p := j.prefix(side, key)
	kvs, err := state.Collect(tx.Scan(p))
	if err != nil {
		return nil, fmt.Errorf("join lookup: %w", err)
	}
	out := make([]zset.Entry, 0, len(kvs))
	for _, kv := range kvs {
		row, err := types.Decode(kv.Key[len(p):])
		if err != nil {
			return nil, fmt.Errorf("join arrangement row: %w", err)
		}
		w, err := state.DecodeInt(kv.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, zset.Entry{Key: string(kv.Key[len(p):]), Row: row, Weight: w})
	}
	return out, nil
}

func (j *Join) update(tx state.Tx, delta []keyed, side string) error {
	for _, d := range delta {
		_, now, err := state.AddWeight(tx, state.Key("j/", j.ID, []byte(side), d.key, []byte(d.e.Key)), d.e.Weight)
		if err != nil {
			return err
		}
		if now < 0 {
			return fmt.Errorf("join %s arrangement %v: %w", side, d.e.Row, ErrNegativeWeight)
		}
	}
	return nil
}
