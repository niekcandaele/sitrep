package tui

import (
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

var blockingCaps = model.Capabilities{BlockingLinks: true}

func markerTicket(id model.TicketID, status model.StatusCategory) model.Ticket {
	return model.Ticket{ID: id, Key: string(id), Title: string(id), Status: status}
}

func blockedByLink(id model.TicketID) model.Link {
	return model.Link{Kind: model.LinkBlockedBy, Target: model.LinkTarget{ID: id}}
}

func blocksLink(id model.TicketID) model.Link {
	return model.Link{Kind: model.LinkBlocks, Target: model.LinkTarget{ID: id}}
}

// A member missing from the links map had its Detail read fail, was never read,
// or was interrupted. Any of those leaves the whole list cold: one unreadable
// Ticket can block anything, so a partial answer is a wrong answer.
func TestActionableMarkersAreColdWhenAMemberIsUnread(t *testing.T) {
	tickets := []model.Ticket{
		markerTicket("A", model.StatusTodo),
		markerTicket("B", model.StatusTodo),
	}
	links := map[model.TicketID][]model.Link{"A": nil}

	if got := actionableMarkers(tickets, links, blockingCaps); got.active {
		t.Errorf("markers are active with B unread: %+v", got)
	}
}

// A key present with no Links is a Detail that was read and genuinely has
// none. That is warmth, and it is what separates it from absence.
func TestActionableMarkersAreWarmWhenEveryMemberWasRead(t *testing.T) {
	tickets := []model.Ticket{
		markerTicket("A", model.StatusTodo),
		markerTicket("B", model.StatusTodo),
		markerTicket("C", model.StatusDone),
	}
	links := map[model.TicketID][]model.Link{
		"A": {blockedByLink("C")},
		"B": {blockedByLink("A")},
		"C": {},
	}

	got := actionableMarkers(tickets, links, blockingCaps)

	if !got.active {
		t.Fatalf("markers are cold with every member read: %+v", got)
	}
	if !got.has("A") {
		t.Error("A is Todo with a Done blocker, so it is Actionable")
	}
	if got.has("B") {
		t.Error("B is blocked by Todo A, so it is not Actionable")
	}
	if got.has("C") {
		t.Error("C is Done, so it is not Actionable")
	}
	if got.count != 1 {
		t.Errorf("count = %d, want the one marked Ticket", got.count)
	}
}

// A Ghost Ticket or a Trail Ticket opened earlier leaves a key in the cache.
// Warmth is planned over the members alone, so those extra keys cannot make a
// list with an unread member look warm.
func TestActionableMarkersIgnoreNonMemberCacheKeys(t *testing.T) {
	tickets := []model.Ticket{
		markerTicket("A", model.StatusTodo),
		markerTicket("B", model.StatusTodo),
	}
	links := map[model.TicketID][]model.Link{"A": nil, "GHOST": nil, "TRAIL": nil}

	if got := actionableMarkers(tickets, links, blockingCaps); got.active {
		t.Errorf("two non-member keys stood in for the unread member B: %+v", got)
	}
}

// A Ticket with no ID cannot be cached, so it must not hold the list cold — and
// it has no Links either, so BuildBlockingGraph never calls it Actionable.
func TestActionableMarkersTolerateATicketWithNoID(t *testing.T) {
	tickets := []model.Ticket{
		markerTicket("A", model.StatusTodo),
		markerTicket("", model.StatusTodo),
	}
	links := map[model.TicketID][]model.Link{"A": nil}

	got := actionableMarkers(tickets, links, blockingCaps)

	if !got.active {
		t.Fatalf("an ID-less Ticket held the list cold: %+v", got)
	}
	if got.has("") {
		t.Error("a Ticket whose Links can never be read was marked Actionable")
	}
	if got.count != 1 {
		t.Errorf("count = %d, want only A", got.count)
	}
}

// A member whose own Detail read failed can still gain a blocker through
// another member's Blocks Link — the reverse edge. "Links unknown" therefore
// never means "no blocking information", and the markers must not treat it as
// warmth on the strength of the edge that was readable.
func TestActionableMarkersAreColdForAReverseEdgeOntoAnUnreadMember(t *testing.T) {
	tickets := []model.Ticket{
		markerTicket("A", model.StatusTodo),
		markerTicket("B", model.StatusTodo),
	}
	// A blocks B, so B has a blocker even though B's own Links were not read.
	links := map[model.TicketID][]model.Link{"A": {blocksLink("B")}}

	if got := actionableMarkers(tickets, links, blockingCaps); got.active {
		t.Errorf("B's failed read was papered over by A's Blocks Link: %+v", got)
	}

	// The graph fails closed underneath as well, so no future warmth rule can
	// make this member Actionable by accident.
	b, ok := model.BuildBlockingGraph(tickets, links, blockingCaps).For("B")
	if !ok {
		t.Fatal("B is not a member of its own graph")
	}
	if b.LinksKnown {
		t.Error("B's Links were never read, so LinksKnown must be false")
	}
	if len(b.Blockers) != 1 {
		t.Errorf("B has %d blockers, want the reverse edge from A", len(b.Blockers))
	}
	if b.Actionable {
		t.Error("a member with unknown Links and an unmet blocker was called Actionable")
	}
}

// A Provider that cannot express "blocks" has nothing to say about direction,
// however full the cache is.
func TestActionableMarkersNeedTheBlockingLinksCapability(t *testing.T) {
	tickets := []model.Ticket{markerTicket("A", model.StatusTodo)}
	links := map[model.TicketID][]model.Link{"A": nil}

	if got := actionableMarkers(tickets, links, model.Capabilities{}); got.active {
		t.Errorf("markers are active without the BlockingLinks Capability: %+v", got)
	}
}

// An empty Watchlist has nothing to claim, and the zero value says so.
func TestActionableMarkersAreColdForAnEmptyWatchlist(t *testing.T) {
	if got := actionableMarkers(nil, nil, blockingCaps); got.active {
		t.Errorf("markers are active over no Tickets: %+v", got)
	}
}

// The zero value never marks anything, which is what makes it safe as the
// "nothing is known" default every cold path returns.
func TestZeroListMarkersMarkNothing(t *testing.T) {
	var zero listMarkers
	if zero.active || zero.count != 0 || zero.has("A") {
		t.Errorf("the zero listMarkers claims something: %+v", zero)
	}
}

func TestActionableEvidenceSnapshotUsesLatestCanonicalEligibleEvidence(t *testing.T) {
	base := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	tickets := []model.Ticket{
		markerTicket("B", model.StatusTodo),
		markerTicket("", model.StatusTodo),
		markerTicket("A", model.StatusTodo),
		markerTicket("B", model.StatusDone),
	}
	details := map[model.TicketID]detailEntry{
		"A": {
			detail:            model.Detail{TicketID: "A"},
			fetchedAt:         base.Add(9 * time.Hour),
			frontierDetail:    model.Detail{TicketID: "A"},
			frontierFetchedAt: base.Add(2 * time.Minute),
			frontierEvidence:  true,
		},
		"B": {
			detail:    model.Detail{TicketID: "B"},
			fetchedAt: base.Add(time.Minute),
		},
		"NON_MEMBER": {
			detail:    model.Detail{TicketID: "NON_MEMBER"},
			fetchedAt: base.Add(24 * time.Hour),
		},
	}

	got, ok := actionableEvidenceSnapshot(tickets, details, listMarkers{active: true})
	if !ok {
		t.Fatal("complete eligible evidence did not produce a snapshot")
	}
	if want := base.Add(2 * time.Minute); !got.Equal(want) {
		t.Errorf("snapshot = %v, want latest eligible member stamp %v", got, want)
	}
}

func TestActionableEvidenceSnapshotRequiresCompleteStampedEligibleCoverage(t *testing.T) {
	at := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	tickets := []model.Ticket{markerTicket("A", model.StatusTodo), markerTicket("B", model.StatusTodo)}
	tests := []struct {
		name    string
		markers listMarkers
		details map[model.TicketID]detailEntry
	}{
		{name: "cold markers", details: map[model.TicketID]detailEntry{"A": {fetchedAt: at}, "B": {fetchedAt: at}}},
		{name: "missing member", markers: listMarkers{active: true}, details: map[model.TicketID]detailEntry{"A": {fetchedAt: at}}},
		{name: "unstamped member", markers: listMarkers{active: true}, details: map[model.TicketID]detailEntry{"A": {fetchedAt: at}, "B": {}}},
		{name: "display only member", markers: listMarkers{active: true}, details: map[model.TicketID]detailEntry{
			"A": {fetchedAt: at},
			"B": {fetchedAt: at, frontierIneligible: true},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := actionableEvidenceSnapshot(tickets, tt.details, tt.markers); ok || !got.IsZero() {
				t.Errorf("snapshot = (%v, %v), want absent", got, ok)
			}
		})
	}
}

func TestFrontierEvidenceStampDoesNotBorrowNewerDisplayAliasTime(t *testing.T) {
	evidenceAt := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	entry := detailEntry{
		detail:             model.Detail{TicketID: "A"},
		fetchedAt:          evidenceAt.Add(time.Hour),
		frontierIneligible: true,
		frontierDetail:     model.Detail{TicketID: "A"},
		frontierFetchedAt:  evidenceAt,
		frontierEvidence:   true,
	}

	got, ok := entry.frontierEvidenceStamp()
	if !ok || !got.Equal(evidenceAt) {
		t.Errorf("evidence stamp = (%v, %v), want (%v, true)", got, ok, evidenceAt)
	}
}

func TestFrontierRebuildRequestsDoNotRestampEvidenceButNewDetailDoes(t *testing.T) {
	evidenceAt := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	retryAt := evidenceAt.Add(7 * time.Minute)
	first := markerTicket("A", model.StatusTodo)
	second := markerTicket("B", model.StatusTodo)
	m := Model{
		mode:                 modeFrontier,
		now:                  func() time.Time { return retryAt },
		frontierGeneration:   3,
		detailFanoutInflight: 1,
		details: map[model.TicketID]detailEntry{
			first.ID: {
				detail:            model.Detail{TicketID: first.ID},
				fetchedAt:         evidenceAt,
				frontierDetail:    model.Detail{TicketID: first.ID},
				frontierFetchedAt: evidenceAt,
				frontierEvidence:  true,
			},
		},
		frontier: frontierState{
			planned: 2,
			input: FrontierInput{
				Tickets:      []model.Ticket{first, second},
				Capabilities: blockingCaps,
			},
		},
	}

	m, firstRebuild := m.requestFrontierRebuild()
	m, secondRebuild := m.requestFrontierRebuild()
	if firstRebuild == nil || secondRebuild != nil {
		t.Fatalf("coalesced rebuild commands = first %v second %v, want one physical timer", firstRebuild != nil, secondRebuild != nil)
	}
	if got := m.details[first.ID].frontierFetchedAt; !got.Equal(evidenceAt) {
		t.Fatalf("rebuild requests moved evidence stamp to %v, want %v", got, evidenceAt)
	}

	updated, _ := m.onFrontierDetail(frontierDetailMsg{
		generation: m.frontierGeneration,
		id:         first.ID,
		detail:     model.Detail{TicketID: first.ID},
		caps:       blockingCaps,
	})
	m = updated.(Model)
	if got := m.details[first.ID].frontierFetchedAt; !got.Equal(retryAt) {
		t.Fatalf("new eligible Detail stamp = %v, want retry landing %v", got, retryAt)
	}
}

func TestAdoptCachedLinksPreservesPaidForEvidenceStamp(t *testing.T) {
	evidenceAt := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	ticket := markerTicket("A", model.StatusTodo)
	m := Model{
		now: func() time.Time { return evidenceAt.Add(time.Hour) },
		details: map[model.TicketID]detailEntry{
			ticket.ID: {
				detail:            model.Detail{TicketID: ticket.ID},
				fetchedAt:         evidenceAt,
				frontierDetail:    model.Detail{TicketID: ticket.ID},
				frontierFetchedAt: evidenceAt,
				frontierEvidence:  true,
			},
		},
		frontier: frontierState{input: FrontierInput{
			Tickets: []model.Ticket{ticket},
			Links:   make(map[model.TicketID][]model.Link),
		}},
	}

	got := m.adoptCachedLinks()
	if _, adopted := got.frontier.input.Links[ticket.ID]; !adopted {
		t.Fatal("eligible cached Links were not adopted")
	}
	if stamp := got.details[ticket.ID].frontierFetchedAt; !stamp.Equal(evidenceAt) {
		t.Errorf("cache adoption moved evidence stamp from %v to %v", evidenceAt, stamp)
	}
}
