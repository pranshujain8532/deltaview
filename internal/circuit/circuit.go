package circuit

import (
	"context"
	"fmt"

	"github.com/pranshujain8532/deltaview/internal/ops"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Node is one vertex of a circuit: either a source (a table's delta) or an
// operator fed by earlier nodes.
type Node struct {
	Source string       // table name, for source nodes
	Op     ops.Operator // nil for source nodes
	Inputs []int        // indices of earlier nodes
}

// Column describes one output column of a view.
type Column struct {
	Name string
	Type types.Type
}

// Circuit is the operators of one view wired together. Nodes are stored in
// topological order, so one pass computes the step. Every view runs through
// the same Step, whatever its shape.
type Circuit struct {
	View    string
	SQL     string
	Columns []Column
	Nodes   []Node
	sources map[string]bool
	viewKey []byte
	// Tracer, when set, is told how long each operator took and how big its
	// output was. It is how metrics and traces see inside a step.
	Tracer OpTracer
}

// OpTracer observes operator steps.
type OpTracer interface {
	StartOp(ctx context.Context, view, op string) (context.Context, func(outRows int))
}

// New builds a circuit. The last node is the view's output.
func New(view, sql string, cols []Column, nodes []Node) (*Circuit, error) {
	c := &Circuit{View: view, SQL: sql, Columns: cols, Nodes: nodes, sources: make(map[string]bool),
		viewKey: state.Key("v/", types.Encode(types.Row{types.Text(view)}))}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("circuit %s: no nodes", view)
	}
	for i, n := range nodes {
		if n.Op == nil {
			if n.Source == "" {
				return nil, fmt.Errorf("circuit %s: node %d has neither source nor operator", view, i)
			}
			c.sources[n.Source] = true
			continue
		}
		for _, in := range n.Inputs {
			if in < 0 || in >= i {
				return nil, fmt.Errorf("circuit %s: node %d reads node %d, which is not earlier", view, i, in)
			}
		}
	}
	return c, nil
}

// Sources lists the tables the view reads.
func (c *Circuit) Sources() []string {
	out := make([]string, 0, len(c.sources))
	for s := range c.sources {
		out = append(out, s)
	}
	return out
}

// Reads reports whether the view depends on table.
func (c *Circuit) Reads(table string) bool { return c.sources[table] }

// ViewPrefix is the state prefix under which the view's rows are stored:
// v/VIEW/ROW -> weight.
func (c *Circuit) ViewPrefix() []byte { return c.viewKey }

// Init runs once when the view is created, before any data. It lets
// operators that output rows for empty input (a global aggregate) emit them.
func (c *Circuit) Init(ctx context.Context, tx state.Tx) (*zset.ZSet, error) {
	return c.run(ctx, tx, nil, true)
}

// Step pushes one transaction's deltas through the circuit, applies the
// result to the stored view and returns the view's delta. A transaction that
// touches none of the view's tables costs nothing.
func (c *Circuit) Step(ctx context.Context, tx state.Tx, deltas map[string]*zset.ZSet) (*zset.ZSet, error) {
	touched := false
	for t := range c.sources {
		if !deltas[t].IsEmpty() {
			touched = true
			break
		}
	}
	if !touched {
		return nil, nil
	}
	return c.run(ctx, tx, deltas, false)
}

func (c *Circuit) run(ctx context.Context, tx state.Tx, deltas map[string]*zset.ZSet, init bool) (*zset.ZSet, error) {
	outs := make([]*zset.ZSet, len(c.Nodes))
	for i, n := range c.Nodes {
		if n.Op == nil {
			outs[i] = deltas[n.Source]
			continue
		}
		in := make([]*zset.ZSet, len(n.Inputs))
		empty := true
		for k, idx := range n.Inputs {
			in[k] = outs[idx]
			if !in[k].IsEmpty() {
				empty = false
			}
		}
		var (
			out *zset.ZSet
			err error
		)
		var done func(int)
		if c.Tracer != nil {
			_, done = c.Tracer.StartOp(ctx, c.View, n.Op.Name())
		}
		switch {
		case init:
			if it, ok := n.Op.(ops.Initer); ok {
				out, err = it.Init(tx)
			}
			if err == nil && !empty {
				var more *zset.ZSet
				more, err = n.Op.Step(tx, in)
				if out == nil {
					out = more
				} else {
					out.Merge(more)
				}
			}
		case !empty:
			out, err = n.Op.Step(tx, in)
		}
		if done != nil {
			done(out.Len())
		}
		if err != nil {
			return nil, fmt.Errorf("view %s, %s: %w", c.View, n.Op.Name(), err)
		}
		outs[i] = out
	}
	delta := outs[len(outs)-1]
	if err := c.apply(tx, delta); err != nil {
		return nil, err
	}
	return delta, nil
}

// apply adds the view delta to the stored view rows.
func (c *Circuit) apply(tx state.Tx, delta *zset.ZSet) error {
	for _, e := range delta.Entries() {
		_, now, err := state.AddWeight(tx, state.Key("", c.viewKey, []byte(e.Key)), e.Weight)
		if err != nil {
			return err
		}
		if now < 0 {
			return fmt.Errorf("view %s row %v: %w", c.View, e.Row, ops.ErrNegativeWeight)
		}
	}
	return nil
}

// ReadView returns the view's rows and weights from a reader (a snapshot
// for HTTP, a Tx in tests).
func (c *Circuit) ReadView(r state.Reader) (*zset.ZSet, error) {
	kvs, err := state.Collect(r.Scan(c.viewKey))
	if err != nil {
		return nil, err
	}
	out := zset.New()
	for _, kv := range kvs {
		enc := kv.Key[len(c.viewKey):]
		row, err := types.Decode(enc)
		if err != nil {
			return nil, fmt.Errorf("view %s: %w", c.View, err)
		}
		w, err := state.DecodeInt(kv.Value)
		if err != nil {
			return nil, err
		}
		out.AddKeyed(string(enc), row, w)
	}
	return out, nil
}
