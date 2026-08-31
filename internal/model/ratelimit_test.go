package model

import (
	"testing"
	"time"
)

func TestRateLimitBudgetValid(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name   string
		budget RateLimitBudget
		want   bool
	}{
		{"zero value", RateLimitBudget{}, false},
		{"negative remaining", RateLimitBudget{Remaining: -1, ResetsAt: reset}, false},
		{"zero remaining", RateLimitBudget{Remaining: 0, ResetsAt: reset}, true},
		{"positive remaining", RateLimitBudget{Remaining: 1, ResetsAt: reset}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.budget.Valid(); got != test.want {
				t.Errorf("Valid() = %v, want %v", got, test.want)
			}
		})
	}
}
