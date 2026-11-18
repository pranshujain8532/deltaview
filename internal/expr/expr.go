package expr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pranshujain8532/deltaview/internal/types"
)

// Evaluation errors that Postgres would also raise. A view whose query errors
// on some row cannot be maintained, so the engine stops on these.
var (
	ErrDivisionByZero = errors.New("division by zero")
	ErrOutOfRange     = errors.New("value out of range")
	ErrInvalidCast    = errors.New("invalid input for cast")
)

// Expr is a bound, typed scalar expression. The binder has already resolved
// names to column positions and inserted every implicit cast, so Eval never
// has to guess a type.
type Expr interface {
	Eval(row types.Row) (types.Value, error)
	Type() types.Type
	String() string
}

// Col reads column Index of the input row.
type Col struct {
	Index int
	T     types.Type
	Name  string
}

func (c *Col) Eval(row types.Row) (types.Value, error) { return row[c.Index], nil }
func (c *Col) Type() types.Type                        { return c.T }
func (c *Col) String() string {
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("$%d", c.Index)
}

// Const is a literal.
type Const struct {
	V types.Value
	T types.Type
}

func (c *Const) Eval(types.Row) (types.Value, error) { return c.V, nil }
func (c *Const) Type() types.Type                    { return c.T }
func (c *Const) String() string                      { return c.V.String() }

// Truth is SQL's three-valued boolean result.
type Truth int8

// The three truth values.
const (
	False Truth = iota
	True
	Unknown
)

// TruthOf converts a boolean value (or NULL) to a Truth.
func TruthOf(v types.Value) Truth {
	if v.IsNull() {
		return Unknown
	}
	if v.AsBool() {
		return True
	}
	return False
}

func (t Truth) value() types.Value {
	switch t {
	case True:
		return types.Bool(true)
	case False:
		return types.Bool(false)
	}
	return types.Null()
}

// IsTrue evaluates a predicate and reports whether it is TRUE. WHERE and
// HAVING keep a row only then; FALSE and UNKNOWN both drop it.
func IsTrue(e Expr, row types.Row) (bool, error) {
	v, err := e.Eval(row)
	if err != nil {
		return false, err
	}
	return TruthOf(v) == True, nil
}

// And is AND over two or more arguments: FALSE wins over UNKNOWN.
type And struct{ Args []Expr }

func (a *And) Eval(row types.Row) (types.Value, error) {
	res := True
	for _, arg := range a.Args {
		v, err := arg.Eval(row)
		if err != nil {
			return types.Value{}, err
		}
		switch TruthOf(v) {
		case False:
			return types.Bool(false), nil
		case Unknown:
			res = Unknown
		}
	}
	return res.value(), nil
}
func (a *And) Type() types.Type { return types.TBool }
func (a *And) String() string   { return join("AND", a.Args) }

// Or is OR over two or more arguments: TRUE wins over UNKNOWN.
type Or struct{ Args []Expr }

func (o *Or) Eval(row types.Row) (types.Value, error) {
	res := False
	for _, arg := range o.Args {
		v, err := arg.Eval(row)
		if err != nil {
			return types.Value{}, err
		}
		switch TruthOf(v) {
		case True:
			return types.Bool(true), nil
		case Unknown:
			res = Unknown
		}
	}
	return res.value(), nil
}
func (o *Or) Type() types.Type { return types.TBool }
func (o *Or) String() string   { return join("OR", o.Args) }

func join(op string, args []Expr) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.String()
	}
	return "(" + strings.Join(parts, " "+op+" ") + ")"
}

// Not negates; NOT UNKNOWN is UNKNOWN.
type Not struct{ Arg Expr }

func (n *Not) Eval(row types.Row) (types.Value, error) {
	v, err := n.Arg.Eval(row)
	if err != nil || v.IsNull() {
		return types.Null(), err
	}
	return types.Bool(!v.AsBool()), nil
}
func (n *Not) Type() types.Type { return types.TBool }
func (n *Not) String() string   { return "(NOT " + n.Arg.String() + ")" }

// CmpOp is a comparison operator.
type CmpOp uint8

// Comparison operators.
const (
	OpEq CmpOp = iota
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
)

var cmpNames = [...]string{"=", "<>", "<", "<=", ">", ">="}

func (o CmpOp) holds(c int) bool {
	switch o {
	case OpEq:
		return c == 0
	case OpNe:
		return c != 0
	case OpLt:
		return c < 0
	case OpLe:
		return c <= 0
	case OpGt:
		return c > 0
	}
	return c >= 0
}

// Cmp compares two values of one type family. Any NULL makes it UNKNOWN,
// so NULL = NULL is not true.
type Cmp struct {
	Op   CmpOp
	L, R Expr
}

func (c *Cmp) Eval(row types.Row) (types.Value, error) {
	l, err := c.L.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	r, err := c.R.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	if l.IsNull() || r.IsNull() {
		return types.Null(), nil
	}
	return types.Bool(c.Op.holds(types.Compare(l, r))), nil
}
func (c *Cmp) Type() types.Type { return types.TBool }
func (c *Cmp) String() string {
	return "(" + c.L.String() + " " + cmpNames[c.Op] + " " + c.R.String() + ")"
}

// IsDistinct is IS [NOT] DISTINCT FROM: a comparison where NULL is a value.
type IsDistinct struct {
	L, R Expr
	Not  bool
}

func (d *IsDistinct) Eval(row types.Row) (types.Value, error) {
	l, err := d.L.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	r, err := d.R.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	var distinct bool
	switch {
	case l.IsNull() && r.IsNull():
		distinct = false
	case l.IsNull() || r.IsNull():
		distinct = true
	default:
		distinct = types.Compare(l, r) != 0
	}
	return types.Bool(distinct != d.Not), nil
}
func (d *IsDistinct) Type() types.Type { return types.TBool }
func (d *IsDistinct) String() string {
	op := " IS DISTINCT FROM "
	if d.Not {
		op = " IS NOT DISTINCT FROM "
	}
	return "(" + d.L.String() + op + d.R.String() + ")"
}

// IsNull is IS [NOT] NULL. It is never UNKNOWN.
type IsNull struct {
	Arg Expr
	Not bool
}

func (n *IsNull) Eval(row types.Row) (types.Value, error) {
	v, err := n.Arg.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	return types.Bool(v.IsNull() != n.Not), nil
}
func (n *IsNull) Type() types.Type { return types.TBool }
func (n *IsNull) String() string {
	if n.Not {
		return "(" + n.Arg.String() + " IS NOT NULL)"
	}
	return "(" + n.Arg.String() + " IS NULL)"
}

// BoolTestKind selects IS TRUE, IS NOT FALSE, IS UNKNOWN and friends.
type BoolTestKind uint8

// Boolean test kinds.
const (
	IsTrueTest BoolTestKind = iota
	IsNotTrueTest
	IsFalseTest
	IsNotFalseTest
	IsUnknownTest
	IsNotUnknownTest
)

// BoolTest is IS [NOT] TRUE/FALSE/UNKNOWN. It is never UNKNOWN itself.
type BoolTest struct {
	Arg  Expr
	Kind BoolTestKind
}

func (b *BoolTest) Eval(row types.Row) (types.Value, error) {
	v, err := b.Arg.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	t := TruthOf(v)
	var res bool
	switch b.Kind {
	case IsTrueTest:
		res = t == True
	case IsNotTrueTest:
		res = t != True
	case IsFalseTest:
		res = t == False
	case IsNotFalseTest:
		res = t != False
	case IsUnknownTest:
		res = t == Unknown
	case IsNotUnknownTest:
		res = t != Unknown
	}
	return types.Bool(res), nil
}
func (b *BoolTest) Type() types.Type { return types.TBool }
func (b *BoolTest) String() string {
	names := [...]string{"IS TRUE", "IS NOT TRUE", "IS FALSE", "IS NOT FALSE", "IS UNKNOWN", "IS NOT UNKNOWN"}
	return "(" + b.Arg.String() + " " + names[b.Kind] + ")"
}

// In is x [NOT] IN (a, b, ...). TRUE if some element equals x; otherwise
// UNKNOWN if x or any element is NULL; otherwise FALSE. NOT IN negates that.
type In struct {
	Arg  Expr
	List []Expr
	Not  bool
}

func (in *In) Eval(row types.Row) (types.Value, error) {
	x, err := in.Arg.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	res := False
	if x.IsNull() {
		res = Unknown
	}
	for _, e := range in.List {
		v, err := e.Eval(row)
		if err != nil {
			return types.Value{}, err
		}
		if v.IsNull() {
			res = Unknown
			continue
		}
		if !x.IsNull() && types.Compare(x, v) == 0 {
			res = True
			break
		}
	}
	if in.Not {
		switch res {
		case True:
			res = False
		case False:
			res = True
		}
	}
	return res.value(), nil
}
func (in *In) Type() types.Type { return types.TBool }
func (in *In) String() string {
	op := " IN "
	if in.Not {
		op = " NOT IN "
	}
	return "(" + in.Arg.String() + op + join(",", in.List) + ")"
}

// Coalesce returns its first non-NULL argument.
type Coalesce struct {
	Args []Expr
	T    types.Type
}

func (c *Coalesce) Eval(row types.Row) (types.Value, error) {
	for _, a := range c.Args {
		v, err := a.Eval(row)
		if err != nil || !v.IsNull() {
			return v, err
		}
	}
	return types.Null(), nil
}
func (c *Coalesce) Type() types.Type { return c.T }
func (c *Coalesce) String() string   { return "COALESCE" + join(",", c.Args) }

// NullIf returns NULL when L = R, else L.
type NullIf struct{ L, R Expr }

func (n *NullIf) Eval(row types.Row) (types.Value, error) {
	l, err := n.L.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	r, err := n.R.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	if !l.IsNull() && !r.IsNull() && types.Compare(l, r) == 0 {
		return types.Null(), nil
	}
	return l, nil
}
func (n *NullIf) Type() types.Type { return n.L.Type() }
func (n *NullIf) String() string   { return "NULLIF(" + n.L.String() + ", " + n.R.String() + ")" }

// When is one WHEN cond THEN result arm.
type When struct{ Cond, Result Expr }

// Case is a searched CASE. A simple CASE x WHEN v is bound into x = v arms.
// Only a TRUE condition selects an arm.
type Case struct {
	Whens []When
	Else  Expr // nil means NULL
	T     types.Type
}

func (c *Case) Eval(row types.Row) (types.Value, error) {
	for _, w := range c.Whens {
		ok, err := IsTrue(w.Cond, row)
		if err != nil {
			return types.Value{}, err
		}
		if ok {
			return w.Result.Eval(row)
		}
	}
	if c.Else == nil {
		return types.Null(), nil
	}
	return c.Else.Eval(row)
}
func (c *Case) Type() types.Type { return c.T }
func (c *Case) String() string {
	var b strings.Builder
	b.WriteString("CASE")
	for _, w := range c.Whens {
		b.WriteString(" WHEN " + w.Cond.String() + " THEN " + w.Result.String())
	}
	if c.Else != nil {
		b.WriteString(" ELSE " + c.Else.String())
	}
	b.WriteString(" END")
	return b.String()
}

// Like is x [NOT] LIKE pattern with % and _ wildcards and \ escapes.
// Matching is by character, comparing bytes (the C collation).
type Like struct {
	Arg, Pattern Expr
	Not          bool
}

func (l *Like) Eval(row types.Row) (types.Value, error) {
	x, err := l.Arg.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	p, err := l.Pattern.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	if x.IsNull() || p.IsNull() {
		return types.Null(), nil
	}
	m, err := likeMatch([]rune(x.AsText()), []rune(p.AsText()))
	if err != nil {
		return types.Value{}, err
	}
	return types.Bool(m != l.Not), nil
}
func (l *Like) Type() types.Type { return types.TBool }
func (l *Like) String() string {
	op := " LIKE "
	if l.Not {
		op = " NOT LIKE "
	}
	return "(" + l.Arg.String() + op + l.Pattern.String() + ")"
}

func likeMatch(s, p []rune) (bool, error) {
	for len(p) > 0 {
		switch p[0] {
		case '%':
			for len(p) > 0 && p[0] == '%' {
				p = p[1:]
			}
			if len(p) == 0 {
				return true, nil
			}
			for i := 0; i <= len(s); i++ {
				ok, err := likeMatch(s[i:], p)
				if err != nil || ok {
					return ok, err
				}
			}
			return false, nil
		case '_':
			if len(s) == 0 {
				return false, nil
			}
			s, p = s[1:], p[1:]
		case '\\':
			if len(p) < 2 {
				return false, errors.New("LIKE pattern must not end with escape character")
			}
			if len(s) == 0 || s[0] != p[1] {
				return false, nil
			}
			s, p = s[1:], p[2:]
		default:
			if len(s) == 0 || s[0] != p[0] {
				return false, nil
			}
			s, p = s[1:], p[1:]
		}
	}
	return len(s) == 0, nil
}
