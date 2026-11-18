package expr

// Rewrite returns e with subtrees replaced. fn is called top-down; when it
// returns (x, true) the subtree is replaced by x and not descended into.
// The planner uses it to turn expressions over input rows into expressions
// over an aggregate's output (group keys and aggregate results).
func Rewrite(e Expr, fn func(Expr) (Expr, bool)) Expr {
	if e == nil {
		return nil
	}
	if r, ok := fn(e); ok {
		return r
	}
	rw := func(x Expr) Expr { return Rewrite(x, fn) }
	all := func(xs []Expr) []Expr {
		out := make([]Expr, len(xs))
		for i, x := range xs {
			out[i] = rw(x)
		}
		return out
	}
	switch n := e.(type) {
	case *And:
		return &And{Args: all(n.Args)}
	case *Or:
		return &Or{Args: all(n.Args)}
	case *Not:
		return &Not{Arg: rw(n.Arg)}
	case *Cmp:
		return &Cmp{Op: n.Op, L: rw(n.L), R: rw(n.R)}
	case *IsDistinct:
		return &IsDistinct{L: rw(n.L), R: rw(n.R), Not: n.Not}
	case *IsNull:
		return &IsNull{Arg: rw(n.Arg), Not: n.Not}
	case *BoolTest:
		return &BoolTest{Arg: rw(n.Arg), Kind: n.Kind}
	case *In:
		return &In{Arg: rw(n.Arg), List: all(n.List), Not: n.Not}
	case *Coalesce:
		return &Coalesce{Args: all(n.Args), T: n.T}
	case *NullIf:
		return &NullIf{L: rw(n.L), R: rw(n.R)}
	case *Case:
		c := &Case{T: n.T, Else: rw(n.Else)}
		for _, w := range n.Whens {
			c.Whens = append(c.Whens, When{Cond: rw(w.Cond), Result: rw(w.Result)})
		}
		return c
	case *Like:
		return &Like{Arg: rw(n.Arg), Pattern: rw(n.Pattern), Not: n.Not}
	case *Arith:
		return &Arith{Op: n.Op, L: rw(n.L), R: rw(n.R), T: n.T}
	case *Neg:
		return &Neg{Arg: rw(n.Arg)}
	case *Concat:
		return &Concat{L: rw(n.L), R: rw(n.R)}
	case *Cast:
		return &Cast{Arg: rw(n.Arg), To: n.To}
	case *Func:
		return &Func{Name: n.Name, Args: all(n.Args), T: n.T}
	}
	return e // leaves: Col, Const, and foreign node types
}

// Walk calls fn for e and every subexpression.
func Walk(e Expr, fn func(Expr)) {
	Rewrite(e, func(x Expr) (Expr, bool) {
		fn(x)
		return nil, false
	})
}
