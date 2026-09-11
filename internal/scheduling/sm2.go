package scheduling

import (
	"math"
	"time"
)

const (
	minEaseFactor   = 1.3
	maxIntervalDays = 365
	passingScore    = 3
)

type State struct {
	Repetitions  int
	IntervalDays int
	EaseFactor   float64
	Lapses       int
}

func Next(s State, score int, now time.Time) (State, time.Time) {
	next := s

	if score < passingScore {
		next.Repetitions = 0
		next.IntervalDays = 1
		next.Lapses++
	} else {
		switch s.Repetitions {
		case 0:
			next.IntervalDays = 1
		case 1:
			next.IntervalDays = 6
		default:
			next.IntervalDays = int(math.Round(float64(s.IntervalDays) * s.EaseFactor))
		}
		next.Repetitions++
	}

	if next.IntervalDays > maxIntervalDays {
		next.IntervalDays = maxIntervalDays
	}

	q := float64(5 - score)
	next.EaseFactor = s.EaseFactor + 0.1 - q*(0.08+q*0.02)
	if next.EaseFactor < minEaseFactor {
		next.EaseFactor = minEaseFactor
	}

	return next, now.AddDate(0, 0, next.IntervalDays)
}
