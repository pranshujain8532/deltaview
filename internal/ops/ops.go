package ops

import (
	"errors"
	"fmt"

	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Operator turns one input delta per input port into an output delta.
// All state reads and writes go through tx so they commit atomically with
// the applied LSN. Operators keep no state in struct fields.
type Operator interface {
	Step(tx state.Tx, in []*zset.ZSet) (*zset.ZSet, error)
	// Name is a short label for metrics and traces.
	Name() string
}

// Initer is implemented by operators whose output is not empty for an empty
// input: an aggregate without GROUP BY outputs one row even for no rows.
// The circuit calls Init exactly once, when the view is created.
type Initer interface {
	Init(tx state.Tx) (*zset.ZSet, error)
}

// ErrNegativeWeight means stored state says a row occurs a negative number
// of times. That can only follow a lost or doubly applied change, so the
// engine stops instead of serving a wrong view.
var ErrNegativeWeight = errors.New("negative weight in state (a change was lost or applied twice)")

// OpID builds the self-delimiting state prefix of one operator.
func OpID(view string, index int) []byte {
	return types.Encode(types.Row{types.Text(view), types.Int(int64(index))})
}

// Filter keeps rows whose predicate is TRUE. It is linear, so
// d_out = filter(d_in) and it needs no state.
type Filter struct {
	Pred expr.Expr
}

// Name implements Operator.
func (f *Filter) Name() string { return "filter" }

// Step implements Operator.
func (f *Filter) Step(_ state.Tx, in []*zset.ZSet) (*zset.ZSet, error) {
	out := zset.New()
	var err error
	in[0].Each(func(key string, r types.Row, w int64) {
		if err != nil {
			return
		}
		var ok bool
		if ok, err = expr.IsTrue(f.Pred, r); ok {
			out.AddKeyed(key, r, w)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("filter %s: %w", f.Pred, err)
	}
	return out, nil
}

// Project maps each row through a list of expressions and keeps its weight.
// Rows that map to the same output row add their weights.
type Project struct {
	Exprs []expr.Expr
}

// Name implements Operator.
func (p *Project) Name() string { return "project" }

// Step implements Operator.
func (p *Project) Step(_ state.Tx, in []*zset.ZSet) (*zset.ZSet, error) {
	out := zset.New()
	var err error
	in[0].Each(func(_ string, r types.Row, w int64) {
		if err != nil {
			return
		}
		var row types.Row
		if row, err = EvalAll(p.Exprs, r); err == nil {
			out.Add(row, w)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("project: %w", err)
	}
	return out, nil
}

// EvalAll evaluates a list of expressions over one row.
func EvalAll(es []expr.Expr, r types.Row) (types.Row, error) {
	out := make(types.Row, len(es))
	for i, e := range es {
		v, err := e.Eval(r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e, err)
		}
		out[i] = v
	}
	return out, nil
}

// Distinct outputs each row once while its accumulated weight is positive.
// State: d/OP/ROW -> weight.
type Distinct struct {
	ID []byte
}

// Name implements Operator.
func (d *Distinct) Name() string { return "distinct" }

// Step implements Operator.
func (d *Distinct) Step(tx state.Tx, in []*zset.ZSet) (*zset.ZSet, error) {
	out := zset.New()
	for _, e := range in[0].Entries() {
		old, now, err := state.AddWeight(tx, state.Key("d/", d.ID, []byte(e.Key)), e.Weight)
		if err != nil {
			return nil, err
		}
		if now < 0 {
			return nil, fmt.Errorf("distinct %v: %w", e.Row, ErrNegativeWeight)
		}
		switch {
		case old <= 0 && now > 0:
			out.AddKeyed(e.Key, e.Row, 1)
		case old > 0 && now <= 0:
			out.AddKeyed(e.Key, e.Row, -1)
		}
	}
	return out, nil
}
