package circuit

import (
	"fmt"

	"github.com/pranshujain8532/deltaview/internal/ops"
	"github.com/pranshujain8532/deltaview/internal/sql"
)

// Build turns a logical plan into a circuit of stateful operators. Each
// stateful operator gets an ID from the view name and its position, which
// prefixes all its state keys.
func Build(p *sql.Plan) (*Circuit, error) {
	b := &builder{view: p.View}
	if _, err := b.add(p.Root); err != nil {
		return nil, err
	}
	var cols []Column
	for _, c := range p.Root.Columns() {
		cols = append(cols, Column{Name: c.Name, Type: c.Type})
	}
	return New(p.View, p.Query, cols, b.nodes)
}

type builder struct {
	view  string
	nodes []Node
}

func (b *builder) push(n Node) int {
	b.nodes = append(b.nodes, n)
	return len(b.nodes) - 1
}

func (b *builder) add(n sql.Node) (int, error) {
	switch n := n.(type) {
	case *sql.Scan:
		return b.push(Node{Source: n.Table}), nil
	case *sql.Filter:
		in, err := b.add(n.Input)
		if err != nil {
			return 0, err
		}
		return b.push(Node{Op: &ops.Filter{Pred: n.Pred}, Inputs: []int{in}}), nil
	case *sql.Project:
		in, err := b.add(n.Input)
		if err != nil {
			return 0, err
		}
		return b.push(Node{Op: &ops.Project{Exprs: n.Exprs}, Inputs: []int{in}}), nil
	case *sql.Join:
		l, err := b.add(n.Left)
		if err != nil {
			return 0, err
		}
		r, err := b.add(n.Right)
		if err != nil {
			return 0, err
		}
		op := &ops.Join{ID: ops.OpID(b.view, len(b.nodes)), LeftKeys: n.LeftKeys, RightKeys: n.RightKeys}
		return b.push(Node{Op: op, Inputs: []int{l, r}}), nil
	case *sql.Aggregate:
		in, err := b.add(n.Input)
		if err != nil {
			return 0, err
		}
		op := &ops.Aggregate{ID: ops.OpID(b.view, len(b.nodes)), GroupBy: n.GroupBy, Aggs: n.Aggs}
		return b.push(Node{Op: op, Inputs: []int{in}}), nil
	case *sql.Distinct:
		in, err := b.add(n.Input)
		if err != nil {
			return 0, err
		}
		return b.push(Node{Op: &ops.Distinct{ID: ops.OpID(b.view, len(b.nodes))}, Inputs: []int{in}}), nil
	}
	return 0, fmt.Errorf("circuit: unknown plan node %T", n)
}
