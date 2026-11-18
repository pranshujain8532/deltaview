package ops

import (
	"math"
	"testing"

	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
)

// Deleting an infinite input must restore the finite sum. With a plain float
// accumulator Inf + (-Inf) = NaN and the group stays NaN until it empties.
func TestFloatSumSurvivesInfinityDelete(t *testing.T) {
	tx := state.NewMemory().Begin()
	a := &Aggregate{ID: OpID("v", 1), Aggs: []AggSpec{
		{Kind: Sum, Arg: col(0, types.TFloat8), Out: types.TFloat8},
		{Kind: Avg, Arg: col(0, types.TFloat8), Out: types.TFloat8},
	}}
	F := types.Float
	step(t, tx, a, z(R(F(1)), 1))
	got := step(t, tx, a, z(R(F(math.Inf(1))), 1))
	expect(t, got, z(R(F(1), F(1)), -1, R(F(math.Inf(1)), F(math.Inf(1))), 1))
	got = step(t, tx, a, z(R(F(math.Inf(-1))), 1))
	expect(t, got, z(R(F(math.Inf(1)), F(math.Inf(1))), -1, R(F(math.NaN()), F(math.NaN())), 1))
	got = step(t, tx, a, z(R(F(math.Inf(1))), -1, R(F(math.Inf(-1))), -1))
	expect(t, got, z(R(F(math.NaN()), F(math.NaN())), -1, R(F(1), F(1)), 1))
}
