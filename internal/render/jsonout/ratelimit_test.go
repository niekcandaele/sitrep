package jsonout_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
)

func TestWatchlistRateLimitBudgetWireContract(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.FixedZone("UTC+2", 2*60*60))
	for _, test := range []struct {
		name       string
		caps       model.Capabilities
		budget     model.RateLimitBudget
		wantObject bool
	}{
		{"valid zero", model.Capabilities{RateLimitBudget: true}, model.RateLimitBudget{Remaining: 0, ResetsAt: reset}, true},
		{"invalid value", model.Capabilities{RateLimitBudget: true}, model.RateLimitBudget{Remaining: -1, ResetsAt: reset}, false},
		{"capability false", model.Capabilities{}, model.RateLimitBudget{Remaining: 2, ResetsAt: reset}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := jsonout.RenderWatchlist(&output, jsonout.WatchlistDocument{
				Snapshot: model.WatchlistSnapshot{
					Capabilities: test.caps, RateLimitBudget: test.budget, FetchedAt: generatedAt,
				},
				Selector: provider.EpicSelector{Ref: ref.Ref{Raw: "1"}}, ProviderName: "fake",
			}); err != nil {
				t.Fatalf("RenderWatchlist: %v", err)
			}
			var document struct {
				Provider struct {
					Capabilities struct {
						RateLimitBudget *bool `json:"rate_limit_budget"`
					} `json:"capabilities"`
				} `json:"provider"`
				Budget *struct {
					Remaining int       `json:"remaining"`
					ResetsAt  time.Time `json:"resets_at"`
				} `json:"rate_limit_budget"`
			}
			if err := json.Unmarshal(output.Bytes(), &document); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}
			if document.Provider.Capabilities.RateLimitBudget == nil || *document.Provider.Capabilities.RateLimitBudget != test.caps.RateLimitBudget {
				t.Errorf("capability = %v, want explicit %v", document.Provider.Capabilities.RateLimitBudget, test.caps.RateLimitBudget)
			}
			if (document.Budget != nil) != test.wantObject {
				t.Fatalf("budget object present = %v, want %v: %s", document.Budget != nil, test.wantObject, output.String())
			}
			if document.Budget != nil && (document.Budget.Remaining != test.budget.Remaining || !document.Budget.ResetsAt.Equal(reset.UTC())) {
				t.Errorf("budget = %+v, want %d at %s", document.Budget, test.budget.Remaining, reset.UTC())
			}
		})
	}
}

func TestStandaloneDetailOmitsRateLimitBudget(t *testing.T) {
	var output bytes.Buffer
	if err := jsonout.RenderDetail(&output, model.Detail{TicketID: "ticket"}, model.Capabilities{RateLimitBudget: true}, "fake", generatedAt); err != nil {
		t.Fatalf("RenderDetail: %v", err)
	}
	if hasKey(t, output.Bytes(), "rate_limit_budget") || bytes.Contains(output.Bytes(), []byte(`"rate_limit_budget"`)) {
		t.Errorf("standalone Detail emitted rate limit data: %s", output.String())
	}
}
