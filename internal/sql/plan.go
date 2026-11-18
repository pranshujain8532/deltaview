package sql

import (
	"fmt"
	"strings"

	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/ops"
	"github.com/pranshujain8532/deltaview/internal/types"
)

// Column is one output column of a plan node.
type Column struct {
	Name string
	Type types.Type
}

// Node is a logical plan node: what to compute, with no state. The circuit
// builder turns a plan into stateful operators that compute it step by step.
type Node interface {
	Columns() []Column
	explain(b *strings.Builder, depth int)
}

// Scan reads a source table's delta.
type Scan struct {
	Table string
	Cols  []Column
}

// Filter keeps rows whose predicate is TRUE.
type Filter struct {
	Input Node
	Pred  expr.Expr
}

// Project computes one expression per output column.
type Project struct {
	Input Node
	Exprs []expr.Expr
	Cols  []Column
}

// Join is an inner equi-join; its output is the left columns then the right.
type Join struct {
	Left, Right         Node
	LeftKeys, RightKeys []expr.Expr
}

// Aggregate groups its input; its output is the group keys then the aggregates.
type Aggregate struct {
	Input   Node
	GroupBy []expr.Expr
	Aggs    []ops.AggSpec
	Cols    []Column
}

// Distinct removes duplicate rows.
type Distinct struct {
	Input Node
}

func (s *Scan) Columns() []Column      { return s.Cols }
func (f *Filter) Columns() []Column    { return f.Input.Columns() }
func (p *Project) Columns() []Column   { return p.Cols }
func (a *Aggregate) Columns() []Column { return a.Cols }
func (d *Distinct) Columns() []Column  { return d.Input.Columns() }
func (j *Join) Columns() []Column {
	return append(append([]Column(nil), j.Left.Columns()...), j.Right.Columns()...)
}

func line(b *strings.Builder, depth int, format string, args ...any) {
	b.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(b, format, args...)
	b.WriteByte('\n')
}

func exprs(es []expr.Expr) string {
	s := make([]string, len(es))
	for i, e := range es {
		s[i] = e.String()
	}
	return strings.Join(s, ", ")
}

func (s *Scan) explain(b *strings.Builder, d int) { line(b, d, "Scan %s", s.Table) }
func (f *Filter) explain(b *strings.Builder, d int) {
	line(b, d, "Filter %s", f.Pred)
	f.Input.explain(b, d+1)
}
func (p *Project) explain(b *strings.Builder, d int) {
	line(b, d, "Project %s", exprs(p.Exprs))
	p.Input.explain(b, d+1)
}
func (j *Join) explain(b *strings.Builder, d int) {
	line(b, d, "Join on (%s) = (%s)", exprs(j.LeftKeys), exprs(j.RightKeys))
	j.Left.explain(b, d+1)
	j.Right.explain(b, d+1)
}
func (a *Aggregate) explain(b *strings.Builder, d int) {
	aggs := make([]string, len(a.Aggs))
	for i, s := range a.Aggs {
		if s.Arg == nil {
			aggs[i] = s.Kind.String()
		} else {
			aggs[i] = fmt.Sprintf("%s(%s)", s.Kind, s.Arg)
		}
	}
	line(b, d, "Aggregate group by (%s) compute (%s)", exprs(a.GroupBy), strings.Join(aggs, ", "))
	a.Input.explain(b, d+1)
}
func (x *Distinct) explain(b *strings.Builder, d int) {
	line(b, d, "Distinct")
	x.Input.explain(b, d+1)
}

// Explain renders a plan as an indented tree.
func Explain(n Node) string {
	var b strings.Builder
	n.explain(&b, 0)
	return b.String()
}

// Plan is a bound, checked view ready to be turned into a circuit.
type Plan struct {
	View   string
	Query  string
	Root   Node
	Tables []string // source tables, in first-use order
}
