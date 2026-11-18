package zset

import (
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/shopspring/decimal"
)

var quickCfg = &quick.Config{MaxCount: 3000}

// qZSet generates small Z-sets over a tiny row universe so that rows collide
// and weights cancel often, which is where Z-set bugs live.
type qZSet struct{ Z *ZSet }

func (qZSet) Generate(r *rand.Rand, _ int) reflect.Value {
	z := New()
	for i := r.Intn(8); i > 0; i-- {
		z.Add(smallRow(r), int64(r.Intn(7)-3))
	}
	return reflect.ValueOf(qZSet{z})
}

func smallRow(r *rand.Rand) types.Row {
	vals := []types.Value{
		types.Null(), types.Int(1), types.Int(2), types.Text("1"), types.Text("NULL"),
		types.Decimal(decimal.RequireFromString("1.0")), types.Decimal(decimal.RequireFromString("1.00")),
	}
	row := make(types.Row, 1+r.Intn(2))
	for i := range row {
		row[i] = vals[r.Intn(len(vals))]
	}
	return row
}

func sum(zs ...*ZSet) *ZSet {
	out := New()
	for _, z := range zs {
		out.Merge(z)
	}
	return out
}

func noZeroWeights(z *ZSet) bool {
	for _, e := range z.Entries() {
		if e.Weight == 0 {
			return false
		}
	}
	return true
}

func TestInverse(t *testing.T) {
	// a + (-a) = empty
	f := func(a qZSet) bool { return sum(a.Z, a.Z.Negate()).IsEmpty() }
	if err := quick.Check(f, quickCfg); err != nil {
		t.Fatal(err)
	}
}

func TestCommutative(t *testing.T) {
	f := func(a, b qZSet) bool { return sum(a.Z, b.Z).Equal(sum(b.Z, a.Z)) }
	if err := quick.Check(f, quickCfg); err != nil {
		t.Fatal(err)
	}
}

func TestAssociative(t *testing.T) {
	f := func(a, b, c qZSet) bool {
		left := sum(sum(a.Z, b.Z), c.Z)
		right := sum(a.Z, sum(b.Z, c.Z))
		return left.Equal(right)
	}
	if err := quick.Check(f, quickCfg); err != nil {
		t.Fatal(err)
	}
}

func TestNoZeroWeightStored(t *testing.T) {
	f := func(a, b qZSet) bool {
		s := sum(a.Z, b.Z)
		return noZeroWeights(a.Z) && noZeroWeights(s) && noZeroWeights(s.Negate())
	}
	if err := quick.Check(f, quickCfg); err != nil {
		t.Fatal(err)
	}
}

func TestEqualValuesShareAnEntry(t *testing.T) {
	z := New()
	z.Add(types.Row{types.Decimal(decimal.RequireFromString("1.0"))}, 1)
	z.Add(types.Row{types.Decimal(decimal.RequireFromString("1.00"))}, -1)
	if !z.IsEmpty() {
		t.Fatalf("1.0 and 1.00 must cancel, got %v", z)
	}
	z.Add(types.Row{types.Text("1")}, 1)
	z.Add(types.Row{types.Int(1)}, 1)
	if z.Len() != 2 {
		t.Fatalf("text '1' and int 1 must stay apart, got %v", z)
	}
}

func TestWorkedExample(t *testing.T) {
	// docs/concepts.md section 2
	row := func(id int64, status string, amount int64) types.Row {
		return types.Row{types.Int(id), types.Text(status), types.Decimal(decimal.NewFromInt(amount))}
	}
	orders := New()
	orders.Add(row(1, "paid", 100), 1)
	orders.Add(row(2, "paid", 50), 1)
	change := New()
	change.Add(row(2, "paid", 50), -1)
	change.Add(row(2, "paid", 70), 1)
	orders.Merge(change)
	want := New()
	want.Add(row(1, "paid", 100), 1)
	want.Add(row(2, "paid", 70), 1)
	if !orders.Equal(want) {
		t.Fatalf("got %v want %v", orders, want)
	}
}

func TestEntriesSortedByKey(t *testing.T) {
	z := New()
	for _, i := range []int64{5, -3, 9, 0, 2} {
		z.Add(types.Row{types.Int(i)}, 1)
	}
	var prev int64 = -1 << 62
	for _, e := range z.Entries() {
		if e.Row[0].AsInt() < prev {
			t.Fatalf("entries out of order: %v", z)
		}
		prev = e.Row[0].AsInt()
	}
}

func TestNilIsEmpty(t *testing.T) {
	var z *ZSet
	if z.Len() != 0 || !z.IsEmpty() || z.Entries() != nil || !z.Negate().IsEmpty() {
		t.Fatal("nil Z-set must behave as empty")
	}
}
