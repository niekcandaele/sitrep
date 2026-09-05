package github

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

type rateLimitNode struct {
	Remaining json.RawMessage `json:"remaining"`
	ResetAt   json.RawMessage `json:"resetAt"`
}

func observeRateLimit(ctx context.Context, node rateLimitNode) {
	collector := provider.RateLimitCollectorFromContext(ctx)
	if collector == nil {
		return
	}
	budget, ok := node.budget()
	if ok {
		collector.Observe(budget)
	}
}

func (n rateLimitNode) budget() (model.RateLimitBudget, bool) {
	remaining, ok := jsonInteger(n.Remaining)
	if !ok || remaining < 0 {
		return model.RateLimitBudget{}, false
	}
	var reset string
	if len(n.ResetAt) == 0 || json.Unmarshal(n.ResetAt, &reset) != nil || strings.TrimSpace(reset) == "" {
		return model.RateLimitBudget{}, false
	}
	resetsAt, err := time.Parse(time.RFC3339, reset)
	if err != nil || resetsAt.IsZero() {
		return model.RateLimitBudget{}, false
	}
	return model.RateLimitBudget{Remaining: remaining, ResetsAt: resetsAt.UTC()}, true
}

func jsonInteger(raw json.RawMessage) (int, bool) {
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 0)
	if err != nil {
		return 0, false
	}
	return int(parsed), true
}
