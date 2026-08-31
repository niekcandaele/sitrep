package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

type unknownSelector struct{}

func (unknownSelector) selector() {}

func TestCheckSelectorSupportAcceptsDeclaredSelectors(t *testing.T) {
	caps := model.Capabilities{Selectors: model.SelectorCapabilities{
		Epic:    true,
		RefList: true,
		Query:   true,
	}}

	for _, selector := range []Selector{EpicSelector{}, RefListSelector{}, QuerySelector{}} {
		if err := CheckSelectorSupport("test", caps, selector); err != nil {
			t.Errorf("CheckSelectorSupport(%T) error = %v", selector, err)
		}
	}
}

func TestCheckSelectorSupportRejectsUndeclaredSelectors(t *testing.T) {
	tests := []struct {
		selector Selector
		want     string
	}{
		{selector: EpicSelector{}, want: "test: Epic Selector is not supported"},
		{selector: RefListSelector{}, want: "test: Ref-list Selector is not supported"},
		{selector: QuerySelector{}, want: "test: Query Selector is not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			err := CheckSelectorSupport("test", model.Capabilities{}, tt.selector)
			if err == nil {
				t.Fatal("CheckSelectorSupport() error = nil")
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
			if got := KindOf(err); got != KindBadRef {
				t.Errorf("KindOf(error) = %s, want %s", got, KindBadRef)
			}
			if KindOf(err).Retryable() {
				t.Error("unsupported Selector error is retryable")
			}
		})
	}
}

func TestCheckSelectorSupportRejectsUnknownAndPointerSelectors(t *testing.T) {
	caps := model.Capabilities{Selectors: model.SelectorCapabilities{
		Epic:    true,
		RefList: true,
		Query:   true,
	}}

	for _, selector := range []Selector{nil, unknownSelector{}, &EpicSelector{}, &RefListSelector{}, &QuerySelector{}} {
		err := CheckSelectorSupport("test", caps, selector)
		if err == nil {
			t.Fatalf("CheckSelectorSupport(%T) error = nil", selector)
		}
		if got := KindOf(err); got != KindBadRef {
			t.Errorf("CheckSelectorSupport(%T) Kind = %s, want %s", selector, got, KindBadRef)
		}
	}
}

func TestSelectorCapabilitiesRemainComparable(t *testing.T) {
	want := model.SelectorCapabilities{Epic: true, RefList: true, Query: true}
	got := want
	if got != want {
		t.Fatalf("SelectorCapabilities = %+v, want %+v", got, want)
	}
}

func TestFetchDetailsDefaultCanonicalizesAndPreservesIdentity(t *testing.T) {
	var calls []model.TicketID
	details, err := FetchDetailsDefault(t.Context(), []model.TicketID{"", "a", "b", "a", "", "c"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		calls = append(calls, id)
		return model.Detail{TicketID: id}, nil
	})
	if err != nil {
		t.Fatalf("FetchDetailsDefault = %v, want nil", err)
	}
	if want := []model.TicketID{"a", "b", "c"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(details) != 3 {
		t.Fatalf("details = %v, want three canonical entries", details)
	}
	for id, detail := range details {
		if detail.TicketID != id {
			t.Errorf("details[%q].TicketID = %q, want %q", id, detail.TicketID, id)
		}
	}
}

func TestFetchDetailsDefaultEmptyInputDoesNoIO(t *testing.T) {
	called := false
	details, err := FetchDetailsDefault(t.Context(), []model.TicketID{"", ""}, func(context.Context, model.TicketID) (model.Detail, error) {
		called = true
		return model.Detail{}, nil
	})
	if err != nil || called || details == nil || len(details) != 0 {
		t.Errorf("details/err/called = %v/%v/%v, want non-nil empty/nil/false", details, err, called)
	}
}

func TestFetchDetailsDefaultReturnsPartialResultsAndTypedFailures(t *testing.T) {
	boom := errors.New("boom")
	details, err := FetchDetailsDefault(t.Context(), []model.TicketID{"a", "b", "c"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		if id == "b" {
			return model.Detail{}, boom
		}
		return model.Detail{TicketID: id}, nil
	})
	if len(details) != 2 || details["a"].TicketID != "a" || details["c"].TicketID != "c" {
		t.Errorf("details = %v, want a and c successes", details)
	}
	var failures *DetailFailures
	if !errors.As(err, &failures) {
		t.Fatalf("error = %v, want *DetailFailures", err)
	}
	if len(failures.Failures) != 1 || !errors.Is(err, boom) || !errors.Is(failures.Failures["b"], boom) {
		t.Errorf("failures = %+v, want b wrapping boom", failures.Failures)
	}
}

func TestFetchDetailsDefaultReturnsCancellationAfterFinalCompletedSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	details, err := FetchDetailsDefault(ctx, []model.TicketID{"a"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		cancel()
		return model.Detail{TicketID: id}, nil
	})
	if !errors.Is(err, context.Canceled) || details["a"].TicketID != "a" {
		t.Errorf("details/error = %v/%v, want completed a and context.Canceled", details, err)
	}
}

func TestFetchDetailsDefaultTreatsProviderLocalCancellationAsTicketFailure(t *testing.T) {
	details, err := FetchDetailsDefault(t.Context(), []model.TicketID{"a"}, func(context.Context, model.TicketID) (model.Detail, error) {
		return model.Detail{}, fmt.Errorf("provider child: %w", context.Canceled)
	})
	var failures *DetailFailures
	if len(details) != 0 || !errors.As(err, &failures) || !errors.Is(failures.Failures["a"], context.Canceled) {
		t.Errorf("details/error = %v/%v, want a typed local cancellation failure", details, err)
	}
}

func TestFetchDetailsDefaultStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls []model.TicketID
	details, err := FetchDetailsDefault(ctx, []model.TicketID{"a", "b", "c"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		calls = append(calls, id)
		cancel()
		return model.Detail{TicketID: id}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if want := []model.TicketID{"a"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(details) != 1 || details["a"].TicketID != "a" {
		t.Errorf("details = %v, want completed a only", details)
	}
}
