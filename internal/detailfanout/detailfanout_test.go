package detailfanout_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

func tickets(ids ...model.TicketID) []model.Ticket {
	out := make([]model.Ticket, 0, len(ids))
	for _, id := range ids {
		out = append(out, model.Ticket{ID: id, Key: string(id)})
	}
	return out
}

func TestPlanReturnsCanonicalOrder(t *testing.T) {
	got := detailfanout.Plan(tickets("c", "a", "b"), nil)
	want := []model.TicketID{"c", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan = %v, want %v", got, want)
	}
}

func TestPlanSkipsEmptyIDsDuplicatesAndWhatTheCallerHolds(t *testing.T) {
	got := detailfanout.Plan(tickets("a", "", "b", "a", "c"), func(id model.TicketID) bool { return id == "b" })
	want := []model.TicketID{"a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan = %v, want %v", got, want)
	}
}

func TestLinksWritesAKeyOnlyForAReadDetail(t *testing.T) {
	links := detailfanout.Links(map[model.TicketID]model.Detail{
		"read-with-links": {Links: []model.Link{{Kind: model.LinkBlockedBy}}},
		"read-empty":      {},
	})
	if got, ok := links["read-with-links"]; !ok || len(got) != 1 {
		t.Errorf("links[read-with-links] = %v (present %v), want one Link", got, ok)
	}
	if got, ok := links["read-empty"]; !ok || len(got) != 0 {
		t.Errorf("links[read-empty] = %v (present %v), want a present empty key", got, ok)
	}
	if _, ok := links["never-read"]; ok {
		t.Error("links carries a key for a Ticket that was never read")
	}
}

func TestUnreadableLinksNotice(t *testing.T) {
	for _, tc := range []struct {
		failed int
		want   string
	}{
		{0, ""},
		{1, "1 Ticket's Links could not be read; anything it blocks is not Actionable"},
		{2, "2 Tickets' Links could not be read; anything they block is not Actionable"},
	} {
		t.Run(fmt.Sprintf("failed_%d", tc.failed), func(t *testing.T) {
			if got := detailfanout.UnreadableLinksNotice(tc.failed); got != tc.want {
				t.Errorf("UnreadableLinksNotice(%d) = %q, want %q", tc.failed, got, tc.want)
			}
		})
	}
}

type panickingTypedNilError struct{}

func (*panickingTypedNilError) Error() string { panic("typed-nil Error called") }

type typedNilWrapper struct{ err error }

type safeNilUnwrapError struct{}

func (*safeNilUnwrapError) Error() string { return "safe nil child" }
func (*safeNilUnwrapError) Unwrap() error { return nil }

type cyclicUnwrapError struct {
	message string
	next    error
}

func (e *cyclicUnwrapError) Error() string { return e.message }
func (e *cyclicUnwrapError) Unwrap() error { return e.next }

type cyclicMultiUnwrapError struct {
	message  string
	children []error
}

func (e *cyclicMultiUnwrapError) Error() string   { return e.message }
func (e *cyclicMultiUnwrapError) Unwrap() []error { return e.children }

func countExactErrorLeaves(root, target error) int {
	queue := []error{root}
	seen := make(map[error]struct{})
	count := 0
	for len(queue) > 0 {
		err := queue[0]
		queue = queue[1:]
		if err == nil {
			continue
		}
		value := reflect.ValueOf(err)
		if value.Type().Comparable() {
			if _, exists := seen[err]; exists {
				continue
			}
			seen[err] = struct{}{}
			if err == target { //nolint:errorlint // Exact sentinel occurrence count is the behavior under test.
				count++
			}
		}
		switch wrapped := err.(type) { //nolint:errorlint // Test traversal over an already-normalized graph.
		case interface{ Unwrap() []error }:
			queue = append(queue, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			queue = append(queue, wrapped.Unwrap())
		}
	}
	return count
}

type nonComparableCycleError []error
type emptyNonComparableCycleError []error

type dynamicComparableError struct {
	value any
	next  error
}

func (dynamicComparableError) Error() string   { return "dynamic comparable error" }
func (e dynamicComparableError) Unwrap() error { return e.next }

func (nonComparableCycleError) Error() string          { return "non-comparable cycle" }
func (e nonComparableCycleError) Unwrap() []error      { return []error(e) }
func (emptyNonComparableCycleError) Error() string     { return "empty non-comparable cycle" }
func (e emptyNonComparableCycleError) Unwrap() []error { return []error{e} }

type cyclicIsError struct {
	next error
}

func (*cyclicIsError) Error() string          { return "cyclic Is" }
func (e *cyclicIsError) Unwrap() error        { return e.next }
func (e *cyclicIsError) Is(target error) bool { return errors.Is(e.next, target) }

type customAsClassification struct{}

func (*customAsClassification) Error() string { return "custom classification" }

type safeCustomMatcher struct {
	isTarget       error
	classification *customAsClassification
}

func (*safeCustomMatcher) Error() string { return "safe custom matcher" }
func (e *safeCustomMatcher) Is(target error) bool {
	return target == e.isTarget
}
func (e *safeCustomMatcher) As(target any) bool {
	classified, ok := target.(**customAsClassification)
	if !ok {
		return false
	}
	*classified = e.classification
	return true
}

type safeSingleCustomMatcher struct {
	*safeCustomMatcher
	child error
}

func (e *safeSingleCustomMatcher) Unwrap() error { return e.child }

type safeMultiCustomMatcher struct {
	*safeCustomMatcher
	children []error
}

func (e *safeMultiCustomMatcher) Unwrap() []error { return e.children }

type nilDetailFailuresMatcher struct{}

func (*nilDetailFailuresMatcher) Error() string { return "nil DetailFailures matcher" }
func (*nilDetailFailuresMatcher) As(target any) bool {
	_, ok := target.(**provider.DetailFailures)
	return ok
}

type synthesizedDetailFailuresMatcher struct {
	failures *provider.DetailFailures
}

func (*synthesizedDetailFailuresMatcher) Error() string { return "synthesized DetailFailures matcher" }
func (e *synthesizedDetailFailuresMatcher) As(target any) bool {
	failures, ok := target.(**provider.DetailFailures)
	if !ok {
		return false
	}
	*failures = e.failures
	return true
}

type cyclicAsError struct {
	next error
}

func (*cyclicAsError) Error() string   { return "cyclic As" }
func (e *cyclicAsError) Unwrap() error { return e.next }
func (e *cyclicAsError) As(target any) bool {
	return errors.As(e.next, target)
}

func (e *typedNilWrapper) Error() string { return "wrapped: " + e.err.Error() }
func (e *typedNilWrapper) Unwrap() error { return e.err }

type typedNilCustomClassifier struct {
	err        error
	classified *provider.Error
}

func (e *typedNilCustomClassifier) Error() string { return "classified: " + e.err.Error() }
func (e *typedNilCustomClassifier) Unwrap() error { return e.err }
func (*typedNilCustomClassifier) Is(target error) bool {
	return target == context.Canceled
}
func (e *typedNilCustomClassifier) As(target any) bool {
	classified, ok := target.(**provider.Error)
	if !ok {
		return false
	}
	*classified = e.classified
	return true
}

func TestNormalizeErrorBoundsCyclicUnwrapGraphs(t *testing.T) {
	self := &cyclicUnwrapError{message: "self"}
	self.next = self
	first := &cyclicUnwrapError{message: "first"}
	second := &cyclicUnwrapError{message: "second"}
	first.next, second.next = second, first
	multi := &cyclicMultiUnwrapError{message: "multi"}
	multi.children = []error{errors.New("safe sibling"), multi}
	branching := &cyclicMultiUnwrapError{message: "branching"}
	branching.children = []error{branching, branching}

	for _, root := range []error{self, first, multi, branching} {
		t.Run(root.Error(), func(t *testing.T) {
			normalized := detailfanout.NormalizeError(root)
			if normalized == root { //nolint:errorlint // Direct identity is the behavior under test.
				t.Fatal("cyclic graph was returned without a safe bounded wrapper")
			}
			termtexttest.AssertClean(t, "cyclic normalized error", normalized.Error())
			if !errors.Is(normalized, root) {
				t.Fatal("cyclic root identity was not preserved")
			}
		})
	}
}

func TestNormalizeErrorPreservesRateLimitSiblingStarvedByCyclicTraversal(t *testing.T) {
	now := time.Date(2026, time.September, 1, 8, 0, 0, 0, time.UTC)
	deadline := now.Add(19 * time.Minute)
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: deadline}, "rate refused")
	cycle := &cyclicMultiUnwrapError{message: "starving cycle"}
	cycle.children = []error{cycle, cycle}
	root := &cyclicMultiUnwrapError{message: "root", children: []error{cycle, refusal}}

	normalized := detailfanout.NormalizeError(root)
	if !errors.Is(normalized, root) {
		t.Fatal("bounded normalization lost the root identity")
	}
	got, ok := provider.InspectRateLimitRefusal(normalized, now)
	if !ok || !got.KnownReset || !got.ResetAt.Equal(deadline) || got.ExpiredOnly {
		t.Fatalf("InspectRateLimitRefusal = %+v, %t; want known reset %v", got, ok, deadline)
	}
	if errors.Is(got.Err, refusal) {
		t.Fatal("bounded normalization exposed the refusal's original child graph")
	}
	classified, direct := got.Err.(*provider.Error) //nolint:errorlint // Inspector must expose a sanitized direct representative.
	if !direct || classified == nil || classified.RateLimit != (provider.RateLimitMetadata{ResetAt: deadline}) {
		t.Fatalf("representative = %#v, want sanitized direct refusal", got.Err)
	}
}

func TestNormalizeErrorPreservesRateLimitSiblingBeforeCyclicTraversal(t *testing.T) {
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{}, "rate refused")
	cycle := &cyclicMultiUnwrapError{message: "bounded cycle"}
	cycle.children = []error{cycle, cycle}
	root := &cyclicMultiUnwrapError{message: "root", children: []error{refusal, cycle}}

	normalized := detailfanout.NormalizeError(root)
	got, ok := provider.InspectRateLimitRefusal(normalized, time.Time{})
	if !ok || got.Err != refusal { //nolint:errorlint // The in-budget refusal should remain unchanged.
		t.Fatalf("InspectRateLimitRefusal = %+v, %t; want original in-budget refusal", got, ok)
	}
}

func TestNormalizeErrorPreservesCancellationSiblingAcrossCyclicTraversal(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, cycleFirst := range []bool{true, false} {
			name := sentinel.Error() + "/semantic-first"
			if cycleFirst {
				name = sentinel.Error() + "/cycle-first"
			}
			t.Run(name, func(t *testing.T) {
				cycle := &cyclicMultiUnwrapError{message: "starving cycle"}
				cycle.children = []error{cycle, cycle}
				children := []error{sentinel, cycle}
				if cycleFirst {
					children = []error{cycle, sentinel}
				}
				root := &cyclicMultiUnwrapError{message: "root", children: children}

				normalized := detailfanout.NormalizeError(root)
				if !errors.Is(normalized, sentinel) {
					t.Fatalf("normalization lost exact %v sibling", sentinel)
				}
				if got := countExactErrorLeaves(normalized, sentinel); got != 1 {
					t.Fatalf("exact %v leaves = %d, want one", sentinel, got)
				}
			})
		}
	}

	cycle := &cyclicMultiUnwrapError{message: "no semantic leaf"}
	cycle.children = []error{cycle, cycle}
	normalized := detailfanout.NormalizeError(cycle)
	if errors.Is(normalized, context.Canceled) || errors.Is(normalized, context.DeadlineExceeded) {
		t.Fatal("bounded traversal fabricated a cancellation leaf")
	}

	deep := context.Canceled
	for i := 0; i < 64; i++ {
		deep = &cyclicUnwrapError{message: fmt.Sprintf("cancellation depth %d", i), next: deep}
	}
	if errors.Is(detailfanout.NormalizeError(deep), context.Canceled) {
		t.Fatal("semantic scan extended beyond its depth bound")
	}
	wideChildren := make([]error, 65)
	for i := range wideChildren[:64] {
		wideChildren[i] = fmt.Errorf("ordinary %d", i)
	}
	wideChildren[64] = context.DeadlineExceeded
	wide := &cyclicMultiUnwrapError{message: "wide semantic root", children: wideChildren}
	if errors.Is(detailfanout.NormalizeError(wide), context.DeadlineExceeded) {
		t.Fatal("semantic scan extended beyond its per-node child bound")
	}
}

func TestNormalizeErrorDoesNotScanBeyondDepthBound(t *testing.T) {
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{}, "out-of-bounds refusal")
	root := refusal
	for i := 0; i < 64; i++ {
		root = &cyclicUnwrapError{message: fmt.Sprintf("depth %d", i), next: root}
	}

	if _, ok := provider.InspectRateLimitRefusal(root, time.Time{}); !ok {
		t.Fatal("control: raw Provider policy inspection lost the depth-64 refusal")
	}
	normalized := detailfanout.NormalizeError(root)
	if !errors.Is(normalized, root) {
		t.Fatal("depth-bounded normalization lost the root identity")
	}
	if got, want := normalized.Error(), "provider: FetchDetails returned an error graph exceeding safe unwrap bounds"; got != want {
		t.Fatalf("depth-bounded error = %q, want %q", got, want)
	}
	if got, ok := provider.InspectRateLimitRefusal(normalized, time.Time{}); ok {
		t.Fatalf("depth-bounded normalization extended its scan or fabricated a refusal: %+v", got)
	}
}

func TestNormalizeErrorDoesNotScanBeyondChildBound(t *testing.T) {
	children := make([]error, 65)
	for i := range children[:64] {
		children[i] = fmt.Errorf("ordinary %d", i)
	}
	children[64] = provider.RateLimitErrorf(provider.RateLimitMetadata{}, "out-of-bounds refusal")
	root := &cyclicMultiUnwrapError{message: "wide root", children: children}

	if _, ok := provider.InspectRateLimitRefusal(root, time.Time{}); !ok {
		t.Fatal("control: raw Provider policy inspection lost the child-65 refusal")
	}
	normalized := detailfanout.NormalizeError(root)
	if got, want := normalized.Error(), "provider: FetchDetails returned an error graph exceeding safe unwrap bounds"; got != want {
		t.Fatalf("child-bounded error = %q, want %q", got, want)
	}
	if got, ok := provider.InspectRateLimitRefusal(normalized, time.Time{}); ok {
		t.Fatalf("child-bounded normalization extended its scan or fabricated a refusal: %+v", got)
	}
}

func TestNormalizeErrorRejectsOversizedDetailFailures(t *testing.T) {
	failures := make(map[model.TicketID]error, 65)
	for i := range 65 {
		failures[model.TicketID(fmt.Sprintf("T-%03d", i))] = errors.New("ordinary failure")
	}
	root := &provider.DetailFailures{Failures: failures}

	normalized := detailfanout.NormalizeError(root)
	if got, want := normalized.Error(), "provider: FetchDetails returned an error graph exceeding safe unwrap bounds"; got != want {
		t.Fatalf("oversized aggregate error = %q, want %q", got, want)
	}
	if !errors.Is(normalized, root) {
		t.Fatal("oversized normalization lost the aggregate identity")
	}
	var exposed *provider.DetailFailures
	if errors.As(normalized, &exposed) {
		t.Fatalf("oversized normalization exposed %d untrusted failures", len(exposed.Failures))
	}
}

func TestRunRetainsRequestedMembershipFromOversizedDetailFailures(t *testing.T) {
	ids := []model.TicketID{"good", "collision"}
	failures := map[model.TicketID]error{"collision": errors.New("collision failed")}
	for i := range 64 {
		id := model.TicketID(fmt.Sprintf("failed-%02d", i))
		ids = append(ids, id)
		failures[id] = errors.New("member failed")
	}
	aggregate := &provider.DetailFailures{Failures: failures}
	responseErr := errors.Join(errors.New("response failed"), aggregate)

	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return map[model.TicketID]model.Detail{
			"good":      {TicketID: "good"},
			"collision": {TicketID: "collision"},
		}, model.Capabilities{BlockingLinks: true}, responseErr
	}, ids, func(outcome detailfanout.Outcome) {
		outcomes = append(outcomes, outcome)
	})

	if err != nil {
		t.Fatalf("Run = %v, want deterministic per-Ticket outcomes", err)
	}
	if len(outcomes) != len(ids) {
		t.Fatalf("outcomes = %d, want %d", len(outcomes), len(ids))
	}
	if outcomes[0].ID != "good" || outcomes[0].Err != nil || outcomes[0].Detail.TicketID != "good" {
		t.Errorf("good outcome = %+v, want preserved successful sibling", outcomes[0])
	}
	if outcomes[1].ID != "collision" || outcomes[1].Err == nil ||
		!strings.Contains(outcomes[1].Err.Error(), "both detail and failure") {
		t.Errorf("collision outcome = %+v, want fail-closed contradiction", outcomes[1])
	}
	for _, outcome := range outcomes[2:] {
		if outcome.Err == nil || outcome.Err.Error() != "provider: FetchDetails returned an error graph exceeding safe unwrap bounds" {
			t.Errorf("outcome[%q] = %v, want bounded membership failure", outcome.ID, outcome.Err)
		}
	}
}

func TestNormalizeErrorRetainsMixedRateFactsAndCancellationAcrossBound(t *testing.T) {
	now := time.Date(2026, time.September, 1, 8, 0, 0, 0, time.UTC)
	soon := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: now.Add(time.Minute)}, "soon")
	later := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: now.Add(7 * time.Minute)}, "later")
	unknown := provider.RateLimitErrorf(provider.RateLimitMetadata{}, "unknown")

	tests := []struct {
		name      string
		refusals  []error
		wantKnown bool
		wantReset time.Time
	}{
		{name: "latest future deadline", refusals: []error{soon, later}, wantKnown: true, wantReset: now.Add(7 * time.Minute)},
		{name: "unknown reset dominates", refusals: []error{soon, later, unknown}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cycle := &cyclicMultiUnwrapError{message: "starving cycle"}
			cycle.children = []error{cycle, cycle}
			children := []error{cycle, context.Canceled}
			children = append(children, tt.refusals...)
			root := &cyclicMultiUnwrapError{message: "root", children: children}

			original, ok := provider.InspectRateLimitRefusal(root, now)
			if !ok {
				t.Fatal("control graph did not expose a refusal")
			}
			normalized := detailfanout.NormalizeError(root)
			if !errors.Is(normalized, context.Canceled) {
				t.Fatal("normalization lost the real cancellation sibling")
			}
			got, ok := provider.InspectRateLimitRefusal(normalized, now)
			if !ok || got.KnownReset != tt.wantKnown || !got.ResetAt.Equal(tt.wantReset) || got.ExpiredOnly {
				t.Fatalf("InspectRateLimitRefusal = %+v, %t; want KnownReset=%t ResetAt=%v", got, ok, tt.wantKnown, tt.wantReset)
			}
			if got.KnownReset != original.KnownReset || !got.ResetAt.Equal(original.ResetAt) || got.ExpiredOnly != original.ExpiredOnly {
				t.Fatalf("normalized refusal %+v differs from original policy %+v", got, original)
			}
			if errors.Is(got.Err, context.Canceled) {
				t.Fatal("sanitized refusal representative carried the cancellation sibling")
			}
		})
	}
}

func TestNormalizeErrorDoesNotConflateNonComparableSlices(t *testing.T) {
	root := nonComparableCycleError{nil}
	root[0] = root
	alias := root[:]
	other := nonComparableCycleError{nil}
	other[0] = other

	normalized := detailfanout.NormalizeError(root)
	if errors.Is(normalized, root) || errors.Is(normalized, alias) || errors.Is(normalized, other) {
		t.Fatal("non-comparable slice values were treated as identical")
	}
}

func TestNormalizeErrorDoesNotCompareMismatchedDynamicInterfaceValues(t *testing.T) {
	var typedNil *panickingTypedNilError
	original := dynamicComparableError{value: 1, next: typedNil}
	target := dynamicComparableError{value: []string{"not comparable"}, next: errors.New("other")}

	normalized := detailfanout.NormalizeError(original)
	if errors.Is(normalized, target) {
		t.Fatal("errors with distinct dynamic interface values matched")
	}
}

func TestNormalizeErrorDoesNotHashDynamicallyNonComparableValues(t *testing.T) {
	for name, payload := range map[string]any{
		"slice": []byte("slice payload"),
		"map":   map[string]int{"key": 1},
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamicComparableError{value: payload}
			if !reflect.TypeOf(err).Comparable() {
				t.Fatal("test precondition: error type is not nominally comparable")
			}
			if reflect.ValueOf(err).Comparable() {
				t.Fatal("test precondition: error value is comparable")
			}

			normalized := detailfanout.NormalizeError(err)
			if normalized == nil || normalized.Error() != err.Error() {
				t.Fatalf("NormalizeError() = %v, want unchanged message %q", normalized, err.Error())
			}
		})
	}
}

func TestNormalizeErrorPreservesSafeCustomMatchers(t *testing.T) {
	isTarget := errors.New("custom Is target")
	classification := &customAsClassification{}
	wrapper := &safeCustomMatcher{
		isTarget:       isTarget,
		classification: classification,
	}

	normalized := detailfanout.NormalizeError(wrapper)
	if normalized != wrapper { //nolint:errorlint // Preserving the wrapper retains its custom semantics.
		t.Fatal("safe custom wrapper was unnecessarily replaced")
	}
	if !errors.Is(normalized, isTarget) {
		t.Fatal("safe custom Is matcher was lost")
	}
	var got *customAsClassification
	if !errors.As(normalized, &got) || got != classification {
		t.Fatalf("safe custom As matcher = %#v, want %#v", got, classification)
	}
}

func TestNormalizeErrorPreservesSafeSingleUnwrapCustomMatchers(t *testing.T) {
	isTarget := errors.New("single custom Is target")
	classification := &customAsClassification{}
	wrapper := &safeSingleCustomMatcher{
		safeCustomMatcher: &safeCustomMatcher{
			isTarget:       isTarget,
			classification: classification,
		},
		child: errors.New("safe child"),
	}

	normalized := detailfanout.NormalizeError(wrapper)
	if normalized != wrapper { //nolint:errorlint // Preserving the wrapper retains its custom semantics.
		t.Fatal("safe single-unwrap custom wrapper was unnecessarily replaced")
	}
	if !errors.Is(normalized, isTarget) {
		t.Fatal("safe single-unwrap custom Is matcher was lost")
	}
	var got *customAsClassification
	if !errors.As(normalized, &got) || got != classification {
		t.Fatalf("safe single-unwrap custom As matcher = %#v, want %#v", got, classification)
	}
}

func TestNormalizeErrorPreservesSafeMultiUnwrapCustomMatchers(t *testing.T) {
	isTarget := errors.New("multi custom Is target")
	classification := &customAsClassification{}
	wrapper := &safeMultiCustomMatcher{
		safeCustomMatcher: &safeCustomMatcher{
			isTarget:       isTarget,
			classification: classification,
		},
		children: []error{errors.New("first safe child"), errors.New("second safe child")},
	}

	normalized := detailfanout.NormalizeError(wrapper)
	if normalized != wrapper { //nolint:errorlint // Preserving the wrapper retains its custom semantics.
		t.Fatal("safe multi-unwrap custom wrapper was unnecessarily replaced")
	}
	if !errors.Is(normalized, isTarget) {
		t.Fatal("safe multi-unwrap custom Is matcher was lost")
	}
	var got *customAsClassification
	if !errors.As(normalized, &got) || got != classification {
		t.Fatalf("safe multi-unwrap custom As matcher = %#v, want %#v", got, classification)
	}
}

func TestRunRejectsNilDetailFailuresFromCustomAs(t *testing.T) {
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return nil, model.Capabilities{}, &nilDetailFailuresMatcher{}
	}, []model.TicketID{"a"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })

	if err != nil || len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("Run = %v, outcomes = %+v; want one fail-closed outcome", err, outcomes)
	}
	if got, want := outcomes[0].Err.Error(), "provider: FetchDetails returned a typed-nil DetailFailures error"; got != want {
		t.Errorf("outcome error = %q, want %q", got, want)
	}
	termtexttest.AssertClean(t, "nil custom DetailFailures", outcomes[0].Err.Error())
}

func TestRunNormalizesDetailFailuresSynthesizedByCustomAs(t *testing.T) {
	var typedNil *panickingTypedNilError
	synthesized := &provider.DetailFailures{Failures: map[model.TicketID]error{"a": typedNil}}
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return nil, model.Capabilities{}, &synthesizedDetailFailuresMatcher{failures: synthesized}
	}, []model.TicketID{"a"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })

	if err != nil || len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("Run = %v, outcomes = %+v; want one normalized fail-closed outcome", err, outcomes)
	}
	if got, want := outcomes[0].Err.Error(), "provider: FetchDetails returned a typed-nil error"; got != want {
		t.Errorf("outcome error = %q, want %q", got, want)
	}
	termtexttest.AssertClean(t, "synthesized DetailFailures", outcomes[0].Err.Error())
}

func TestRunRetainsOversizedDetailFailuresSynthesizedByCustomAs(t *testing.T) {
	failures := map[model.TicketID]error{"a": errors.New("a failed")}
	for i := range 64 {
		failures[model.TicketID(fmt.Sprintf("unrequested-%02d", i))] = errors.New("member failed")
	}
	synthesized := &provider.DetailFailures{Failures: failures}
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return map[model.TicketID]model.Detail{
			"good": {TicketID: "good"},
			"a":    {TicketID: "a"},
		}, model.Capabilities{}, &synthesizedDetailFailuresMatcher{failures: synthesized}
	}, []model.TicketID{"good", "a"}, func(outcome detailfanout.Outcome) {
		outcomes = append(outcomes, outcome)
	})

	if err != nil || len(outcomes) != 2 {
		t.Fatalf("Run = %v, outcomes = %+v; want two deterministic outcomes", err, outcomes)
	}
	if outcomes[0].Err != nil || outcomes[0].Detail.TicketID != "good" {
		t.Errorf("good outcome = %+v, want preserved successful sibling", outcomes[0])
	}
	if outcomes[1].Err == nil || !strings.Contains(outcomes[1].Err.Error(), "both detail and failure") {
		t.Errorf("a outcome = %+v, want fail-closed contradiction", outcomes[1])
	}
}

func TestNormalizeErrorBoundsCustomMatchersAndWideGraphs(t *testing.T) {
	cyclic := &cyclicIsError{}
	cyclic.next = cyclic
	normalized := detailfanout.NormalizeError(cyclic)
	if !errors.Is(normalized, cyclic) {
		t.Fatal("bounded custom-Is root identity was lost")
	}
	done := make(chan bool, 1)
	go func() { done <- errors.Is(normalized, errors.New("other")) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("custom cyclic Is matcher did not terminate")
	}

	cyclicAs := &cyclicAsError{}
	cyclicAs.next = cyclicAs
	normalized = detailfanout.NormalizeError(cyclicAs)
	if !errors.Is(normalized, cyclicAs) {
		t.Fatal("bounded custom-As root identity was lost")
	}
	done = make(chan bool, 1)
	go func() {
		var classified *customAsClassification
		done <- errors.As(normalized, &classified)
	}()
	select {
	case matched := <-done:
		if matched {
			t.Fatal("custom cyclic As matcher unexpectedly classified the error")
		}
	case <-time.After(time.Second):
		t.Fatal("custom cyclic As matcher did not terminate")
	}

	wide := &cyclicMultiUnwrapError{message: "wide", children: make([]error, 1024)}
	for i := range wide.children {
		wide.children[i] = wide
	}
	wide.children[63] = provider.RateLimitErrorf(provider.RateLimitMetadata{}, "late in-budget refusal")
	normalized = detailfanout.NormalizeError(wide)
	if !errors.Is(normalized, wide) {
		t.Fatal("wide bounded graph lost root identity")
	}
	if provider.KindOf(normalized) != provider.KindRateLimit {
		t.Fatalf("wide graph lost late rate refusal: kind=%s", provider.KindOf(normalized))
	}

	empty := make(emptyNonComparableCycleError, 0, 1)
	normalized = detailfanout.NormalizeError(empty)
	if errors.Is(normalized, empty) {
		t.Fatal("empty non-comparable slice identity was unsafely synthesized")
	}
}

func TestRunCallsFetchOnceWithCompleteSliceAndEmitsCanonicalPartialOutcomes(t *testing.T) {
	boom := errors.New("boom")
	var calls int
	var gotIDs []model.TicketID
	f := func(_ context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		calls++
		gotIDs = append([]model.TicketID(nil), ids...)
		return map[model.TicketID]model.Detail{
			"c": {TicketID: "c"},
			"a": {TicketID: "a"},
		}, model.Capabilities{BlockingLinks: true}, &provider.DetailFailures{Failures: map[model.TicketID]error{"b": boom}}
	}
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), f, []model.TicketID{"a", "b", "c"}, func(o detailfanout.Outcome) {
		outcomes = append(outcomes, o)
	})
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if calls != 1 || !reflect.DeepEqual(gotIDs, []model.TicketID{"a", "b", "c"}) {
		t.Errorf("Fetch calls/ids = %d/%v, want 1/[a b c]", calls, gotIDs)
	}
	if got := []model.TicketID{outcomes[0].ID, outcomes[1].ID, outcomes[2].ID}; !reflect.DeepEqual(got, gotIDs) {
		t.Errorf("outcome order = %v, want %v", got, gotIDs)
	}
	if !errors.Is(outcomes[1].Err, boom) || outcomes[0].Err != nil || outcomes[2].Err != nil {
		t.Errorf("outcomes = %+v, want only b to fail", outcomes)
	}
}

func TestRunRejectsTopLevelTypedNilErrorFailClosed(t *testing.T) {
	var typedNil *panickingTypedNilError
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return nil, model.Capabilities{}, typedNil
	}, []model.TicketID{"a", "b"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })

	if err != nil {
		t.Fatalf("Run = %v, want deterministic per-Ticket failures", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want two fail-closed outcomes", outcomes)
	}
	for _, outcome := range outcomes {
		if outcome.Err == nil || outcome.Err.Error() != "provider: FetchDetails returned a typed-nil error" {
			t.Errorf("outcome[%q] = %v, want deterministic typed-nil failure", outcome.ID, outcome.Err)
			continue
		}
		termtexttest.AssertClean(t, "top-level typed-nil failure", outcome.Err.Error())
		if provider.KindOf(outcome.Err) != provider.KindUnknown || errors.Is(outcome.Err, context.Canceled) {
			t.Errorf("outcome[%q] classification/traversal was not safely fail-closed: %v", outcome.ID, outcome.Err)
		}
	}
}

func TestRunRejectsNestedTypedNilErrorBeforeTraversal(t *testing.T) {
	var typedNil *panickingTypedNilError
	wrapped := &typedNilWrapper{err: typedNil}
	safeSibling := errors.New("safe sibling")
	classified := &provider.Error{
		Kind: provider.KindAuth,
		Err:  errors.Join(wrapped, safeSibling),
	}
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return nil, model.Capabilities{}, classified
	}, []model.TicketID{"a"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })

	if err != nil || len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("Run = %v, outcomes = %+v; want one safe fail-closed outcome", err, outcomes)
	}
	failure := outcomes[0].Err
	if got, want := failure.Error(), "provider: FetchDetails returned an error containing a typed-nil cause"; got != want {
		t.Fatalf("nested typed-nil error = %q, want %q", got, want)
	}
	termtexttest.AssertClean(t, "nested typed-nil failure", failure.Error())
	if provider.KindOf(failure) != provider.KindAuth {
		t.Errorf("nested failure kind = %s, want auth", provider.KindOf(failure))
	}
	for name, target := range map[string]error{
		"classified wrapper": classified,
		"nested wrapper":     wrapped,
		"safe sibling":       safeSibling,
	} {
		if !errors.Is(failure, target) {
			t.Errorf("nested normalization lost %s identity", name)
		}
	}
	if errors.Is(failure, context.Canceled) {
		t.Error("nested typed nil was misclassified as cancellation")
	}
}

func TestNormalizeErrorDoesNotDelegateCustomIsOrAs(t *testing.T) {
	var typedNil *panickingTypedNilError
	classified := &provider.Error{Kind: provider.KindAuth, Err: errors.New("classified cause")}
	wrapper := &typedNilCustomClassifier{err: typedNil, classified: classified}

	normalized := detailfanout.NormalizeError(wrapper)
	termtexttest.AssertClean(t, "custom-classified typed nil", normalized.Error())
	if errors.Is(normalized, context.Canceled) {
		t.Error("normalized error delegated to an unsafe custom Is matcher")
	}
	if !errors.Is(normalized, wrapper) {
		t.Error("custom wrapper identity was lost")
	}
	if provider.KindOf(normalized) != provider.KindUnknown {
		t.Errorf("custom As provider kind = %s, want unknown", provider.KindOf(normalized))
	}
	var got *provider.Error
	if errors.As(normalized, &got) {
		t.Errorf("normalized error delegated to unsafe custom As: %#v", got)
	}
	var original *typedNilCustomClassifier
	if errors.As(normalized, &original) {
		t.Errorf("normalized error exposed replaced concrete wrapper: %#v", original)
	}
}

func TestRunRejectsTypedNilDetailFailureConstituentFailClosed(t *testing.T) {
	var typedNil *provider.DetailFailures
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return map[model.TicketID]model.Detail{"a": {TicketID: "a"}}, model.Capabilities{}, &provider.DetailFailures{
			Failures: map[model.TicketID]error{"b": typedNil},
		}
	}, []model.TicketID{"a", "b"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })

	if err != nil {
		t.Fatalf("Run = %v, want constituent failure in the canonical outcome", err)
	}
	if len(outcomes) != 2 || outcomes[0].Err != nil || outcomes[1].Err == nil {
		t.Fatalf("outcomes = %+v, want a success and one fail-closed constituent", outcomes)
	}
	failure := outcomes[1].Err
	if got, want := failure.Error(), "provider: FetchDetails returned a typed-nil DetailFailures error"; got != want {
		t.Errorf("failure = %q, want %q", got, want)
	}
	termtexttest.AssertClean(t, "typed-nil constituent", failure.Error())
	if provider.KindOf(failure) != provider.KindUnknown || errors.Is(failure, context.Canceled) {
		t.Errorf("constituent classification/traversal was not safely fail-closed: %v", failure)
	}
}

func TestNormalizeErrorPreservesAggregateChainAroundNestedTypedNilConstituent(t *testing.T) {
	var typedNil *panickingTypedNilError
	wrapped := &typedNilWrapper{err: typedNil}
	aggregate := &provider.DetailFailures{Failures: map[model.TicketID]error{
		"b": errors.Join(wrapped, context.Canceled),
	}}
	classified := &provider.Error{Kind: provider.KindRateLimit, Err: aggregate}

	normalized := detailfanout.NormalizeError(classified)
	termtexttest.AssertClean(t, "normalized aggregate", normalized.Error())
	if provider.KindOf(normalized) != provider.KindRateLimit {
		t.Errorf("normalized aggregate kind = %s, want rate limit", provider.KindOf(normalized))
	}
	for name, target := range map[string]error{
		"classified wrapper":   classified,
		"original aggregate":   aggregate,
		"nested wrapper":       wrapped,
		"cancellation sibling": context.Canceled,
	} {
		if !errors.Is(normalized, target) {
			t.Errorf("aggregate normalization lost %s identity", name)
		}
	}

	var failures *provider.DetailFailures
	if !errors.As(normalized, &failures) || failures == nil || failures == aggregate {
		t.Fatalf("errors.As = %#v, want a distinct safe DetailFailures clone", failures)
	}
	failure := failures.Failures["b"]
	if failure == nil {
		t.Fatal("normalized aggregate omitted b's failure")
	}
	termtexttest.AssertClean(t, "normalized nested constituent", failure.Error())
	if !errors.Is(failure, wrapped) || !errors.Is(failure, context.Canceled) {
		t.Errorf("normalized constituent lost wrapper or sibling: %v", failure)
	}
}

func TestNormalizeErrorLabelsNestedNilCauseWithoutClaimingTypedNil(t *testing.T) {
	original := &provider.Error{Kind: provider.KindAuth}
	root := fmt.Errorf("outer: %w", original)

	normalized := detailfanout.NormalizeError(root)
	if got, want := normalized.Error(), "provider: FetchDetails returned an error containing a nil cause"; got != want {
		t.Fatalf("nested nil-cause error = %q, want %q", got, want)
	}
	if strings.Contains(normalized.Error(), "typed-nil") {
		t.Fatalf("nested nil-cause diagnostic falsely claims typed-nil: %q", normalized)
	}
	if !errors.Is(normalized, root) || !errors.Is(normalized, original) {
		t.Fatal("nested nil-cause normalization lost wrapper identity")
	}
	if provider.KindOf(normalized) != provider.KindAuth {
		t.Fatalf("nested nil-cause kind = %s, want auth", provider.KindOf(normalized))
	}
	var classified *provider.Error
	if !errors.As(normalized, &classified) || classified == nil || classified == original || classified.Err == nil {
		t.Fatalf("errors.As = %#v, want distinct safe classification", classified)
	}
}

func TestNormalizeErrorRepairsNilCauseProviderClassification(t *testing.T) {
	metadata := provider.RateLimitMetadata{ResetAt: time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)}
	original := &provider.Error{Kind: provider.KindRateLimit, RateLimit: metadata}

	normalized := detailfanout.NormalizeError(original)
	if normalized == original { //nolint:errorlint // The malformed trusted wrapper must be replaced.
		t.Fatal("nil-cause provider classification was returned unchanged")
	}
	if got, want := normalized.Error(), "provider: FetchDetails returned a classified error with a nil cause"; got != want {
		t.Fatalf("normalized error = %q, want %q", got, want)
	}
	termtexttest.AssertClean(t, "nil-cause provider classification", normalized.Error())
	if !errors.Is(normalized, original) {
		t.Fatal("normalization lost original provider wrapper identity")
	}
	var classified *provider.Error
	if !errors.As(normalized, &classified) || classified == nil || classified == original {
		t.Fatalf("errors.As = %#v, want distinct safe provider clone", classified)
	}
	if classified.Kind != provider.KindRateLimit || classified.RateLimit != metadata || classified.Err == nil {
		t.Fatalf("safe classification = %#v, want preserved rate metadata and nonnil cause", classified)
	}
	if errors.Is(classified, original) {
		t.Fatal("safe classification retained the malformed original as a child")
	}
	refusal, ok := provider.InspectRateLimitRefusal(normalized, time.Time{})
	if !ok || refusal.Err != classified || !refusal.KnownReset || !refusal.ResetAt.Equal(metadata.ResetAt) { //nolint:errorlint // Both APIs must expose the same safe clone.
		t.Fatalf("InspectRateLimitRefusal = %+v/%t, want safe known-reset clone", refusal, ok)
	}
}

func TestNormalizeErrorPreservesSafeNilUnwrapper(t *testing.T) {
	original := &safeNilUnwrapError{}
	normalized := detailfanout.NormalizeError(original)
	if normalized != original { //nolint:errorlint // Safe leaf semantics should remain unchanged.
		t.Fatalf("NormalizeError = %#v, want unchanged safe nil-unwrapper", normalized)
	}
	if got := normalized.Error(); got != "safe nil child" {
		t.Fatalf("Error = %q, want safe leaf message", got)
	}
}

func TestNormalizeErrorPreservesDirectRateLimitRefusal(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	knownReset := now.Add(17 * time.Minute)

	tests := []struct {
		name      string
		metadata  provider.RateLimitMetadata
		child     func() error
		wantReset time.Time
		wantKnown bool
	}{
		{
			name: "unknown reset around typed nil",
			child: func() error {
				var typedNil *panickingTypedNilError
				return errors.Join(&typedNilWrapper{err: typedNil}, context.Canceled)
			},
		},
		{
			name:     "known reset around bounded cycle",
			metadata: provider.RateLimitMetadata{ResetAt: knownReset},
			child: func() error {
				cycle := &cyclicUnwrapError{message: "bounded rate-limit child"}
				cycle.next = cycle
				return errors.Join(cycle, context.Canceled)
			},
			wantReset: knownReset,
			wantKnown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := &provider.Error{
				Kind:      provider.KindRateLimit,
				Err:       tt.child(),
				RateLimit: tt.metadata,
			}

			normalized := detailfanout.NormalizeError(classified)
			if normalized == classified { //nolint:errorlint // Replacement is required for the unsafe descendant.
				t.Fatal("unsafe rate-limit wrapper was not normalized")
			}
			if !errors.Is(normalized, classified) {
				t.Fatal("normalization lost the original rate-limit wrapper identity")
			}
			if !errors.Is(normalized, context.Canceled) {
				t.Fatal("normalization lost the real cancellation sibling")
			}

			var safeClassification *provider.Error
			if !errors.As(normalized, &safeClassification) {
				t.Fatal("normalization lost provider error classification")
			}
			if safeClassification == classified {
				t.Fatal("errors.As exposed the original wrapper with its unsafe descendant")
			}
			if safeClassification.Kind != provider.KindRateLimit || safeClassification.RateLimit != tt.metadata {
				t.Fatalf("safe classification = %#v, want rate limit with metadata %#v", safeClassification, tt.metadata)
			}
			if errors.Is(safeClassification, context.Canceled) {
				t.Fatal("safe classification exposed the original cancellation/unsafe graph")
			}

			refusal, ok := provider.InspectRateLimitRefusal(normalized, now)
			if !ok {
				t.Fatal("direct rate-limit inspection lost the normalized outer refusal")
			}
			if refusal.Err != safeClassification { //nolint:errorlint // Both APIs must expose the same safe representative.
				t.Fatalf("inspector representative = %#v, errors.As classification = %#v", refusal.Err, safeClassification)
			}
			if refusal.KnownReset != tt.wantKnown || !refusal.ResetAt.Equal(tt.wantReset) || refusal.ExpiredOnly {
				t.Fatalf("inspection = %+v, want KnownReset=%t ResetAt=%v ExpiredOnly=false", refusal, tt.wantKnown, tt.wantReset)
			}
		})
	}
}

func TestRunDoesNotDuplicateNormalizedRateLimitFailure(t *testing.T) {
	var typedNil *panickingTypedNilError
	classified := &provider.Error{
		Kind: provider.KindRateLimit,
		Err:  errors.Join(&typedNilWrapper{err: typedNil}, context.Canceled),
	}
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return map[model.TicketID]model.Detail{"a": {TicketID: "a"}}, model.Capabilities{}, &provider.DetailFailures{
			Failures: map[model.TicketID]error{"b": classified},
		}
	}, []model.TicketID{"a", "b"}, func(outcome detailfanout.Outcome) {
		outcomes = append(outcomes, outcome)
	})

	if err != nil {
		t.Fatalf("Run = %v, want per-Ticket outcome", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want exactly one outcome per requested Ticket", outcomes)
	}
	if outcomes[0].ID != "a" || outcomes[0].Err != nil || outcomes[1].ID != "b" || outcomes[1].Err == nil {
		t.Fatalf("outcomes = %+v, want one success for a and one failure for b", outcomes)
	}
	if !errors.Is(outcomes[1].Err, classified) || !errors.Is(outcomes[1].Err, context.Canceled) {
		t.Fatalf("b failure lost wrapper identity or cancellation sibling: %v", outcomes[1].Err)
	}
	if _, ok := provider.InspectRateLimitRefusal(outcomes[1].Err, time.Time{}); !ok {
		t.Fatal("b failure lost direct rate-limit inspection")
	}
}

func TestRunTreatsReturnedCancellationWithLiveContextAsAttributedFailure(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			cycle := &cyclicMultiUnwrapError{message: "starving cycle"}
			cycle.children = []error{cycle, cycle}
			responseError := &cyclicMultiUnwrapError{message: "response", children: []error{cycle, sentinel}}
			ids := []model.TicketID{"a", "b"}
			var outcomes []detailfanout.Outcome

			err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
				return nil, model.Capabilities{}, responseError
			}, ids, func(outcome detailfanout.Outcome) {
				outcomes = append(outcomes, outcome)
			})
			if err != nil {
				t.Fatalf("Run = %v, want live-context per-Ticket failures", err)
			}
			if len(outcomes) != len(ids) {
				t.Fatalf("outcomes = %+v, want exactly one per requested Ticket", outcomes)
			}
			for i, outcome := range outcomes {
				if outcome.ID != ids[i] || !errors.Is(outcome.Err, sentinel) {
					t.Fatalf("outcome[%d] = %+v, want attributed %v failure", i, outcome, sentinel)
				}
			}
		})
	}
}

func TestRunDoesNotDuplicateRateLimitOutcomesAfterBoundedTraversal(t *testing.T) {
	refusal := provider.RateLimitErrorf(provider.RateLimitMetadata{}, "rate refused")
	cycle := &cyclicMultiUnwrapError{message: "starving cycle"}
	cycle.children = []error{cycle, cycle}
	responseError := &cyclicMultiUnwrapError{message: "response", children: []error{cycle, refusal}}

	ids := []model.TicketID{"a", "b", "c"}
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return nil, model.Capabilities{}, responseError
	}, ids, func(outcome detailfanout.Outcome) {
		outcomes = append(outcomes, outcome)
	})

	if err != nil {
		t.Fatalf("Run = %v, want response-wide outcomes", err)
	}
	if len(outcomes) != len(ids) {
		t.Fatalf("outcomes = %+v, want exactly one per requested Ticket", outcomes)
	}
	for i, outcome := range outcomes {
		if outcome.ID != ids[i] || outcome.Err == nil {
			t.Fatalf("outcome[%d] = %+v, want failure for %q", i, outcome, ids[i])
		}
		if _, ok := provider.InspectRateLimitRefusal(outcome.Err, time.Time{}); !ok {
			t.Fatalf("outcome[%d] lost bounded rate refusal", i)
		}
	}
}

func TestRunPreservesLargeDefaultAggregateSemanticsAndRawRatePolicy(t *testing.T) {
	ids := make([]model.TicketID, 300)
	for i := range ids {
		ids[i] = model.TicketID(fmt.Sprintf("ticket-%03d", i))
	}
	var calls int
	var rawErr error
	fetch := func(ctx context.Context, requested []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		details, err := provider.FetchDetailsDefault(ctx, requested, func(_ context.Context, id model.TicketID) (model.Detail, error) {
			index := calls
			calls++
			switch {
			case index < 5:
				return model.Detail{TicketID: id}, nil
			case index < 270:
				return model.Detail{}, fmt.Errorf("ordinary failure %d", index)
			default:
				return model.Detail{}, provider.RateLimitErrorf(provider.RateLimitMetadata{}, "rate refused")
			}
		})
		rawErr = err
		return details, model.Capabilities{BlockingLinks: true}, err
	}

	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), fetch, ids, func(outcome detailfanout.Outcome) {
		outcomes = append(outcomes, outcome)
	})
	if err != nil {
		t.Fatalf("Run = %v, want per-Ticket outcomes", err)
	}
	if calls != 271 {
		t.Fatalf("singular calls = %d, want stop at the rate-refused Ticket", calls)
	}
	if len(outcomes) != len(ids) {
		t.Fatalf("outcomes = %d, want %d", len(outcomes), len(ids))
	}
	for i, outcome := range outcomes {
		if outcome.ID != ids[i] {
			t.Fatalf("outcome[%d].ID = %q, want %q", i, outcome.ID, ids[i])
		}
		if i < 5 {
			if outcome.Err != nil || outcome.Detail.TicketID != ids[i] {
				t.Fatalf("outcome[%d] = %+v, want retained success", i, outcome)
			}
			continue
		}
		if outcome.Err == nil {
			t.Fatalf("outcome[%d] = %+v, want one attributed failure", i, outcome)
		}
	}
	if refusal, ok := provider.InspectRateLimitRefusal(detailfanout.NormalizeError(rawErr), time.Time{}); !ok || refusal.KnownReset || refusal.ExpiredOnly {
		t.Fatalf("normalized raw aggregate refusal = %+v/%t, want unknown reset", refusal, ok)
	}
}

func TestRunTurnsResponseWideFailureIntoPerTicketOutcomes(t *testing.T) {
	boom := errors.New("request failed")
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		return map[model.TicketID]model.Detail{"a": {TicketID: "a"}}, model.Capabilities{}, boom
	}, []model.TicketID{"a", "b", "c"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(outcomes) != 3 || outcomes[0].Err != nil || !errors.Is(outcomes[1].Err, boom) || !errors.Is(outcomes[2].Err, boom) {
		t.Errorf("outcomes = %+v, want success then two response-wide failures", outcomes)
	}
}

func TestRunRejectsMalformedNativeResultsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details map[model.TicketID]model.Detail
	}{
		{"missing", map[model.TicketID]model.Detail{"a": {TicketID: "a"}}},
		{"identity mismatch", map[model.TicketID]model.Detail{"a": {TicketID: "wrong"}, "b": {TicketID: "b"}}},
		{"unexpected", map[model.TicketID]model.Detail{"a": {TicketID: "a"}, "b": {TicketID: "b"}, "other": {TicketID: "other"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outcomes []detailfanout.Outcome
			err := detailfanout.Run(t.Context(), func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
				return tc.details, model.Capabilities{}, nil
			}, []model.TicketID{"a", "b"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })
			if tc.name == "unexpected" {
				if err == nil || len(outcomes) != 2 || outcomes[0].Err != nil || outcomes[1].Err != nil {
					t.Errorf("Run/outcomes = %v/%+v, want batch error and two valid successes", err, outcomes)
				}
				return
			}
			failed := 0
			for _, outcome := range outcomes {
				if outcome.Err != nil {
					failed++
				}
			}
			if err != nil || failed == 0 {
				t.Errorf("Run/outcomes = %v/%+v, want malformed output to be a per-ID failure", err, outcomes)
			}
		})
	}
}

func TestRunCancellationEmitsCompletedSuccessesOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(ctx, func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		cancel()
		return map[model.TicketID]model.Detail{"a": {TicketID: "a"}}, model.Capabilities{}, context.Canceled
	}, []model.TicketID{"a", "b"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(outcomes) != 1 || outcomes[0].ID != "a" || outcomes[0].Err != nil {
		t.Errorf("outcomes = %+v, want completed a success only", outcomes)
	}
}

func TestRunCancellationRejectsContradictoryResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	boom := errors.New("failed after producing a Detail")
	var outcomes []detailfanout.Outcome
	err := detailfanout.Run(ctx, func(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
		cancel()
		return map[model.TicketID]model.Detail{"a": {TicketID: "a"}}, model.Capabilities{}, &provider.DetailFailures{
			Failures: map[model.TicketID]error{"a": boom},
		}
	}, []model.TicketID{"a"}, func(o detailfanout.Outcome) { outcomes = append(outcomes, o) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(outcomes) != 0 {
		t.Errorf("outcomes = %+v, want contradictory Detail omitted during cancellation", outcomes)
	}
}

func TestFromProviderPassesCompleteSliceOnce(t *testing.T) {
	p := &pluralSpy{}
	f := detailfanout.FromProvider(p)
	ids := []model.TicketID{"a", "b"}
	details, _, err := f(t.Context(), ids)
	if err != nil {
		t.Fatalf("Fetch = %v, want Details", err)
	}
	if p.pluralCalls != 1 || p.singularCalls != 0 || !reflect.DeepEqual(p.ids, ids) {
		t.Errorf("plural/singular/ids = %d/%d/%v, want 1/0/%v", p.pluralCalls, p.singularCalls, p.ids, ids)
	}
	if len(details) != 2 {
		t.Errorf("details = %v, want two entries", details)
	}
}

type pluralSpy struct {
	pluralCalls   int
	singularCalls int
	ids           []model.TicketID
}

func (*pluralSpy) Name() string                     { return "spy" }
func (*pluralSpy) Capabilities() model.Capabilities { return model.Capabilities{} }
func (*pluralSpy) Resolve(context.Context, provider.Selector) (model.WatchlistSnapshot, error) {
	return model.WatchlistSnapshot{}, nil
}
func (p *pluralSpy) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	p.singularCalls++
	return model.Detail{}, errors.New("singular call")
}
func (p *pluralSpy) FetchDetails(_ context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	p.pluralCalls++
	p.ids = append([]model.TicketID(nil), ids...)
	return map[model.TicketID]model.Detail{"a": {TicketID: "a"}, "b": {TicketID: "b"}}, nil
}
