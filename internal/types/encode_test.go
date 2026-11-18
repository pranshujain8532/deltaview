package types

import (
	"bytes"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/shopspring/decimal"
)

var quickCfg = &quick.Config{MaxCount: 5000}

// randValue draws a value biased toward edge cases: zero, negatives, NULL,
// empty strings, strings with NUL bytes, decimals with trailing zeros.
func randValue(r *rand.Rand) Value {
	switch r.Intn(8) {
	case 0:
		return Null()
	case 1:
		return Bool(r.Intn(2) == 0)
	case 2:
		switch r.Intn(4) {
		case 0:
			return Int(0)
		case 1:
			return Int(math.MinInt64 + r.Int63n(3))
		case 2:
			return Int(math.MaxInt64 - r.Int63n(3))
		}
		return Int(r.Int63n(2000) - 1000)
	case 3:
		switch r.Intn(6) {
		case 0:
			return Float(math.NaN())
		case 1:
			return Float(math.Inf(1 - 2*r.Intn(2)))
		case 2:
			return Float(math.Copysign(0, -1))
		}
		return Float((r.Float64() - 0.5) * math.Pow(10, float64(r.Intn(40)-20)))
	case 4:
		return Decimal(randDecimal(r))
	case 5:
		return Text(randString(r))
	case 6:
		return Timestamp(r.Int63() - r.Int63())
	}
	return Date(r.Int63n(200000) - 100000)
}

func randDecimal(r *rand.Rand) decimal.Decimal {
	coef := r.Int63n(2_000_000) - 1_000_000
	if r.Intn(5) == 0 {
		coef *= 1000 // trailing zeros in the coefficient
	}
	return decimal.New(coef, int32(r.Intn(16)-8))
}

func randString(r *rand.Rand) string {
	alphabet := []byte{0, 1, 'a', 'b', 'c', 0xff, 0xfe, '1', 'N', 'U', 'L'}
	b := make([]byte, r.Intn(6))
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}

func randRow(r *rand.Rand) Row {
	row := make(Row, r.Intn(5))
	for i := range row {
		row[i] = randValue(r)
	}
	return row
}

type qRow struct{ Row Row }

func (qRow) Generate(r *rand.Rand, _ int) reflect.Value {
	return reflect.ValueOf(qRow{randRow(r)})
}

type qVal struct{ V Value }

func (qVal) Generate(r *rand.Rand, _ int) reflect.Value {
	return reflect.ValueOf(qVal{randValue(r)})
}

func rowsEqual(a, b Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind() != b[i].Kind() || !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestRoundTrip(t *testing.T) {
	f := func(q qRow) bool {
		enc := Encode(q.Row)
		dec, err := Decode(enc)
		if err != nil {
			t.Logf("decode %v: %v", q.Row, err)
			return false
		}
		// decode(encode(row)) = row, and the re-encoding is identical (canonical).
		return rowsEqual(dec, q.Row) && bytes.Equal(Encode(dec), enc)
	}
	if err := quick.Check(f, quickCfg); err != nil {
		t.Fatal(err)
	}
}

func sign(i int) int {
	switch {
	case i < 0:
		return -1
	case i > 0:
		return 1
	}
	return 0
}

func TestOrderPreservingSameKind(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 50000; i++ {
		a := randValue(r)
		b := randValue(r)
		for b.Kind() != a.Kind() {
			b = randValue(r)
		}
		got := sign(bytes.Compare(AppendValue(nil, a), AppendValue(nil, b)))
		if want := Compare(a, b); got != want {
			t.Fatalf("order of %v vs %v: bytes say %d, values say %d", a, b, got, want)
		}
	}
}

func TestEqualValuesEncodeIdentically(t *testing.T) {
	cases := []struct{ a, b Value }{
		{Decimal(decimal.RequireFromString("1.0")), Decimal(decimal.RequireFromString("1.00"))},
		{Decimal(decimal.RequireFromString("150")), Decimal(decimal.New(15, 1))},
		{Decimal(decimal.RequireFromString("-0.500")), Decimal(decimal.RequireFromString("-0.5"))},
		{Decimal(decimal.RequireFromString("0.00")), Decimal(decimal.Zero)},
		{Float(0), Float(math.Copysign(0, -1))},
		{Float(math.NaN()), Float(-math.NaN())},
	}
	for _, c := range cases {
		if !bytes.Equal(AppendValue(nil, c.a), AppendValue(nil, c.b)) {
			t.Errorf("%v and %v are equal but encode differently", c.a, c.b)
		}
	}
}

func TestDistinctValuesEncodeDifferently(t *testing.T) {
	cases := []struct{ a, b Row }{
		{Row{Text("1")}, Row{Int(1)}},
		{Row{Null()}, Row{Text("NULL")}},
		{Row{Text("ab"), Text("c")}, Row{Text("a"), Text("bc")}},
		{Row{Text("a\x00")}, Row{Text("a")}},
		{Row{Text("")}, Row{}},
		{Row{Int(1)}, Row{Decimal(decimal.NewFromInt(1))}},
		{Row{Timestamp(5)}, Row{Int(5)}},
		{Row{Null(), Null()}, Row{Null()}},
	}
	for _, c := range cases {
		if bytes.Equal(Encode(c.a), Encode(c.b)) {
			t.Errorf("%v and %v must encode differently", c.a, c.b)
		}
	}
}

func TestSelfDelimiting(t *testing.T) {
	// Any concatenation of encoded values decodes back to the same sequence.
	f := func(a, b qRow) bool {
		enc := append(Encode(a.Row), Encode(b.Row)...)
		first, rest, err := DecodeN(enc, len(a.Row))
		if err != nil {
			return false
		}
		second, err := Decode(rest)
		return err == nil && rowsEqual(first, a.Row) && rowsEqual(second, b.Row)
	}
	if err := quick.Check(f, quickCfg); err != nil {
		t.Fatal(err)
	}
}

func TestDecimalOrderAcrossScales(t *testing.T) {
	vals := []string{"-1000", "-10.5", "-10.25", "-10.2", "-1", "-0.001", "0", "0.001",
		"0.01", "0.1", "0.11", "1", "1.000001", "9.99", "10", "10.2", "10.25", "100", "1e20"}
	for i := 1; i < len(vals); i++ {
		a := AppendValue(nil, Decimal(decimal.RequireFromString(vals[i-1])))
		b := AppendValue(nil, Decimal(decimal.RequireFromString(vals[i])))
		if bytes.Compare(a, b) >= 0 {
			t.Errorf("%s should encode below %s", vals[i-1], vals[i])
		}
	}
}

func TestParseText(t *testing.T) {
	cases := []struct {
		typ  Type
		in   string
		want Value
	}{
		{TInt4, "-42", Int(-42)},
		{TNumeric, "150.50", Decimal(decimal.RequireFromString("150.5"))},
		{TBool, "t", Bool(true)},
		{TText, "", Text("")},
		{TDate, "2026-11-18", Date(20775)},
		{TTimestamptz, "2026-11-18 10:00:00.5+00", Timestamp(1794996000500000)},
		{TTimestamptz, "2026-11-18 15:30:00.5+05:30", Timestamp(1794996000500000)},
		{TTimestamp, "2026-11-18 10:00:00.5", Timestamp(1794996000500000)},
		{TFloat8, "-Infinity", Float(math.Inf(-1))},
	}
	for _, c := range cases {
		got, err := ParseText(c.typ, c.in)
		if err != nil {
			t.Errorf("ParseText(%s, %q): %v", c.typ, c.in, err)
			continue
		}
		if got.Kind() != c.want.Kind() || !Equal(got, c.want) {
			t.Errorf("ParseText(%s, %q) = %v, want %v", c.typ, c.in, got, c.want)
		}
	}
	for _, bad := range []struct {
		typ Type
		in  string
	}{{TNumeric, "NaN"}, {TInt8, "1.5"}, {TBool, "yes"}, {TNumeric, "1.0e"}} {
		if _, err := ParseText(bad.typ, bad.in); err == nil {
			t.Errorf("ParseText(%s, %q) should fail", bad.typ, bad.in)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add(int64(0), 0.0, "", "0", true)
	f.Add(int64(-1), math.Inf(1), "a\x00b", "-12.3400", false)
	f.Add(int64(math.MaxInt64), -0.0, "\xff\x00\x01", "1e-30", true)
	f.Fuzz(func(t *testing.T, i int64, fl float64, s string, dec string, b bool) {
		row := Row{Int(i), Float(fl), Text(s), Bool(b), Timestamp(i), Date(i % 1000000), Null()}
		if d, err := decimal.NewFromString(dec); err == nil && d.Exponent() > -1000 && d.Exponent() < 1000 {
			row = append(row, Decimal(d))
		}
		enc := Encode(row)
		got, err := Decode(enc)
		if err != nil {
			t.Fatalf("decode %v: %v", row, err)
		}
		if !rowsEqual(got, row) || !bytes.Equal(Encode(got), enc) {
			t.Fatalf("round trip changed %v into %v", row, got)
		}
	})
}

func FuzzOrder(f *testing.F) {
	f.Add("1.5", "1.50", "a", "a\x00")
	f.Add("-3", "-30", "", "\x00")
	f.Fuzz(func(t *testing.T, d1, d2, s1, s2 string) {
		check := func(a, b Value) {
			got := sign(bytes.Compare(AppendValue(nil, a), AppendValue(nil, b)))
			if want := Compare(a, b); got != want {
				t.Fatalf("order of %v vs %v: bytes %d, values %d", a, b, got, want)
			}
		}
		check(Text(s1), Text(s2))
		a, err1 := decimal.NewFromString(d1)
		b, err2 := decimal.NewFromString(d2)
		if err1 == nil && err2 == nil && abs32(a.Exponent()) < 1000 && abs32(b.Exponent()) < 1000 {
			check(Decimal(a), Decimal(b))
		}
	})
}

func FuzzDecodeGarbage(f *testing.F) {
	f.Add([]byte{tagText, 'a', 0, 1})
	f.Add([]byte{tagDecPos, 0x80, 0, 0, 1, 2, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		// Must never panic. When it decodes, the result must re-decode the same.
		row, err := Decode(b)
		if err != nil {
			return
		}
		again, err := Decode(Encode(row))
		if err != nil || !rowsEqual(row, again) {
			t.Fatalf("decoded %v but re-encoding did not round trip", row)
		}
	})
}

func abs32(i int32) int32 {
	if i < 0 {
		return -i
	}
	return i
}
