package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestRateLimitHeaderPreservesPrimaryFactsAtResponsiveWidths(t *testing.T) {
	fact := rateLimitHeader(model.Capabilities{RateLimitBudget: true}, model.RateLimitBudget{
		Remaining: 0,
		ResetsAt:  time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
	})
	counts := "3/9 done · 1 cancelled · 33%"
	staleness := "just now"

	wide := fact.appendToLine(counts, staleness, 160)
	if !strings.Contains(wide, "budget 0 · resets 2030-01-02T03:04:05Z") {
		t.Errorf("wide header = %q, want complete budget", wide)
	}
	medium := fact.appendToLine(counts, staleness, 60)
	if !strings.Contains(medium, "budget 0") || strings.Contains(medium, "resets") {
		t.Errorf("medium header = %q, want compact budget", medium)
	}
	narrow := fact.appendToLine(counts, staleness, 40)
	if narrow != counts {
		t.Errorf("narrow header = %q, want primary facts unchanged %q", narrow, counts)
	}
}

func TestRateLimitHeaderGatesInvalidAndUnsupportedBudget(t *testing.T) {
	counts := "3/9 done · 33%"
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name string
		fact rateLimitHeaderFact
	}{
		{"unsupported", rateLimitHeader(model.Capabilities{}, model.RateLimitBudget{Remaining: 1, ResetsAt: reset})},
		{"invalid", rateLimitHeader(model.Capabilities{RateLimitBudget: true}, model.RateLimitBudget{Remaining: -1, ResetsAt: reset})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.fact.appendToLine(counts, "just now", 160); got != counts {
				t.Errorf("appendToLine() = %q, want unchanged %q", got, counts)
			}
		})
	}
}

func TestFrontierCarriesRateLimitBudgetFromList(t *testing.T) {
	budget := model.RateLimitBudget{Remaining: 3, ResetsAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	input := ListInput{Capabilities: model.Capabilities{RateLimitBudget: true}, RateLimitBudget: budget}
	if got := FrontierFromList(input, nil).RateLimitBudget; got != budget {
		t.Errorf("FrontierFromList budget = %+v, want %+v", got, budget)
	}
}

func TestListCopiesRateLimitBudgetFromWatchlistSnapshot(t *testing.T) {
	budget := model.RateLimitBudget{Remaining: 3, ResetsAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	input := ListFromWatchlistSnapshot(model.WatchlistSnapshot{RateLimitBudget: budget})
	if input.RateLimitBudget != budget {
		t.Errorf("List budget = %+v, want %+v", input.RateLimitBudget, budget)
	}
}

func TestRefreshReplacesAndFailurePreservesRateLimitBudget(t *testing.T) {
	oldBudget := model.RateLimitBudget{Remaining: 9, ResetsAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}
	state := Model{
		generation: 1,
		hasData:    true,
		input: ListInput{
			Capabilities:    model.Capabilities{RateLimitBudget: true},
			RateLimitBudget: oldBudget,
		},
	}
	failed := state.onRefreshed(refreshedMsg{generation: 1, err: errors.New("unavailable")})
	if failed.input.RateLimitBudget != oldBudget {
		t.Errorf("failed refresh budget = %+v, want preserved %+v", failed.input.RateLimitBudget, oldBudget)
	}
	cleared := failed.onRefreshed(refreshedMsg{generation: 1, input: ListInput{
		Capabilities: model.Capabilities{RateLimitBudget: true},
	}})
	if cleared.input.RateLimitBudget.Valid() {
		t.Errorf("successful refresh retained old budget %+v", cleared.input.RateLimitBudget)
	}
}
