package detailfanout_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

func tickets(ids ...model.TicketID) []model.Ticket {
	out := make([]model.Ticket, 0, len(ids))
	for _, id := range ids {
		out = append(out, model.Ticket{ID: id, Key: string(id)})
	}
	return out
}

// Plan is the canonical order every progress count and golden frame derives
// from: the Watchlist's own order, with nothing to fetch twice.
func TestPlanReturnsCanonicalOrder(t *testing.T) {
	got := detailfanout.Plan(tickets("c", "a", "b"), nil)

	want := []model.TicketID{"c", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("Plan = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Plan[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestPlanSkipsEmptyIDsDuplicatesAndWhatTheCallerHolds(t *testing.T) {
	held := map[model.TicketID]bool{"b": true}

	got := detailfanout.Plan(tickets("a", "", "b", "a", "c"), func(id model.TicketID) bool {
		return held[id]
	})

	want := []model.TicketID{"a", "c"}
	if len(got) != len(want) {
		t.Fatalf("Plan = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Plan[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Key presence is the tri-state BuildBlockingGraph reads: a Detail that was
// read and genuinely has no Links is a present key with no entries, and a
// Ticket whose read failed is absent altogether.
func TestLinksWritesAKeyOnlyForAReadDetail(t *testing.T) {
	details := map[model.TicketID]model.Detail{
		"read-with-links": {Links: []model.Link{{Kind: model.LinkBlockedBy}}},
		"read-empty":      {},
	}

	links := detailfanout.Links(details)

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

func TestRunEmitsOneOutcomePerIDAndSurfacesPerIDErrors(t *testing.T) {
	boom := errors.New("boom")
	f := func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		if id == "b" {
			return model.Detail{}, model.Capabilities{}, boom
		}
		return model.Detail{TicketID: id}, model.Capabilities{BlockingLinks: true}, nil
	}

	got := make(map[model.TicketID]error)
	if err := detailfanout.Run(t.Context(), f, []model.TicketID{"a", "b", "c"}, func(o detailfanout.Outcome) {
		got[o.ID] = o.Err
	}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	if len(got) != 3 {
		t.Fatalf("outcomes = %v, want one per id", got)
	}
	if !errors.Is(got["b"], boom) {
		t.Errorf("outcome for b = %v, want %v", got["b"], boom)
	}
	if got["a"] != nil || got["c"] != nil {
		t.Errorf("outcomes = %v, want only b to fail", got)
	}
}

func TestRunNeverExceedsParallelism(t *testing.T) {
	var inflight, peak atomic.Int64
	release := make(chan struct{})
	f := func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		n := inflight.Add(1)
		for {
			was := peak.Load()
			if n <= was || peak.CompareAndSwap(was, n) {
				break
			}
		}
		<-release
		inflight.Add(-1)
		return model.Detail{TicketID: id}, model.Capabilities{}, nil
	}

	ids := make([]model.TicketID, 0, 32)
	for i := range 32 {
		ids = append(ids, model.TicketID(string(rune('a'+i%26)))+model.TicketID(rune('0'+i/26)))
	}
	done := make(chan error, 1)
	go func() {
		done <- detailfanout.Run(t.Context(), f, ids, func(detailfanout.Outcome) {})
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	if peak.Load() > detailfanout.Parallelism {
		t.Errorf("peak in flight = %d, want at most %d", peak.Load(), detailfanout.Parallelism)
	}
}

// An interrupted fan-out simply stops issuing: the ids it never reached are
// un-emitted, which is what leaves their Links unknown and their dependents
// not Actionable.
func TestRunReturnsPromptlyOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var calls atomic.Int64
	f := func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		if calls.Add(1) == 1 {
			cancel()
		}
		return model.Detail{TicketID: id}, model.Capabilities{}, nil
	}

	ids := make([]model.TicketID, 0, 200)
	for i := range 200 {
		ids = append(ids, model.TicketID(rune('a'+i%26))+model.TicketID(rune('0'+i/26)))
	}
	var emitted int
	err := detailfanout.Run(ctx, f, ids, func(detailfanout.Outcome) { emitted++ })

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if emitted >= len(ids) {
		t.Errorf("emitted %d of %d, want the cancellation to stop the fan-out short", emitted, len(ids))
	}
}

// FromProvider is the seam a one-shot renderer already holds: one call is
// exactly one FetchDetail, with the Provider's Capabilities attached.
func TestFromProviderReadsOneDetailPerCall(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	snap := fake.FixtureBlockingSnapshot()
	f := detailfanout.FromProvider(p)

	d, caps, err := f(t.Context(), snap.Tickets[0].ID)

	if err != nil {
		t.Fatalf("Fetch = %v, want a Detail", err)
	}
	if d.TicketID != snap.Tickets[0].ID {
		t.Errorf("Detail.TicketID = %q, want %q", d.TicketID, snap.Tickets[0].ID)
	}
	if !caps.BlockingLinks {
		t.Error("Capabilities did not come from the Provider")
	}
	if p.DetailCalls() != 1 {
		t.Errorf("DetailCalls = %d, want exactly 1", p.DetailCalls())
	}
}
