package types

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/shopspring/decimal"
)

// Encoding tags. Each encoded value starts with one tag byte; tags sort in kind
// order, and the three decimal tags put negatives before zero before positives.
const (
	tagNull      byte = 0x01
	tagBool      byte = 0x02
	tagInt       byte = 0x03
	tagFloat     byte = 0x04
	tagDecNeg    byte = 0x05
	tagDecZero   byte = 0x06
	tagDecPos    byte = 0x07
	tagText      byte = 0x08
	tagTimestamp byte = 0x09
	tagDate      byte = 0x0a
)

// ErrCorrupt is returned when bytes are not a valid encoding.
var ErrCorrupt = errors.New("types: corrupt encoding")

// Encode returns the canonical, order-preserving encoding of a row.
//
// Canonical: equal rows give identical bytes (numeric 1.0 and 1.00 included).
// Order-preserving: for two values of the same kind, bytes.Compare of their
// encodings agrees with Compare. Self-delimiting: every value encoding marks
// its own end, so ("ab","c") and ("a","bc") never collide.
func Encode(r Row) []byte {
	return AppendRow(make([]byte, 0, 16*len(r)), r)
}

// AppendRow appends the encoding of r to dst.
func AppendRow(dst []byte, r Row) []byte {
	for _, v := range r {
		dst = AppendValue(dst, v)
	}
	return dst
}

// AppendValue appends the encoding of one value to dst.
func AppendValue(dst []byte, v Value) []byte {
	switch v.kind {
	case KindBool:
		return append(dst, tagBool, byte(v.i))
	case KindInt:
		return appendUint64(append(dst, tagInt), uint64(v.i)^(1<<63))
	case KindFloat:
		return appendUint64(append(dst, tagFloat), floatKey(v.f))
	case KindDecimal:
		return appendDecimal(dst, v.d)
	case KindText:
		return appendText(append(dst, tagText), v.s)
	case KindTimestamp:
		return appendUint64(append(dst, tagTimestamp), uint64(v.i)^(1<<63))
	case KindDate:
		return appendUint64(append(dst, tagDate), uint64(v.i)^(1<<63))
	default: // KindNull; kind is unexported, so no other kind can exist
		return append(dst, tagNull)
	}
}

func appendUint64(dst []byte, u uint64) []byte {
	return binary.BigEndian.AppendUint64(dst, u)
}

// floatKey maps a float to a uint64 whose unsigned order is the float order.
// -0 is folded into +0 and every NaN into one NaN that sorts above +Inf,
// matching Postgres float8 equality and ordering.
func floatKey(f float64) uint64 {
	if f == 0 {
		f = 0
	}
	if math.IsNaN(f) {
		return math.MaxUint64
	}
	bits := math.Float64bits(f)
	if bits&(1<<63) != 0 {
		return ^bits
	}
	return bits | 1<<63
}

func floatFromKey(u uint64) float64 {
	if u == math.MaxUint64 {
		return math.NaN()
	}
	if u&(1<<63) != 0 {
		return math.Float64frombits(u &^ (1 << 63))
	}
	return math.Float64frombits(^u)
}

// appendText escapes 0x00 as 0x00 0xFF and terminates with 0x00 0x01, so the
// terminator sorts below every continuation and the encoding is prefix-free.
func appendText(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 {
			dst = append(dst, 0x00, 0xff)
			continue
		}
		dst = append(dst, c)
	}
	return append(dst, 0x00, 0x01)
}

// decimalParts normalizes d to sign, significant digits (no leading or
// trailing zeros) and exponent e such that |d| = 0.DIGITS x 10^e.
func decimalParts(d decimal.Decimal) (sign int, digits string, e int64) {
	coef := d.Coefficient()
	sign = coef.Sign()
	if sign == 0 {
		return 0, "", 0
	}
	s := new(big.Int).Abs(coef).String()
	exp := int64(d.Exponent())
	trimmed := strings.TrimRight(s, "0")
	exp += int64(len(s) - len(trimmed))
	return sign, trimmed, int64(len(trimmed)) + exp
}

// appendDecimal writes: tag (by sign), adjusted exponent, digits, terminator.
// Comparing exponents first then digits gives numeric order for positives;
// negatives store everything inverted so larger magnitudes sort first.
func appendDecimal(dst []byte, d decimal.Decimal) []byte {
	sign, digits, e := decimalParts(d)
	if sign == 0 {
		return append(dst, tagDecZero)
	}
	ue := uint32(int32(e)) ^ (1 << 31)
	if sign > 0 {
		dst = append(dst, tagDecPos)
		dst = binary.BigEndian.AppendUint32(dst, ue)
		for i := 0; i < len(digits); i++ {
			dst = append(dst, digits[i]-'0'+1) // 1..10, never 0
		}
		return append(dst, 0x00)
	}
	dst = append(dst, tagDecNeg)
	dst = binary.BigEndian.AppendUint32(dst, ^ue)
	for i := 0; i < len(digits); i++ {
		dst = append(dst, 0xfe-(digits[i]-'0')) // 0xf5..0xfe, never 0xff
	}
	return append(dst, 0xff)
}

// Decode parses a whole encoded row.
func Decode(b []byte) (Row, error) {
	var r Row
	for len(b) > 0 {
		v, rest, err := DecodeValue(b)
		if err != nil {
			return nil, err
		}
		r = append(r, v)
		b = rest
	}
	return r, nil
}

// DecodeN parses exactly n values from the front of b and returns the rest.
// Composite storage keys use it to split a fixed-arity prefix from a row.
func DecodeN(b []byte, n int) (Row, []byte, error) {
	r := make(Row, 0, n)
	for i := 0; i < n; i++ {
		v, rest, err := DecodeValue(b)
		if err != nil {
			return nil, nil, err
		}
		r = append(r, v)
		b = rest
	}
	return r, b, nil
}

// SkipN returns b without its first n encoded values.
func SkipN(b []byte, n int) ([]byte, error) {
	_, rest, err := DecodeN(b, n)
	return rest, err
}

// DecodeValue parses one value from the front of b and returns the rest.
func DecodeValue(b []byte) (Value, []byte, error) {
	if len(b) == 0 {
		return Value{}, nil, ErrCorrupt
	}
	tag, b := b[0], b[1:]
	switch tag {
	case tagNull:
		return Null(), b, nil
	case tagBool:
		if len(b) < 1 || b[0] > 1 {
			return Value{}, nil, ErrCorrupt
		}
		return Bool(b[0] == 1), b[1:], nil
	case tagInt, tagTimestamp, tagDate:
		if len(b) < 8 {
			return Value{}, nil, ErrCorrupt
		}
		i := int64(binary.BigEndian.Uint64(b) ^ (1 << 63))
		v := Value{kind: KindInt, i: i}
		if tag == tagTimestamp {
			v.kind = KindTimestamp
		} else if tag == tagDate {
			v.kind = KindDate
		}
		return v, b[8:], nil
	case tagFloat:
		if len(b) < 8 {
			return Value{}, nil, ErrCorrupt
		}
		return Float(floatFromKey(binary.BigEndian.Uint64(b))), b[8:], nil
	case tagDecZero:
		return Decimal(decimal.Zero), b, nil
	case tagDecPos, tagDecNeg:
		return decodeDecimal(tag, b)
	case tagText:
		return decodeText(b)
	}
	return Value{}, nil, fmt.Errorf("%w: unknown tag 0x%02x", ErrCorrupt, tag)
}

func decodeDecimal(tag byte, b []byte) (Value, []byte, error) {
	if len(b) < 4 {
		return Value{}, nil, ErrCorrupt
	}
	ue := binary.BigEndian.Uint32(b)
	b = b[4:]
	neg := tag == tagDecNeg
	if neg {
		ue = ^ue
	}
	e := int64(int32(ue ^ (1 << 31)))
	var digits []byte
	for {
		if len(b) == 0 {
			return Value{}, nil, ErrCorrupt
		}
		c := b[0]
		b = b[1:]
		if !neg && c == 0x00 || neg && c == 0xff {
			break
		}
		var d byte
		if neg {
			if c < 0xf5 {
				return Value{}, nil, ErrCorrupt
			}
			d = 0xfe - c
		} else {
			if c < 1 || c > 10 {
				return Value{}, nil, ErrCorrupt
			}
			d = c - 1
		}
		digits = append(digits, '0'+d)
	}
	if len(digits) == 0 || digits[0] == '0' || digits[len(digits)-1] == '0' {
		return Value{}, nil, ErrCorrupt // not canonical
	}
	coef, ok := new(big.Int).SetString(string(digits), 10)
	if !ok {
		return Value{}, nil, ErrCorrupt
	}
	if neg {
		coef.Neg(coef)
	}
	exp := e - int64(len(digits))
	if exp < math.MinInt32 || exp > math.MaxInt32 {
		return Value{}, nil, ErrCorrupt
	}
	return Decimal(decimal.NewFromBigInt(coef, int32(exp))), b, nil
}

func decodeText(b []byte) (Value, []byte, error) {
	var sb []byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != 0x00 {
			sb = append(sb, c)
			continue
		}
		if i+1 >= len(b) {
			return Value{}, nil, ErrCorrupt
		}
		switch b[i+1] {
		case 0x01:
			return Text(string(sb)), b[i+2:], nil
		case 0xff:
			sb = append(sb, 0x00)
			i++
		default:
			return Value{}, nil, ErrCorrupt
		}
	}
	return Value{}, nil, ErrCorrupt
}
