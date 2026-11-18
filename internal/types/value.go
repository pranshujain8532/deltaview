package types

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Kind is the runtime representation of a Value. Several SQL types share a
// kind (int2, int4 and int8 are all KindInt), which is what makes an int4 key
// and an int8 key with the same number encode to the same bytes.
type Kind uint8

// The kinds, in the order their encodings sort.
const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindFloat
	KindDecimal
	KindText
	KindTimestamp // microseconds since the Unix epoch, UTC
	KindDate      // days since the Unix epoch
)

var kindNames = [...]string{"null", "bool", "int", "float", "decimal", "text", "timestamp", "date"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("kind(%d)", k)
}

// Value is one SQL value. It is a small immutable struct passed by value so
// rows can be built and copied without allocation per column.
type Value struct {
	kind Kind
	i    int64 // bool (0/1), int, timestamp micros, date days
	f    float64
	d    decimal.Decimal
	s    string
}

// Null returns the SQL NULL value.
func Null() Value { return Value{} }

// Bool returns a boolean value.
func Bool(b bool) Value {
	v := Value{kind: KindBool}
	if b {
		v.i = 1
	}
	return v
}

// Int returns an integer value (any of int2, int4, int8).
func Int(i int64) Value { return Value{kind: KindInt, i: i} }

// Float returns a float8 value.
func Float(f float64) Value { return Value{kind: KindFloat, f: f} }

// Decimal returns a numeric value. Scale is not significant: 1.0 equals 1.00.
func Decimal(d decimal.Decimal) Value { return Value{kind: KindDecimal, d: d} }

// Text returns a text value.
func Text(s string) Value { return Value{kind: KindText, s: s} }

// Timestamp returns a timestamp from microseconds since the Unix epoch (UTC).
func Timestamp(micros int64) Value { return Value{kind: KindTimestamp, i: micros} }

// TimestampFromTime converts a time.Time to a microsecond timestamp.
func TimestampFromTime(t time.Time) Value { return Timestamp(t.UnixMicro()) }

// Date returns a date from days since the Unix epoch.
func Date(days int64) Value { return Value{kind: KindDate, i: days} }

// Kind reports the runtime kind of v.
func (v Value) Kind() Kind { return v.kind }

// IsNull reports whether v is SQL NULL.
func (v Value) IsNull() bool { return v.kind == KindNull }

// AsBool returns the boolean payload. Only valid for KindBool.
func (v Value) AsBool() bool { return v.i != 0 }

// AsInt returns the integer payload. Valid for KindInt, KindTimestamp, KindDate.
func (v Value) AsInt() int64 { return v.i }

// AsFloat returns the float payload. Only valid for KindFloat.
func (v Value) AsFloat() float64 { return v.f }

// AsDecimal returns the numeric payload. KindInt is widened to a decimal.
func (v Value) AsDecimal() decimal.Decimal {
	if v.kind == KindInt {
		return decimal.NewFromInt(v.i)
	}
	return v.d
}

// AsText returns the text payload. Only valid for KindText.
func (v Value) AsText() string { return v.s }

// AsTime returns a timestamp value as a UTC time.Time.
func (v Value) AsTime() time.Time { return time.UnixMicro(v.i).UTC() }

// Compare orders two values. NULL sorts first. Int and decimal compare by
// numeric value; other mixed kinds compare by kind. Floats follow Postgres:
// NaN equals NaN and is greater than every other number, -0 equals 0.
func Compare(a, b Value) int {
	if a.kind != b.kind {
		if a.kind == KindInt && b.kind == KindDecimal || a.kind == KindDecimal && b.kind == KindInt {
			return a.AsDecimal().Cmp(b.AsDecimal())
		}
		return cmpInt(int64(a.kind), int64(b.kind))
	}
	switch a.kind {
	case KindNull:
		return 0
	case KindBool, KindInt, KindTimestamp, KindDate:
		return cmpInt(a.i, b.i)
	case KindFloat:
		return cmpFloat(a.f, b.f)
	case KindDecimal:
		return a.d.Cmp(b.d)
	case KindText:
		return strings.Compare(a.s, b.s)
	}
	return 0
}

// Equal reports whether two values are the same value (NULL equals NULL here;
// SQL's NULL = NULL being unknown is handled in package expr).
func Equal(a, b Value) bool { return Compare(a, b) == 0 }

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	an, bn := math.IsNaN(a), math.IsNaN(b)
	switch {
	case an && bn:
		return 0
	case an:
		return 1
	case bn:
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// String renders the value for logs and error messages.
func (v Value) String() string {
	switch v.kind {
	case KindNull:
		return "NULL"
	case KindBool:
		if v.AsBool() {
			return "true"
		}
		return "false"
	case KindInt:
		return strconv.FormatInt(v.i, 10)
	case KindFloat:
		return FormatFloat(v.f)
	case KindDecimal:
		return v.d.String()
	case KindText:
		return strconv.Quote(v.s)
	case KindTimestamp:
		return v.AsTime().Format(TimestampLayout)
	case KindDate:
		return time.Unix(v.i*86400, 0).UTC().Format(DateLayout)
	}
	return "?"
}

// TimestampLayout is how timestamps are rendered in output (UTC, RFC 3339).
const TimestampLayout = "2006-01-02T15:04:05.999999Z07:00"

// DateLayout is how dates are rendered in output.
const DateLayout = "2006-01-02"

// FormatFloat renders a float the way Postgres does by default: the shortest
// text that reads back to the same number, with NaN and Infinity spelled out.
func FormatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0"
		}
		return "0"
	}
	// Postgres prints the shortest digits in fixed notation when the decimal
	// exponent is in [-4, 15), and as d.ddde+XX otherwise.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp < -4 || exp >= 15 {
		return e
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Row is one tuple. Its position i holds column i of the producing operator.
type Row []Value

// String renders a row as (v1, v2, ...).
func (r Row) String() string {
	var b strings.Builder
	b.WriteByte('(')
	for i, v := range r {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(v.String())
	}
	b.WriteByte(')')
	return b.String()
}

// Concat returns a new row holding a followed by b. Used to build join output.
func Concat(a, b Row) Row {
	out := make(Row, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}
