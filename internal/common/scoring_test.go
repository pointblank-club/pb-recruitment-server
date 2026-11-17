package common

import (
	"math"
	"testing"
)

func TestCalculateCodePartialScore(t *testing.T) {
	tests := []struct {
		name      string
		passed    int
		total     int
		maxScore  float64
		wantScore float64
	}{
		// Zero and invalid inputs
		{"zero total", 0, 0, 100, 0.0},
		{"negative passed", -1, 5, 100, 0.0},
		{"zero passed", 0, 10, 100, 0.0},
		{"zero max score", 5, 10, 0, 0.0},

		// N = 1
		{"N=1 passed 0", 0, 1, 100, 0.0},
		{"N=1 passed 1", 1, 1, 100, 100.0},

		// Edge case N = 2 (user specified)
		{"N=2 passed 0", 0, 2, 100, 0.0},
		{"N=2 passed 1", 1, 2, 100, 0.0},
		{"N=2 passed 2", 2, 2, 100, 100.0},

		// Edge case N = 3 (user specified)
		{"N=3 passed 0", 0, 3, 100, 0.0},
		{"N=3 passed 1", 1, 3, 100, 0.0},
		{"N=3 passed 2", 2, 3, 100, 50.0},
		{"N=3 passed 3", 3, 3, 100, 100.0},

		// N = 4
		{"N=4 passed 2", 2, 4, 100, 0.0},
		{"N=4 passed 3", 3, 4, 100, 50.0},
		{"N=4 passed 4", 4, 4, 100, 100.0},

		// N = 5
		{"N=5 passed 2", 2, 5, 100, 0.0},
		{"N=5 passed 3", 3, 5, 100, 25.0},
		{"N=5 passed 4", 4, 5, 100, 50.0},
		{"N=5 passed 5", 5, 5, 100, 100.0},

		// Standard N = 10 (user example) with maxScore = 100
		{"N=10 passed 0", 0, 10, 100, 0.0},
		{"N=10 passed 5", 5, 10, 100, 0.0},
		{"N=10 passed 6", 6, 10, 100, 12.5},
		{"N=10 passed 7", 7, 10, 100, 25.0},
		{"N=10 passed 8", 8, 10, 100, 37.5},
		{"N=10 passed 9", 9, 10, 100, 50.0},
		{"N=10 passed 10", 10, 10, 100, 100.0},

		// Standard N = 10 with maxScore = 10 (testing 2 decimal places)
		{"N=10 passed 5 score 10", 5, 10, 10, 0.0},
		{"N=10 passed 6 score 10", 6, 10, 10, 1.25},
		{"N=10 passed 7 score 10", 7, 10, 10, 2.5},
		{"N=10 passed 8 score 10", 8, 10, 10, 3.75},
		{"N=10 passed 9 score 10", 9, 10, 10, 5.0},
		{"N=10 passed 10 score 10", 10, 10, 10, 10.0},

		// Odd decimal rounding check (e.g., N=7, maxScore=33)
		// half = 3, intermediateSteps = 7 - 3 - 1 = 3
		// step = (50% / 3) * 33 = 5.5
		{"N=7 passed 4 score 33", 4, 7, 33, 5.5},
		{"N=7 passed 5 score 33", 5, 7, 33, 11.0},
		{"N=7 passed 6 score 33", 6, 7, 33, 16.5},
		{"N=7 passed 7 score 33", 7, 7, 33, 33.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCodePartialScore(tt.passed, tt.total, tt.maxScore)
			if math.Abs(got-tt.wantScore) > 0.001 {
				t.Errorf("CalculateCodePartialScore(%d, %d, %v) = %v; want %v",
					tt.passed, tt.total, tt.maxScore, got, tt.wantScore)
			}
		})
	}
}
