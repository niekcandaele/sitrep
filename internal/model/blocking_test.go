package model_test

import (
	"reflect"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// allCaps is the ordinary case: a Provider that can express blocking.
var allCaps = model.Capabilities{BlockingLinks: true}

// target names a Ticket the way a Link would, member or Ghost alike.
func target(id string, status model.StatusCategory) model.LinkTarget {
	return model.LinkTarget{
		ID:     model.TicketID(id),
		Key:    id,
		Title:  "Title of " + id,
		URL:    "https://tracker.example.test/" + id,
		Status: status,
	}
}

func blockedBy(t model.LinkTarget) model.Link {
	return model.Link{Kind: model.LinkBlockedBy, NativeLabel: "is blocked by", Target: t}
}

func blocks(t model.LinkTarget) model.Link {
	return model.Link{Kind: model.LinkBlocks, NativeLabel: "blocks", Target: t}
}

func relates(t model.LinkTarget) model.Link {
	return model.Link{Kind: model.LinkRelates, NativeLabel: "relates to", Target: t}
}

// forID fails the test when id is not a member: every case below names members
// it built itself.
func forID(t *testing.T, g model.BlockingGraph, id string) model.Actionability {
	t.Helper()
	a, ok := g.For(model.TicketID(id))
	if !ok {
		t.Fatalf("For(%q): not a member", id)
	}
	return a
}

func blockerKeys(bs []model.Blocker) []string {
	keys := make([]string, len(bs))
	for i, b := range bs {
		keys[i] = string(b.Target.ID)
	}
	return keys
}

func TestActionable(t *testing.T) {
	tests := []struct {
		name       string
		tickets    []model.Ticket
		links      map[model.TicketID][]model.Link
		subject    string
		actionable bool
		linksKnown bool
		hasUnknown bool
		unmet      []string
	}{
		{
			name: "done and cancelled blockers satisfy",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusDone, "closed"),
				ticket("c", model.StatusCancelled, "not planned"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusDone)), blockedBy(target("c", model.StatusCancelled))},
				"b": {},
				"c": {},
			},
			subject:    "a",
			actionable: true,
			linksKnown: true,
		},
		{
			name:       "no blockers with links read",
			tickets:    []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links:      map[model.TicketID][]model.Link{"a": nil},
			subject:    "a",
			actionable: true,
			linksKnown: true,
		},
		{
			name:       "links unreadable",
			tickets:    []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links:      map[model.TicketID][]model.Link{},
			subject:    "a",
			hasUnknown: true,
		},
		{
			name: "blocked by a todo member is known-blocked",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusTodo))},
				"b": {},
			},
			subject:    "a",
			linksKnown: true,
			unmet:      []string{"b"},
		},
		{
			name:    "blocked by a todo ghost",
			tickets: []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("g", model.StatusTodo))},
			},
			subject:    "a",
			linksKnown: true,
			unmet:      []string{"g"},
		},
		{
			name:    "blocked by a ghost of unknown status is unverified",
			tickets: []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("g", model.StatusUnknown))},
			},
			subject:    "a",
			linksKnown: true,
			hasUnknown: true,
			unmet:      []string{"g"},
		},
		{
			name:    "a finished ghost satisfies",
			tickets: []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("g", model.StatusDone))},
			},
			subject:    "a",
			actionable: true,
			linksKnown: true,
		},
		{
			name: "blocked by a member of unknown status is unverified",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusUnknown, ""),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusUnknown))},
				"b": {},
			},
			subject:    "a",
			linksKnown: true,
			hasUnknown: true,
			unmet:      []string{"b"},
		},
		{
			name: "relates is not an edge",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {relates(target("g", model.StatusInProgress))},
			},
			subject:    "a",
			actionable: true,
			linksKnown: true,
		},
		{
			name: "a blocks link creates the reverse edge on its target",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blocks(target("b", model.StatusTodo))},
				"b": {},
			},
			subject:    "b",
			linksKnown: true,
			unmet:      []string{"a"},
		},
		{
			name: "reciprocal links dedup to one blocker",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusTodo))},
				"b": {blocks(target("a", model.StatusTodo))},
			},
			subject:    "a",
			linksKnown: true,
			unmet:      []string{"b"},
		},
		{
			name: "an anonymous blocker fails closed",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(model.LinkTarget{Key: "#404", Status: model.StatusDone})},
			},
			subject:    "a",
			linksKnown: true,
			hasUnknown: true,
			unmet:      []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := model.BuildBlockingGraph(tt.tickets, tt.links, allCaps)
			a := forID(t, g, tt.subject)
			if a.Actionable != tt.actionable {
				t.Errorf("Actionable = %v, want %v", a.Actionable, tt.actionable)
			}
			if a.LinksKnown != tt.linksKnown {
				t.Errorf("LinksKnown = %v, want %v", a.LinksKnown, tt.linksKnown)
			}
			if a.HasUnknown() != tt.hasUnknown {
				t.Errorf("HasUnknown() = %v, want %v", a.HasUnknown(), tt.hasUnknown)
			}
			got := blockerKeys(a.Unmet())
			want := tt.unmet
			if want == nil {
				want = []string{}
			}
			if len(got) == 0 {
				got = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Unmet() = %v, want %v", got, want)
			}
			if len(tt.unmet) == 0 && a.Unmet() != nil {
				t.Errorf("Unmet() = %v, want nil for none", a.Unmet())
			}
		})
	}
}

func TestOnlyTodoMembersAreActionable(t *testing.T) {
	for _, status := range []model.StatusCategory{
		model.StatusTodo,
		model.StatusInProgress,
		model.StatusDone,
		model.StatusCancelled,
		model.StatusUnknown,
	} {
		t.Run(status.String(), func(t *testing.T) {
			tickets := []model.Ticket{
				ticket("a", status, ""),
				ticket("b", model.StatusDone, "closed"),
			}
			links := map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusDone))},
				"b": {},
			}
			g := model.BuildBlockingGraph(tickets, links, allCaps)
			want := status == model.StatusTodo
			if got := forID(t, g, "a").Actionable; got != want {
				t.Errorf("Actionable = %v, want %v", got, want)
			}
		})
	}
}

func TestInProgressAndBlockedStaysVisible(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusInProgress, "In Review"),
		ticket("b", model.StatusTodo, "open"),
	}
	links := map[model.TicketID][]model.Link{
		"a": {blockedBy(target("b", model.StatusTodo))},
		"b": {},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	a := forID(t, g, "a")
	if a.Actionable {
		t.Error("an InProgress Ticket must never be Actionable")
	}
	if got := blockerKeys(a.Unmet()); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("Unmet() = %v, want [b]: the blocked signal must stay visible", got)
	}
}

func TestGhosts(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusTodo, "open"),
	}
	links := map[model.TicketID][]model.Link{
		"a": {
			blockedBy(target("g1", model.StatusTodo)),
			relates(target("g-relates", model.StatusTodo)),
		},
		"b": {blocks(target("g2", model.StatusTodo))},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)

	var keys []string
	for _, gh := range g.Ghosts() {
		keys = append(keys, string(gh.Target.ID))
	}
	if !reflect.DeepEqual(keys, []string{"g1", "g2"}) {
		t.Fatalf("Ghosts() = %v, want [g1 g2]: Relates names no Ghost", keys)
	}
	if _, ok := g.For("g1"); ok {
		t.Error("For(ghost) must report that a Ghost is not a member")
	}
	blockersOfA := forID(t, g, "a").Blockers
	if len(blockersOfA) != 1 || blockersOfA[0].Member {
		t.Errorf("Blockers = %+v, want one non-member", blockersOfA)
	}
}

func TestGhostStatusPrefersTheFirstKnownOccurrence(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusTodo, "open"),
	}
	links := map[model.TicketID][]model.Link{
		"a": {blockedBy(target("g", model.StatusUnknown))},
		"b": {blockedBy(target("g", model.StatusDone))},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	ghosts := g.Ghosts()
	if len(ghosts) != 1 || ghosts[0].Target.Status != model.StatusDone {
		t.Fatalf("Ghosts() = %+v, want one Ghost resolved to done", ghosts)
	}
	if !forID(t, g, "a").Actionable || !forID(t, g, "b").Actionable {
		t.Error("a finished Ghost satisfies every dependent that names it")
	}
}

func TestAnonymousTargetIsNoGhost(t *testing.T) {
	tickets := []model.Ticket{ticket("a", model.StatusTodo, "open")}
	links := map[model.TicketID][]model.Link{
		"a": {
			blockedBy(model.LinkTarget{Key: "#1"}),
			blockedBy(model.LinkTarget{Key: "#2"}),
		},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	if got := g.Ghosts(); len(got) != 0 {
		t.Errorf("Ghosts() = %+v, want none: an anonymous target has no identity to draw", got)
	}
	a := forID(t, g, "a")
	if len(a.Blockers) != 2 {
		t.Errorf("Blockers = %+v, want two: anonymous targets must not merge", a.Blockers)
	}
	if a.Actionable || !a.HasUnknown() {
		t.Errorf("Actionable = %v, HasUnknown = %v, want false/true", a.Actionable, a.HasUnknown())
	}
}

func TestBlocksOnAGhostBlocksTheGhostNotTheMember(t *testing.T) {
	tickets := []model.Ticket{ticket("a", model.StatusTodo, "open")}
	links := map[model.TicketID][]model.Link{
		"a": {blocks(target("g", model.StatusTodo))},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	if a := forID(t, g, "a"); !a.Actionable || len(a.Blockers) != 0 {
		t.Errorf("blocking a Ghost must not block the member: %+v", a)
	}
	if len(g.Ghosts()) != 1 {
		t.Errorf("Ghosts() = %+v, want the Blocks target", g.Ghosts())
	}
}

func TestMemberBlockerStatusIsAuthoritative(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusTodo, "open"),
	}
	// The Link's second-hand copy says done; the batched list read says todo.
	links := map[model.TicketID][]model.Link{
		"a": {blockedBy(target("b", model.StatusDone))},
		"b": {},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	a := forID(t, g, "a")
	if a.Actionable {
		t.Error("a member blocker's own Ticket.Status wins over the LinkTarget copy")
	}
	if !a.Blockers[0].Member || a.Blockers[0].Status != model.StatusTodo {
		t.Errorf("Blockers[0] = %+v, want member/todo", a.Blockers[0])
	}
}

func TestUnreadableLinksStillCollectBlockersFromOtherMembers(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusTodo, "open"),
	}
	links := map[model.TicketID][]model.Link{
		"a": {blocks(target("b", model.StatusTodo))},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	b := forID(t, g, "b")
	if b.LinksKnown || b.Actionable {
		t.Errorf("b = %+v, want links unknown and not actionable", b)
	}
	if got := blockerKeys(b.Blockers); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Blockers = %v, want [a]", got)
	}
}

func TestCycles(t *testing.T) {
	tests := []struct {
		name    string
		tickets []model.Ticket
		links   map[model.TicketID][]model.Link
		want    [][]model.TicketID
	}{
		{
			name: "two member cycle",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusTodo))},
				"b": {blockedBy(target("a", model.StatusTodo))},
			},
			want: [][]model.TicketID{{"a", "b"}},
		},
		{
			name:    "a self blocking ticket is a cycle of one",
			tickets: []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("a", model.StatusTodo))},
			},
			want: [][]model.TicketID{{"a"}},
		},
		{
			name:    "a cycle through a ghost",
			tickets: []model.Ticket{ticket("a", model.StatusTodo, "open")},
			links: map[model.TicketID][]model.Link{
				"a": {
					blockedBy(target("g", model.StatusTodo)),
					blocks(target("g", model.StatusTodo)),
				},
			},
			want: [][]model.TicketID{{"a", "g"}},
		},
		{
			name: "two disjoint cycles in canonical order",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusTodo, "open"),
				ticket("c", model.StatusTodo, "open"),
				ticket("d", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusTodo))},
				"b": {blockedBy(target("a", model.StatusTodo))},
				"c": {blockedBy(target("d", model.StatusTodo))},
				"d": {blockedBy(target("c", model.StatusTodo))},
			},
			want: [][]model.TicketID{{"a", "b"}, {"c", "d"}},
		},
		{
			name: "an acyclic chain reports nothing",
			tickets: []model.Ticket{
				ticket("a", model.StatusTodo, "open"),
				ticket("b", model.StatusTodo, "open"),
			},
			links: map[model.TicketID][]model.Link{
				"a": {blockedBy(target("b", model.StatusTodo))},
				"b": {},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := model.BuildBlockingGraph(tt.tickets, tt.links, allCaps)
			if got := g.Cycles(); !reflect.DeepEqual(normalizeCycles(got), normalizeCycles(tt.want)) {
				t.Fatalf("Cycles() = %v, want %v", got, tt.want)
			}
			inCycle := map[model.TicketID]bool{}
			for _, c := range tt.want {
				for _, id := range c {
					inCycle[id] = true
				}
			}
			for _, m := range g.Members() {
				if m.InCycle != inCycle[m.TicketID] {
					t.Errorf("%s InCycle = %v, want %v", m.TicketID, m.InCycle, inCycle[m.TicketID])
				}
				if inCycle[m.TicketID] && m.Actionable {
					t.Errorf("%s in an all-unfinished cycle must not be Actionable", m.TicketID)
				}
			}

			// The same input twice must produce byte-identical output: no map
			// iteration may reach it.
			again := model.BuildBlockingGraph(tt.tickets, tt.links, allCaps)
			if !reflect.DeepEqual(g.Cycles(), again.Cycles()) {
				t.Errorf("Cycles() is not deterministic: %v vs %v", g.Cycles(), again.Cycles())
			}
		})
	}
}

func normalizeCycles(c [][]model.TicketID) [][]model.TicketID {
	if len(c) == 0 {
		return nil
	}
	return c
}

func TestCycleWithAFinishedMemberStillLeavesANeighbourActionable(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusDone, "closed"),
	}
	links := map[model.TicketID][]model.Link{
		"a": {blockedBy(target("b", model.StatusDone))},
		"b": {blockedBy(target("a", model.StatusTodo))},
	}
	g := model.BuildBlockingGraph(tickets, links, allCaps)
	a := forID(t, g, "a")
	if !a.Actionable {
		t.Error("InCycle must not override the Actionable definition")
	}
	if !a.InCycle {
		t.Error("the malformed data must still be reported")
	}
}

func TestWithoutBlockingLinksCapabilityTheGraphClaimsNothing(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusDone, "closed"),
	}
	links := map[model.TicketID][]model.Link{
		"a": {blockedBy(target("b", model.StatusDone)), blockedBy(target("g", model.StatusTodo))},
		"b": {blockedBy(target("a", model.StatusTodo))},
	}
	g := model.BuildBlockingGraph(tickets, links, model.Capabilities{})
	for _, m := range g.Members() {
		if m.Actionable || m.LinksKnown || m.InCycle || len(m.Blockers) != 0 {
			t.Errorf("%s = %+v, want a graph that claims nothing", m.TicketID, m)
		}
		if !m.HasUnknown() {
			t.Errorf("%s must read as unknown, not as cleanly unblocked", m.TicketID)
		}
	}
	if len(g.Ghosts()) != 0 || len(g.Cycles()) != 0 {
		t.Errorf("Ghosts() = %v, Cycles() = %v, want none", g.Ghosts(), g.Cycles())
	}
}

func TestEmptyInputs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tickets []model.Ticket
		links   map[model.TicketID][]model.Link
	}{
		{name: "zero value graph"},
		{name: "nil tickets, nil links"},
		{name: "nil links", tickets: []model.Ticket{ticket("a", model.StatusTodo, "open")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := model.BuildBlockingGraph(tt.tickets, tt.links, allCaps)
			if len(g.Members()) != len(tt.tickets) {
				t.Errorf("Members() = %v, want %d", g.Members(), len(tt.tickets))
			}
			if len(g.Ghosts()) != 0 || len(g.Cycles()) != 0 {
				t.Error("an empty Watchlist has no Ghosts and no cycles")
			}
			for _, m := range g.Members() {
				if m.Actionable {
					t.Errorf("%s: nothing is Actionable before any Detail resolves", m.TicketID)
				}
			}
		})
	}

	var zero model.BlockingGraph
	if len(zero.Members()) != 0 || len(zero.Ghosts()) != 0 || len(zero.Cycles()) != 0 {
		t.Error("the zero BlockingGraph must be a graph over no Tickets")
	}
	if _, ok := zero.For("a"); ok {
		t.Error("the zero BlockingGraph has no members")
	}
}

func TestBuildBlockingGraphIsPure(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusTodo, "open"),
		ticket("b", model.StatusDone, "closed"),
	}
	aLinks := []model.Link{blockedBy(target("b", model.StatusDone))}
	links := map[model.TicketID][]model.Link{"a": aLinks, "b": {}}

	g := model.BuildBlockingGraph(tickets, links, allCaps)
	before := g.Members()

	tickets[0].Status = model.StatusInProgress
	tickets[1].Status = model.StatusTodo
	aLinks[0] = blockedBy(target("c", model.StatusTodo))
	links["a"] = nil
	delete(links, "b")

	if !reflect.DeepEqual(g.Members(), before) {
		t.Errorf("Members() changed after the caller mutated its inputs: %v vs %v", g.Members(), before)
	}
	if !forID(t, g, "a").Actionable {
		t.Error("the graph must not read the caller's slices after building")
	}

	members := g.Members()
	members[0].Actionable = false
	members[0].Blockers[0].Status = model.StatusTodo
	ghosts := model.BuildBlockingGraph(
		[]model.Ticket{ticket("a", model.StatusTodo, "open")},
		map[model.TicketID][]model.Link{"a": {blockedBy(target("g", model.StatusTodo))}},
		allCaps,
	)
	gs := ghosts.Ghosts()
	gs[0].Target.ID = "mutated"
	if ghosts.Ghosts()[0].Target.ID != "g" {
		t.Error("Ghosts() must return a copy, not the graph's storage")
	}
	cyclic := model.BuildBlockingGraph(
		[]model.Ticket{ticket("a", model.StatusTodo, "open")},
		map[model.TicketID][]model.Link{"a": {blockedBy(target("a", model.StatusTodo))}},
		allCaps,
	)
	cs := cyclic.Cycles()
	cs[0][0] = "mutated"
	if cyclic.Cycles()[0][0] != "a" {
		t.Error("Cycles() must return a copy, not the graph's storage")
	}
	if !reflect.DeepEqual(g.Members(), before) {
		t.Error("Members() must return a copy, not the graph's storage")
	}
}

// A Watchlist may name one Ticket twice — an exact Ref list is free to repeat a
// Ref — and the Ticket still has one blocking state. The member list stays
// positional because that is the --json contract, so the rows must agree.
func TestDuplicateMembersShareOneBlockingAnswer(t *testing.T) {
	a := ticket("a", model.StatusTodo, "open")
	g := model.BuildBlockingGraph(
		[]model.Ticket{a, ticket("b", model.StatusTodo, "open"), a},
		map[model.TicketID][]model.Link{
			"a": {blockedBy(target("b", model.StatusTodo))},
			"b": nil,
		}, allCaps)

	members := g.Members()
	if len(members) != 3 {
		t.Fatalf("Members() = %d rows, want one per input Ticket", len(members))
	}
	first, second := members[0], members[2]
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the two rows for a disagree:\nrow 0: %+v\nrow 2: %+v", first, second)
	}
	if second.Actionable {
		t.Error("the duplicate row is Actionable while the Ticket it names is blocked")
	}
	if got := blockerKeys(second.Blockers); len(got) != 1 || got[0] != "b" {
		t.Errorf("the duplicate row's Blockers = %v, want b", got)
	}
	// Cloned, not shared: mutating one row must not reach the other.
	second.Blockers[0].Member = false
	if !g.Members()[2].Blockers[0].Member {
		t.Error("Members() handed out the graph's own Blocker storage")
	}
}

func TestBlocksLinksCanonicalizeDuplicateMemberBlocker(t *testing.T) {
	g := model.BuildBlockingGraph(
		[]model.Ticket{
			ticket("a", model.StatusTodo, "open"),
			ticket("a", model.StatusTodo, "open"),
			ticket("c", model.StatusTodo, "open"),
		},
		map[model.TicketID][]model.Link{
			"a": {blocks(target("c", model.StatusTodo))},
			"c": {},
		}, allCaps)

	c := forID(t, g, "c")
	if got := len(c.Blockers); got != 1 {
		t.Fatalf("c carries %d blockers, want the duplicate a rows to name one blocker", got)
	}
	if blocker := c.Blockers[0]; blocker.Target.ID != "a" || !blocker.Member {
		t.Errorf("c's blocker = %+v, want member a", blocker)
	}
	if got := blockerKeys(c.Unmet()); len(got) != 1 || got[0] != "a" {
		t.Errorf("c's unmet blockers = %v, want a", got)
	}
	if c.Actionable {
		t.Error("c is Actionable behind the Todo blocker a")
	}
}

func TestBlocksLinksUseCanonicalStatusForDuplicateMembers(t *testing.T) {
	tickets := []model.Ticket{
		ticket("a", model.StatusDone, "closed"),
		ticket("a", model.StatusTodo, "open"),
		ticket("c", model.StatusTodo, "open"),
	}
	throughBlocks := model.BuildBlockingGraph(tickets, map[model.TicketID][]model.Link{
		"a": {blocks(target("c", model.StatusTodo))},
		"c": {},
	}, allCaps)
	throughBlockedBy := model.BuildBlockingGraph(tickets, map[model.TicketID][]model.Link{
		"a": {},
		"c": {blockedBy(target("a", model.StatusDone))},
	}, allCaps)

	c := forID(t, throughBlocks, "c")
	if got := len(c.Blockers); got != 1 {
		t.Fatalf("c carries %d blockers through Blocks, want one", got)
	}
	if blocker := c.Blockers[0]; blocker.Target.ID != "a" || !blocker.Member || blocker.Status != model.StatusDone {
		t.Errorf("c's blocker through Blocks = %+v, want canonical member a at Done", blocker)
	}
	if !c.Actionable {
		t.Error("c is not Actionable although canonical a is Done")
	}
	mirror := forID(t, throughBlockedBy, "c")
	if c.Actionable != mirror.Actionable {
		t.Errorf("Blocks Actionable = %v, BlockedBy Actionable = %v for the same relation", c.Actionable, mirror.Actionable)
	}
}

func TestBlocksLinksCanonicalizeDuplicateGhostBlocker(t *testing.T) {
	g := model.BuildBlockingGraph(
		[]model.Ticket{
			ticket("a", model.StatusTodo, "open"),
			ticket("a", model.StatusTodo, "open"),
		},
		map[model.TicketID][]model.Link{
			"a": {blocks(target("ghost", model.StatusTodo))},
		}, allCaps)

	ghosts := g.Ghosts()
	if got := len(ghosts); got != 1 {
		t.Fatalf("Ghosts() = %d, want one ghost", got)
	}
	if got := len(ghosts[0].Blockers); got != 1 {
		t.Fatalf("the Ghost carries %d blockers, want one canonical a", got)
	}
	if blocker := ghosts[0].Blockers[0]; blocker.Target.ID != "a" || !blocker.Member {
		t.Errorf("the Ghost's blocker = %+v, want member a", blocker)
	}
}

func TestBlocksLinksTreatEmptyIDBlockerAsAnonymous(t *testing.T) {
	g := model.BuildBlockingGraph(
		[]model.Ticket{
			ticket("", model.StatusDone, "closed"),
			ticket("c", model.StatusTodo, "open"),
		},
		map[model.TicketID][]model.Link{
			"":  {blocks(target("c", model.StatusTodo))},
			"c": {},
		}, allCaps)

	c := forID(t, g, "c")
	if c.Actionable {
		t.Error("c is Actionable behind an anonymous blocker")
	}
	if got := len(c.Blockers); got != 1 {
		t.Fatalf("c carries %d blockers, want the anonymous reverse blocker", got)
	}
	blocker := c.Blockers[0]
	if blocker.Member || blocker.Target.ID != "" || blocker.StatusKnown {
		t.Errorf("c's blocker = %+v, want an anonymous member-unknown blocker", blocker)
	}
	if blocker.Satisfied() {
		t.Error("the anonymous blocker is satisfied by the empty-ID row's Done status")
	}
	if got := len(c.Unmet()); got != 1 {
		t.Errorf("c has %d unmet blockers, want the anonymous blocker", got)
	}
}

// A member with no ID is not an identity. Before, it was indexed like any other
// member, so every anonymous blocker Link in the Watchlist resolved to it and it
// came back Actionable: fail-open on an unidentified blocker, which is the exact
// inverse of the rule Actionable exists to enforce.
func TestAnEmptyIDMemberAbsorbsNoAnonymousBlockers(t *testing.T) {
	anonymous := model.Link{
		Kind:        model.LinkBlockedBy,
		NativeLabel: "is blocked by",
		Target:      model.LinkTarget{Key: "an unresolvable blocker"},
	}
	g := model.BuildBlockingGraph(
		[]model.Ticket{ticket("", model.StatusTodo, "open"), ticket("b", model.StatusTodo, "open")},
		// No key for the empty ID: detailfanout.Plan skips it, so nothing ever
		// reads a nameless member's Links and it is unverified by construction.
		map[model.TicketID][]model.Link{"b": {anonymous}}, allCaps)

	members := g.Members()
	if len(members) != 2 {
		t.Fatalf("Members() = %d rows, want one per input Ticket", len(members))
	}
	if got := len(members[0].Blockers); got != 0 {
		t.Errorf("the empty-ID member carries %d blockers, want the anonymous Link to have gone to b", got)
	}
	if members[0].Actionable {
		t.Error("the empty-ID member is Actionable; a Ticket with no identity is never verified")
	}
	if got := len(members[1].Blockers); got != 1 {
		t.Fatalf("b carries %d blockers, want its own anonymous one", got)
	}
	if members[1].Actionable {
		t.Error("b is Actionable behind an anonymous blocker")
	}
}

// A Ghost reached as a dependent — a member's Blocks Link pointing out of the
// Watchlist — carries the edge on the Ghost's own Blockers. Without it the
// canvas draws a card connected to nothing while the header counts it.
func TestAGhostReachedAsADependentCarriesItsBlockers(t *testing.T) {
	g := model.BuildBlockingGraph(
		[]model.Ticket{ticket("a", model.StatusTodo, "open")},
		map[model.TicketID][]model.Link{
			"a": {blocks(target("ghost", model.StatusTodo))},
		}, allCaps)

	ghosts := g.Ghosts()
	if len(ghosts) != 1 {
		t.Fatalf("Ghosts() = %d, want the one a blocks", len(ghosts))
	}
	if got := blockerKeys(ghosts[0].Blockers); len(got) != 1 || got[0] != "a" {
		t.Fatalf("the Ghost's Blockers = %v, want a", got)
	}
	if !ghosts[0].Blockers[0].Member {
		t.Error("the Ghost's blocker is not marked a Watchlist member")
	}
	// Cloned, not shared, the same way Members() clones Actionability.
	ghosts[0].Blockers[0].Member = false
	if !g.Ghosts()[0].Blockers[0].Member {
		t.Error("Ghosts() handed out the graph's own Blocker storage")
	}
}
