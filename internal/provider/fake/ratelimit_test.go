package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
)

func TestFixtureRateLimitBudgetPropagatesAndCapabilityFiltersIt(t *testing.T) {
	want := model.RateLimitBudget{
		Remaining: 4870,
		ResetsAt:  time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	selector := provider.EpicSelector{}

	snap, err := fake.New().Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("Resolve fixture: %v", err)
	}
	if snap.RateLimitBudget != want {
		t.Errorf("fixture budget = %+v, want %+v", snap.RateLimitBudget, want)
	}

	filtered, err := fake.New(fake.WithCapabilities(model.Capabilities{Selectors: model.SelectorCapabilities{Epic: true}})).Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("Resolve filtered fixture: %v", err)
	}
	if filtered.RateLimitBudget.Valid() {
		t.Errorf("filtered fixture retained budget %+v", filtered.RateLimitBudget)
	}
}

func TestRefListCarriesFixtureRateLimitBudget(t *testing.T) {
	refs := []ref.Ref{{Raw: "#112"}}
	snap, err := fake.New().Resolve(context.Background(), provider.RefListSelector{Refs: refs})
	if err != nil {
		t.Fatalf("Resolve ref list: %v", err)
	}
	if got, want := snap.RateLimitBudget, fake.FixtureRefListSnapshot().RateLimitBudget; got != want {
		t.Errorf("Ref-list budget = %+v, want %+v", got, want)
	}
}
