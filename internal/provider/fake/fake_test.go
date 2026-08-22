package fake_test

import (
	"context"
	"errors"
	"fmt"
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

// testRef is any Ref: the fake serves them all alike.
func testRef(raw string) ref.Ref {
	return ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Raw: raw}
}

func fixtureRef(number int) ref.Ref {
	return ref.Ref{
		Tracker: ref.TrackerGitHub,
		Host:    "github.com",
		Owner:   "acme",
		Repo:    "widgets",
		Number:  number,
		Raw:     fmt.Sprintf("acme/widgets#%d", number),
	}
}

func TestNameAndCapabilities(t *testing.T) {
	p := fake.New()
	if p.Name() != "fake" {
		t.Errorf("Name() = %q, want %q", p.Name(), "fake")
	}
	want := model.Capabilities{
		Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true,
		Selectors: model.SelectorCapabilities{Epic: true, RefList: true, Query: true},
	}
	if got := p.Capabilities(); got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestResolveIsDeterministic(t *testing.T) {
	p := fake.New()

	first, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("something-else")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Error("two Resolve calls returned different snapshots")
	}
	if !first.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want the zero time: the caller stamps it", first.FetchedAt)
	}
	if len(first.Tickets) == 0 {
		t.Fatal("the fixture epic has no tickets")
	}
}

func TestResolveRefListKeepsOrderAndHasNoOuterEpic(t *testing.T) {
	p := fake.New()
	selector := provider.RefListSelector{Refs: []ref.Ref{fixtureRef(121), fixtureRef(112), fixtureRef(115)}}

	snap, err := p.Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantIDs := []model.TicketID{"acme/widgets#121", "acme/widgets#112", "acme/widgets#115"}
	gotIDs := make([]model.TicketID, len(snap.Tickets))
	for i, ticket := range snap.Tickets {
		gotIDs[i] = ticket.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("Tickets = %v, want %v", gotIDs, wantIDs)
	}
	if snap.Header != provider.RefListHeader(3) {
		t.Errorf("Header = %+v, want 3 tickets", snap.Header)
	}
	if !reflect.DeepEqual(snap.Epic, model.Epic{}) || !snap.Parent.IsZero() {
		t.Errorf("Ref-list snapshot has an outer Epic/Parent: %+v / %+v", snap.Epic, snap.Parent)
	}
	if snap.Tickets == nil {
		t.Error("Tickets is nil on success")
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestResolveQueryUsesCurrentMembershipAndExactHeader(t *testing.T) {
	first := fake.FixtureSnapshot()
	first.Epic = model.Epic{Key: "ignored"}
	first.Parent = model.Parent{Key: "ignored"}
	first.Tickets = first.Tickets[:1]
	second := fake.FixtureSnapshot()
	second.Tickets = second.Tickets[1:3]
	p := fake.New(fake.WithSnapshots(first, second))
	selector := provider.QuerySelector{Query: "  state=opened&labels=agent  "}

	before, err := p.Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	before.Tickets[0].Title = "clobbered"
	selector.Query = "changed by caller"

	after, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "  state=opened&labels=agent  "})
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if before.Header != provider.QueryHeader("  state=opened&labels=agent  ") || after.Header != before.Header {
		t.Errorf("headers = %+v then %+v, want exact Query header", before.Header, after.Header)
	}
	if !reflect.DeepEqual(before.Epic, model.Epic{}) || !before.Parent.IsZero() {
		t.Errorf("Query snapshot has an outer Epic/Parent: %+v / %+v", before.Epic, before.Parent)
	}
	if len(before.Tickets) != 1 || len(after.Tickets) != 2 {
		t.Fatalf("membership = %d then %d, want 1 then 2", len(before.Tickets), len(after.Tickets))
	}
	if after.Tickets[0].Title == "clobbered" {
		t.Error("Resolve exposed a configured snapshot's Ticket storage")
	}
	stored := p.LastSelector().(provider.QuerySelector)
	if stored.Query != "  state=opened&labels=agent  " {
		t.Errorf("stored Query = %q, want exact value", stored.Query)
	}
	if !after.FetchedAt.IsZero() || p.DetailCalls() != 0 {
		t.Errorf("FetchedAt/Detail calls = %v/%d, want zero/none", after.FetchedAt, p.DetailCalls())
	}
}

func TestResolveQueryAppliesMaxTicketsOnlyAboveTheBoundary(t *testing.T) {
	tickets := []model.Ticket{
		{ID: "one", Key: "one", Title: "One"},
		{ID: "two", Key: "two", Title: "Two"},
		{ID: "three", Key: "three", Title: "Three"},
	}
	tests := []struct {
		name         string
		maxTickets   int
		membership   int
		wantTickets  int
		limitReached bool
	}{
		{name: "below", maxTickets: 3, membership: 2, wantTickets: 2},
		{name: "exact boundary", maxTickets: 3, membership: 3, wantTickets: 3},
		{name: "above", maxTickets: 2, membership: 3, wantTickets: 2, limitReached: true},
		{name: "singular cutoff", maxTickets: 1, membership: 3, wantTickets: 1, limitReached: true},
		{name: "non-positive option keeps default", maxTickets: 0, membership: 3, wantTickets: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fake.New(
				fake.WithMaxTickets(tt.maxTickets),
				fake.WithSnapshot(model.WatchlistSnapshot{Tickets: append([]model.Ticket(nil), tickets[:tt.membership]...)}),
			)
			snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "opaque"})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(snap.Tickets) != tt.wantTickets || snap.LimitReached != tt.limitReached {
				t.Errorf("Tickets/LimitReached = %d/%v, want %d/%v", len(snap.Tickets), snap.LimitReached, tt.wantTickets, tt.limitReached)
			}
			for i := range snap.Tickets {
				if snap.Tickets[i].ID != tickets[i].ID {
					t.Errorf("Tickets[%d].ID = %q, want ordered prefix %q", i, snap.Tickets[i].ID, tickets[i].ID)
				}
			}
			if !reflect.DeepEqual(snap.Epic, model.Epic{}) || !snap.Parent.IsZero() || snap.Header != provider.QueryHeader("opaque") {
				t.Errorf("Query shape = Header %+v Epic %+v Parent %+v", snap.Header, snap.Epic, snap.Parent)
			}
			if p.DetailCalls() != 0 {
				t.Errorf("DetailCalls = %d, want 0", p.DetailCalls())
			}
		})
	}
}

func TestResolveQueryRecomputesLimitReachedOnEverySnapshot(t *testing.T) {
	snapshot := func(count int) model.WatchlistSnapshot {
		tickets := make([]model.Ticket, count)
		for i := range tickets {
			tickets[i] = model.Ticket{ID: model.TicketID(fmt.Sprintf("ticket-%d", i))}
		}
		return model.WatchlistSnapshot{Tickets: tickets, LimitReached: true}
	}
	p := fake.New(fake.WithMaxTickets(2), fake.WithSnapshots(snapshot(1), snapshot(3), snapshot(2)))
	for i, want := range []bool{false, true, false} {
		snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "same query"})
		if err != nil {
			t.Fatalf("Resolve %d: %v", i, err)
		}
		if snap.LimitReached != want {
			t.Errorf("Resolve %d LimitReached = %v, want %v", i, snap.LimitReached, want)
		}
		if len(snap.Tickets) > 2 {
			t.Errorf("Resolve %d returned %d Tickets, want at most 2", i, len(snap.Tickets))
		}
	}
	if p.ResolveCalls() != 3 {
		t.Errorf("ResolveCalls = %d, want 3", p.ResolveCalls())
	}
}

func TestMaxTicketsDoesNotCapEpicOrRefList(t *testing.T) {
	snapshot := fake.FixtureSnapshot()
	snapshot.LimitReached = true
	p := fake.New(fake.WithMaxTickets(1), fake.WithSnapshot(snapshot))

	epic, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Epic Resolve: %v", err)
	}
	if len(epic.Tickets) != len(snapshot.Tickets) || epic.LimitReached {
		t.Errorf("Epic Tickets/LimitReached = %d/%v, want %d/false", len(epic.Tickets), epic.LimitReached, len(snapshot.Tickets))
	}

	refs := []ref.Ref{fixtureRef(112), fixtureRef(115), fixtureRef(121)}
	list, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refs})
	if err != nil {
		t.Fatalf("Ref-list Resolve: %v", err)
	}
	if len(list.Tickets) != len(refs) || list.LimitReached {
		t.Errorf("Ref-list Tickets/LimitReached = %d/%v, want %d/false", len(list.Tickets), list.LimitReached, len(refs))
	}
}

func TestResolveQueryReturnsNonNilEmptyMembership(t *testing.T) {
	p := fake.New(fake.WithSnapshot(model.WatchlistSnapshot{Tickets: nil}))
	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: ""})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Tickets == nil || len(snap.Tickets) != 0 {
		t.Errorf("Tickets = %#v, want non-nil empty", snap.Tickets)
	}
	if snap.Header != provider.QueryHeader("") {
		t.Errorf("Header = %+v, want explicit empty Query", snap.Header)
	}
}

func TestUnsupportedQueryFailsBeforeDelayContextAndInjectedError(t *testing.T) {
	boom := errors.New("injected later-stage error")
	p := fake.New(
		fake.WithCapabilities(model.Capabilities{Selectors: model.SelectorCapabilities{Epic: true}}),
		fake.WithResolveError(boom),
		fake.WithDelay(time.Hour),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Resolve(ctx, provider.QuerySelector{Query: "q"})
	if err == nil || provider.KindOf(err) != provider.KindBadRef {
		t.Fatalf("error = %v (kind %v), want selector KindBadRef", err, provider.KindOf(err))
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, boom) {
		t.Errorf("error = %v, Query support must be checked first", err)
	}
	if got, want := err.Error(), "fake: Query Selector is not supported"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if p.ResolveCalls() != 1 {
		t.Errorf("ResolveCalls = %d, want failed call counted", p.ResolveCalls())
	}
}

func TestResolveRefListHeaderPluralization(t *testing.T) {
	p := fake.New()
	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: []ref.Ref{fixtureRef(112)}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Header.Title != "1 ticket" {
		t.Errorf("Header.Title = %q, want 1 ticket", snap.Header.Title)
	}
}

func TestResolveRefListDefensivelyCopiesSelectorAndTickets(t *testing.T) {
	p := fake.New()
	refs := []ref.Ref{fixtureRef(112), fixtureRef(115)}
	snap, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: refs})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	refs[0] = fixtureRef(121)
	snap.Tickets[0].Title = "clobbered"

	stored := p.LastSelector().(provider.RefListSelector)
	if stored.Refs[0].Number != 112 {
		t.Errorf("stored Selector mutated to %+v", stored.Refs[0])
	}
	stored.Refs[0] = fixtureRef(121)
	if again := p.LastSelector().(provider.RefListSelector); again.Refs[0].Number != 112 {
		t.Errorf("LastSelector exposed its Ref slice: %+v", again.Refs)
	}

	again, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: []ref.Ref{fixtureRef(112), fixtureRef(115)}})
	if err != nil {
		t.Fatalf("Resolve again: %v", err)
	}
	if again.Tickets[0].Title == "clobbered" {
		t.Error("Resolve exposed the fixture Ticket slice")
	}
}

func TestResolveRefListKeepsMembershipAcrossSnapshots(t *testing.T) {
	before := fake.FixtureSnapshot()
	after := fake.FixtureSnapshot()
	after.Tickets[0].Status = model.StatusDone
	after.Tickets = append(after.Tickets, model.Ticket{ID: "acme/widgets#999", Key: "#999"})
	p := fake.New(fake.WithSnapshots(before, after))
	selector := provider.RefListSelector{Refs: []ref.Ref{fixtureRef(112), fixtureRef(115)}}

	first, err := p.Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	second, err := p.Resolve(context.Background(), selector)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if len(first.Tickets) != 2 || len(second.Tickets) != 2 {
		t.Fatalf("membership changed: %d then %d", len(first.Tickets), len(second.Tickets))
	}
	if second.Tickets[0].ID != "acme/widgets#112" || second.Tickets[0].Status != model.StatusDone {
		t.Errorf("second first Ticket = %+v, want refreshed #112", second.Tickets[0])
	}
	if p.DetailCalls() != 0 {
		t.Errorf("DetailCalls = %d, want 0", p.DetailCalls())
	}
}

func TestResolveEmptyRefListFailsBeforeServingSnapshot(t *testing.T) {
	p := fake.New()
	_, err := p.Resolve(context.Background(), provider.RefListSelector{Refs: []ref.Ref{}})
	if err == nil || provider.KindOf(err) != provider.KindBadRef {
		t.Fatalf("error = %v (kind %v), want KindBadRef", err, provider.KindOf(err))
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestResolveRejectsPointerSelector(t *testing.T) {
	p := fake.New()
	_, err := p.Resolve(context.Background(), &provider.EpicSelector{Ref: testRef("111")})
	if err == nil || provider.KindOf(err) != provider.KindBadRef {
		t.Fatalf("error = %v (kind %v), want unsupported KindBadRef", err, provider.KindOf(err))
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

// The fixture is shared by every downstream test, so a caller mutating what it
// got must not be able to corrupt it.
func TestResolveReturnsADefensiveCopy(t *testing.T) {
	p := fake.New()

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantTitle := snap.Tickets[0].Title
	snap.Tickets[0].Title = "clobbered"
	snap.Tickets = snap.Tickets[:1]

	again, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if again.Tickets[0].Title != wantTitle {
		t.Errorf("ticket title = %q, want %q: the fixture was mutated by a caller", again.Tickets[0].Title, wantTitle)
	}
	if len(again.Tickets) < 2 {
		t.Errorf("got %d tickets, want the whole fixture back", len(again.Tickets))
	}
}

func TestFixtureSpansTheModel(t *testing.T) {
	snap, err := fake.New().Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	p := fake.New(fake.WithCapabilities(model.Capabilities{
		Selectors: model.SelectorCapabilities{Epic: true},
	}))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantCapabilities := model.Capabilities{Selectors: model.SelectorCapabilities{Epic: true}}
	if snap.Capabilities != wantCapabilities {
		t.Errorf("snapshot capabilities = %+v, want %+v", snap.Capabilities, wantCapabilities)
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
	first := model.WatchlistSnapshot{Epic: model.Epic{Key: "#1"}}
	second := model.WatchlistSnapshot{Epic: model.Epic{Key: "#2"}}
	p := fake.New(fake.WithSnapshots(first, second))

	want := []string{"#1", "#2", "#2"}
	for i, key := range want {
		snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
		if err != nil {
			t.Fatalf("Resolve %d: %v", i, err)
		}
		if snap.Epic.Key != key {
			t.Errorf("call %d returned epic %q, want %q", i+1, snap.Epic.Key, key)
		}
	}
	if got := p.ResolveCalls(); got != len(want) {
		t.Errorf("ResolveCalls() = %d, want %d", got, len(want))
	}
}

func TestWithSnapshotReplacesTheFixture(t *testing.T) {
	replacement := model.WatchlistSnapshot{Epic: model.Epic{Key: "#42"}}
	p := fake.New(fake.WithSnapshot(replacement))

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if snap.Epic.Key != "#42" || len(snap.Tickets) != 0 {
		t.Errorf("snapshot = %+v, want the replacement", snap.Epic)
	}
}

func TestErrorInjection(t *testing.T) {
	boom := errors.New("boom")

	p := fake.New(fake.WithResolveError(boom), fake.WithDetailError(boom))

	if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")}); !errors.Is(err, boom) {
		t.Errorf("Resolve error = %v, want %v", err, boom)
	}
	if _, err := p.FetchDetail(context.Background(), richTicket); !errors.Is(err, boom) {
		t.Errorf("FetchDetail error = %v, want %v", err, boom)
	}
	if got := p.ResolveCalls(); got != 1 {
		t.Errorf("ResolveCalls() = %d, want 1: failed calls count too", got)
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
		if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")}); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	if _, err := p.FetchDetail(context.Background(), richTicket); err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if got := p.ResolveCalls(); got != 2 {
		t.Errorf("ResolveCalls() = %d, want 2", got)
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

	if _, err := p.Resolve(ctx, provider.EpicSelector{Ref: testRef("111")}); !errors.Is(err, context.Canceled) {
		t.Errorf("Resolve error = %v, want context.Canceled", err)
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
		_, err := p.Resolve(ctx, provider.EpicSelector{Ref: testRef("111")})
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Resolve error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve ignored the cancelled context and kept waiting")
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
			if _, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: testRef("111")}); err != nil {
				t.Errorf("Resolve: %v", err)
			}
			if _, err := p.FetchDetail(context.Background(), richTicket); err != nil {
				t.Errorf("FetchDetail: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := p.ResolveCalls(); got != 8 {
		t.Errorf("ResolveCalls() = %d, want 8", got)
	}
	if got := p.DetailCallsFor(richTicket); got != 8 {
		t.Errorf("DetailCallsFor() = %d, want 8", got)
	}
}

// The fake ignores the Selector's content but records it, so caller-side
// construction can be asserted at the seam where it lands.
func TestLastSelector(t *testing.T) {
	p := fake.New()
	if got := p.LastSelector(); got != nil {
		t.Errorf("LastSelector() before any resolve = %+v, want nil", got)
	}

	want := provider.EpicSelector{Ref: testRef("111")}
	if _, err := p.Resolve(context.Background(), want); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := p.LastSelector(); got != want {
		t.Errorf("LastSelector() = %+v, want %+v", got, want)
	}
}
