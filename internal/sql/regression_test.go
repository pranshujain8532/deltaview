package sql

import (
	"errors"
	"strings"
	"testing"

	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/shopspring/decimal"
)

func evalConst(t *testing.T, q string) types.Value {
	t.Helper()
	e := plan(t, "SELECT "+q+" FROM orders").Root.(*Project).Exprs[0]
	v, err := e.Eval(make(types.Row, 5))
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestNumericModuloIsExact(t *testing.T) {
	// Div rounds to 16 places, which made trunc(x/y) = 1 and the remainder negative.
	got := evalConst(t, "0.99999999999999999 % 1")
	if !got.AsDecimal().Equal(decimal.RequireFromString("0.99999999999999999")) {
		t.Fatalf("got %v", got)
	}
}

func TestTimestampLiteralIgnoresOffset(t *testing.T) {
	// Postgres reads '...+05:30'::timestamp as 10:00 wall-clock time, offset ignored.
	got := evalConst(t, "'2026-01-01 10:00:00+05:30'::timestamp")
	if want := "2026-01-01T10:00:00Z"; got.String() != want {
		t.Fatalf("got %v want %s", got, want)
	}
	tz := evalConst(t, "'2026-01-01 10:00:00+05:30'::timestamptz")
	if want := "2026-01-01T04:30:00Z"; tz.String() != want {
		t.Fatalf("timestamptz: got %v want %s", tz, want)
	}
}

func TestAggregatesWithSamePrintedArgStaySeparate(t *testing.T) {
	p := plan(t, "SELECT SUM(2), SUM(2.0) FROM orders")
	agg := p.Root.(*Project).Input.(*Aggregate)
	if len(agg.Aggs) != 2 || agg.Aggs[0].Out != types.TInt8 || agg.Aggs[1].Out != types.TNumeric {
		t.Fatalf("sum(2) and sum(2.0) must be two aggregates: %+v", agg.Aggs)
	}
}

func TestRealIsRejected(t *testing.T) {
	def, err := ParseSelect("v", "SELECT amount::real FROM orders")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Bind(def, demoCatalog)
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "real") {
		t.Fatalf("want real rejected, got %v", err)
	}
	if _, ok := types.TypeFromOID(types.OIDFloat4); ok {
		t.Fatal("float4 columns must be unsupported")
	}
}

var _ = expr.IsTrue
