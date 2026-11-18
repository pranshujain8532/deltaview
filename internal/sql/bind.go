package sql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/ops"
	"github.com/pranshujain8532/deltaview/internal/types"
)

// Catalog looks up a source table's columns (from the Postgres catalog).
type Catalog func(name string) (*cdc.Table, error)

type scopeItem struct {
	alias  string
	cols   []Column
	offset int
}

// scope maps names to positions in the row a FROM clause produces.
type scope struct {
	items []scopeItem
	width int
}

func (s *scope) add(alias string, cols []Column) error {
	for _, it := range s.items {
		if it.alias == alias {
			return fmt.Errorf("table name %q specified more than once", alias)
		}
	}
	s.items = append(s.items, scopeItem{alias: alias, cols: cols, offset: s.width})
	s.width += len(cols)
	return nil
}

func (s *scope) merge(o *scope) error {
	for _, it := range o.items {
		if err := s.add(it.alias, it.cols); err != nil {
			return err
		}
	}
	return nil
}

func (s *scope) resolve(qual, name string) (*expr.Col, error) {
	var found *expr.Col
	for _, it := range s.items {
		if qual != "" && it.alias != qual {
			continue
		}
		for i, c := range it.cols {
			if c.Name != name {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("column reference %q is ambiguous", name)
			}
			found = &expr.Col{Index: it.offset + i, T: c.Type, Name: it.alias + "." + c.Name}
		}
	}
	if found == nil {
		if qual != "" {
			return nil, fmt.Errorf("column %s.%s does not exist", qual, name)
		}
		return nil, fmt.Errorf("column %q does not exist", name)
	}
	return found, nil
}

func (s *scope) star(qual string) ([]*expr.Col, []string, error) {
	var cols []*expr.Col
	var names []string
	for _, it := range s.items {
		if qual != "" && it.alias != qual {
			continue
		}
		for i, c := range it.cols {
			cols = append(cols, &expr.Col{Index: it.offset + i, T: c.Type, Name: it.alias + "." + c.Name})
			names = append(names, c.Name)
		}
	}
	if len(cols) == 0 {
		return nil, nil, fmt.Errorf("missing FROM-clause entry for table %q", qual)
	}
	return cols, names, nil
}

// aggRef stands for an aggregate result while the select list is bound.
// It is replaced by a column of the Aggregate's output before planning ends.
type aggRef struct {
	idx int
	t   types.Type
}

func (a *aggRef) Eval(types.Row) (types.Value, error) {
	return types.Value{}, errors.New("aggregate evaluated outside of aggregation")
}
func (a *aggRef) Type() types.Type { return a.t }
func (a *aggRef) String() string   { return fmt.Sprintf("agg#%d", a.idx) }

type binder struct {
	cat     Catalog
	tables  []string
	aggs    []ops.AggSpec
	aggKeys map[string]int
}

// Bind checks a view against the supported subset, resolves every name and
// type, and returns its logical plan. Anything outside the subset is
// rejected with an error that names the feature: a wrong answer that looks
// right is worse than an error.
func Bind(def ViewDef, cat Catalog) (*Plan, error) {
	b := &binder{cat: cat, aggKeys: make(map[string]int)}
	root, err := b.bindSelect(def.Select)
	if err != nil {
		return nil, fmt.Errorf("view %s: %w", def.Name, err)
	}
	return &Plan{View: def.Name, Query: def.Query, Root: root, Tables: b.tables}, nil
}

func (b *binder) bindSelect(sel *pg.SelectStmt) (Node, error) {
	if err := checkShape(sel); err != nil {
		return nil, err
	}
	from := sel.GetFromClause()
	if len(from) == 0 {
		return nil, unsupported("SELECT without FROM", "there is no table to follow")
	}
	if len(from) > 1 {
		return nil, unsupported("comma-separated FROM list", "write the join as JOIN ... ON")
	}
	root, sc, err := b.bindFrom(from[0])
	if err != nil {
		return nil, err
	}
	if w := sel.GetWhereClause(); w != nil {
		if hasAggregate(w) {
			return nil, errors.New("aggregate functions are not allowed in WHERE")
		}
		pred, err := b.bindPredicate(w, sc, "WHERE")
		if err != nil {
			return nil, err
		}
		root = &Filter{Input: root, Pred: pred}
	}

	targets := sel.GetTargetList()
	grouped := len(sel.GetGroupClause()) > 0 || sel.GetHavingClause() != nil
	for _, t := range targets {
		grouped = grouped || hasAggregate(t.GetResTarget().GetVal())
	}
	if !grouped {
		exprs, cols, err := b.bindTargets(targets, sc, false)
		if err != nil {
			return nil, err
		}
		root = &Project{Input: root, Exprs: exprs, Cols: cols}
	} else {
		root, err = b.bindAggregate(sel, root, sc)
		if err != nil {
			return nil, err
		}
	}
	if d := sel.GetDistinctClause(); len(d) > 0 {
		if len(d) != 1 || d[0].GetNode() != nil {
			return nil, unsupported("DISTINCT ON", "")
		}
		root = &Distinct{Input: root}
	}
	return root, nil
}

// checkShape rejects clauses that have no incremental rule in v1.
func checkShape(sel *pg.SelectStmt) error {
	switch {
	case sel.GetOp() != pg.SetOperation_SETOP_NONE:
		return unsupported("UNION / INTERSECT / EXCEPT", "")
	case sel.GetWithClause() != nil:
		return unsupported("WITH (common table expressions)", "")
	case len(sel.GetSortClause()) > 0:
		return unsupported("ORDER BY", "incremental top-k is its own problem; clients sort")
	case sel.GetLimitCount() != nil || sel.GetLimitOffset() != nil:
		return unsupported("LIMIT / OFFSET", "incremental top-k is its own problem; clients sort")
	case len(sel.GetWindowClause()) > 0:
		return unsupported("window functions", "each needs its own incremental rule")
	case len(sel.GetValuesLists()) > 0:
		return unsupported("VALUES", "")
	case len(sel.GetLockingClause()) > 0:
		return unsupported("FOR UPDATE / FOR SHARE", "")
	case sel.GetIntoClause() != nil:
		return unsupported("SELECT INTO", "")
	case sel.GetGroupDistinct():
		return unsupported("GROUP BY DISTINCT", "")
	}
	return nil
}

func (b *binder) bindFrom(n *pg.Node) (Node, *scope, error) {
	switch {
	case n.GetRangeVar() != nil:
		rv := n.GetRangeVar()
		name := rv.GetRelname()
		if s := rv.GetSchemaname(); s != "" {
			name = s + "." + name
		}
		t, err := b.cat(name)
		if err != nil {
			return nil, nil, err
		}
		cols := make([]Column, len(t.Columns))
		for i, c := range t.Columns {
			if c.Type == types.TUnknown {
				return nil, nil, fmt.Errorf("table %s column %s has an unsupported type (oid %d)", t.Name, c.Name, c.OID)
			}
			cols[i] = Column{Name: c.Name, Type: c.Type}
		}
		alias := rv.GetRelname()
		if a := rv.GetAlias(); a != nil {
			alias = a.GetAliasname()
			if len(a.GetColnames()) > 0 {
				return nil, nil, unsupported("column aliases in FROM", "")
			}
		}
		b.addTable(t.Name)
		sc := &scope{}
		if err := sc.add(alias, cols); err != nil {
			return nil, nil, err
		}
		return &Scan{Table: t.Name, Cols: cols}, sc, nil
	case n.GetJoinExpr() != nil:
		return b.bindJoin(n.GetJoinExpr())
	case n.GetRangeSubselect() != nil:
		return nil, nil, unsupported("subquery in FROM", "each needs its own incremental rule")
	case n.GetRangeFunction() != nil:
		return nil, nil, unsupported("function in FROM", "")
	}
	return nil, nil, unsupported(nodeName(n)+" in FROM", "")
}

func (b *binder) addTable(name string) {
	for _, t := range b.tables {
		if t == name {
			return
		}
	}
	b.tables = append(b.tables, name)
}

func (b *binder) bindJoin(j *pg.JoinExpr) (Node, *scope, error) {
	switch {
	case j.GetJointype() != pg.JoinType_JOIN_INNER:
		return nil, nil, unsupported("outer joins", "rows that gain or lose their last match need extra rules")
	case j.GetIsNatural():
		return nil, nil, unsupported("NATURAL JOIN", "write JOIN ... ON")
	case len(j.GetUsingClause()) > 0:
		return nil, nil, unsupported("JOIN ... USING", "write JOIN ... ON")
	case j.GetAlias() != nil:
		return nil, nil, unsupported("alias on a join", "")
	case j.GetQuals() == nil:
		return nil, nil, unsupported("CROSS JOIN", "a join needs an equality condition")
	}
	left, ls, err := b.bindFrom(j.GetLarg())
	if err != nil {
		return nil, nil, err
	}
	right, rs, err := b.bindFrom(j.GetRarg())
	if err != nil {
		return nil, nil, err
	}
	sc := &scope{}
	if err := sc.merge(ls); err != nil {
		return nil, nil, err
	}
	if err := sc.merge(rs); err != nil {
		return nil, nil, err
	}
	if hasAggregate(j.GetQuals()) {
		return nil, nil, errors.New("aggregate functions are not allowed in JOIN conditions")
	}
	cond, err := b.bindPredicate(j.GetQuals(), sc, "JOIN ON")
	if err != nil {
		return nil, nil, err
	}
	split := ls.width
	var lk, rk, rest []expr.Expr
	for _, c := range conjuncts(cond) {
		cmp, ok := c.(*expr.Cmp)
		if ok && cmp.Op == expr.OpEq {
			lside, rside := sides(cmp.L, split), sides(cmp.R, split)
			switch {
			case lside == sideLeft && rside == sideRight:
				lk, rk = append(lk, cmp.L), append(rk, shift(cmp.R, -split))
				continue
			case lside == sideRight && rside == sideLeft:
				lk, rk = append(lk, cmp.R), append(rk, shift(cmp.L, -split))
				continue
			}
		}
		rest = append(rest, c)
	}
	if len(lk) == 0 {
		return nil, nil, unsupported("join without an equality between the two sides", "only equi-joins are incremental in v1")
	}
	var node Node = &Join{Left: left, Right: right, LeftKeys: lk, RightKeys: rk}
	if len(rest) > 0 {
		node = &Filter{Input: node, Pred: and(rest)}
	}
	return node, sc, nil
}

func conjuncts(e expr.Expr) []expr.Expr {
	if a, ok := e.(*expr.And); ok {
		var out []expr.Expr
		for _, x := range a.Args {
			out = append(out, conjuncts(x)...)
		}
		return out
	}
	return []expr.Expr{e}
}

func and(es []expr.Expr) expr.Expr {
	if len(es) == 1 {
		return es[0]
	}
	return &expr.And{Args: es}
}

const (
	sideNone = iota
	sideLeft
	sideRight
	sideBoth
)

// sides reports which join input an expression's columns come from.
func sides(e expr.Expr, split int) int {
	s := sideNone
	expr.Walk(e, func(x expr.Expr) {
		c, ok := x.(*expr.Col)
		if !ok {
			return
		}
		side := sideLeft
		if c.Index >= split {
			side = sideRight
		}
		if s == sideNone {
			s = side
		} else if s != side {
			s = sideBoth
		}
	})
	return s
}

func shift(e expr.Expr, by int) expr.Expr {
	return expr.Rewrite(e, func(x expr.Expr) (expr.Expr, bool) {
		if c, ok := x.(*expr.Col); ok {
			return &expr.Col{Index: c.Index + by, T: c.T, Name: c.Name}, true
		}
		return nil, false
	})
}

func (b *binder) bindPredicate(n *pg.Node, sc *scope, clause string) (expr.Expr, error) {
	e, err := b.bindExpr(n, sc, false)
	if err != nil {
		return nil, err
	}
	if e.Type() == types.TUnknown {
		return coerce(e, types.TBool)
	}
	if e.Type() != types.TBool {
		return nil, fmt.Errorf("argument of %s must be type boolean, not %s", clause, e.Type())
	}
	return e, nil
}

func (b *binder) bindTargets(targets []*pg.Node, sc *scope, aggOK bool) ([]expr.Expr, []Column, error) {
	var exprs []expr.Expr
	var cols []Column
	for _, t := range targets {
		rt := t.GetResTarget()
		val := rt.GetVal()
		if cr := val.GetColumnRef(); cr != nil && isStar(cr) {
			qual := ""
			if f := cr.GetFields(); len(f) == 2 {
				qual = f[0].GetString_().GetSval()
			}
			stars, names, err := sc.star(qual)
			if err != nil {
				return nil, nil, err
			}
			for i, c := range stars {
				exprs = append(exprs, c)
				cols = append(cols, Column{Name: names[i], Type: c.T})
			}
			continue
		}
		e, err := b.bindExpr(val, sc, aggOK)
		if err != nil {
			return nil, nil, err
		}
		if e.Type() == types.TUnknown {
			if e, err = coerce(e, types.TText); err != nil {
				return nil, nil, err
			}
		}
		name := rt.GetName()
		if name == "" {
			name = colName(val)
		}
		exprs = append(exprs, e)
		cols = append(cols, Column{Name: name, Type: e.Type()})
	}
	return exprs, cols, nil
}

func isStar(cr *pg.ColumnRef) bool {
	f := cr.GetFields()
	return len(f) > 0 && f[len(f)-1].GetAStar() != nil
}

// colName is Postgres's default output column name.
func colName(n *pg.Node) string {
	switch {
	case n.GetColumnRef() != nil:
		f := n.GetColumnRef().GetFields()
		return f[len(f)-1].GetString_().GetSval()
	case n.GetFuncCall() != nil:
		f := n.GetFuncCall().GetFuncname()
		return f[len(f)-1].GetString_().GetSval()
	case n.GetTypeCast() != nil:
		if name := colName(n.GetTypeCast().GetArg()); name != "?column?" {
			return name
		}
		names := n.GetTypeCast().GetTypeName().GetNames()
		return names[len(names)-1].GetString_().GetSval()
	case n.GetCaseExpr() != nil:
		return "case"
	case n.GetCoalesceExpr() != nil:
		return "coalesce"
	case n.GetAExpr() != nil && n.GetAExpr().GetKind() == pg.A_Expr_Kind_AEXPR_NULLIF:
		return "nullif"
	}
	return "?column?"
}

// bindAggregate plans GROUP BY / HAVING / aggregates:
// Aggregate(group keys, aggregates) -> Filter(HAVING) -> Project(select list).
func (b *binder) bindAggregate(sel *pg.SelectStmt, input Node, sc *scope) (Node, error) {
	targets := sel.GetTargetList()
	var groups []expr.Expr
	for _, g := range sel.GetGroupClause() {
		e, err := b.bindGroupItem(g, targets, sc)
		if err != nil {
			return nil, err
		}
		groups = append(groups, e)
	}
	groupIdx := make(map[string]int)
	cols := make([]Column, 0)
	for i, g := range groups {
		if _, dup := groupIdx[g.String()]; !dup {
			groupIdx[g.String()] = i
		}
		cols = append(cols, Column{Name: g.String(), Type: g.Type()})
	}

	exprs, outCols, err := b.bindTargets(targets, sc, true)
	if err != nil {
		return nil, err
	}
	var having expr.Expr
	if h := sel.GetHavingClause(); h != nil {
		e, err := b.bindExpr(h, sc, true)
		if err != nil {
			return nil, err
		}
		if e.Type() != types.TBool {
			return nil, fmt.Errorf("argument of HAVING must be type boolean, not %s", e.Type())
		}
		having = e
	}
	nGroups := len(groups)
	for _, s := range b.aggs {
		cols = append(cols, Column{Name: s.Kind.String(), Type: s.Out})
	}
	agg := &Aggregate{Input: input, GroupBy: groups, Aggs: b.aggs, Cols: cols}

	toOutput := func(e expr.Expr) (expr.Expr, error) {
		var bad error
		out := expr.Rewrite(e, func(x expr.Expr) (expr.Expr, bool) {
			if r, ok := x.(*aggRef); ok {
				return &expr.Col{Index: nGroups + r.idx, T: r.t, Name: r.String()}, true
			}
			if i, ok := groupIdx[x.String()]; ok {
				return &expr.Col{Index: i, T: groups[i].Type(), Name: x.String()}, true
			}
			if c, ok := x.(*expr.Col); ok && bad == nil {
				bad = fmt.Errorf("column %s must appear in the GROUP BY clause or be used in an aggregate function", c.Name)
			}
			return nil, false
		})
		return out, bad
	}
	var root Node = agg
	if having != nil {
		pred, err := toOutput(having)
		if err != nil {
			return nil, err
		}
		root = &Filter{Input: root, Pred: pred}
	}
	for i, e := range exprs {
		if exprs[i], err = toOutput(e); err != nil {
			return nil, err
		}
	}
	return &Project{Input: root, Exprs: exprs, Cols: outCols}, nil
}

// bindGroupItem resolves a GROUP BY item: an input expression, an output
// column ordinal (GROUP BY 1) or an output column alias, in that order.
func (b *binder) bindGroupItem(g *pg.Node, targets []*pg.Node, sc *scope) (expr.Expr, error) {
	if g.GetGroupingSet() != nil {
		return nil, unsupported("GROUPING SETS / ROLLUP / CUBE", "")
	}
	if c := g.GetAConst(); c != nil && c.GetIval() != nil {
		i := int(c.GetIval().GetIval())
		if i < 1 || i > len(targets) {
			return nil, fmt.Errorf("GROUP BY position %d is not in select list", i)
		}
		return b.bindGroupExpr(targets[i-1].GetResTarget().GetVal(), sc)
	}
	e, err := b.bindGroupExpr(g, sc)
	if err == nil {
		return e, nil
	}
	if cr := g.GetColumnRef(); cr != nil && len(cr.GetFields()) == 1 {
		name := cr.GetFields()[0].GetString_().GetSval()
		for _, t := range targets {
			if t.GetResTarget().GetName() == name {
				return b.bindGroupExpr(t.GetResTarget().GetVal(), sc)
			}
		}
	}
	return nil, err
}

func (b *binder) bindGroupExpr(n *pg.Node, sc *scope) (expr.Expr, error) {
	if hasAggregate(n) {
		return nil, errors.New("aggregate functions are not allowed in GROUP BY")
	}
	e, err := b.bindExpr(n, sc, false)
	if err != nil {
		return nil, err
	}
	if e.Type() == types.TUnknown {
		return coerce(e, types.TText)
	}
	return e, nil
}

var aggregateNames = map[string]ops.AggKind{"count": ops.Count, "sum": ops.Sum, "avg": ops.Avg, "min": ops.Min, "max": ops.Max}

var otherAggregates = map[string]bool{"array_agg": true, "string_agg": true, "json_agg": true, "jsonb_agg": true,
	"bool_and": true, "bool_or": true, "every": true, "stddev": true, "stddev_pop": true, "stddev_samp": true,
	"variance": true, "var_pop": true, "var_samp": true, "bit_and": true, "bit_or": true, "percentile_cont": true,
	"percentile_disc": true, "mode": true, "corr": true, "covar_pop": true, "covar_samp": true, "regr_slope": true,
	"json_object_agg": true, "xmlagg": true, "any_value": true}

var volatileFuncs = map[string]bool{"now": true, "random": true, "clock_timestamp": true, "statement_timestamp": true,
	"transaction_timestamp": true, "timeofday": true, "nextval": true, "currval": true, "gen_random_uuid": true,
	"setseed": true, "pg_current_xact_id": true, "txid_current": true}

func funcName(f *pg.FuncCall) string {
	names := f.GetFuncname()
	return strings.ToLower(names[len(names)-1].GetString_().GetSval())
}

// hasAggregate reports whether a parse tree contains an aggregate call.
func hasAggregate(n *pg.Node) bool {
	if n == nil {
		return false
	}
	found := false
	_ = walkParse(n, func(x *pg.Node) {
		if f := x.GetFuncCall(); f != nil {
			name := funcName(f)
			if _, ok := aggregateNames[name]; ok || otherAggregates[name] {
				found = true
			}
		}
	})
	return found
}

// walkParse visits the expression nodes of a parse tree that binding can
// reach. It does not need to be complete for unsupported nodes, because
// binding rejects those anyway.
func walkParse(n *pg.Node, fn func(*pg.Node)) error {
	if n == nil {
		return nil
	}
	fn(n)
	var kids []*pg.Node
	switch {
	case n.GetAExpr() != nil:
		kids = append(kids, n.GetAExpr().GetLexpr(), n.GetAExpr().GetRexpr())
	case n.GetBoolExpr() != nil:
		kids = n.GetBoolExpr().GetArgs()
	case n.GetNullTest() != nil:
		kids = append(kids, n.GetNullTest().GetArg())
	case n.GetBooleanTest() != nil:
		kids = append(kids, n.GetBooleanTest().GetArg())
	case n.GetTypeCast() != nil:
		kids = append(kids, n.GetTypeCast().GetArg())
	case n.GetCoalesceExpr() != nil:
		kids = n.GetCoalesceExpr().GetArgs()
	case n.GetCaseExpr() != nil:
		c := n.GetCaseExpr()
		kids = append(kids, c.GetArg(), c.GetDefresult())
		for _, w := range c.GetArgs() {
			kids = append(kids, w.GetCaseWhen().GetExpr(), w.GetCaseWhen().GetResult())
		}
	case n.GetFuncCall() != nil:
		kids = n.GetFuncCall().GetArgs()
	case n.GetList() != nil:
		kids = n.GetList().GetItems()
	case n.GetResTarget() != nil:
		kids = append(kids, n.GetResTarget().GetVal())
	}
	for _, k := range kids {
		if err := walkParse(k, fn); err != nil {
			return err
		}
	}
	return nil
}

func nodeName(n *pg.Node) string {
	if n == nil || n.GetNode() == nil {
		return "empty expression"
	}
	s := fmt.Sprintf("%T", n.GetNode())
	s = strings.TrimPrefix(s, "*pg_query.Node_")
	return s
}

// bindExpr binds one scalar expression. With aggOK, aggregate calls are
// allowed and become aggRef placeholders.
func (b *binder) bindExpr(n *pg.Node, sc *scope, aggOK bool) (expr.Expr, error) {
	switch {
	case n.GetColumnRef() != nil:
		f := n.GetColumnRef().GetFields()
		if isStar(n.GetColumnRef()) {
			return nil, errors.New("* is only allowed directly in the select list")
		}
		switch len(f) {
		case 1:
			return sc.resolve("", f[0].GetString_().GetSval())
		case 2:
			return sc.resolve(f[0].GetString_().GetSval(), f[1].GetString_().GetSval())
		}
		return nil, unsupported("schema-qualified column references", "use a table alias")
	case n.GetAConst() != nil:
		return bindConst(n.GetAConst())
	case n.GetTypeCast() != nil:
		return b.bindCast(n.GetTypeCast(), sc, aggOK)
	case n.GetAExpr() != nil:
		return b.bindAExpr(n.GetAExpr(), sc, aggOK)
	case n.GetBoolExpr() != nil:
		be := n.GetBoolExpr()
		var args []expr.Expr
		for _, a := range be.GetArgs() {
			e, err := b.bindExpr(a, sc, aggOK)
			if err != nil {
				return nil, err
			}
			if e, err = coerce(e, types.TBool); err != nil {
				return nil, fmt.Errorf("argument of %s must be type boolean: %w", be.GetBoolop(), err)
			}
			args = append(args, e)
		}
		switch be.GetBoolop() {
		case pg.BoolExprType_AND_EXPR:
			return &expr.And{Args: args}, nil
		case pg.BoolExprType_OR_EXPR:
			return &expr.Or{Args: args}, nil
		default:
			return &expr.Not{Arg: args[0]}, nil
		}
	case n.GetNullTest() != nil:
		nt := n.GetNullTest()
		if nt.GetArgisrow() {
			return nil, unsupported("row IS NULL", "")
		}
		e, err := b.bindExpr(nt.GetArg(), sc, aggOK)
		if err != nil {
			return nil, err
		}
		return &expr.IsNull{Arg: e, Not: nt.GetNulltesttype() == pg.NullTestType_IS_NOT_NULL}, nil
	case n.GetBooleanTest() != nil:
		bt := n.GetBooleanTest()
		e, err := b.bindExpr(bt.GetArg(), sc, aggOK)
		if err != nil {
			return nil, err
		}
		if e, err = coerce(e, types.TBool); err != nil {
			return nil, err
		}
		kinds := map[pg.BoolTestType]expr.BoolTestKind{
			pg.BoolTestType_IS_TRUE: expr.IsTrueTest, pg.BoolTestType_IS_NOT_TRUE: expr.IsNotTrueTest,
			pg.BoolTestType_IS_FALSE: expr.IsFalseTest, pg.BoolTestType_IS_NOT_FALSE: expr.IsNotFalseTest,
			pg.BoolTestType_IS_UNKNOWN: expr.IsUnknownTest, pg.BoolTestType_IS_NOT_UNKNOWN: expr.IsNotUnknownTest,
		}
		return &expr.BoolTest{Arg: e, Kind: kinds[bt.GetBooltesttype()]}, nil
	case n.GetCoalesceExpr() != nil:
		args, t, err := b.bindCommon(n.GetCoalesceExpr().GetArgs(), sc, aggOK, "COALESCE")
		if err != nil {
			return nil, err
		}
		return &expr.Coalesce{Args: args, T: t}, nil
	case n.GetCaseExpr() != nil:
		return b.bindCase(n.GetCaseExpr(), sc, aggOK)
	case n.GetFuncCall() != nil:
		return b.bindFunc(n.GetFuncCall(), sc, aggOK)
	case n.GetSubLink() != nil:
		return nil, unsupported("subqueries", "each needs its own incremental rule")
	case n.GetSqlvalueFunction() != nil:
		return nil, unsupported("CURRENT_TIMESTAMP, CURRENT_DATE and similar",
			"the answer changes when no row changed, so there is no delta to react to")
	case n.GetParamRef() != nil:
		return nil, unsupported("parameters", "")
	case n.GetCollateClause() != nil:
		return nil, unsupported("COLLATE", "DeltaView compares text byte by byte")
	case n.GetMinMaxExpr() != nil:
		return nil, unsupported("GREATEST / LEAST", "")
	}
	return nil, unsupported(nodeName(n), "")
}

func bindConst(c *pg.A_Const) (expr.Expr, error) {
	switch {
	case c.GetIsnull():
		return &expr.Const{V: types.Null(), T: types.TUnknown}, nil
	case c.GetIval() != nil:
		return &expr.Const{V: types.Int(int64(c.GetIval().GetIval())), T: types.TInt4}, nil
	case c.GetFval() != nil:
		s := c.GetFval().GetFval()
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return &expr.Const{V: types.Int(i), T: types.TInt8}, nil
			}
		}
		v, err := expr.CastValue(types.Text(s), types.TText, types.TNumeric)
		if err != nil {
			return nil, err
		}
		return &expr.Const{V: v, T: types.TNumeric}, nil
	case c.GetSval() != nil:
		return &expr.Const{V: types.Text(c.GetSval().GetSval()), T: types.TUnknown}, nil
	case c.GetBoolval() != nil:
		return &expr.Const{V: types.Bool(c.GetBoolval().GetBoolval()), T: types.TBool}, nil
	}
	return nil, unsupported("bit string constants", "")
}

// typeByName maps a SQL type name to a Type.
func typeByName(tn *pg.TypeName) (types.Type, error) {
	if len(tn.GetArrayBounds()) > 0 {
		return 0, unsupported("arrays", "")
	}
	names := tn.GetNames()
	name := strings.ToLower(names[len(names)-1].GetString_().GetSval())
	switch name {
	case "int2", "smallint":
		return types.TInt2, nil
	case "int4", "int", "integer":
		return types.TInt4, nil
	case "int8", "bigint":
		return types.TInt8, nil
	case "numeric", "decimal":
		if len(tn.GetTypmods()) > 0 {
			return 0, unsupported("numeric(p, s) casts", "DeltaView does not round to a declared scale; cast to numeric")
		}
		return types.TNumeric, nil
	case "float4", "real":
		return 0, unsupported("type real", "single-precision arithmetic is not modelled; use double precision")
	case "float8", "double precision":
		return types.TFloat8, nil
	case "text", "varchar":
		if len(tn.GetTypmods()) > 0 {
			return 0, unsupported("varchar(n) casts", "")
		}
		return types.TText, nil
	case "bool", "boolean":
		return types.TBool, nil
	case "date":
		return types.TDate, nil
	case "timestamp":
		return types.TTimestamp, nil
	case "timestamptz":
		return types.TTimestamptz, nil
	}
	return 0, unsupported("type "+name, "")
}

func (b *binder) bindCast(tc *pg.TypeCast, sc *scope, aggOK bool) (expr.Expr, error) {
	to, err := typeByName(tc.GetTypeName())
	if err != nil {
		return nil, err
	}
	e, err := b.bindExpr(tc.GetArg(), sc, aggOK)
	if err != nil {
		return nil, err
	}
	if c, ok := e.(*expr.Const); ok && c.T == types.TUnknown {
		return coerce(c, to)
	}
	if !expr.CanCast(e.Type(), to) {
		return nil, unsupported(fmt.Sprintf("cast from %s to %s", e.Type(), to), "")
	}
	if e.Type() == to {
		return e, nil
	}
	return &expr.Cast{Arg: e, To: to}, nil
}

func (b *binder) bindAExpr(a *pg.A_Expr, sc *scope, aggOK bool) (expr.Expr, error) {
	op := ""
	if names := a.GetName(); len(names) > 0 {
		op = names[len(names)-1].GetString_().GetSval()
	}
	bind := func(n *pg.Node) (expr.Expr, error) { return b.bindExpr(n, sc, aggOK) }
	switch a.GetKind() {
	case pg.A_Expr_Kind_AEXPR_OP:
		if a.GetLexpr() == nil {
			r, err := bind(a.GetRexpr())
			if err != nil {
				return nil, err
			}
			switch op {
			case "-":
				return negate(r)
			case "+":
				return r, nil
			}
			return nil, unsupported("prefix operator "+op, "")
		}
		l, err := bind(a.GetLexpr())
		if err != nil {
			return nil, err
		}
		r, err := bind(a.GetRexpr())
		if err != nil {
			return nil, err
		}
		return binaryOp(op, l, r)
	case pg.A_Expr_Kind_AEXPR_IN:
		x, err := bind(a.GetLexpr())
		if err != nil {
			return nil, err
		}
		items := a.GetRexpr().GetList().GetItems()
		all := []expr.Expr{x}
		for _, it := range items {
			e, err := bind(it)
			if err != nil {
				return nil, err
			}
			all = append(all, e)
		}
		all, _, err = unifyAll(all, "IN")
		if err != nil {
			return nil, err
		}
		return &expr.In{Arg: all[0], List: all[1:], Not: op == "<>"}, nil
	case pg.A_Expr_Kind_AEXPR_BETWEEN, pg.A_Expr_Kind_AEXPR_NOT_BETWEEN:
		x, err := bind(a.GetLexpr())
		if err != nil {
			return nil, err
		}
		bounds := a.GetRexpr().GetList().GetItems()
		lo, err := bind(bounds[0])
		if err != nil {
			return nil, err
		}
		hi, err := bind(bounds[1])
		if err != nil {
			return nil, err
		}
		ge, err := binaryOp(">=", x, lo)
		if err != nil {
			return nil, err
		}
		le, err := binaryOp("<=", x, hi)
		if err != nil {
			return nil, err
		}
		var e expr.Expr = &expr.And{Args: []expr.Expr{ge, le}}
		if a.GetKind() == pg.A_Expr_Kind_AEXPR_NOT_BETWEEN {
			e = &expr.Not{Arg: e}
		}
		return e, nil
	case pg.A_Expr_Kind_AEXPR_DISTINCT, pg.A_Expr_Kind_AEXPR_NOT_DISTINCT, pg.A_Expr_Kind_AEXPR_NULLIF:
		l, err := bind(a.GetLexpr())
		if err != nil {
			return nil, err
		}
		r, err := bind(a.GetRexpr())
		if err != nil {
			return nil, err
		}
		l, r, _, err = unify(l, r, "comparison")
		if err != nil {
			return nil, err
		}
		if a.GetKind() == pg.A_Expr_Kind_AEXPR_NULLIF {
			return &expr.NullIf{L: l, R: r}, nil
		}
		return &expr.IsDistinct{L: l, R: r, Not: a.GetKind() == pg.A_Expr_Kind_AEXPR_NOT_DISTINCT}, nil
	case pg.A_Expr_Kind_AEXPR_LIKE:
		l, err := bind(a.GetLexpr())
		if err != nil {
			return nil, err
		}
		r, err := bind(a.GetRexpr())
		if err != nil {
			return nil, err
		}
		if l, err = coerceText(l); err != nil {
			return nil, err
		}
		if r, err = coerceText(r); err != nil {
			return nil, err
		}
		return &expr.Like{Arg: l, Pattern: r, Not: op == "!~~"}, nil
	case pg.A_Expr_Kind_AEXPR_ILIKE:
		return nil, unsupported("ILIKE", "case folding depends on locale")
	case pg.A_Expr_Kind_AEXPR_SIMILAR:
		return nil, unsupported("SIMILAR TO", "")
	case pg.A_Expr_Kind_AEXPR_OP_ANY, pg.A_Expr_Kind_AEXPR_OP_ALL:
		return nil, unsupported("ANY / ALL", "use IN (list)")
	}
	return nil, unsupported("BETWEEN SYMMETRIC", "")
}

func coerceText(e expr.Expr) (expr.Expr, error) {
	if e.Type() == types.TText {
		return e, nil
	}
	if e.Type() == types.TUnknown {
		return coerce(e, types.TText)
	}
	return nil, fmt.Errorf("operator does not exist: %s LIKE text", e.Type())
}

func negate(e expr.Expr) (expr.Expr, error) {
	if c, ok := e.(*expr.Const); ok && !c.V.IsNull() {
		switch c.T {
		case types.TInt4, types.TInt8:
			return &expr.Const{V: types.Int(-c.V.AsInt()), T: c.T}, nil
		case types.TNumeric:
			return &expr.Const{V: types.Decimal(c.V.AsDecimal().Neg()), T: c.T}, nil
		}
	}
	if !e.Type().IsNumeric() {
		return nil, fmt.Errorf("operator does not exist: - %s", e.Type())
	}
	return &expr.Neg{Arg: e}, nil
}

func binaryOp(op string, l, r expr.Expr) (expr.Expr, error) {
	cmps := map[string]expr.CmpOp{"=": expr.OpEq, "<>": expr.OpNe, "!=": expr.OpNe, "<": expr.OpLt,
		"<=": expr.OpLe, ">": expr.OpGt, ">=": expr.OpGe}
	if c, ok := cmps[op]; ok {
		l, r, _, err := unify(l, r, "comparison")
		if err != nil {
			return nil, err
		}
		return &expr.Cmp{Op: c, L: l, R: r}, nil
	}
	switch op {
	case "+", "-", "*", "/", "%":
		l, r, t, err := unify(l, r, "operator "+op)
		if err != nil {
			return nil, err
		}
		if !t.IsNumeric() {
			return nil, fmt.Errorf("operator does not exist: %s %s %s", l.Type(), op, r.Type())
		}
		if op == "%" && (t == types.TFloat8 || t == types.TFloat4) {
			return nil, fmt.Errorf("operator does not exist: double precision %% double precision")
		}
		return &expr.Arith{Op: expr.ArithOp(op[0]), L: l, R: r, T: t}, nil
	case "||":
		var err error
		for _, side := range []*expr.Expr{&l, &r} {
			switch t := (*side).Type(); {
			case t == types.TText:
			case t == types.TUnknown:
				*side, err = coerce(*side, types.TText)
			case expr.CanCast(t, types.TText):
				*side = &expr.Cast{Arg: *side, To: types.TText}
			default:
				err = fmt.Errorf("operator does not exist: %s || text", t)
			}
			if err != nil {
				return nil, err
			}
		}
		return &expr.Concat{L: l, R: r}, nil
	}
	return nil, unsupported("operator "+op, "")
}

func (b *binder) bindCommon(nodes []*pg.Node, sc *scope, aggOK bool, what string) ([]expr.Expr, types.Type, error) {
	var es []expr.Expr
	for _, n := range nodes {
		e, err := b.bindExpr(n, sc, aggOK)
		if err != nil {
			return nil, 0, err
		}
		es = append(es, e)
	}
	return unifyAll(es, what)
}

func (b *binder) bindCase(c *pg.CaseExpr, sc *scope, aggOK bool) (expr.Expr, error) {
	var subject expr.Expr
	if c.GetArg() != nil {
		var err error
		if subject, err = b.bindExpr(c.GetArg(), sc, aggOK); err != nil {
			return nil, err
		}
	}
	var conds, results []expr.Expr
	for _, w := range c.GetArgs() {
		cw := w.GetCaseWhen()
		cond, err := b.bindExpr(cw.GetExpr(), sc, aggOK)
		if err != nil {
			return nil, err
		}
		if subject != nil {
			if cond, err = binaryOp("=", subject, cond); err != nil {
				return nil, err
			}
		} else if cond, err = coerce(cond, types.TBool); err != nil {
			return nil, fmt.Errorf("argument of CASE/WHEN must be type boolean: %w", err)
		}
		res, err := b.bindExpr(cw.GetResult(), sc, aggOK)
		if err != nil {
			return nil, err
		}
		conds = append(conds, cond)
		results = append(results, res)
	}
	if c.GetDefresult() != nil {
		e, err := b.bindExpr(c.GetDefresult(), sc, aggOK)
		if err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	results, t, err := unifyAll(results, "CASE")
	if err != nil {
		return nil, err
	}
	out := &expr.Case{T: t}
	for i, cond := range conds {
		out.Whens = append(out.Whens, expr.When{Cond: cond, Result: results[i]})
	}
	if c.GetDefresult() != nil {
		out.Else = results[len(results)-1]
	}
	return out, nil
}

func (b *binder) bindFunc(f *pg.FuncCall, sc *scope, aggOK bool) (expr.Expr, error) {
	name := funcName(f)
	if f.GetOver() != nil {
		return nil, unsupported("window functions", "each needs its own incremental rule")
	}
	if kind, ok := aggregateNames[name]; ok {
		return b.bindAggCall(name, kind, f, sc, aggOK)
	}
	if otherAggregates[name] {
		return nil, unsupported("aggregate "+name, "v1 supports count, sum, avg, min and max")
	}
	if volatileFuncs[name] {
		return nil, unsupported(name+"()", "the answer changes when no row changed, so there is no delta to react to")
	}
	if f.GetAggStar() || f.GetAggDistinct() || f.GetFuncVariadic() || len(f.GetAggOrder()) > 0 {
		return nil, unsupported("special call syntax for "+name, "")
	}
	var args []expr.Expr
	for _, a := range f.GetArgs() {
		e, err := b.bindExpr(a, sc, aggOK)
		if err != nil {
			return nil, err
		}
		args = append(args, e)
	}
	switch {
	case name == "abs" && len(args) == 1:
		if args[0].Type() == types.TUnknown {
			return nil, errors.New("function abs(unknown) is not unique")
		}
		if !args[0].Type().IsNumeric() {
			return nil, fmt.Errorf("function abs(%s) does not exist", args[0].Type())
		}
		return &expr.Func{Name: name, Args: args, T: args[0].Type()}, nil
	case (name == "lower" || name == "upper") && len(args) == 1:
		a, err := coerceText(args[0])
		if err != nil {
			return nil, fmt.Errorf("function %s(%s) does not exist", name, args[0].Type())
		}
		return &expr.Func{Name: name, Args: []expr.Expr{a}, T: types.TText}, nil
	case name == "length" && len(args) == 1:
		a, err := coerceText(args[0])
		if err != nil {
			return nil, fmt.Errorf("function length(%s) does not exist", args[0].Type())
		}
		return &expr.Func{Name: name, Args: []expr.Expr{a}, T: types.TInt4}, nil
	case name == "round" && (len(args) == 1 || len(args) == 2):
		x := args[0]
		if x.Type().IsInteger() || x.Type() == types.TUnknown || len(args) == 2 {
			var err error
			if x, err = coerce(x, types.TNumeric); err != nil {
				return nil, err
			}
		}
		out := []expr.Expr{x}
		if len(args) == 2 {
			s, err := coerce(args[1], types.TInt4)
			if err != nil || !s.Type().IsInteger() {
				return nil, errors.New("round(numeric, s): s must be an integer")
			}
			out = append(out, s)
		}
		if !x.Type().IsNumeric() {
			return nil, fmt.Errorf("function round(%s) does not exist", x.Type())
		}
		return &expr.Func{Name: name, Args: out, T: x.Type()}, nil
	case name == "coalesce":
		args, t, err := unifyAll(args, "COALESCE")
		if err != nil {
			return nil, err
		}
		return &expr.Coalesce{Args: args, T: t}, nil
	}
	return nil, unsupported("function "+name, "v1 supports abs, lower, upper, length, round and coalesce")
}

func (b *binder) bindAggCall(name string, kind ops.AggKind, f *pg.FuncCall, sc *scope, aggOK bool) (expr.Expr, error) {
	switch {
	case !aggOK:
		return nil, fmt.Errorf("aggregate function %s is not allowed here", name)
	case f.GetAggDistinct():
		return nil, unsupported(strings.ToUpper(name)+"(DISTINCT ...)", "")
	case f.GetAggFilter() != nil:
		return nil, unsupported("FILTER clause on aggregates", "")
	case len(f.GetAggOrder()) > 0 || f.GetAggWithinGroup():
		return nil, unsupported("ORDER BY inside an aggregate", "")
	}
	spec := ops.AggSpec{Kind: kind}
	if f.GetAggStar() {
		if kind != ops.Count {
			return nil, fmt.Errorf("%s(*) is not valid", name)
		}
		spec.Kind = ops.CountStar
		spec.Out = types.TInt8
	} else {
		if len(f.GetArgs()) != 1 {
			return nil, fmt.Errorf("%s takes exactly one argument", name)
		}
		if hasAggregate(f.GetArgs()[0]) {
			return nil, errors.New("aggregate function calls cannot be nested")
		}
		arg, err := b.bindExpr(f.GetArgs()[0], sc, false)
		if err != nil {
			return nil, err
		}
		if arg.Type() == types.TUnknown {
			if kind == ops.Count {
				arg, err = coerce(arg, types.TText)
			} else {
				err = fmt.Errorf("function %s(unknown) is not unique", name)
			}
			if err != nil {
				return nil, err
			}
		}
		spec.Arg = arg
		t := arg.Type()
		switch kind {
		case ops.Count:
			spec.Out = types.TInt8
		case ops.Sum:
			switch {
			case t == types.TInt2 || t == types.TInt4:
				spec.Out = types.TInt8
			case t == types.TInt8 || t == types.TNumeric:
				spec.Out = types.TNumeric
			case t == types.TFloat8 || t == types.TFloat4:
				spec.Out = types.TFloat8
			default:
				return nil, fmt.Errorf("function sum(%s) does not exist", t)
			}
		case ops.Avg:
			switch {
			case t.IsInteger() || t == types.TNumeric:
				spec.Out = types.TNumeric
			case t == types.TFloat8 || t == types.TFloat4:
				spec.Out = types.TFloat8
			default:
				return nil, fmt.Errorf("function avg(%s) does not exist", t)
			}
		case ops.Min, ops.Max:
			if t == types.TBool {
				return nil, fmt.Errorf("function %s(boolean) does not exist", name)
			}
			spec.Out = t
		}
	}
	key := spec.Kind.String()
	if spec.Arg != nil {
		key += "(" + spec.Arg.String() + " " + spec.Arg.Type().String() + ")"
	}
	idx, ok := b.aggKeys[key]
	if !ok {
		idx = len(b.aggs)
		b.aggs = append(b.aggs, spec)
		b.aggKeys[key] = idx
	}
	return &aggRef{idx: idx, t: spec.Out}, nil
}

// commonType picks the type both sides of an operator are converted to,
// following Postgres's implicit casts for the supported types.
func commonType(a, b types.Type) (types.Type, bool) {
	switch {
	case a == b:
		if a == types.TUnknown {
			return types.TText, true
		}
		return a, true
	case a == types.TUnknown:
		return b, true
	case b == types.TUnknown:
		return a, true
	case a.IsNumeric() && b.IsNumeric():
		switch {
		case a == types.TFloat8 || b == types.TFloat8 || a == types.TFloat4 || b == types.TFloat4:
			return types.TFloat8, true
		case a == types.TNumeric || b == types.TNumeric:
			return types.TNumeric, true
		}
		return max(a, b), true // int2 < int4 < int8
	case isTime(a) && isTime(b):
		if a == types.TTimestamptz || b == types.TTimestamptz {
			return types.TTimestamptz, true
		}
		return types.TTimestamp, true
	}
	return 0, false
}

func isTime(t types.Type) bool {
	return t == types.TDate || t == types.TTimestamp || t == types.TTimestamptz
}

func unify(l, r expr.Expr, what string) (expr.Expr, expr.Expr, types.Type, error) {
	t, ok := commonType(l.Type(), r.Type())
	if !ok {
		return nil, nil, 0, fmt.Errorf("%s: cannot compare or combine %s with %s", what, l.Type(), r.Type())
	}
	var err error
	if l, err = coerce(l, t); err != nil {
		return nil, nil, 0, err
	}
	if r, err = coerce(r, t); err != nil {
		return nil, nil, 0, err
	}
	return l, r, t, nil
}

func unifyAll(es []expr.Expr, what string) ([]expr.Expr, types.Type, error) {
	t := types.TUnknown
	for _, e := range es {
		var ok bool
		if t, ok = commonType(t, e.Type()); !ok {
			return nil, 0, fmt.Errorf("%s types %s and %s cannot be matched", what, t, e.Type())
		}
	}
	out := make([]expr.Expr, len(es))
	for i, e := range es {
		var err error
		if out[i], err = coerce(e, t); err != nil {
			return nil, 0, err
		}
	}
	return out, t, nil
}

// coerce converts e to type t implicitly. Untyped literals are parsed now,
// at bind time, the way Postgres resolves 'paid' or '2026-01-01'.
func coerce(e expr.Expr, t types.Type) (expr.Expr, error) {
	from := e.Type()
	if from == t {
		return e, nil
	}
	if c, ok := e.(*expr.Const); ok && from == types.TUnknown {
		if c.V.IsNull() {
			return &expr.Const{V: types.Null(), T: t}, nil
		}
		v, err := expr.CastValue(c.V, types.TText, t)
		if err != nil {
			return nil, err
		}
		return &expr.Const{V: v, T: t}, nil
	}
	implicit := (from.IsNumeric() && t.IsNumeric() && rank(from) <= rank(t)) ||
		(from == types.TDate && (t == types.TTimestamp || t == types.TTimestamptz)) ||
		(from == types.TTimestamp && t == types.TTimestamptz) || (from == types.TTimestamptz && t == types.TTimestamp)
	if !implicit {
		return nil, fmt.Errorf("cannot use %s where %s is expected (add an explicit cast)", from, t)
	}
	return &expr.Cast{Arg: e, To: t}, nil
}

func rank(t types.Type) int {
	switch t {
	case types.TInt2:
		return 1
	case types.TInt4:
		return 2
	case types.TInt8:
		return 3
	case types.TNumeric:
		return 4
	case types.TFloat4:
		return 5
	}
	return 6
}
