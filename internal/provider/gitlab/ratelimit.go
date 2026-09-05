package gitlab

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

func observeRateLimit(ctx context.Context, header http.Header) {
	collector := provider.RateLimitCollectorFromContext(ctx)
	if collector == nil {
		return
	}
	remaining, err := strconv.ParseInt(strings.TrimSpace(header.Get("RateLimit-Remaining")), 10, 0)
	if err != nil || remaining < 0 {
		return
	}
	resetsAt, ok := rateLimitReset(header)
	if !ok {
		return
	}
	collector.Observe(model.RateLimitBudget{Remaining: int(remaining), ResetsAt: resetsAt.UTC()})
}

func rateLimitReset(header http.Header) (time.Time, bool) {
	if value := strings.TrimSpace(header.Get("RateLimit-Reset")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).UTC(), true
		}
	}
	if value := strings.TrimSpace(header.Get("RateLimit-ResetTime")); value != "" {
		if reset, err := http.ParseTime(value); err == nil && !reset.IsZero() {
			return reset.UTC(), true
		}
	}
	return time.Time{}, false
}
