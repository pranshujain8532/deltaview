package expr

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/pranshujain8532/deltaview/internal/types"
)

// ArithOp is a binary arithmetic operator.
type ArithOp byte

// Arithmetic operators.
const (
	OpAdd ArithOp = '+'
	OpSub ArithOp = '-'
	OpMul ArithOp = '*'
	OpDiv ArithOp = '/'
	OpMod ArithOp = '%'
)

// Arith applies an operator to two operands that the binder has already
// cast to T's family (both integer, both numeric, or both float8).
type Arith struct {
	Op   ArithOp
	L, R Expr
	T    types.Type
}

func (a *Arith) Type() types.Type { return a.T }
func (a *Arith) String() string {
	return "(" + a.L.String() + " " + string(a.Op) + " " + a.R.String() + ")"
}

func (a *Arith) Eval(row types.Row) (types.Value, error) {
	l, err := a.L.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	r, err := a.R.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	if l.IsNull() || r.IsNull() {
		return types.Null(), nil
	}
	switch {
	case a.T.IsInteger():
		return intArith(a.Op, l.AsInt(), r.AsInt(), a.T)
	case a.T == types.TNumeric:
		return NumericArith(a.Op, l.AsDecimal(), r.AsDecimal())
	case a.T == types.TFloat8 || a.T == types.TFloat4:
		return floatArith(a.Op, l.AsFloat(), r.AsFloat())
	}
	return types.Value{}, fmt.Errorf("arithmetic on %s", a.T)
}

// intRange returns the bounds of an integer type.
func intRange(t types.Type) (int64, int64) {
	switch t {
	case types.TInt2:
		return math.MinInt16, math.MaxInt16
	case types.TInt4:
		return math.MinInt32, math.MaxInt32
	}
	return math.MinInt64, math.MaxInt64
}

// CheckIntRange errors like Postgres when v does not fit type t.
func CheckIntRange(v int64, t types.Type) (types.Value, error) {
	lo, hi := intRange(t)
	if v < lo || v > hi {
		return types.Value{}, fmt.Errorf("%w: %s out of range", ErrOutOfRange, t)
	}
	return types.Int(v), nil
}

func intArith(op ArithOp, x, y int64, t types.Type) (types.Value, error) {
	overflow := fmt.Errorf("%w: %s out of range", ErrOutOfRange, t)
	var r int64
	switch op {
	case OpAdd:
		r = x + y
		if (y > 0 && r < x) || (y < 0 && r > x) {
			return types.Value{}, overflow
		}
	case OpSub:
		r = x - y
		if (y < 0 && r < x) || (y > 0 && r > x) {
			return types.Value{}, overflow
		}
	case OpMul:
		if x != 0 && y != 0 {
			r = x * y
			if r/y != x || (x == -1 && y == math.MinInt64) || (y == -1 && x == math.MinInt64) {
				return types.Value{}, overflow
			}
		}
	case OpDiv:
		if y == 0 {
			return types.Value{}, ErrDivisionByZero
		}
		if x == math.MinInt64 && y == -1 {
			return types.Value{}, overflow
		}
		r = x / y // Go and Postgres both truncate toward zero
	case OpMod:
		if y == 0 {
			return types.Value{}, ErrDivisionByZero
		}
		if y == -1 {
			return types.Int(0), nil
		}
		r = x % y // sign follows the dividend in both
	}
	return CheckIntRange(r, t)
}

func floatArith(op ArithOp, x, y float64) (types.Value, error) {
	var r float64
	switch op {
	case OpAdd:
		r = x + y
	case OpSub:
		r = x - y
	case OpMul:
		r = x * y
		if r == 0 && x != 0 && y != 0 {
			return types.Value{}, fmt.Errorf("%w: underflow", ErrOutOfRange)
		}
	case OpDiv:
		if y == 0 {
			return types.Value{}, ErrDivisionByZero
		}
		r = x / y
		if r == 0 && x != 0 && !math.IsInf(y, 0) {
			return types.Value{}, fmt.Errorf("%w: underflow", ErrOutOfRange)
		}
	case OpMod:
		return types.Value{}, fmt.Errorf("operator %% is not defined for double precision")
	}
	if math.IsInf(r, 0) && !math.IsInf(x, 0) && !math.IsInf(y, 0) {
		return types.Value{}, fmt.Errorf("%w: overflow", ErrOutOfRange)
	}
	return types.Float(r), nil
}

// NumericArith implements numeric + - * / % with Postgres's result scales.
func NumericArith(op ArithOp, x, y decimal.Decimal) (types.Value, error) {
	switch op {
	case OpAdd:
		return types.Decimal(x.Add(y)), nil
	case OpSub:
		return types.Decimal(x.Sub(y)), nil
	case OpMul:
		return types.Decimal(x.Mul(y)), nil
	case OpDiv:
		if y.IsZero() {
			return types.Value{}, ErrDivisionByZero
		}
		return types.Decimal(x.DivRound(y, DivScale(x, y))), nil
	case OpMod:
		if y.IsZero() {
			return types.Value{}, ErrDivisionByZero
		}
		// QuoRem truncates the quotient exactly; Div would round it to 16
		// places first and could flip the sign of the remainder.
		_, r := x.QuoRem(y, 0)
		return types.Decimal(r), nil
	}
	return types.Value{}, fmt.Errorf("numeric operator %c", op)
}

// DivScale is Postgres's select_div_scale: the number of decimal places of
// a numeric quotient. It aims for at least 16 significant digits, and never
// fewer places than either input shows. Matching it exactly means AVG and
// numeric division agree with Postgres digit for digit.
func DivScale(x, y decimal.Decimal) int32 {
	w1, f1 := nbaseWeight(x)
	w2, f2 := nbaseWeight(y)
	qweight := w1 - w2
	if f1 <= f2 {
		qweight--
	}
	rscale := 16 - qweight*4
	rscale = max(rscale, dscale(x), dscale(y), 0)
	return int32(min(rscale, 1000))
}

// nbaseWeight returns the weight (power of 10000) of the first non-zero
// base-10000 digit of |d| and that digit, as Postgres stores numerics.
func nbaseWeight(d decimal.Decimal) (weight int, first int64) {
	if d.IsZero() {
		return 0, 0
	}
	a := d.Abs()
	// Decimal position of the leading digit: 10^p <= a < 10^(p+1).
	p := len(a.Coefficient().String()) + int(a.Exponent()) - 1
	weight = floorDiv(p, 4)
	first = a.Shift(int32(-4 * weight)).IntPart()
	return weight, first
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// dscale is the number of digits after the decimal point the value carries.
func dscale(d decimal.Decimal) int {
	if e := int(d.Exponent()); e < 0 {
		return -e
	}
	return 0
}

// Neg is unary minus.
type Neg struct{ Arg Expr }

func (n *Neg) Type() types.Type { return n.Arg.Type() }
func (n *Neg) String() string   { return "(-" + n.Arg.String() + ")" }
func (n *Neg) Eval(row types.Row) (types.Value, error) {
	v, err := n.Arg.Eval(row)
	if err != nil || v.IsNull() {
		return v, err
	}
	switch t := n.Arg.Type(); {
	case t.IsInteger():
		if v.AsInt() == math.MinInt64 {
			return types.Value{}, fmt.Errorf("%w: %s out of range", ErrOutOfRange, t)
		}
		return CheckIntRange(-v.AsInt(), t)
	case t == types.TNumeric:
		return types.Decimal(v.AsDecimal().Neg()), nil
	default:
		return types.Float(-v.AsFloat()), nil
	}
}

// Concat is text || text.
type Concat struct{ L, R Expr }

func (c *Concat) Type() types.Type { return types.TText }
func (c *Concat) String() string   { return "(" + c.L.String() + " || " + c.R.String() + ")" }
func (c *Concat) Eval(row types.Row) (types.Value, error) {
	l, err := c.L.Eval(row)
	if err != nil {
		return types.Value{}, err
	}
	r, err := c.R.Eval(row)
	if err != nil || l.IsNull() || r.IsNull() {
		return types.Null(), err
	}
	return types.Text(l.AsText() + r.AsText()), nil
}

// Cast converts between types. The binder only builds casts CanCast allows.
type Cast struct {
	Arg Expr
	To  types.Type
}

func (c *Cast) Type() types.Type { return c.To }
func (c *Cast) String() string   { return c.Arg.String() + "::" + c.To.String() }
func (c *Cast) Eval(row types.Row) (types.Value, error) {
	v, err := c.Arg.Eval(row)
	if err != nil || v.IsNull() {
		return v, err
	}
	return CastValue(v, c.Arg.Type(), c.To)
}

// CanCast reports whether DeltaView can convert from one type to another
// exactly as Postgres does. Casts to text from numeric and timestamps are
// refused: Postgres's output depends on display scale and time zone.
func CanCast(from, to types.Type) bool {
	if from == to || from == types.TUnknown {
		return true
	}
	num := func(t types.Type) bool { return t.IsNumeric() }
	switch {
	case num(from) && num(to):
		return true
	case to == types.TText:
		return from.IsInteger() || from == types.TFloat8 || from == types.TFloat4 || from == types.TBool || from == types.TDate
	case from == types.TText:
		return true
	case from == types.TBool && to == types.TInt4, from == types.TInt4 && to == types.TBool:
		return true
	case from == types.TDate && (to == types.TTimestamp || to == types.TTimestamptz):
		return true
	case (from == types.TTimestamp || from == types.TTimestamptz) && (to == types.TDate || to == types.TTimestamp || to == types.TTimestamptz):
		return true
	}
	return false
}

// CastValue converts a non-NULL value.
func CastValue(v types.Value, from, to types.Type) (types.Value, error) {
	if from == to {
		return v, nil
	}
	if v.Kind() == types.KindText && to != types.TText {
		return parseLiteral(v.AsText(), to)
	}
	switch to {
	case types.TInt2, types.TInt4, types.TInt8:
		switch v.Kind() {
		case types.KindInt:
			return CheckIntRange(v.AsInt(), to)
		case types.KindDecimal:
			r := v.AsDecimal().Round(0) // half away from zero, like numeric_int8
			if !r.IsInteger() || r.Cmp(decimal.NewFromInt(math.MaxInt64)) > 0 || r.Cmp(decimal.NewFromInt(math.MinInt64)) < 0 {
				return types.Value{}, fmt.Errorf("%w: %s out of range", ErrOutOfRange, to)
			}
			return CheckIntRange(r.IntPart(), to)
		case types.KindFloat:
			f := math.RoundToEven(v.AsFloat()) // rint, like dtoi8
			if math.IsNaN(f) || f < math.MinInt64 || f >= math.MaxInt64 {
				return types.Value{}, fmt.Errorf("%w: %s out of range", ErrOutOfRange, to)
			}
			return CheckIntRange(int64(f), to)
		case types.KindBool:
			if v.AsBool() {
				return types.Int(1), nil
			}
			return types.Int(0), nil
		}
	case types.TNumeric:
		switch v.Kind() {
		case types.KindInt:
			return types.Decimal(decimal.NewFromInt(v.AsInt())), nil
		case types.KindDecimal:
			return v, nil
		case types.KindFloat:
			f := v.AsFloat()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return types.Value{}, fmt.Errorf("%w: cannot convert %s to numeric", ErrInvalidCast, types.FormatFloat(f))
			}
			// float8_numeric goes through text with DBL_DIG (15) digits.
			d, err := decimal.NewFromString(strconv.FormatFloat(f, 'g', 15, 64))
			if err != nil {
				return types.Value{}, fmt.Errorf("%w: %v", ErrInvalidCast, err)
			}
			return types.Decimal(d), nil
		}
	case types.TFloat8, types.TFloat4:
		switch v.Kind() {
		case types.KindInt:
			return types.Float(float64(v.AsInt())), nil
		case types.KindDecimal:
			f, _ := v.AsDecimal().Float64()
			return types.Float(f), nil
		case types.KindFloat:
			return v, nil
		}
	case types.TText:
		switch v.Kind() {
		case types.KindInt:
			return types.Text(strconv.FormatInt(v.AsInt(), 10)), nil
		case types.KindFloat:
			return types.Text(types.FormatFloat(v.AsFloat())), nil
		case types.KindBool:
			if v.AsBool() {
				return types.Text("true"), nil
			}
			return types.Text("false"), nil
		case types.KindDate:
			return types.Text(v.String()), nil
		}
	case types.TBool:
		if v.Kind() == types.KindInt {
			return types.Bool(v.AsInt() != 0), nil
		}
	case types.TTimestamp, types.TTimestamptz:
		switch v.Kind() {
		case types.KindDate:
			return types.Timestamp(v.AsInt() * 86400 * 1e6), nil
		case types.KindTimestamp:
			return v, nil
		}
	case types.TDate:
		if v.Kind() == types.KindTimestamp {
			return types.Date(floorDiv64(v.AsInt(), 86400*1e6)), nil
		}
	}
	return types.Value{}, fmt.Errorf("%w: %s to %s", ErrInvalidCast, from, to)
}

func floorDiv64(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// parseLiteral converts text input to a type, accepting what Postgres's
// input functions accept for the common cases.
func parseLiteral(s string, to types.Type) (types.Value, error) {
	t := strings.TrimSpace(s)
	switch to {
	case types.TBool:
		switch strings.ToLower(t) {
		case "t", "true", "yes", "y", "on", "1":
			return types.Bool(true), nil
		case "f", "false", "no", "n", "off", "0":
			return types.Bool(false), nil
		}
	case types.TInt2, types.TInt4, types.TInt8:
		i, err := strconv.ParseInt(t, 10, 64)
		if err == nil {
			return CheckIntRange(i, to)
		}
	case types.TNumeric:
		d, err := decimal.NewFromString(t)
		if err == nil {
			return types.Decimal(d), nil
		}
	case types.TFloat8, types.TFloat4:
		switch strings.ToLower(t) {
		case "nan":
			return types.Float(math.NaN()), nil
		case "infinity", "inf", "+infinity":
			return types.Float(math.Inf(1)), nil
		case "-infinity", "-inf":
			return types.Float(math.Inf(-1)), nil
		}
		f, err := strconv.ParseFloat(t, 64)
		if err == nil {
			return types.Float(f), nil
		}
	case types.TText:
		return types.Text(s), nil
	case types.TDate:
		if tm, err := time.Parse(types.DateLayout, t); err == nil {
			return types.Date(floorDiv64(tm.Unix(), 86400)), nil
		}
	case types.TTimestamp, types.TTimestamptz:
		// Literals without a zone are read as UTC (DeltaView runs in UTC).
		// For timestamp without time zone Postgres ignores a given offset,
		// so the wall-clock time is kept as written.
		for _, layout := range []string{"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07",
			"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999",
			"2006-01-02"} {
			if tm, err := time.Parse(layout, t); err == nil {
				if to == types.TTimestamp {
					tm = time.Date(tm.Year(), tm.Month(), tm.Day(), tm.Hour(), tm.Minute(), tm.Second(), tm.Nanosecond(), time.UTC)
				}
				return types.TimestampFromTime(tm), nil
			}
		}
	}
	return types.Value{}, fmt.Errorf("%w: invalid input syntax for type %s: %q", ErrInvalidCast, to, s)
}

// Func is a deterministic scalar function.
type Func struct {
	Name string // abs, lower, upper, length, round
	Args []Expr
	T    types.Type
}

func (f *Func) Type() types.Type { return f.T }
func (f *Func) String() string   { return f.Name + join(",", f.Args) }
func (f *Func) Eval(row types.Row) (types.Value, error) {
	args := make([]types.Value, len(f.Args))
	for i, a := range f.Args {
		v, err := a.Eval(row)
		if err != nil {
			return types.Value{}, err
		}
		if v.IsNull() {
			return types.Null(), nil // all supported functions are strict
		}
		args[i] = v
	}
	switch f.Name {
	case "abs":
		switch args[0].Kind() {
		case types.KindInt:
			if args[0].AsInt() < 0 {
				return (&Neg{Arg: &Const{V: args[0], T: f.T}}).Eval(row)
			}
			return args[0], nil
		case types.KindDecimal:
			return types.Decimal(args[0].AsDecimal().Abs()), nil
		default:
			return types.Float(math.Abs(args[0].AsFloat())), nil
		}
	case "lower", "upper":
		// The C collation changes only ASCII letters.
		b := []byte(args[0].AsText())
		for i, c := range b {
			if f.Name == "lower" && c >= 'A' && c <= 'Z' {
				b[i] = c + 32
			} else if f.Name == "upper" && c >= 'a' && c <= 'z' {
				b[i] = c - 32
			}
		}
		return types.Text(string(b)), nil
	case "length":
		return types.Int(int64(utf8.RuneCountInString(args[0].AsText()))), nil
	case "round":
		places := int32(0)
		if len(args) == 2 {
			places = int32(args[1].AsInt())
		}
		if args[0].Kind() == types.KindFloat {
			return types.Float(math.RoundToEven(args[0].AsFloat())), nil
		}
		return types.Decimal(args[0].AsDecimal().Round(places)), nil
	}
	return types.Value{}, fmt.Errorf("unknown function %s", f.Name)
}
