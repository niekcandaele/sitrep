package tui

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The layout is pure, so these tests build a model.BlockingGraph from plain
// fixture data and never open a screen. That is data, not a screen input, so it
// does not bypass the terminal-text boundary the frame tests go through.

func blockingTicket(key string, status model.StatusCategory) model.Ticket {
	return model.Ticket{ID: model.TicketID(key), Key: key, Title: "title " + key, Status: status}
}

func blockedBy(keys ...string) []model.Link {
	links := make([]model.Link, 0, len(keys))
	for _, k := range keys {
		links = append(links, model.Link{
			Kind:   model.LinkBlockedBy,
			Target: model.LinkTarget{ID: model.TicketID(k), Key: k, Status: model.StatusTodo},
		})
	}
	return links
}

func graphOf(tickets []model.Ticket, links map[model.TicketID][]model.Link) model.BlockingGraph {
	return model.BuildBlockingGraph(tickets, links, model.Capabilities{BlockingLinks: true})
}

func layoutOf(tickets []model.Ticket, links map[model.TicketID][]model.Link) (model.BlockingGraph, frontierLayout) {
	g := graphOf(tickets, links)
	return g, layoutFrontier(g, frontierNodes(g, tickets, true), 120)
}

// Blockers sit left of their dependents: reading left to right is "this must
// finish before that".
func TestFrontierLayoutPutsBlockersLeftOfDependents(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusDone),
	}
	_, l := layoutOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2"),
		"#2": blockedBy("#3"),
		"#3": nil,
	})

	for _, want := range []struct {
		id     model.TicketID
		column int
	}{{"#3", 0}, {"#2", 1}, {"#1", 2}} {
		if got := l.columnOf[want.id]; got != want.column {
			t.Errorf("column of %s = %d, want %d", want.id, got, want.column)
		}
	}
}

// A Ghost Ticket's Links are never followed, so it has no blockers and always
// lands in column 0.
func TestFrontierLayoutPutsAGhostInColumnZero(t *testing.T) {
	tickets := []model.Ticket{blockingTicket("#1", model.StatusTodo)}
	_, l := layoutOf(tickets, map[model.TicketID][]model.Link{"#1": blockedBy("#ghost")})

	if got, ok := l.columnOf["#ghost"]; !ok || got != 0 {
		t.Errorf("Ghost column = %d (present %v), want 0", got, ok)
	}
	if l.columnOf["#1"] != 1 {
		t.Errorf("dependent column = %d, want 1", l.columnOf["#1"])
	}
}

// Cycle members share a component and therefore a column. The test is also the
// termination case: a layout that recursed through the cycle would hang, and
// the test would fail by timeout rather than by assertion.
func TestFrontierLayoutTerminatesOnACycleAndASelfLoop(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusTodo),
	}
	g, l := layoutOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2"),
		"#2": blockedBy("#1"),
		"#3": blockedBy("#3"),
	})

	if l.columnOf["#1"] != l.columnOf["#2"] {
		t.Errorf("cycle members are in columns %d and %d, want one column",
			l.columnOf["#1"], l.columnOf["#2"])
	}
	if len(g.Cycles()) != 2 {
		t.Fatalf("Cycles() = %v, want the two-cycle and the self-loop", g.Cycles())
	}
	for _, id := range []model.TicketID{"#1", "#2", "#3"} {
		if _, ok := l.nodeAt[id]; !ok {
			t.Errorf("%s was not drawn", id)
		}
	}
}

// A card is drawn once, onto cells no edge can reach: the waypoint pass makes
// every segment join adjacent columns, so routing is purely local.
func TestFrontierLayoutSpansTwoColumnsWithOneWaypointAndNoEdgeOverACard(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusDone),
	}
	// #1 is blocked by #3 directly and through #2, so the direct edge spans two
	// columns and needs exactly one waypoint.
	g, l := layoutOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2", "#3"),
		"#2": blockedBy("#3"),
		"#3": nil,
	})
	borders := make(map[model.TicketID]frontierBorder)
	for _, n := range frontierNodes(g, tickets, true) {
		borders[n.id] = n.emphasis.border
	}

	if l.columnOf["#1"] != 2 || l.columnOf["#2"] != 1 || l.columnOf["#3"] != 0 {
		t.Fatalf("columns = %v, want #3 0, #2 1, #1 2", l.columnOf)
	}

	// Every card is intact after routing: the waypoint pass makes each segment
	// join adjacent columns, so no edge can cross a card's cells.
	for id, rect := range l.nodeAt {
		border := borders[id]
		for x := rect.X; x < rect.X+rect.W; x++ {
			want := border.horizontal
			switch x {
			case rect.X:
				want = border.topLeft
			case rect.X + rect.W - 1:
				want = border.topRight
			}
			if got := l.cells[rect.Y][x].r; got != want {
				t.Fatalf("card %s top border at %d = %q, want %q", id, x, got, want)
			}
		}
	}

	// The waypoint's run crosses column 1's card band on a row no card occupies.
	waypointRow := -1
	for y := range l.cells {
		if !insideAnyCard(l, l.nodeAt["#2"].X, y) && l.cells[y][l.nodeAt["#2"].X].r == '─' {
			waypointRow = y
		}
	}
	if waypointRow < 0 {
		t.Error("the two-column edge inserted no waypoint")
	}
}

func insideAnyCard(l frontierLayout, x, y int) bool {
	for _, rect := range l.nodeAt {
		if x >= rect.X && x < rect.X+rect.W && y >= rect.Y && y < rect.Y+rect.H {
			return true
		}
	}
	return false
}

func TestMergeGlyph(t *testing.T) {
	tests := []struct {
		existing, incoming, want rune
	}{
		{' ', '─', '─'},
		{0, '│', '│'},
		{'─', '▶', '▶'},
		{'▶', '─', '▶'},
		{'─', '─', '─'},
		{'│', '│', '│'},
		{'─', '│', '┼'},
		{'│', '─', '┼'},
		{'┐', '┐', '┐'},
		{'┐', '└', '┼'},
	}
	for _, tt := range tests {
		if got := mergeGlyph(tt.existing, tt.incoming); got != tt.want {
			t.Errorf("mergeGlyph(%q, %q) = %q, want %q", tt.existing, tt.incoming, got, tt.want)
		}
	}
}

func TestClampFrontierOffset(t *testing.T) {
	tests := []struct{ offset, canvas, window, want int }{
		{-3, 100, 10, 0},
		{5, 100, 10, 5},
		{999, 100, 10, 90},
		{4, 8, 20, 0},
	}
	for _, tt := range tests {
		if got := clampFrontierOffset(tt.offset, tt.canvas, tt.window); got != tt.want {
			t.Errorf("clampFrontierOffset(%d, %d, %d) = %d, want %d",
				tt.offset, tt.canvas, tt.window, got, tt.want)
		}
	}
}

func TestEnsureNodeVisibleScrollsMinimally(t *testing.T) {
	rect := frontierRect{X: 60, Y: 24, W: 28, H: 5}

	x, y := ensureNodeVisible(rect, 0, 0, 40, 10)
	if x != 60-40+28 || y != 24-10+5 {
		t.Errorf("offsets = %d,%d, want the card's far edge just inside the body", x, y)
	}

	x, y = ensureNodeVisible(rect, 60, 24, 40, 10)
	if x != 60 || y != 24 {
		t.Errorf("offsets = %d,%d, want no movement for a card already in view", x, y)
	}

	x, _ = ensureNodeVisible(rect, 100, 24, 40, 10)
	if x != 60 {
		t.Errorf("offsetX = %d, want a scroll back to the card's left edge", x)
	}

	// A card wider than the body scrolls to its left edge rather than to a
	// right edge the reader cannot use.
	x, _ = ensureNodeVisible(rect, 0, 24, 10, 10)
	if x != 60 {
		t.Errorf("offsetX = %d, want the card's left edge", x)
	}
}

// Focus movement is the list's rule in two dimensions: it steps within a
// column, stays put at the ends, and crosses to the nearest row on the blocker
// or dependent side.
func TestFrontierFocusMovement(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusDone),
	}
	_, l := layoutOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#3"),
		"#2": blockedBy("#3"),
		"#3": nil,
	})

	if got, _ := l.moveFocus("#1", 0, 1); got != "#2" {
		t.Errorf("down from #1 = %q, want #2", got)
	}
	if got, _ := l.moveFocus("#2", 0, 1); got != "#2" {
		t.Errorf("down from the last node in a column = %q, want it to stay put", got)
	}
	if got, _ := l.moveFocus("#1", -1, 0); got != "#3" {
		t.Errorf("blocker side of #1 = %q, want #3", got)
	}
	if got, _ := l.moveFocus("#3", 1, 0); got != "#1" {
		t.Errorf("dependent side of #3 = %q, want the nearest row, #1", got)
	}
	if got, _ := l.moveFocus("#3", -1, 0); got != "#3" {
		t.Errorf("blocker side of a column-0 node = %q, want it to stay put", got)
	}
	if _, ok := l.moveFocus("#nowhere", 0, 1); ok {
		t.Error("moving focus from an unknown node reported success")
	}
}

// Emphasis is withheld until every Detail read has answered: a half-loaded
// Frontier that already claims something is Actionable is the one wrong answer
// this screen must not give.
func TestFrontierEmphasisIsWithheldUntilResolved(t *testing.T) {
	tickets := []model.Ticket{blockingTicket("#1", model.StatusTodo)}
	g := graphOf(tickets, map[model.TicketID][]model.Link{"#1": nil})

	loading := frontierNodes(g, tickets, false)
	if loading[0].emphasis.badge != "" {
		t.Errorf("badge while loading = %q, want none", loading[0].emphasis.badge)
	}
	resolved := frontierNodes(g, tickets, true)
	if resolved[0].emphasis.badge != "ACTIONABLE" {
		t.Errorf("badge once resolved = %q, want ACTIONABLE", resolved[0].emphasis.badge)
	}
}

func TestFrontierBadgeWords(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusTodo),
		blockingTicket("#4", model.StatusTodo),
	}
	// #2 is blocked by a Todo member; #3's own Links were never read; #4 sits on
	// a self-loop.
	g := graphOf(tickets, map[model.TicketID][]model.Link{
		"#1": nil,
		"#2": blockedBy("#1"),
		"#4": blockedBy("#4"),
	})
	nodes := frontierNodes(g, tickets, true)

	want := map[model.TicketID]string{
		"#1": "ACTIONABLE",
		"#2": "blocked by 1",
		"#3": "UNVERIFIED",
		"#4": "CYCLE",
	}
	for _, n := range nodes {
		if n.emphasis.badge != want[n.id] {
			t.Errorf("badge of %s = %q, want %q", n.id, n.emphasis.badge, want[n.id])
		}
	}
}

// A blocking Link that named no Ticket has no node to draw, so the card says
// how many there are rather than looking unblocked.
func TestFrontierBadgeCountsAnonymousBlockers(t *testing.T) {
	tickets := []model.Ticket{blockingTicket("#1", model.StatusTodo)}
	g := graphOf(tickets, map[model.TicketID][]model.Link{
		"#1": {
			{Kind: model.LinkBlockedBy, Target: model.LinkTarget{Title: "somewhere else"}},
			{Kind: model.LinkBlockedBy, Target: model.LinkTarget{Title: "and again"}},
		},
	})
	nodes := frontierNodes(g, tickets, true)

	if got := frontierBadgeLine(nodes[0], g); got != "UNVERIFIED +2 unnamed blockers" {
		t.Errorf("badge line = %q, want an unverified card carrying the unnamed blocker count", got)
	}
}
