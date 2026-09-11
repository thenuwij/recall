package scheduling

import (
	"math"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.September, 11, 9, 0, 0, 0, time.UTC)

func sameState(got, want State) bool {
	return got.Repetitions == want.Repetitions &&
		got.IntervalDays == want.IntervalDays &&
		got.Lapses == want.Lapses &&
		math.Abs(got.EaseFactor-want.EaseFactor) < 1e-9
}

func TestNextFollowsHandComputedSequence(t *testing.T) {
	steps := []struct {
		score int
		want  State
	}{
		{score: 5, want: State{Repetitions: 1, IntervalDays: 1, EaseFactor: 2.6, Lapses: 0}},
		{score: 4, want: State{Repetitions: 2, IntervalDays: 6, EaseFactor: 2.6, Lapses: 0}},
		{score: 3, want: State{Repetitions: 3, IntervalDays: 16, EaseFactor: 2.46, Lapses: 0}},
		{score: 2, want: State{Repetitions: 0, IntervalDays: 1, EaseFactor: 2.14, Lapses: 1}},
		{score: 4, want: State{Repetitions: 1, IntervalDays: 1, EaseFactor: 2.14, Lapses: 1}},
		{score: 5, want: State{Repetitions: 2, IntervalDays: 6, EaseFactor: 2.24, Lapses: 1}},
		{score: 5, want: State{Repetitions: 3, IntervalDays: 13, EaseFactor: 2.34, Lapses: 1}},
		{score: 4, want: State{Repetitions: 4, IntervalDays: 30, EaseFactor: 2.34, Lapses: 1}},
	}

	state := State{EaseFactor: 2.5}
	now := testNow
	for i, step := range steps {
		next, due := Next(state, step.score, now)
		if !sameState(next, step.want) {
			t.Fatalf("review %d (score %d): state = %+v, want %+v", i+1, step.score, next, step.want)
		}
		wantDue := now.AddDate(0, 0, step.want.IntervalDays)
		if !due.Equal(wantDue) {
			t.Fatalf("review %d: due = %v, want %v", i+1, due, wantDue)
		}
		state = next
		now = due
	}
}

func TestNextScoreBoundaries(t *testing.T) {
	start := State{Repetitions: 3, IntervalDays: 10, EaseFactor: 2.5, Lapses: 0}

	tests := []struct {
		name  string
		score int
		want  State
	}{
		{name: "score 0 resets", score: 0, want: State{Repetitions: 0, IntervalDays: 1, EaseFactor: 1.7, Lapses: 1}},
		{name: "score 2 resets", score: 2, want: State{Repetitions: 0, IntervalDays: 1, EaseFactor: 2.18, Lapses: 1}},
		{name: "score 3 passes", score: 3, want: State{Repetitions: 4, IntervalDays: 25, EaseFactor: 2.36, Lapses: 0}},
		{name: "score 5 passes", score: 5, want: State{Repetitions: 4, IntervalDays: 25, EaseFactor: 2.6, Lapses: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, due := Next(start, tt.score, testNow)
			if !sameState(next, tt.want) {
				t.Fatalf("state = %+v, want %+v", next, tt.want)
			}
			wantDue := testNow.AddDate(0, 0, tt.want.IntervalDays)
			if !due.Equal(wantDue) {
				t.Fatalf("due = %v, want %v", due, wantDue)
			}
		})
	}
}

func TestNextFirstReview(t *testing.T) {
	tests := []struct {
		name  string
		score int
		want  State
	}{
		{name: "pass", score: 4, want: State{Repetitions: 1, IntervalDays: 1, EaseFactor: 2.5, Lapses: 0}},
		{name: "fail", score: 1, want: State{Repetitions: 0, IntervalDays: 1, EaseFactor: 1.96, Lapses: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, due := Next(State{EaseFactor: 2.5}, tt.score, testNow)
			if !sameState(next, tt.want) {
				t.Fatalf("state = %+v, want %+v", next, tt.want)
			}
			if !due.Equal(testNow.AddDate(0, 0, 1)) {
				t.Fatalf("due = %v, want one day after %v", due, testNow)
			}
		})
	}
}

func TestNextEaseFactorFloor(t *testing.T) {
	state := State{EaseFactor: 2.5}
	for i := 0; i < 10; i++ {
		state, _ = Next(state, 0, testNow)
		if state.EaseFactor < minEaseFactor {
			t.Fatalf("review %d: ease factor = %v, below %v", i+1, state.EaseFactor, minEaseFactor)
		}
	}
	if state.EaseFactor != minEaseFactor {
		t.Fatalf("ease factor = %v, want %v", state.EaseFactor, minEaseFactor)
	}
	if state.Lapses != 10 {
		t.Fatalf("lapses = %d, want 10", state.Lapses)
	}
}

func TestNextCapsInterval(t *testing.T) {
	tests := []struct {
		name  string
		start State
	}{
		{name: "growth past the cap", start: State{Repetitions: 5, IntervalDays: 200, EaseFactor: 2.5}},
		{name: "already at the cap", start: State{Repetitions: 6, IntervalDays: 365, EaseFactor: 1.3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, due := Next(tt.start, 5, testNow)
			if next.IntervalDays != maxIntervalDays {
				t.Fatalf("interval = %d, want %d", next.IntervalDays, maxIntervalDays)
			}
			if !due.Equal(testNow.AddDate(0, 0, maxIntervalDays)) {
				t.Fatalf("due = %v, want %d days after %v", due, maxIntervalDays, testNow)
			}
		})
	}
}
