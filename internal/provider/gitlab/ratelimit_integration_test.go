package gitlab_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestResolveCollectsGitLabRateLimitBudgetFromSuccessfulResponses(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath: {{file: "issue.json", headers: map[string]string{
			"RateLimit-Remaining": "0", "RateLimit-Reset": "1893553445",
		}}},
		issueMRsPath: {{file: "closed_by_empty.json", headers: map[string]string{
			"RateLimit-Remaining": "11", "RateLimit-ResetTime": "Wed, 02 Jan 2030 04:04:05 GMT",
		}}},
	})
	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: issueRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := model.RateLimitBudget{Remaining: 0, ResetsAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	if snap.RateLimitBudget != want {
		t.Errorf("RateLimitBudget = %+v, want %+v", snap.RateLimitBudget, want)
	}
	if got := len(s.requests); got != 2 {
		t.Errorf("requests = %d, want unchanged 2", got)
	}
}

func TestFailedGitLabResponseDoesNotProduceBudget(t *testing.T) {
	s := newReplayServer(t, map[string][]response{
		issuePath: {{status: http.StatusInternalServerError, body: `{"message":"no"}`, headers: map[string]string{
			"RateLimit-Remaining": "0", "RateLimit-Reset": "1893553445",
		}}},
	})
	if _, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: issueRef}); err == nil {
		t.Fatal("Resolve succeeded with failing response")
	}
}
