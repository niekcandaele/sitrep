package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

type cyclicError struct {
	next error
}

func (*cyclicError) Error() string   { return "cycle" }
func (e *cyclicError) Unwrap() error { return e.next }

type valueCyclicError struct{}

func (valueCyclicError) Error() string { return "value cycle" }
func (valueCyclicError) Unwrap() error { return valueCyclicError{} }

type nonComparableValueCycle struct {
	values []int
}

func (nonComparableValueCycle) Error() string   { return "non-comparable value cycle" }
func (e nonComparableValueCycle) Unwrap() error { return e }

type dynamicComparableError struct {
	value any
	next  error
}

func (dynamicComparableError) Error() string   { return "dynamic comparable error" }
func (e dynamicComparableError) Unwrap() error { return e.next }

type sliceError []error

func (sliceError) Error() string     { return "slice error" }
func (e sliceError) Unwrap() []error { return []error(e) }

type panicOnError struct{}

func (*panicOnError) Error() string { panic("Error must not be called") }

type countedUnwrapError struct {
	calls *int
}

func (*countedUnwrapError) Error() string { return "counted unwrap" }
func (e *countedUnwrapError) Unwrap() error {
	(*e.calls)++
	return nil
}

func TestInspectRateLimitRefusalBoundsOversizedDetailFailures(t *testing.T) {
	calls := 0
	failures := make(map[model.TicketID]error, maxDetailFailureUnwrapChildren+1)
	for i := 0; i < maxDetailFailureUnwrapChildren+1; i++ {
		failures[model.TicketID(fmt.Sprintf("T-%03d", i))] = &countedUnwrapError{calls: &calls}
	}

	if refusal, ok := InspectRateLimitRefusal(&DetailFailures{Failures: failures}, time.Time{}); ok {
		t.Fatalf("InspectRateLimitRefusal() = %+v, true; want no refusal", refusal)
	}
	if calls != 0 {
		t.Fatalf("inspector traversed %d oversized aggregate members, want none", calls)
	}
}

func TestRateLimitBudgetCollectorChoosesConservativePairedObservation(t *testing.T) {
	firstReset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.FixedZone("UTC+2", 2*60*60))
	laterReset := time.Date(2030, time.January, 2, 3, 4, 6, 0, time.UTC)
	var collector RateLimitBudgetCollector

	collector.Observe(model.RateLimitBudget{Remaining: 12, ResetsAt: firstReset})
	collector.Observe(model.RateLimitBudget{Remaining: -1, ResetsAt: laterReset})
	collector.Observe(model.RateLimitBudget{Remaining: 15, ResetsAt: laterReset})
	collector.Observe(model.RateLimitBudget{Remaining: 12, ResetsAt: laterReset})

	got := collector.Budget()
	want := model.RateLimitBudget{Remaining: 12, ResetsAt: laterReset}
	if got != want {
		t.Errorf("Budget() = %+v, want %+v", got, want)
	}
}

func TestRateLimitBudgetCollectorPreservesValidZeroAndReturnsCopy(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	var collector RateLimitBudgetCollector
	collector.Observe(model.RateLimitBudget{Remaining: 0, ResetsAt: reset})

	got := collector.Budget()
	if got != (model.RateLimitBudget{Remaining: 0, ResetsAt: reset}) {
		t.Fatalf("Budget() = %+v, want valid zero", got)
	}
	got.Remaining = 99
	if after := collector.Budget(); after.Remaining != 0 {
		t.Errorf("Budget after mutating return = %+v, want stored value unchanged", after)
	}
}

func TestRateLimitBudgetCollectorSynchronizesObservations(t *testing.T) {
	var collector RateLimitBudgetCollector
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	var group sync.WaitGroup
	for remaining := 1; remaining <= 100; remaining++ {
		group.Add(1)
		go func(remaining int) {
			defer group.Done()
			collector.Observe(model.RateLimitBudget{Remaining: remaining, ResetsAt: reset.Add(time.Duration(remaining) * time.Second)})
		}(remaining)
	}
	group.Wait()

	want := model.RateLimitBudget{Remaining: 1, ResetsAt: reset.Add(time.Second)}
	if got := collector.Budget(); got != want {
		t.Errorf("Budget() = %+v, want %+v", got, want)
	}
}

func TestInspectRateLimitRefusalUnknownDominatesAndTraversalIsDeterministic(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	unknown := RateLimitErrorf(RateLimitMetadata{}, "github: unknown reset")
	known := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(time.Hour)}, "github: known reset")
	err := fmt.Errorf("batch: %w", &DetailFailures{Failures: map[model.TicketID]error{
		"z": known,
		"a": errors.Join(context.Canceled, unknown),
	}})

	got, ok := InspectRateLimitRefusal(err, now)
	if !ok {
		t.Fatal("InspectRateLimitRefusal() = false, want refusal")
	}
	if got.KnownReset || got.ExpiredOnly || !got.ResetAt.IsZero() || !errors.Is(got.Err, unknown) {
		t.Errorf("refusal = %+v, want representative genuinely unknown refusal", got)
	}
}

func TestInspectRateLimitRefusalChoosesLatestFutureDeadline(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	earlier := RateLimitErrorf(RateLimitMetadata{RetryAfter: 30 * time.Second}, "jira: earlier")
	later := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(time.Minute)}, "github: later")

	got, ok := InspectRateLimitRefusal(errors.Join(earlier, context.DeadlineExceeded, later), now)
	if !ok || !got.KnownReset || !got.ResetAt.Equal(now.Add(time.Minute)) || !errors.Is(got.Err, later) {
		t.Errorf("refusal = %+v, %t; want latest reset", got, ok)
	}
}

func TestInspectRateLimitRefusalTreatsExpiredDeadlineAsExpiredOnly(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	expired := RateLimitErrorf(RateLimitMetadata{ResetAt: now}, "github: stale reset")
	got, ok := InspectRateLimitRefusal(expired, now)
	if !ok || got.KnownReset || !got.ExpiredOnly || !errors.Is(got.Err, expired) {
		t.Errorf("refusal = %+v, %t; want expired-only refusal", got, ok)
	}
}

func TestInspectRateLimitRefusalFutureDeadlineBeatsElapsedDeadline(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	expired := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(-time.Minute)}, "github: stale reset")
	future := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(time.Minute)}, "github: current reset")

	for _, err := range []error{errors.Join(expired, future), errors.Join(future, expired)} {
		got, ok := InspectRateLimitRefusal(err, now)
		if !ok || !got.KnownReset || got.ExpiredOnly || !got.ResetAt.Equal(now.Add(time.Minute)) || !errors.Is(got.Err, future) {
			t.Errorf("refusal = %+v, %t; want future refusal", got, ok)
		}
	}
}

func TestInspectRateLimitRefusalDoesNotMarkMixedUnknownGraphExpiredOnly(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	expired := RateLimitErrorf(RateLimitMetadata{ResetAt: now}, "github: stale reset")
	unknown := RateLimitErrorf(RateLimitMetadata{}, "github: no reset")
	got, ok := InspectRateLimitRefusal(errors.Join(expired, unknown), now)
	if !ok || got.KnownReset || got.ExpiredOnly {
		t.Errorf("refusal = %+v, %t; want genuinely unknown aggregate", got, ok)
	}
}

func TestInspectRateLimitRefusalMalformedTimingDominatesFutureDeadline(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	malformed := RateLimitErrorf(RateLimitMetadata{RetryAfter: -time.Second}, "github: malformed retry delay")
	future := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(time.Minute)}, "github: current reset")

	got, ok := InspectRateLimitRefusal(errors.Join(future, malformed), now)
	if !ok || got.KnownReset || got.ExpiredOnly || !got.ResetAt.IsZero() || !errors.Is(got.Err, malformed) {
		t.Errorf("refusal = %+v, %t; want malformed timing to produce unknown refusal", got, ok)
	}
}

func TestInspectRateLimitRefusalIgnoresCancellationWithoutRefusal(t *testing.T) {
	if got, ok := InspectRateLimitRefusal(fmt.Errorf("request: %w", context.Canceled), time.Time{}); ok {
		t.Errorf("refusal = %+v, true; want none", got)
	}
}

func TestRequestPolicyContextRoundTrip(t *testing.T) {
	if got := RequestPolicyFromContext(t.Context()); got != (RequestPolicy{}) {
		t.Fatalf("unversioned policy = %+v, want zero", got)
	}
	want := RequestPolicy{Epoch: 7, ProbeToken: 11}
	if got := RequestPolicyFromContext(WithRequestPolicy(t.Context(), want)); got != want {
		t.Errorf("policy = %+v, want %+v", got, want)
	}
}

func TestInspectRateLimitRefusalEqualDeadlineChoosesStableRepresentative(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	stable := RateLimitErrorf(RateLimitMetadata{ResetAt: now.Add(time.Minute)}, "github: stable")
	other := RateLimitErrorf(RateLimitMetadata{RetryAfter: time.Minute}, "jira: other")
	for _, err := range []error{errors.Join(stable, other), errors.Join(other, stable)} {
		got, ok := InspectRateLimitRefusal(err, now)
		if !ok || !got.KnownReset || !errors.Is(got.Err, stable) {
			t.Errorf("refusal = %+v, %t; want stable equal-deadline representative", got, ok)
		}
	}
}

func TestInspectRateLimitRefusalTieBreakDoesNotRenderMalformedErrors(t *testing.T) {
	first := &Error{Kind: KindRateLimit, Err: (*panicOnError)(nil)}
	second := &Error{Kind: KindRateLimit, Err: (*panicOnError)(nil)}
	forward, ok := InspectRateLimitRefusal(errors.Join(first, second), time.Time{})
	if !ok {
		t.Fatal("forward inspection found no refusal")
	}
	reverse, ok := InspectRateLimitRefusal(errors.Join(second, first), time.Time{})
	if !ok || reverse.Err != forward.Err { //nolint:errorlint // Representative identity is the contract under test.
		t.Errorf("forward/reverse representatives = %p/%p, want stable identity", forward.Err, reverse.Err)
	}
}

func TestInspectRateLimitRefusalStopsAtPointerAndValueCycles(t *testing.T) {
	pointerCycle := &cyclicError{}
	refusal := &Error{Kind: KindRateLimit, Err: pointerCycle}
	pointerCycle.next = refusal
	got, ok := InspectRateLimitRefusal(errors.Join(
		nonComparableValueCycle{values: []int{1}},
		valueCyclicError{},
		refusal,
	), time.Time{})
	if !ok || got.KnownReset || got.Err != refusal { //nolint:errorlint // Exact cyclic node identity is required.
		t.Errorf("refusal = %+v, %t; want rate node once across cyclic branches", got, ok)
	}
}

func TestInspectRateLimitRefusalDoesNotHashDynamicallyNonComparableValues(t *testing.T) {
	refusal := RateLimitErrorf(RateLimitMetadata{}, "github: refusal")
	for name, payload := range map[string]any{
		"slice": []byte("slice payload"),
		"map":   map[string]int{"key": 1},
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamicComparableError{value: payload, next: refusal}
			if !reflect.TypeOf(err).Comparable() {
				t.Fatal("test precondition: error type is not nominally comparable")
			}
			if reflect.ValueOf(err).Comparable() {
				t.Fatal("test precondition: error value is comparable")
			}

			got, ok := InspectRateLimitRefusal(err, time.Time{})
			if !ok || got.KnownReset || got.ExpiredOnly || !errors.Is(got.Err, refusal) {
				t.Fatalf("refusal = %+v, %t; want nested unknown-reset refusal", got, ok)
			}
		})
	}
}

func TestInspectRateLimitRefusalDoesNotDeduplicateDistinctSharedSlices(t *testing.T) {
	refusal := RateLimitErrorf(RateLimitMetadata{}, "github: refusal")
	backing := []error{errors.New("ordinary"), refusal}
	ordinary := sliceError(backing[:1])
	withRefusal := sliceError(backing[:2])

	got, ok := InspectRateLimitRefusal(errors.Join(ordinary, withRefusal), time.Time{})
	if !ok || got.KnownReset || !errors.Is(got.Err, refusal) {
		t.Errorf("refusal = %+v, %t; want refusal from distinct shared-backing slice", got, ok)
	}
}
