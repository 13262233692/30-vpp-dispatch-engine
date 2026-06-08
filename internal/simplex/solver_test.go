package simplex

import (
	"math"
	"testing"
)

func TestSimpleMaximization(t *testing.T) {
	objective := []float64{3, 2}
	constraints := [][]float64{
		{1, 1},
		{2, 1},
	}
	rhs := []float64{4, 6}
	types := []int{LTE, LTE}

	sol := SolveMaximize(objective, constraints, rhs, types)

	if !sol.Feasible || !sol.Optimal {
		t.Fatalf("expected feasible optimal solution, got feasible=%v optimal=%v", sol.Feasible, sol.Optimal)
	}

	if math.Abs(sol.ObjectiveValue-10) > 0.01 {
		t.Errorf("expected objective=10, got %.4f", sol.ObjectiveValue)
	}

	t.Logf("x1=%.4f, x2=%.4f, obj=%.4f", sol.Variables[0], sol.Variables[1], sol.ObjectiveValue)
}

func TestUnbounded(t *testing.T) {
	objective := []float64{1, 1}
	constraints := [][]float64{
		{-1, 1},
	}
	rhs := []float64{1}
	types := []int{LTE}

	sol := SolveMaximize(objective, constraints, rhs, types)

	if !sol.Unbounded {
		t.Error("expected unbounded solution")
	}
}

func TestBuilderPattern(t *testing.T) {
	sol := NewBuilder(2).
		Maximize([]float64{5, 4}).
		AddRow([]float64{6, 4}, LTE, 24).
		AddRow([]float64{1, 2}, LTE, 6).
		Solve()

	if !sol.Feasible || !sol.Optimal {
		t.Fatalf("expected feasible optimal, got feasible=%v optimal=%v", sol.Feasible, sol.Optimal)
	}

	if sol.ObjectiveValue < 19 {
		t.Errorf("expected objective >= 19, got %.4f", sol.ObjectiveValue)
	}

	t.Logf("obj=%.4f, x1=%.4f, x2=%.4f, iters=%d", sol.ObjectiveValue, sol.Variables[0], sol.Variables[1], sol.Iterations)
}

func TestGTEConstraint(t *testing.T) {
	sol := NewBuilder(2).
		Maximize([]float64{2, 3}).
		AddRow([]float64{1, 1}, LTE, 10).
		AddRow([]float64{1, 0}, GTE, 2).
		Solve()

	if !sol.Feasible {
		t.Error("expected feasible solution")
	}

	t.Logf("obj=%.4f, x1=%.4f, x2=%.4f", sol.ObjectiveValue, sol.Variables[0], sol.Variables[1])
}

func TestThreeVariableProblem(t *testing.T) {
	sol := NewBuilder(3).
		Maximize([]float64{3, 5, 4}).
		AddRow([]float64{2, 3, 1}, LTE, 18).
		AddRow([]float64{1, 2, 3}, LTE, 12).
		AddRow([]float64{1, 0, 0}, LTE, 5).
		Solve()

	if !sol.Feasible || !sol.Optimal {
		t.Fatalf("expected feasible optimal, got feasible=%v optimal=%v", sol.Feasible, sol.Optimal)
	}

	t.Logf("obj=%.4f, x1=%.4f, x2=%.4f, x3=%.4f, iters=%d",
		sol.ObjectiveValue, sol.Variables[0], sol.Variables[1], sol.Variables[2], sol.Iterations)
}

func TestDegenerateSolution(t *testing.T) {
	sol := NewBuilder(2).
		Maximize([]float64{1, 1}).
		AddRow([]float64{1, 0}, LTE, 0).
		AddRow([]float64{0, 1}, LTE, 5).
		AddRow([]float64{1, 1}, LTE, 5).
		Solve()

	if !sol.Feasible {
		t.Error("expected feasible solution for degenerate problem")
	}

	if math.Abs(sol.ObjectiveValue-5) > 0.01 {
		t.Errorf("expected objective=5, got %.4f", sol.ObjectiveValue)
	}
}
