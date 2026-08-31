package gitlab

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestObserveRateLimitUsesDocumentedHeadersOnly(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	date := reset.Format(http.TimeFormat)
	for _, test := range []struct {
		name    string
		headers map[string]string
		want    model.RateLimitBudget
	}{
		{"valid unix zero", map[string]string{"RateLimit-Remaining": "0", "RateLimit-Reset": "1893553445"}, model.RateLimitBudget{Remaining: 0, ResetsAt: reset}},
		{"http date fallback", map[string]string{"RateLimit-Remaining": "9", "RateLimit-Reset": "not-a-time", "RateLimit-ResetTime": date}, model.RateLimitBudget{Remaining: 9, ResetsAt: reset}},
		{"unix wins over fallback", map[string]string{"RateLimit-Remaining": "7", "RateLimit-Reset": "1893553445", "RateLimit-ResetTime": reset.Add(time.Hour).Format(http.TimeFormat)}, model.RateLimitBudget{Remaining: 7, ResetsAt: reset}},
		{"retry after is ignored", map[string]string{"RateLimit-Remaining": "1", "Retry-After": "120"}, model.RateLimitBudget{}},
		{"negative remaining", map[string]string{"RateLimit-Remaining": "-1", "RateLimit-Reset": "1893553445"}, model.RateLimitBudget{}},
		{"malformed remaining", map[string]string{"RateLimit-Remaining": "no", "RateLimit-Reset": "1893553445"}, model.RateLimitBudget{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header, len(test.headers))
			for key, value := range test.headers {
				header.Set(key, value)
			}
			collector := &provider.RateLimitBudgetCollector{}
			observeRateLimit(withRateLimitCollector(context.Background(), collector), header)
			if got := collector.Budget(); got != test.want {
				t.Errorf("Budget() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestObserveRateLimitRequiresResolveContext(t *testing.T) {
	observeRateLimit(context.Background(), http.Header{
		"RateLimit-Remaining": {"1"},
		"RateLimit-Reset":     {"1893553445"},
	})
}
