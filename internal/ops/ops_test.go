package ops

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

func I(i int64) types.Value  { return types.Int(i) }
func T(s string) types.Value { return types.Text(s) }
func D(s string) types.Value { return types.Decimal(decimal.RequireFromString(s)) }

var null = types.Null()

func z(pairs ...any) *zset.ZSet {
	out := zset.New()
	for i := 0; i < len(pairs); i += 2 {
		out.Add(pairs[i].(types.Row), int64(pairs[i+1].(int)))
	}
	return out
}

func R(vs ...types.Value) types.Row { return vs }

func col(i int, t types.Type) expr.Expr { return &expr.Col{Index: i, T: t} }

func step(t *testing.T, tx state.Tx, op Operator, in ...*zset.ZSet) *zset.ZSet {
	t.Helper()
	out, err := op.Step(tx, in)
	if err != nil {
		t.Fatalf("%s: %v", op.Name(), err)
	}
	return out
}

func expect(t *testing.T, got, want *zset.ZSet) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// orders rows: (id, customer_id, status, amount)
func TestFilterWorkedExample(t *testing.T) {
	f := &Filter{Pred: &expr.Cmp{Op: expr.OpEq, L: col(2, types.TText), R: &expr.Const{V: T("paid"), T: types.TText}}}
	in := z(R(I(2), I(7), T("paid"), D("50")), -1, R(I(2), I(7), T("paid"), D("70")), 1,
		R(I(3), I(7), T("pending"), D("5")), 1, R(I(4), I(7), null, D("5")), 1)
	want := z(R(I(2), I(7), T("paid"), D("50")), -1, R(I(2), I(7), T("paid"), D("70")), 1)
	expect(t, step(t, nil, f, in), want)
}

func TestProjectAddsCollidingRows(t *testing.T) {
	p := &Project{Exprs: []expr.Expr{col(2, types.TText)}}
	in := z(R(I(1), I(7), T("paid"), D("1")), 1, R(I(2), I(7), T("paid"), D("2")), 1, R(I(3), I(7), T("x"), D("2")), -1)
	expect(t, step(t, nil, p, in), z(R(T("paid")), 2, R(T("x")), -1))
}

func countSum() *Aggregate {
	return &Aggregate{ID: OpID("v", 1), GroupBy: []expr.Expr{col(0, types.TText)}, Aggs: []AggSpec{
		{Kind: CountStar, Out: types.TInt8},
		{Kind: Sum, Arg: col(1, types.TNumeric), Out: types.TNumeric},
	}}
}

func TestAggregateWorkedExample(t *testing.T) {
	// docs/concepts.md section 5: Delhi (2, 150), new order of 70.
	tx := state.NewMemory().Begin()
	a := countSum()
	step(t, tx, a, z(R(T("Delhi"), D("100")), 1, R(T("Delhi"), D("50")), 1))
	got := step(t, tx, a, z(R(T("Delhi"), D("70")), 1))
	expect(t, got, z(R(T("Delhi"), I(2), D("150")), -1, R(T("Delhi"), I(3), D("220")), 1))
	// Remove every row: only the -1 is emitted and the group is gone.
	got = step(t, tx, a, z(R(T("Delhi"), D("100")), -1, R(T("Delhi"), D("50")), -1, R(T("Delhi"), D("70")), -1))
	expect(t, got, z(R(T("Delhi"), I(3), D("220")), -1))
	kvs, _ := state.Collect(tx.Scan([]byte("a/")))
	if len(kvs) != 0 {
		t.Fatalf("empty group left state behind: %d keys", len(kvs))
	}
}

func TestAggregateMoveBetweenGroups(t *testing.T) {
	tx := state.NewMemory().Begin()
	a := countSum()
	step(t, tx, a, z(R(T("Delhi"), D("100")), 1))
	got := step(t, tx, a, z(R(T("Delhi"), D("100")), -1, R(T("Mumbai"), D("100")), 1))
	expect(t, got, z(R(T("Delhi"), I(1), D("100")), -1, R(T("Mumbai"), I(1), D("100")), 1))
}

func TestAggregateNullRules(t *testing.T) {
	tx := state.NewMemory().Begin()
	a := &Aggregate{ID: OpID("v", 1), GroupBy: []expr.Expr{col(0, types.TText)}, Aggs: []AggSpec{
		{Kind: CountStar, Out: types.TInt8},
		{Kind: Count, Arg: col(1, types.TNumeric), Out: types.TInt8},
		{Kind: Sum, Arg: col(1, types.TNumeric), Out: types.TNumeric},
		{Kind: Avg, Arg: col(1, types.TNumeric), Out: types.TNumeric},
		{Kind: Min, Arg: col(1, types.TNumeric), Out: types.TNumeric},
	}}
	// NULL group key and NULL amounts: one group, SUM of only NULLs is NULL.
	got := step(t, tx, a, z(R(null, null), 2))
	expect(t, got, z(R(null, I(2), I(0), null, null, null), 1))
	got = step(t, tx, a, z(R(null, D("10")), 1))
	expect(t, got, z(R(null, I(2), I(0), null, null, null), -1, R(null, I(3), I(1), D("10"), D("10"), D("10")), 1))
}

func TestGlobalAggregateAlwaysOneRow(t *testing.T) {
	tx := state.NewMemory().Begin()
	a := &Aggregate{ID: OpID("v", 1), Aggs: []AggSpec{
		{Kind: CountStar, Out: types.TInt8},
		{Kind: Sum, Arg: col(0, types.TNumeric), Out: types.TNumeric},
	}}
	init, err := a.Init(tx)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, init, z(R(I(0), null), 1))
	expect(t, step(t, tx, a, z(R(D("5")), 1)), z(R(I(0), null), -1, R(I(1), D("5")), 1))
	// Deleting the last row brings back the empty row instead of no row.
	expect(t, step(t, tx, a, z(R(D("5")), -1)), z(R(I(1), D("5")), -1, R(I(0), null), 1))
}

func TestAvgMatchesPostgresScale(t *testing.T) {
	tx := state.NewMemory().Begin()
	a := &Aggregate{ID: OpID("v", 1), Aggs: []AggSpec{{Kind: Avg, Arg: col(0, types.TNumeric), Out: types.TNumeric}}}
	got := step(t, tx, a, z(R(D("10")), 1, R(D("20")), 1, R(D("20.00")), 1))
	// avg(10, 20, 20) = 16.6666666666666667 in Postgres (16 significant digits).
	expect(t, got, z(R(null), -1, R(D("16.6666666666666667")), 1))
}

func TestMinMaxSurviveDeletes(t *testing.T) {
	tx := state.NewMemory().Begin()
	a := &Aggregate{ID: OpID("v", 1), GroupBy: []expr.Expr{col(0, types.TText)}, Aggs: []AggSpec{
		{Kind: Min, Arg: col(1, types.TNumeric), Out: types.TNumeric},
		{Kind: Max, Arg: col(1, types.TNumeric), Out: types.TNumeric},
	}}
	step(t, tx, a, z(R(T("g"), D("5")), 1, R(T("g"), D("1")), 2, R(T("g"), D("9")), 1))
	// Delete one of the two 1s: MIN stays 1.
	expect(t, step(t, tx, a, z(R(T("g"), D("1")), -1)), z())
	// Delete the other 1 and the 9: next smallest is 5, largest is 5.
	got := step(t, tx, a, z(R(T("g"), D("1")), -1, R(T("g"), D("9")), -1))
	expect(t, got, z(R(T("g"), D("1"), D("9")), -1, R(T("g"), D("5"), D("5")), 1))
}

func TestNegativeCountIsAnError(t *testing.T) {
	tx := state.NewMemory().Begin()
	_, err := countSum().Step(tx, []*zset.ZSet{z(R(T("x"), D("1")), -1)})
	if !errors.Is(err, ErrNegativeWeight) {
		t.Fatalf("want ErrNegativeWeight, got %v", err)
	}
}

// customers (id, city) joined with orders (id, cust, amount) on cust = id.
func custJoin() *Join {
	return &Join{ID: OpID("v", 2), LeftKeys: []expr.Expr{col(1, types.TInt8)}, RightKeys: []expr.Expr{col(0, types.TInt8)}}
}

func TestJoinWorkedExample(t *testing.T) {
	// docs/concepts.md section 6.
	tx := state.NewMemory().Begin()
	j := custJoin()
	step(t, tx, j, z(), z(R(I(7), T("Delhi")), 1))
	got := step(t, tx, j, z(R(I(1), I(7), D("100")), 1), z())
	expect(t, got, z(R(I(1), I(7), D("100"), I(7), T("Delhi")), 1))
	got = step(t, tx, j, z(), z(R(I(7), T("Delhi")), -1, R(I(7), T("Mumbai")), 1))
	expect(t, got, z(R(I(1), I(7), D("100"), I(7), T("Delhi")), -1, R(I(1), I(7), D("100"), I(7), T("Mumbai")), 1))
}

func TestJoinBothSidesInOneStep(t *testing.T) {
	// dA JOIN dB must be counted exactly once.
	tx := state.NewMemory().Begin()
	got := step(t, tx, custJoin(), z(R(I(1), I(7), D("100")), 1), z(R(I(7), T("Delhi")), 1))
	expect(t, got, z(R(I(1), I(7), D("100"), I(7), T("Delhi")), 1))
}

func TestJoinMultipliesWeightsAndSkipsNull(t *testing.T) {
	tx := state.NewMemory().Begin()
	j := custJoin()
	step(t, tx, j, z(), z(R(I(7), T("Delhi")), 2, R(null, T("Nowhere")), 1))
	got := step(t, tx, j, z(R(I(1), I(7), D("1")), 3, R(I(2), null, D("1")), 1), z())
	expect(t, got, z(R(I(1), I(7), D("1"), I(7), T("Delhi")), 6))
	kvs, _ := state.Collect(tx.Scan([]byte("j/")))
	if len(kvs) != 2 {
		t.Fatalf("NULL keys must not be stored; arrangements hold %d keys", len(kvs))
	}
}

func TestJoinInt4MatchesInt8(t *testing.T) {
	tx := state.NewMemory().Begin()
	j := &Join{ID: OpID("v", 2), LeftKeys: []expr.Expr{col(0, types.TInt4)}, RightKeys: []expr.Expr{col(0, types.TInt8)}}
	step(t, tx, j, z(R(I(5)), 1), z())
	expect(t, step(t, tx, j, z(), z(R(I(5)), 1)), z(R(I(5), I(5)), 1))
}

func TestDistinct(t *testing.T) {
	tx := state.NewMemory().Begin()
	d := &Distinct{ID: OpID("v", 3)}
	expect(t, step(t, tx, d, z(R(T("a")), 2)), z(R(T("a")), 1))
	expect(t, step(t, tx, d, z(R(T("a")), -1)), z())
	expect(t, step(t, tx, d, z(R(T("a")), -1)), z(R(T("a")), -1))
}
