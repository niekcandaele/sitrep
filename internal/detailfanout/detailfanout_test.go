package detailfanout_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
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
