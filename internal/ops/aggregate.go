package ops

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// AggKind is an aggregate function.
type AggKind uint8

// Supported aggregate functions.
const (
	CountStar AggKind = iota
	Count
	Sum
	Avg
	Min
	Max
)

var aggNames = [...]string{"count(*)", "count", "sum", "avg", "min", "max"}

func (k AggKind) String() string { return aggNames[k] }

// AggSpec is one aggregate call in a GROUP BY.
type AggSpec struct {
	Kind AggKind
	Arg  expr.Expr  // nil for COUNT(*)
	Out  types.Type // result type, chosen by the binder with Postgres's rules
}

// Aggregate maintains GROUP BY aggregates. It is not linear: a group's
// output depends on all its rows, so it keeps per-group state:
//
//	a/OP/GROUPKEY            -> (row count, per aggregate: non-null count, sum)
//	x/OP/AGG/GROUPKEY/VALUE  -> count   (ordered multiset for MIN and MAX)
//
// For every group a delta touches it reads the old output row, applies all
// of the group's delta rows, computes the new output row and emits
// {old: -1, new: +1}: at most one pair per group per step. A group whose
// row count reaches 0 is removed and emits only -old. With no GROUP BY the
// single group always exists, so the query always returns one row.
type Aggregate struct {
	ID      []byte
	GroupBy []expr.Expr
	Aggs    []AggSpec
}

// Name implements Operator.
func (a *Aggregate) Name() string { return "aggregate" }

func (a *Aggregate) global() bool { return len(a.GroupBy) == 0 }

// aggState is the decoded a/ value. Missing state is the zero state.
type aggState struct {
	count int64
	nn    []int64       // non-null input count per aggregate
	sum   []types.Value // decimal or float sum per aggregate (SUM, AVG)
}

func (a *Aggregate) zeroState() *aggState {
	st := &aggState{nn: make([]int64, len(a.Aggs)), sum: make([]types.Value, len(a.Aggs))}
	for i, s := range a.Aggs {
		if s.Kind == Sum || s.Kind == Avg {
			if isFloat(s.Arg) {
				st.sum[i] = types.Float(0)
			} else {
				st.sum[i] = types.Decimal(decimal.Zero)
			}
		}
	}
	return st
}

func isFloat(e expr.Expr) bool {
	t := e.Type()
	return t == types.TFloat8 || t == types.TFloat4
}

func (a *Aggregate) stateKey(gk []byte) []byte { return state.Key("a/", a.ID, gk) }

func (a *Aggregate) multisetPrefix(agg int, gk []byte) []byte {
	return state.Key("x/", a.ID, types.Encode(types.Row{types.Int(int64(agg))}), gk)
}

func (a *Aggregate) load(tx state.Tx, gk []byte) (*aggState, error) {
	b, ok, err := tx.Get(a.stateKey(gk))
	if err != nil {
		return nil, err
	}
	st := a.zeroState()
	if !ok {
		return st, nil
	}
	r, err := types.Decode(b)
	if err != nil || len(r) != 1+2*len(a.Aggs) {
		return nil, fmt.Errorf("aggregate state: corrupt value (%v)", err)
	}
	st.count = r[0].AsInt()
	for i := range a.Aggs {
		st.nn[i] = r[1+2*i].AsInt()
		if v := r[2+2*i]; !v.IsNull() {
			st.sum[i] = v
		}
	}
	return st, nil
}

func (a *Aggregate) save(tx state.Tx, gk []byte, st *aggState) {
	if st.count == 0 {
		tx.Delete(a.stateKey(gk))
		return
	}
	r := make(types.Row, 0, 1+2*len(a.Aggs))
	r = append(r, types.Int(st.count))
	for i := range a.Aggs {
		r = append(r, types.Int(st.nn[i]))
		if st.sum[i].IsNull() {
			r = append(r, types.Null())
		} else {
			r = append(r, st.sum[i])
		}
	}
	tx.Set(a.stateKey(gk), types.Encode(r))
}

// output builds the group's output row: group key columns, then aggregates.
func (a *Aggregate) output(tx state.Tx, key types.Row, gk []byte, st *aggState) (types.Row, error) {
	row := make(types.Row, 0, len(key)+len(a.Aggs))
	row = append(row, key...)
	for i, s := range a.Aggs {
		v, err := a.aggValue(tx, i, s, gk, st)
		if err != nil {
			return nil, err
		}
		row = append(row, v)
	}
	return row, nil
}

func (a *Aggregate) aggValue(tx state.Tx, i int, s AggSpec, gk []byte, st *aggState) (types.Value, error) {
	switch s.Kind {
	case CountStar:
		return types.Int(st.count), nil
	case Count:
		return types.Int(st.nn[i]), nil
	}
	if st.nn[i] == 0 {
		return types.Null(), nil // SUM/AVG/MIN/MAX of no non-NULL values is NULL, not 0
	}
	switch s.Kind {
	case Sum:
		if s.Out.IsInteger() {
			d := st.sum[i].AsDecimal()
			if !d.IsInteger() || d.Cmp(decimal.NewFromInt(1<<62).Mul(decimal.NewFromInt(2))) >= 0 ||
				d.Cmp(decimal.NewFromInt(-1<<62).Mul(decimal.NewFromInt(2))) < 0 {
				return types.Value{}, fmt.Errorf("sum: %w: bigint out of range", expr.ErrOutOfRange)
			}
			return types.Int(d.IntPart()), nil
		}
		return st.sum[i], nil
	case Avg:
		if st.sum[i].Kind() == types.KindFloat {
			return types.Float(st.sum[i].AsFloat() / float64(st.nn[i])), nil
		}
		return expr.NumericArith(expr.OpDiv, st.sum[i].AsDecimal(), decimal.NewFromInt(st.nn[i]))
	case Min, Max:
		p := a.multisetPrefix(i, gk)
		it := tx.Scan(p)
		if s.Kind == Max {
			it = tx.ScanReverse(p)
		}
		defer it.Close()
		if !it.Next() {
			return types.Value{}, fmt.Errorf("%s: multiset empty but %d non-null inputs: %w", s.Kind, st.nn[i], ErrNegativeWeight)
		}
		v, _, err := types.DecodeValue(it.Key()[len(p):])
		return v, err
	}
	return types.Value{}, fmt.Errorf("aggregate %s", s.Kind)
}

type group struct {
	key  types.Row
	gk   []byte
	rows []zset.Entry
}

// Init emits the single row of a global aggregate over no input.
func (a *Aggregate) Init(tx state.Tx) (*zset.ZSet, error) {
	out := zset.New()
	if !a.global() {
		return out, nil
	}
	row, err := a.output(tx, nil, nil, a.zeroState())
	if err != nil {
		return nil, err
	}
	out.Add(row, 1)
	return out, nil
}

// Step implements Operator.
func (a *Aggregate) Step(tx state.Tx, in []*zset.ZSet) (*zset.ZSet, error) {
	groups := make(map[string]*group)
	for _, e := range in[0].Entries() {
		key, err := EvalAll(a.GroupBy, e.Row)
		if err != nil {
			return nil, fmt.Errorf("group by: %w", err)
		}
		gk := types.Encode(key) // NULL keys share one encoding, so one group
		g, ok := groups[string(gk)]
		if !ok {
			g = &group{key: key, gk: gk}
			groups[string(gk)] = g
		}
		g.rows = append(g.rows, e)
	}
	order := make([]string, 0, len(groups))
	for k := range groups {
		order = append(order, k)
	}
	sort.Strings(order)

	out := zset.New()
	for _, k := range order {
		if err := a.stepGroup(tx, groups[k], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *Aggregate) stepGroup(tx state.Tx, g *group, out *zset.ZSet) error {
	st, err := a.load(tx, g.gk)
	if err != nil {
		return err
	}
	var old types.Row
	if st.count > 0 || a.global() {
		if old, err = a.output(tx, g.key, g.gk, st); err != nil {
			return err
		}
	}
	for _, e := range g.rows {
		st.count += e.Weight
		for i, s := range a.Aggs {
			if s.Kind == CountStar {
				continue
			}
			v, err := s.Arg.Eval(e.Row)
			if err != nil {
				return fmt.Errorf("%s(%s): %w", s.Kind, s.Arg, err)
			}
			if v.IsNull() {
				continue // aggregates other than COUNT(*) ignore NULL
			}
			st.nn[i] += e.Weight
			switch s.Kind {
			case Sum, Avg:
				if st.sum[i].Kind() == types.KindFloat {
					st.sum[i] = types.Float(st.sum[i].AsFloat() + float64(e.Weight)*v.AsFloat())
				} else {
					st.sum[i] = types.Decimal(st.sum[i].AsDecimal().Add(v.AsDecimal().Mul(decimal.NewFromInt(e.Weight))))
				}
			case Min, Max:
				k := state.Key("", a.multisetPrefix(i, g.gk), types.AppendValue(nil, v))
				_, now, err := state.AddWeight(tx, k, e.Weight)
				if err != nil {
					return err
				}
				if now < 0 {
					return fmt.Errorf("%s multiset value %v: %w", s.Kind, v, ErrNegativeWeight)
				}
			}
		}
	}
	if st.count < 0 {
		return fmt.Errorf("group %v row count %d: %w", g.key, st.count, ErrNegativeWeight)
	}
	for i, n := range st.nn {
		if n < 0 || n > st.count {
			return fmt.Errorf("group %v aggregate %d non-null count %d: %w", g.key, i, n, ErrNegativeWeight)
		}
	}
	var now types.Row
	if st.count > 0 || a.global() {
		if now, err = a.output(tx, g.key, g.gk, st); err != nil {
			return err
		}
	}
	a.save(tx, g.gk, st)
	if old != nil && now != nil && string(types.Encode(old)) == string(types.Encode(now)) {
		return nil
	}
	if old != nil {
		out.Add(old, -1)
	}
	if now != nil {
		out.Add(now, 1)
	}
	return nil
}
