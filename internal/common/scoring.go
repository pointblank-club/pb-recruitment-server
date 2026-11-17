package common

import "math"

// CalculateCodePartialScore calculates the score awarded for a code problem submission
// based on passed test cases, total test cases, and the problem's max score.
//
// Grading Scheme:
// - If passed <= total/2: 0 marks.
// - For every test case above total/2 (up to total-1):
//   each awards (50% / (total - total/2 - 1)) of maxScore.
// - If all total test cases are passed:
//   awards 100% of maxScore.
//
// Edge cases:
// - total <= 0 or passed <= 0: 0.0
// - total == 1: passed == 1 -> 100%, passed == 0 -> 0%
// - total == 2: passed == 2 -> 100%, passed <= 1 -> 0%
// - total == 3: passed == 3 -> 100%, passed == 2 -> 50%, passed <= 1 -> 0%
//
// The result is rounded to 2 decimal places.
func CalculateCodePartialScore(passed, total int, maxScore float64) float64 {
	if total <= 0 || passed <= 0 || maxScore <= 0 {
		return 0.0
	}
	if passed >= total {
		return RoundTo2Decimals(maxScore)
	}

	half := total / 2
	if passed <= half {
		return 0.0
	}

	intermediateSteps := total - half - 1
	if intermediateSteps <= 0 {
		return 0.0
	}

	passedAboveHalf := passed - half
	fraction := (float64(passedAboveHalf) / float64(intermediateSteps)) * 0.50
	score := fraction * maxScore
	return RoundTo2Decimals(score)
}

func RoundTo2Decimals(val float64) float64 {
	return math.Round(val*100.0) / 100.0
}
