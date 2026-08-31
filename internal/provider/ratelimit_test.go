package provider

import (
	"sync"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestRateLimitBudgetCollectorChoosesConservativePairedObservation(t *testing.T) {
	firstReset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.FixedZone("UTC+2", 2*60*60))
	laterReset := time.Date(2030, time.January, 2, 3, 4, 6, 0, time.UTC)
	var collector RateLimitBudgetCollector

	collector.Observe(model.RateLimitBudget{Remaining: 12, ResetsAt: firstReset})
	collector.Observe(model.RateLimitBudget{Remaining: -1, ResetsAt: laterReset})
	collector.Observe(model.RateLimitBudget{Remaining: 15, ResetsAt: laterReset})
	collector.Observe(model.RateLimitBudget{Remaining: 12, ResetsAt: laterReset})

	got := collector.Budget()
	want := model.RateLimitBudget{Remaining: 12, ResetsAt: laterReset}
	if got != want {
		t.Errorf("Budget() = %+v, want %+v", got, want)
	}
}

func TestRateLimitBudgetCollectorPreservesValidZeroAndReturnsCopy(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	var collector RateLimitBudgetCollector
	collector.Observe(model.RateLimitBudget{Remaining: 0, ResetsAt: reset})

	got := collector.Budget()
	if got != (model.RateLimitBudget{Remaining: 0, ResetsAt: reset}) {
		t.Fatalf("Budget() = %+v, want valid zero", got)
	}
	got.Remaining = 99
	if after := collector.Budget(); after.Remaining != 0 {
		t.Errorf("Budget after mutating return = %+v, want stored value unchanged", after)
	}
}

func TestRateLimitBudgetCollectorSynchronizesObservations(t *testing.T) {
	var collector RateLimitBudgetCollector
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	var group sync.WaitGroup
	for remaining := 1; remaining <= 100; remaining++ {
		group.Add(1)
		go func(remaining int) {
			defer group.Done()
			collector.Observe(model.RateLimitBudget{Remaining: remaining, ResetsAt: reset.Add(time.Duration(remaining) * time.Second)})
		}(remaining)
	}
	group.Wait()

	want := model.RateLimitBudget{Remaining: 1, ResetsAt: reset.Add(time.Second)}
	if got := collector.Budget(); got != want {
		t.Errorf("Budget() = %+v, want %+v", got, want)
	}
}
