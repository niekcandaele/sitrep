package fake_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// The fake must satisfy the interface every other Provider does.
var _ provider.Provider = (*fake.Provider)(nil)

const richTicket = model.TicketID("acme/widgets#112")

// testRef is any Epic Ref: the fake serves them all alike.
func testRef(raw string) ref.Ref {
	return ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Raw: raw}
}

func TestNameAndCapabilities(t *testing.T) {
	p := fake.New()
	if p.Name() != "fake" {
		t.Errorf("Name() = %q, want %q", p.Name(), "fake")
	}
	want := model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true}
	if got := p.Capabilities(); got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestFetchEpicIsDeterministic(t *testing.T) {
	p := fake.New()

	first, err := p.FetchEpic(context.Background(), testRef("111"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	second, err := p.FetchEpic(context.Background(), testRef("something-else"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Error("two FetchEpic calls returned different snapshots")
	}
	if !first.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want the zero time: the caller stamps it", first.FetchedAt)
	}
	if len(first.Tickets) == 0 {
		t.Fatal("the fixture epic has no tickets")
	}
}

// The fixture is shared by every downstream test, so a caller mutating what it
// got must not be able to corrupt it.
func TestFetchEpicReturnsADefensiveCopy(t *testing.T) {
	p := fake.New()

	snap, err := p.FetchEpic(context.Background(), testRef("111"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	wantTitle := snap.Tickets[0].Title
	snap.Tickets[0].Title = "clobbered"
	snap.Tickets = snap.Tickets[:1]

	again, err := p.FetchEpic(context.Background(), testRef("111"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if again.Tickets[0].Title != wantTitle {
		t.Errorf("ticket title = %q, want %q: the fixture was mutated by a caller", again.Tickets[0].Title, wantTitle)
	}
	if len(again.Tickets) < 2 {
		t.Errorf("got %d tickets, want the whole fixture back", len(again.Tickets))
	}
}

func TestFixtureSpansTheModel(t *testing.T) {
	snap, err := fake.New().FetchEpic(context.Background(), testRef("111"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}

	seen := map[model.StatusCategory]bool{}
	var withParent, crossRepo, withoutPR, withPR int
	prStates := map[model.PRState]bool{}
	for _, ticket := range snap.Tickets {
		seen[ticket.Status] = true
		if ticket.ParentID != "" {
			withParent++
		}
		if ticket.Repository != snap.Tickets[0].Repository {
			crossRepo++
		}
		if len(ticket.PullRequests) == 0 {
			withoutPR++
		} else {
			withPR++
		}
		for _, pr := range ticket.PullRequests {
			prStates[pr.State] = true
		}
	}

	for _, status := range []model.StatusCategory{
		model.StatusTodo, model.StatusInProgress, model.StatusDone, model.StatusCancelled,
	} {
		if !seen[status] {
			t.Errorf("the fixture has no %s ticket", status)
		}
	}
	for _, state := range []model.PRState{model.PRDraft, model.PROpen, model.PRMerged} {
		if !prStates[state] {
			t.Errorf("the fixture has no %s pull request", state)
		}
	}
	if withParent == 0 {
		t.Error("the fixture has no ticket with a parent")
	}
	if crossRepo == 0 {
		t.Error("the fixture has no cross-repo ticket")
	}
	if withoutPR == 0 || withPR == 0 {
		t.Errorf("want tickets both with and without pull requests, got %d with and %d without", withPR, withoutPR)
	}
}

func TestWithCapabilitiesStripsTheData(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{}))

	snap, err := p.FetchEpic(context.Background(), testRef("111"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if snap.Capabilities != (model.Capabilities{}) {
		t.Errorf("snapshot capabilities = %+v, want none declared", snap.Capabilities)
	}
	for _, ticket := range snap.Tickets {
		if ticket.PullRequests != nil {
			t.Errorf("ticket %s kept pull requests without the capability", ticket.Key)
		}
		if ticket.ParentID != "" {
			t.Errorf("ticket %s kept a parent without the hierarchy capability", ticket.Key)
		}
	}

	detail, err := p.FetchDetail(context.Background(), richTicket)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if len(detail.Comments) != 0 {
		t.Errorf("detail kept %d comments without the capability", len(detail.Comments))
	}
	for _, link := range detail.Links {
		if link.Kind != model.LinkRelates {
			t.Errorf("detail kept a %s link without the blocking-links capability", link.Kind)
		}
	}
}

func TestFetchDetailServesTheFixture(t *testing.T) {
	p := fake.New()

	detail, err := p.FetchDetail(context.Background(), richTicket)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if detail.TicketID != richTicket {
		t.Errorf("TicketID = %q, want %q", detail.TicketID, richTicket)
	}
	if len(detail.Comments) < 2 {
		t.Errorf("got %d comments, want the rich fixture's several", len(detail.Comments))
	}

	kinds := map[model.LinkKind]bool{}
	for _, link := range detail.Links {
		kinds[link.Kind] = true
		if link.NativeLabel == "" {
			t.Errorf("link to %s has no native label", link.Target.Key)
		}
	}
	for _, kind := range []model.LinkKind{model.LinkRelates, model.LinkBlockedBy, model.LinkBlocks} {
		if !kinds[kind] {
			t.Errorf("the rich fixture detail has no %s link", kind)
		}
	}

	if _, err := p.FetchDetail(context.Background(), "no-such-ticket"); err == nil {
		t.Error("FetchDetail of an unknown ticket succeeded, want an error")
	} else if !strings.Contains(err.Error(), "no-such-ticket") {
		t.Errorf("error = %q, want it to name the ticket", err)
	}
}

func TestWithSnapshotsAdvancesAndRepeatsTheLast(t *testing.T) {
	first := model.EpicSnapshot{Epic: model.Epic{Key: "#1"}}
	second := model.EpicSnapshot{Epic: model.Epic{Key: "#2"}}
	p := fake.New(fake.WithSnapshots(first, second))

	want := []string{"#1", "#2", "#2"}
	for i, key := range want {
		snap, err := p.FetchEpic(context.Background(), testRef("111"))
		if err != nil {
			t.Fatalf("FetchEpic %d: %v", i, err)
		}
		if snap.Epic.Key != key {
			t.Errorf("call %d returned epic %q, want %q", i+1, snap.Epic.Key, key)
		}
	}
	if got := p.EpicCalls(); got != len(want) {
		t.Errorf("EpicCalls() = %d, want %d", got, len(want))
	}
}

func TestWithSnapshotReplacesTheFixture(t *testing.T) {
	replacement := model.EpicSnapshot{Epic: model.Epic{Key: "#42"}}
	p := fake.New(fake.WithSnapshot(replacement))

	snap, err := p.FetchEpic(context.Background(), testRef("111"))
	if err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if snap.Epic.Key != "#42" || len(snap.Tickets) != 0 {
		t.Errorf("snapshot = %+v, want the replacement", snap.Epic)
	}
}

func TestErrorInjection(t *testing.T) {
	boom := errors.New("boom")

	p := fake.New(fake.WithEpicError(boom), fake.WithDetailError(boom))

	if _, err := p.FetchEpic(context.Background(), testRef("111")); !errors.Is(err, boom) {
		t.Errorf("FetchEpic error = %v, want %v", err, boom)
	}
	if _, err := p.FetchDetail(context.Background(), richTicket); !errors.Is(err, boom) {
		t.Errorf("FetchDetail error = %v, want %v", err, boom)
	}
	if got := p.EpicCalls(); got != 1 {
		t.Errorf("EpicCalls() = %d, want 1: failed calls count too", got)
	}
	if got := p.DetailCalls(); got != 1 {
		t.Errorf("DetailCalls() = %d, want 1: failed calls count too", got)
	}
}

func TestCallCounters(t *testing.T) {
	p := fake.New()

	if got := p.DetailCalls(); got != 0 {
		t.Errorf("DetailCalls() = %d on a fresh Provider, want 0", got)
	}

	for range 2 {
		if _, err := p.FetchEpic(context.Background(), testRef("111")); err != nil {
			t.Fatalf("FetchEpic: %v", err)
		}
	}
	if _, err := p.FetchDetail(context.Background(), richTicket); err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if got := p.EpicCalls(); got != 2 {
		t.Errorf("EpicCalls() = %d, want 2", got)
	}
	if got := p.DetailCalls(); got != 1 {
		t.Errorf("DetailCalls() = %d, want 1", got)
	}
	if got := p.DetailCallsFor(richTicket); got != 1 {
		t.Errorf("DetailCallsFor(%q) = %d, want 1", richTicket, got)
	}
	if got := p.DetailCallsFor("acme/widgets#999"); got != 0 {
		t.Errorf("DetailCallsFor(untouched ticket) = %d, want 0", got)
	}
}

func TestCancelledContext(t *testing.T) {
	p := fake.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := p.FetchEpic(ctx, testRef("111")); !errors.Is(err, context.Canceled) {
		t.Errorf("FetchEpic error = %v, want context.Canceled", err)
	}
	if _, err := p.FetchDetail(ctx, richTicket); !errors.Is(err, context.Canceled) {
		t.Errorf("FetchDetail error = %v, want context.Canceled", err)
	}
}

// A delay must be interruptible: the TUI cancels in-flight fetches.
func TestWithDelayRespectsCancellation(t *testing.T) {
	p := fake.New(fake.WithDelay(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := p.FetchEpic(ctx, testRef("111"))
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("FetchEpic error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchEpic ignored the cancelled context and kept waiting")
	}
}

// The TUI fetches from goroutines, so the counters and the snapshot cursor have
// to hold up under -race.
func TestConcurrentUse(t *testing.T) {
	p := fake.New()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.FetchEpic(context.Background(), testRef("111")); err != nil {
				t.Errorf("FetchEpic: %v", err)
			}
			if _, err := p.FetchDetail(context.Background(), richTicket); err != nil {
				t.Errorf("FetchDetail: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := p.EpicCalls(); got != 8 {
		t.Errorf("EpicCalls() = %d, want 8", got)
	}
	if got := p.DetailCallsFor(richTicket); got != 8 {
		t.Errorf("DetailCallsFor() = %d, want 8", got)
	}
}

// The fake ignores the Epic Ref's content but records it, so a caller's ref
// resolution can be asserted at the seam where it lands.
func TestLastRef(t *testing.T) {
	p := fake.New()
	if got := p.LastRef(); got != (ref.Ref{}) {
		t.Errorf("LastRef() before any fetch = %+v, want the zero Ref", got)
	}

	want := testRef("111")
	if _, err := p.FetchEpic(context.Background(), want); err != nil {
		t.Fatalf("FetchEpic: %v", err)
	}
	if got := p.LastRef(); got != want {
		t.Errorf("LastRef() = %+v, want %+v", got, want)
	}
}
