package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestDetailFailuresMethodsPreserveExactUnwrapBound(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	refusal := RateLimitErrorf(RateLimitMetadata{ResetAt: reset}, "final refusal")
	failures := make(map[model.TicketID]error, maxDetailFailureUnwrapChildren)
	ids := make([]string, 0, maxDetailFailureUnwrapChildren)
	for i := 0; i < maxDetailFailureUnwrapChildren; i++ {
		id := fmt.Sprintf("T-%03d", i)
		ids = append(ids, id)
		failures[model.TicketID(id)] = fmt.Errorf("failure %d", i)
	}
	failures[model.TicketID(ids[len(ids)-1])] = refusal
	aggregate := &DetailFailures{Failures: failures}

	wantMessage := "details could not be read for " + strings.Join(ids, ", ")
	if got := aggregate.Error(); got != wantMessage {
		t.Fatalf("Error() = %q, want %q", got, wantMessage)
	}
	children := aggregate.Unwrap()
	if len(children) != maxDetailFailureUnwrapChildren {
		t.Fatalf("Unwrap() returned %d children, want all %d", len(children), maxDetailFailureUnwrapChildren)
	}
	for i, id := range ids {
		failure := failures[model.TicketID(id)]
		if !errors.Is(children[i], failure) {
			t.Fatalf("child %d = %v, want failure for %s", i, children[i], id)
		}
		if errors.Is(children[i], errDetailFailuresExceedSafeBounds) {
			t.Fatalf("child %d unexpectedly contains bounds sentinel", i)
		}
	}
	policy, ok := InspectRateLimitRefusal(aggregate, reset.Add(-time.Minute))
	if !ok || !policy.KnownReset || !policy.ResetAt.Equal(reset) || !errors.Is(policy.Err, refusal) {
		t.Fatalf("refusal = %+v/%t, want final sorted child with reset %s", policy, ok, reset)
	}
}

func TestDetailFailuresMethodsBoundOversizedAggregate(t *testing.T) {
	failures := make(map[model.TicketID]error, maxDetailFailureUnwrapChildren+1)
	for i := 0; i < maxDetailFailureUnwrapChildren+1; i++ {
		failures[model.TicketID(fmt.Sprintf("T-%03d", i))] = context.Canceled
	}
	aggregate := &DetailFailures{Failures: failures}

	if got := aggregate.Error(); got != errDetailFailuresExceedSafeBounds.Error() {
		t.Fatalf("Error() = %q, want %q", got, errDetailFailuresExceedSafeBounds)
	}
	children := aggregate.Unwrap()
	if len(children) != 1 || !errors.Is(children[0], errDetailFailuresExceedSafeBounds) {
		t.Fatalf("Unwrap() = %v, want only bounds sentinel", children)
	}
	if errors.Is(aggregate, context.Canceled) {
		t.Fatal("oversized aggregate exposed a map-order-dependent child")
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

func TestFetchDetailsDefaultStopsAtRateRefusalAndAttributesUnissuedIDs(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	refusal := RateLimitErrorf(RateLimitMetadata{ResetAt: reset}, "fake: rate limited")
	var calls []model.TicketID
	details, err := FetchDetailsDefault(t.Context(), []model.TicketID{"a", "b", "c"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		calls = append(calls, id)
		if id == "b" {
			return model.Detail{}, refusal
		}
		return model.Detail{TicketID: id}, nil
	})

	if want := []model.TicketID{"a", "b"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(details) != 1 || details["a"].TicketID != "a" {
		t.Errorf("details = %v, want completed a", details)
	}
	var failures *DetailFailures
	if !errors.As(err, &failures) {
		t.Fatalf("error = %v, want *DetailFailures", err)
	}
	for _, id := range []model.TicketID{"b", "c"} {
		failure := failures.Failures[id]
		policy, ok := InspectRateLimitRefusal(failure, reset.Add(-time.Minute))
		if !ok || !policy.KnownReset || !policy.ResetAt.Equal(reset) || !errors.Is(failure, refusal) {
			t.Errorf("failure[%q] = %v, policy %+v/%t", id, failure, policy, ok)
		}
	}
}

func TestFetchDetailsDefaultRetainsJoinedRateLimitEvidenceForLiveRetryAfter(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	expired := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(-time.Minute)}, "fake: elapsed reset")
	retryAfter := 30 * time.Second
	retry := RateLimitErrorf(RateLimitMetadata{RetryAfter: retryAfter}, "fake: retry later")
	callbackErr := errors.Join(expired, retry)
	var calls []model.TicketID

	details, err := FetchDetailsDefault(t.Context(), []model.TicketID{"a", "b"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		calls = append(calls, id)
		return model.Detail{}, callbackErr
	})

	if want := []model.TicketID{"a"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(details) != 0 {
		t.Errorf("details = %v, want no completed details", details)
	}
	if !errors.Is(err, callbackErr) || !errors.Is(err, expired) || !errors.Is(err, retry) {
		t.Fatalf("error = %v, want intact joined callback evidence", err)
	}
	refusal, limited := InspectRateLimitRefusal(err, now)
	if !limited || !refusal.KnownReset || !refusal.ResetAt.Equal(now.Add(retryAfter)) || !errors.Is(refusal.Err, retry) {
		t.Errorf("rate refusal = %+v/%t, want live RetryAfter hold ending %s", refusal, limited, now.Add(retryAfter))
	}
}

func TestFetchDetailsDefaultCancellationRacingRateRefusalPreservesAggregate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	refusal := RateLimitErrorf(RateLimitMetadata{ResetAt: reset}, "fake: rate limited")
	var calls []model.TicketID
	details, err := FetchDetailsDefault(ctx, []model.TicketID{"a", "b", "c"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		calls = append(calls, id)
		if id == "b" {
			cancel()
			return model.Detail{}, refusal
		}
		return model.Detail{TicketID: id}, nil
	})

	if want := []model.TicketID{"a", "b"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(details) != 1 || details["a"].TicketID != "a" {
		t.Errorf("details = %v, want completed a", details)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	policy, limited := InspectRateLimitRefusal(err, reset.Add(-time.Minute))
	if !limited || !policy.KnownReset || !policy.ResetAt.Equal(reset) || !errors.Is(policy.Err, refusal) {
		t.Errorf("rate refusal = %+v/%t, want %v", policy, limited, refusal)
	}
	var failures *DetailFailures
	if !errors.As(err, &failures) {
		t.Fatalf("error = %v, want *DetailFailures aggregate", err)
	}
	for _, id := range []model.TicketID{"b", "c"} {
		failure := failures.Failures[id]
		if !errors.Is(failure, refusal) || errors.Is(failure, context.Canceled) {
			t.Errorf("failure[%q] = %v, want rate refusal without fabricated cancellation", id, failure)
		}
	}
}

func TestFetchDetailsDefaultUnknownRateRefusalAlsoStops(t *testing.T) {
	refusal := Errorf(KindRateLimit, "fake: rate limited until an unknown time")
	var calls []model.TicketID
	_, err := FetchDetailsDefault(t.Context(), []model.TicketID{"a", "b"}, func(_ context.Context, id model.TicketID) (model.Detail, error) {
		calls = append(calls, id)
		return model.Detail{}, refusal
	})
	if want := []model.TicketID{"a"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	var failures *DetailFailures
	if !errors.As(err, &failures) || !errors.Is(failures.Failures["b"], refusal) {
		t.Errorf("error = %v, want shared refusal on unissued b", err)
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
