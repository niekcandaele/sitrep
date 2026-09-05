package github_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestResolveCollectsValidGitHubRateLimitBudget(t *testing.T) {
	s := newReplayServer(t, response{file: "ticket_with_parent_ratelimit.json"})
	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: ticketRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := model.RateLimitBudget{
		Remaining: 0,
		ResetsAt:  time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	if snap.RateLimitBudget != want {
		t.Errorf("RateLimitBudget = %+v, want %+v", snap.RateLimitBudget, want)
	}
	if got := len(s.requests); got != 1 {
		t.Errorf("requests = %d, want unchanged one request", got)
	}
}

func TestResolveConservativelyAggregatesGitHubPageBudgets(t *testing.T) {
	s := newReplayServer(t,
		rateLimitedFixture(t, "epic_page1.json", 20, "2030-01-02T03:04:05Z"),
		rateLimitedFixture(t, "epic_page2.json", 7, "2030-01-02T04:04:05Z"),
	)
	snap, err := newProvider(s).Resolve(context.Background(), provider.EpicSelector{Ref: epicRef})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := model.RateLimitBudget{
		Remaining: 7,
		ResetsAt:  time.Date(2030, time.January, 2, 4, 4, 5, 0, time.UTC),
	}
	if snap.RateLimitBudget != want {
		t.Errorf("RateLimitBudget = %+v, want %+v", snap.RateLimitBudget, want)
	}
	if got := len(s.requests); got != 2 {
		t.Errorf("requests = %d, want unchanged two requests", got)
	}
}

func rateLimitedFixture(t *testing.T, name string, remaining int, resetsAt string) response {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	payload["data"].(map[string]any)["rateLimit"] = map[string]any{
		"remaining": remaining,
		"resetAt":   resetsAt,
	}
	body, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	return response{body: string(body)}
}
