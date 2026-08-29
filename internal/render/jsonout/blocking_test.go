package jsonout_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
)

// blockingWire is the part of the Watchlist document this ticket adds, decoded
// with pointers throughout so an absent key stays distinguishable from false.
type blockingWire struct {
	Blocking *struct {
		Cycles [][]string `json:"cycles"`
	} `json:"blocking"`
	Tickets []struct {
		Key           string `json:"key"`
		Actionable    *bool  `json:"actionable"`
		LinksKnown    *bool  `json:"links_known"`
		InCycle       *bool  `json:"in_cycle"`
		Status        string `json:"status"`
		NativeStatus  string `json:"native_status"`
		UnmetBlockers []struct {
			ID           string `json:"id"`
			Key          string `json:"key"`
			Title        string `json:"title"`
			URL          string `json:"url"`
			Status       string `json:"status"`
			NativeStatus string `json:"native_status"`
			Member       bool   `json:"member"`
			StatusKnown  bool   `json:"status_known"`
		} `json:"unmet_blockers"`
	} `json:"tickets"`
}

func renderWatchlist(t *testing.T, snap model.WatchlistSnapshot, graph *model.BlockingGraph) []byte {
	t.Helper()

	var buf bytes.Buffer
	err := jsonout.RenderWatchlist(&buf, jsonout.WatchlistDocument{
		Snapshot:     snap,
		Selector:     provider.RefListSelector{Refs: []ref.Ref{{Raw: "a"}}},
		ProviderName: "fake",
		Blocking:     graph,
	})
	if err != nil {
		t.Fatalf("RenderWatchlist: %v", err)
	}
	return buf.Bytes()
}

func decodeBlocking(t *testing.T, raw []byte) blockingWire {
	t.Helper()

	var doc blockingWire
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc
}

func blockingLinks() model.Capabilities {
	return model.Capabilities{BlockingLinks: true}
}

// An anonymous blocker — a BlockedBy Link whose target carried no ID — must
// survive to the wire. Dropping it would make a blocked Ticket look actionable,
// which is the one wrong answer Actionable exists to avoid.
func TestRenderWatchlistEmitsAnonymousBlockers(t *testing.T) {
	tickets := []model.Ticket{{ID: "a", Key: "#a", Status: model.StatusTodo}}
	links := map[model.TicketID][]model.Link{
		"a": {{Kind: model.LinkBlockedBy, NativeLabel: "is blocked by"}},
	}
	graph := model.BuildBlockingGraph(tickets, links, blockingLinks())

	raw := renderWatchlist(t, model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}, &graph)

	doc := decodeBlocking(t, raw)
	if len(doc.Tickets) != 1 || len(doc.Tickets[0].UnmetBlockers) != 1 {
		t.Fatalf("the anonymous blocker was dropped:\n%s", raw)
	}
	blocker := doc.Tickets[0].UnmetBlockers[0]
	if blocker.ID != "" || blocker.Key != "" || blocker.Title != "" || blocker.URL != "" {
		t.Errorf("anonymous blocker carries an identity: %+v", blocker)
	}
	if blocker.Status != "unknown" || blocker.StatusKnown || blocker.Member {
		t.Errorf("anonymous blocker = %+v, want unknown/unverified/non-member", blocker)
	}
	if doc.Tickets[0].Actionable == nil || *doc.Tickets[0].Actionable {
		t.Error("a Ticket blocked by an anonymous target must not be actionable")
	}
}

// A Ticket whose own Detail could not be read has no blockers to report, and
// must not be given an empty array as if sitrep had looked.
func TestRenderWatchlistOmitsUnmetBlockersForUnreadableLinks(t *testing.T) {
	tickets := []model.Ticket{{ID: "a", Key: "#a", Status: model.StatusTodo}}
	graph := model.BuildBlockingGraph(tickets, nil, blockingLinks())

	raw := renderWatchlist(t, model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}, &graph)

	doc := decodeBlocking(t, raw)
	ticket := doc.Tickets[0]
	if ticket.LinksKnown == nil || *ticket.LinksKnown {
		t.Errorf("links_known = %v, want a computed false", ticket.LinksKnown)
	}
	if ticket.Actionable == nil || *ticket.Actionable {
		t.Error("an unreadable Ticket must not be actionable")
	}
	if strings.Contains(string(raw), "unmet_blockers") {
		t.Errorf("an unreadable Ticket claims blockers it never read:\n%s", raw)
	}
}

// The other half of the unreadable case: a member whose own Links failed can
// still be known-blocked through a readable member's Blocks Link. That blocker
// was genuinely read, so it is reported rather than suppressed, and
// links_known: false says the list may be incomplete.
func TestRenderWatchlistKeepsBlockersDiscoveredThroughAnotherMember(t *testing.T) {
	tickets := []model.Ticket{
		{ID: "a", Key: "#a", Status: model.StatusInProgress},
		{ID: "b", Key: "#b", Status: model.StatusTodo},
	}
	links := map[model.TicketID][]model.Link{
		// Only a's Detail was read; b's failed.
		"a": {{Kind: model.LinkBlocks, Target: model.LinkTarget{ID: "b", Key: "#b"}}},
	}
	graph := model.BuildBlockingGraph(tickets, links, blockingLinks())

	raw := renderWatchlist(t, model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}, &graph)

	doc := decodeBlocking(t, raw)
	b := doc.Tickets[1]
	if b.LinksKnown == nil || *b.LinksKnown {
		t.Errorf("#b links_known = %v, want a computed false", b.LinksKnown)
	}
	if len(b.UnmetBlockers) != 1 || b.UnmetBlockers[0].ID != "a" {
		t.Fatalf("#b unmet_blockers = %+v, want the blocker #a discovered through its Blocks Link",
			b.UnmetBlockers)
	}
	if !b.UnmetBlockers[0].Member || !b.UnmetBlockers[0].StatusKnown {
		t.Errorf("#b blocker = %+v, want a member whose status was read", b.UnmetBlockers[0])
	}
}

// nil Blocking means "not computed", and every blocking key is then absent —
// not null, not false.
func TestRenderWatchlistWithoutABlockingGraphEmitsNoBlockingKeys(t *testing.T) {
	tickets := []model.Ticket{{ID: "a", Key: "#a", Status: model.StatusTodo}}

	raw := renderWatchlist(t, model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}, nil)

	// "blocking_links" is the Capability and stays; the top-level object does not.
	for _, key := range []string{`"blocking":`, `"actionable"`, `"links_known"`, `"in_cycle"`, `"unmet_blockers"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("an uncomputed document carries %s:\n%s", key, raw)
		}
	}
	if doc := decodeBlocking(t, raw); doc.Blocking != nil {
		t.Error("an uncomputed document decoded a blocking object")
	}
}

// cycles is always an array, [] when there are none, the same convention as
// tickets.
func TestRenderWatchlistEmitsAnEmptyCyclesArray(t *testing.T) {
	tickets := []model.Ticket{{ID: "a", Key: "#a", Status: model.StatusTodo}}
	graph := model.BuildBlockingGraph(tickets, map[model.TicketID][]model.Link{"a": nil}, blockingLinks())

	raw := renderWatchlist(t, model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}, &graph)

	if !strings.Contains(string(raw), `"cycles": []`) {
		t.Errorf("a Watchlist with no cycles must emit an empty array:\n%s", raw)
	}
	doc := decodeBlocking(t, raw)
	if doc.Blocking == nil || doc.Blocking.Cycles == nil {
		t.Errorf("cycles decoded as null rather than an empty array:\n%s", raw)
	}
}

// Members are walked positionally, so a graph built over different Tickets is a
// mislabelling waiting to happen. Refuse rather than render it.
func TestRenderWatchlistRejectsAGraphOverDifferentTickets(t *testing.T) {
	graph := model.BuildBlockingGraph([]model.Ticket{{ID: "a"}, {ID: "b"}}, nil, blockingLinks())
	snap := model.WatchlistSnapshot{
		Tickets:      []model.Ticket{{ID: "a", Key: "#a"}},
		Capabilities: blockingLinks(),
	}

	var buf bytes.Buffer
	err := jsonout.RenderWatchlist(&buf, jsonout.WatchlistDocument{
		Snapshot:     snap,
		Selector:     provider.RefListSelector{Refs: []ref.Ref{{Raw: "a"}}},
		ProviderName: "fake",
		Blocking:     &graph,
	})
	if err == nil {
		t.Fatalf("RenderWatchlist accepted a 2-member graph over 1 Ticket:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "2 members for 1 Tickets") {
		t.Errorf("error = %q, want it to name the mismatch", err)
	}
}

// The length check alone would pass a graph built over the same Tickets in a
// different order, and every row would carry another Ticket's blocking state.
func TestRenderWatchlistRejectsAReorderedGraph(t *testing.T) {
	graph := model.BuildBlockingGraph([]model.Ticket{{ID: "b"}, {ID: "a"}}, nil, blockingLinks())
	snap := model.WatchlistSnapshot{
		Tickets:      []model.Ticket{{ID: "a", Key: "#a"}, {ID: "b", Key: "#b"}},
		Capabilities: blockingLinks(),
	}

	var buf bytes.Buffer
	err := jsonout.RenderWatchlist(&buf, jsonout.WatchlistDocument{
		Snapshot:     snap,
		Selector:     provider.RefListSelector{Refs: []ref.Ref{{Raw: "a"}}},
		ProviderName: "fake",
		Blocking:     &graph,
	})
	if err == nil {
		t.Fatalf("RenderWatchlist accepted a graph in the wrong order:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "member 0 is b, want a") {
		t.Errorf("error = %q, want it to name the mispaired member", err)
	}
}

// One Ticket, one status. A blocker's status is the authoritative one the graph
// resolved, not the copy the Link that named it carried — the same Ticket
// appearing twice in one document with two statuses is a document that lies.
func TestBlockerStatusAgreesWithTheBlockersOwnTicket(t *testing.T) {
	blocker := model.Ticket{
		ID: "a", Key: "#a", Status: model.StatusTodo, NativeStatus: "Selected for Development",
	}
	dependent := model.Ticket{ID: "b", Key: "#b", Status: model.StatusTodo}
	// The Link is stale: it names #a as done, which it is not.
	links := map[model.TicketID][]model.Link{
		"a": nil,
		"b": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{
			ID: "a", Key: "#a", Status: model.StatusDone, NativeStatus: "closed",
		}}},
	}
	tickets := []model.Ticket{blocker, dependent}
	graph := model.BuildBlockingGraph(tickets, links, blockingLinks())
	snap := model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}

	doc := decodeBlocking(t, renderWatchlist(t, snap, &graph))
	if len(doc.Tickets[1].UnmetBlockers) != 1 {
		t.Fatalf("#b has %d unmet blockers, want the one that is not done",
			len(doc.Tickets[1].UnmetBlockers))
	}
	got := doc.Tickets[1].UnmetBlockers[0]
	if got.Status != doc.Tickets[0].Status {
		t.Errorf("unmet_blockers[0].status = %q but tickets[0].status = %q — the same Ticket",
			got.Status, doc.Tickets[0].Status)
	}
	if got.NativeStatus != doc.Tickets[0].NativeStatus {
		t.Errorf("unmet_blockers[0].native_status = %q but tickets[0].native_status = %q",
			got.NativeStatus, doc.Tickets[0].NativeStatus)
	}
}

// An anonymous blocker has no Ticket and no Ghost to read, so its status is
// unknown however confidently the Link that named it wrote one. The Tracker's
// own word goes with the Category it belonged to rather than standing beside a
// contradicting one.
func TestAnonymousBlockerDropsANativeStatusThatContradictsItsCategory(t *testing.T) {
	dependent := model.Ticket{ID: "b", Key: "#b", Status: model.StatusTodo}
	links := map[model.TicketID][]model.Link{
		"b": {{Kind: model.LinkBlockedBy, Target: model.LinkTarget{
			Key: "#g", Status: model.StatusDone, NativeStatus: "closed",
		}}},
	}
	tickets := []model.Ticket{dependent}
	graph := model.BuildBlockingGraph(tickets, links, blockingLinks())
	snap := model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}

	doc := decodeBlocking(t, renderWatchlist(t, snap, &graph))
	if len(doc.Tickets[0].UnmetBlockers) != 1 {
		t.Fatalf("#b has %d unmet blockers, want the anonymous one",
			len(doc.Tickets[0].UnmetBlockers))
	}
	got := doc.Tickets[0].UnmetBlockers[0]
	if got.StatusKnown {
		t.Fatalf("an anonymous blocker reported status_known: this test needs an unread one")
	}
	if got.Status != model.StatusUnknown.String() {
		t.Errorf("status = %q beside status_known: false", got.Status)
	}
	if got.NativeStatus != "" {
		t.Errorf("native_status = %q beside status %q — a contradiction on the wire",
			got.NativeStatus, got.Status)
	}
}

// A Ref list may legitimately name the same Ticket twice. Rows are matched
// positionally so both carry the same computed state, rather than one of them
// being collapsed away.
func TestRenderWatchlistMatchesDuplicateTicketsPositionally(t *testing.T) {
	ticket := model.Ticket{ID: "a", Key: "#a", Status: model.StatusTodo}
	tickets := []model.Ticket{ticket, ticket}
	links := map[model.TicketID][]model.Link{"a": nil}
	graph := model.BuildBlockingGraph(tickets, links, blockingLinks())

	raw := renderWatchlist(t, model.WatchlistSnapshot{Tickets: tickets, Capabilities: blockingLinks()}, &graph)

	doc := decodeBlocking(t, raw)
	if len(doc.Tickets) != 2 {
		t.Fatalf("tickets = %d, want both rows kept:\n%s", len(doc.Tickets), raw)
	}
	for i, ticket := range doc.Tickets {
		if ticket.Actionable == nil || !*ticket.Actionable {
			t.Errorf("tickets[%d].actionable = %v, want a computed true", i, ticket.Actionable)
		}
	}
}
