package provider

import (
	"sync"

	"github.com/niekcandaele/sitrep/internal/model"
)

// RateLimitBudgetCollector conservatively retains the tightest valid budget
// observed while one Resolve is in flight.
type RateLimitBudgetCollector struct {
	mu     sync.Mutex
	budget model.RateLimitBudget
}

// Observe records a valid budget when it is more conservative than the current
// value. Equal remaining capacity uses the later reset as the deterministic
// conservative tie-break.
func (c *RateLimitBudgetCollector) Observe(budget model.RateLimitBudget) {
	if !budget.Valid() {
		return
	}
	budget.ResetsAt = budget.ResetsAt.UTC()

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.budget.Valid() || budget.Remaining < c.budget.Remaining ||
		(budget.Remaining == c.budget.Remaining && budget.ResetsAt.After(c.budget.ResetsAt)) {
		c.budget = budget
	}
}

// Budget returns the collected budget by value.
func (c *RateLimitBudgetCollector) Budget() model.RateLimitBudget {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.budget
}
