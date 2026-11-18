package types

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Type is a SQL column or expression type. The binder needs the exact type
// (int4 vs int8, numeric vs float8) to pick Postgres's result types and
// overflow rules; the runtime only needs the Kind.
type Type uint8

// Supported SQL types. TUnknown is the type of an untyped literal such as
// 'paid' or NULL before it is coerced to the other side of an operator.
const (
	TUnknown Type = iota
	TBool
	TInt2
	TInt4
	TInt8
	TFloat4
	TFloat8
	TNumeric
	TText
	TTimestamp
	TTimestamptz
	TDate
)

var typeNames = [...]string{"unknown", "boolean", "smallint", "integer", "bigint", "real",
	"double precision", "numeric", "text", "timestamp", "timestamptz", "date"}

func (t Type) String() string {
	if int(t) < len(typeNames) {
		return typeNames[t]
	}
	return fmt.Sprintf("type(%d)", t)
}

// Kind maps a SQL type to its runtime kind.
func (t Type) Kind() Kind {
	switch t {
	case TBool:
		return KindBool
	case TInt2, TInt4, TInt8:
		return KindInt
	case TFloat4, TFloat8:
		return KindFloat
	case TNumeric:
		return KindDecimal
	case TText, TUnknown:
		return KindText
	case TTimestamp, TTimestamptz:
		return KindTimestamp
	case TDate:
		return KindDate
	}
	return KindNull
}

// IsInteger reports whether t is int2, int4 or int8.
func (t Type) IsInteger() bool { return t == TInt2 || t == TInt4 || t == TInt8 }

// IsNumeric reports whether t is any number type.
func (t Type) IsNumeric() bool { return t.IsInteger() || t == TNumeric || t == TFloat4 || t == TFloat8 }

// Postgres type OIDs we decode.
const (
	OIDBool        = 16
	OIDInt8        = 20
	OIDInt2        = 21
	OIDInt4        = 23
	OIDText        = 25
	OIDFloat4      = 700
	OIDFloat8      = 701
	OIDVarchar     = 1043
	OIDDate        = 1082
	OIDTimestamp   = 1114
	OIDTimestamptz = 1184
	OIDNumeric     = 1700
)

// TypeFromOID maps a Postgres type OID to a Type. Unknown types are an error
// at the call site: guessing a conversion would silently corrupt views.
func TypeFromOID(oid uint32) (Type, bool) {
	switch oid {
	case OIDBool:
		return TBool, true
	case OIDInt2:
		return TInt2, true
	case OIDInt4:
		return TInt4, true
	case OIDInt8:
		return TInt8, true
	case OIDFloat4:
		return TFloat4, true
	case OIDFloat8:
		return TFloat8, true
	case OIDNumeric:
		return TNumeric, true
	case OIDText, OIDVarchar:
		return TText, true
	case OIDTimestamp:
		return TTimestamp, true
	case OIDTimestamptz:
		return TTimestamptz, true
	case OIDDate:
		return TDate, true
	}
	return TUnknown, false
}

var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999999Z07",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z07:00:00",
}

// ParseText converts Postgres's text output format for a column of type t
// into a Value. This is the format pgoutput sends and the snapshot reads.
func ParseText(t Type, s string) (Value, error) {
	switch t {
	case TBool:
		switch s {
		case "t":
			return Bool(true), nil
		case "f":
			return Bool(false), nil
		}
	case TInt2, TInt4, TInt8:
		i, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return Int(i), nil
		}
	case TFloat4, TFloat8:
		return parseFloat(s)
	case TNumeric:
		if s == "NaN" || strings.Contains(s, "Infinity") {
			return Value{}, fmt.Errorf("numeric %s is not supported", s)
		}
		d, err := decimal.NewFromString(s)
		if err == nil {
			return Decimal(d), nil
		}
	case TText:
		return Text(s), nil
	case TTimestamptz:
		if v, ok := parseInfinity(s); ok {
			return v, nil
		}
		for _, layout := range timestampLayouts {
			if tm, err := time.Parse(layout, s); err == nil {
				return TimestampFromTime(tm), nil
			}
		}
	case TTimestamp:
		if v, ok := parseInfinity(s); ok {
			return v, nil
		}
		if tm, err := time.Parse("2006-01-02 15:04:05.999999999", s); err == nil {
			return TimestampFromTime(tm), nil
		}
	case TDate:
		if tm, err := time.Parse(DateLayout, s); err == nil {
			return Date(tm.Unix() / 86400), nil
		}
	}
	return Value{}, fmt.Errorf("cannot parse %q as %s", s, t)
}

func parseFloat(s string) (Value, error) {
	switch s {
	case "NaN":
		return Float(math.NaN()), nil
	case "Infinity":
		return Float(math.Inf(1)), nil
	case "-Infinity":
		return Float(math.Inf(-1)), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return Value{}, fmt.Errorf("cannot parse %q as float: %w", s, err)
	}
	return Float(f), nil
}

func parseInfinity(s string) (Value, bool) {
	switch s {
	case "infinity":
		return Timestamp(math.MaxInt64), true
	case "-infinity":
		return Timestamp(math.MinInt64), true
	}
	return Value{}, false
}

// AppendJSON appends v as a JSON value. Decimals are written as JSON number
// literals with their exact digits, so no precision is lost to float64.
func AppendJSON(dst []byte, v Value) []byte {
	switch v.kind {
	case KindBool:
		return strconv.AppendBool(dst, v.AsBool())
	case KindInt:
		return strconv.AppendInt(dst, v.i, 10)
	case KindFloat:
		if math.IsNaN(v.f) || math.IsInf(v.f, 0) {
			return strconv.AppendQuote(dst, FormatFloat(v.f))
		}
		return strconv.AppendFloat(dst, v.f, 'g', -1, 64)
	case KindDecimal:
		return append(dst, v.d.String()...)
	case KindText:
		return appendJSONString(dst, v.s)
	case KindTimestamp:
		return strconv.AppendQuote(dst, v.AsTime().Format(TimestampLayout))
	case KindDate:
		return strconv.AppendQuote(dst, time.Unix(v.i*86400, 0).UTC().Format(DateLayout))
	}
	return append(dst, "null"...)
}

func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			dst = append(dst, '\\', c)
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}
