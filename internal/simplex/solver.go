package simplex

import (
	"fmt"
	"math"
)

const (
	epsilon = 1e-10
)

type LPProblem struct {
	Objective []float64
	ConstraintMatrix [][]float64
	ConstraintRHS    []float64
	ConstraintTypes  []int
	NumVars     int
	NumConstraints int
}

const (
	LTE = 0
	GTE = 1
	EQ  = 2
)

type LPSolution struct {
	Feasible      bool
	Optimal       bool
	ObjectiveValue float64
	Variables     []float64
	DualPrices    []float64
	Iterations    int
	Unbounded     bool
}

func NewLPProblem(numVars int) *LPProblem {
	return &LPProblem{
		Objective:        make([]float64, numVars),
		ConstraintMatrix: nil,
		ConstraintRHS:    nil,
		ConstraintTypes:  nil,
		NumVars:          numVars,
	}
}

func (lp *LPProblem) SetObjective(coeffs []float64) {
	copy(lp.Objective, coeffs)
}

func (lp *LPProblem) AddConstraint(coeffs []float64, constraintType int, rhs float64) {
	row := make([]float64, lp.NumVars)
	copy(row, coeffs)
	lp.ConstraintMatrix = append(lp.ConstraintMatrix, row)
	lp.ConstraintRHS = append(lp.ConstraintRHS, rhs)
	lp.ConstraintTypes = append(lp.ConstraintTypes, constraintType)
	lp.NumConstraints++
}

func (lp *LPProblem) Solve() *LPSolution {
	numSlacks := 0
	for _, ct := range lp.ConstraintTypes {
		if ct == LTE {
			numSlacks++
		} else if ct == GTE {
			numSlacks++
		} else {
			numSlacks++
		}
	}

	totalVars := lp.NumVars + numSlacks

	tableau := make([][]float64, lp.NumConstraints+1)
	for i := range tableau {
		tableau[i] = make([]float64, totalVars+1)
	}

	slackIdx := lp.NumVars
	for i := 0; i < lp.NumConstraints; i++ {
		for j := 0; j < lp.NumVars; j++ {
			tableau[i][j] = lp.ConstraintMatrix[i][j]
		}

		switch lp.ConstraintTypes[i] {
		case LTE:
			tableau[i][slackIdx] = 1
			slackIdx++
		case GTE:
			tableau[i][slackIdx] = -1
			slackIdx++
		case EQ:
			tableau[i][slackIdx] = 1
			slackIdx++
		}

		if lp.ConstraintRHS[i] < 0 {
			for j := 0; j <= totalVars; j++ {
				tableau[i][j] = -tableau[i][j]
			}
		}

		tableau[i][totalVars] = math.Abs(lp.ConstraintRHS[i])
	}

	for j := 0; j < lp.NumVars; j++ {
		tableau[lp.NumConstraints][j] = -lp.Objective[j]
	}

	basis := make([]int, lp.NumConstraints)
	slackIdx2 := lp.NumVars
	for i := 0; i < lp.NumConstraints; i++ {
		switch lp.ConstraintTypes[i] {
		case LTE:
			basis[i] = slackIdx2
			slackIdx2++
		case GTE:
			basis[i] = slackIdx2
			slackIdx2++
		case EQ:
			basis[i] = slackIdx2
			slackIdx2++
		}
	}

	maxIter := 10 * totalVars
	iterations := 0

	for iterations < maxIter {
		pivotCol := -1
		minVal := -epsilon
		for j := 0; j < totalVars; j++ {
			if tableau[lp.NumConstraints][j] < minVal {
				minVal = tableau[lp.NumConstraints][j]
				pivotCol = j
			}
		}

		if pivotCol == -1 {
			solution := &LPSolution{
				Feasible:      true,
				Optimal:       true,
				ObjectiveValue: tableau[lp.NumConstraints][totalVars],
				Variables:     make([]float64, lp.NumVars),
				DualPrices:    make([]float64, lp.NumConstraints),
				Iterations:    iterations,
			}

			for i := 0; i < lp.NumConstraints; i++ {
				if basis[i] < lp.NumVars {
					solution.Variables[basis[i]] = tableau[i][totalVars]
				}
			}

			for i := 0; i < lp.NumConstraints; i++ {
				solution.DualPrices[i] = tableau[lp.NumConstraints][lp.NumVars+i]
			}

			return solution
		}

		pivotRow := -1
		minRatio := math.Inf(1)
		for i := 0; i < lp.NumConstraints; i++ {
			if tableau[i][pivotCol] > epsilon {
				ratio := tableau[i][totalVars] / tableau[i][pivotCol]
				if ratio < minRatio {
					minRatio = ratio
					pivotRow = i
				}
			}
		}

		if pivotRow == -1 {
			return &LPSolution{
				Feasible:   false,
				Optimal:    false,
				Unbounded:  true,
				Iterations: iterations,
			}
		}

		pivotVal := tableau[pivotRow][pivotCol]
		for j := 0; j <= totalVars; j++ {
			tableau[pivotRow][j] /= pivotVal
		}

		for i := 0; i <= lp.NumConstraints; i++ {
			if i == pivotRow {
				continue
			}
			factor := tableau[i][pivotCol]
			if math.Abs(factor) < epsilon {
				continue
			}
			for j := 0; j <= totalVars; j++ {
				tableau[i][j] -= factor * tableau[pivotRow][j]
			}
		}

		basis[pivotRow] = pivotCol
		iterations++
	}

	return &LPSolution{
		Feasible:   false,
		Optimal:    false,
		Iterations: iterations,
	}
}

func SolveMaximize(objective []float64, constraintMatrix [][]float64, constraintRHS []float64, constraintTypes []int) *LPSolution {
	n := len(objective)
	lp := NewLPProblem(n)
	lp.SetObjective(objective)

	for i := 0; i < len(constraintMatrix); i++ {
		lp.AddConstraint(constraintMatrix[i], constraintTypes[i], constraintRHS[i])
	}

	return lp.Solve()
}

type LPBuilder struct {
	numVars int
	obj     []float64
	rows    [][]float64
	rhs     []float64
	types   []int
}

func NewBuilder(numVars int) *LPBuilder {
	return &LPBuilder{
		numVars: numVars,
		obj:     make([]float64, numVars),
	}
}

func (b *LPBuilder) Maximize(coeffs []float64) *LPBuilder {
	copy(b.obj, coeffs)
	return b
}

func (b *LPBuilder) AddRow(coeffs []float64, ctype int, rhs float64) *LPBuilder {
	row := make([]float64, b.numVars)
	copy(row, coeffs)
	b.rows = append(b.rows, row)
	b.rhs = append(b.rhs, rhs)
	b.types = append(b.types, ctype)
	return b
}

func (b *LPBuilder) Build() *LPProblem {
	lp := NewLPProblem(b.numVars)
	lp.SetObjective(b.obj)
	for i := range b.rows {
		lp.AddConstraint(b.rows[i], b.types[i], b.rhs[i])
	}
	return lp
}

func (b *LPBuilder) Solve() *LPSolution {
	return b.Build().Solve()
}

func (s *LPSolution) String() string {
	if s.Unbounded {
		return "LP Solution: UNBOUNDED"
	}
	if !s.Feasible {
		return fmt.Sprintf("LP Solution: INFEASIBLE (iterations=%d)", s.Iterations)
	}
	return fmt.Sprintf("LP Solution: optimal=%v, obj=%.4f, vars=%v, iterations=%d",
		s.Optimal, s.ObjectiveValue, s.Variables, s.Iterations)
}
