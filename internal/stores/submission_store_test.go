package stores

import "testing"

func TestMCQAnswersMatch(t *testing.T) {
	tests := []struct {
		name    string
		choices []int64
		answer  []int64
		want    bool
	}{
		{name: "same order", choices: []int64{1, 3}, answer: []int64{1, 3}, want: true},
		{name: "different order", choices: []int64{3, 1}, answer: []int64{1, 3}, want: true},
		{name: "wrong choice", choices: []int64{1, 2}, answer: []int64{1, 3}, want: false},
		{name: "missing choice", choices: []int64{1}, answer: []int64{1, 3}, want: false},
		{name: "duplicate choice", choices: []int64{1, 1}, answer: []int64{1, 3}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcqAnswersMatch(tt.choices, tt.answer); got != tt.want {
				t.Fatalf("mcqAnswersMatch(%v, %v) = %v, want %v", tt.choices, tt.answer, got, tt.want)
			}
		})
	}
}
