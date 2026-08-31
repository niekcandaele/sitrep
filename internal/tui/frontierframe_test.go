package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
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
	return g, layoutFrontier(g, frontierNodes(g, tickets, true), frontierLayoutOptions{
		innerWidth: 120,
		direction:  frontierRanksHorizontal,
	})
}

func crossingFrontierFixture() ([]model.Ticket, map[model.TicketID][]model.Link) {
	tickets := make([]model.Ticket, 0, 7)
	for _, id := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		tickets = append(tickets, blockingTicket(id, model.StatusTodo))
	}
	return tickets, map[model.TicketID][]model.Link{
		"A": nil,
		"B": nil,
		"C": blockedBy("B"),
		"D": blockedBy("A"),
		"E": blockedBy("D"),
		"F": blockedBy("C"),
		"G": blockedBy("E", "F", "A"),
	}
}

func frontierRouteCells(route frontierRoute) map[frontierPoint]struct{} {
	cells := make(map[frontierPoint]struct{})
	for _, stroke := range route.strokes {
		dx, dy := 0, 0
		if stroke.from.x < stroke.to.x {
			dx = 1
		} else if stroke.from.x > stroke.to.x {
			dx = -1
		}
		if stroke.from.y < stroke.to.y {
			dy = 1
		} else if stroke.from.y > stroke.to.y {
			dy = -1
		}
		for at := stroke.from; ; at.x, at.y = at.x+dx, at.y+dy {
			cells[at] = struct{}{}
			if at == stroke.to {
				break
			}
		}
	}
	return cells
}

func sharedRouteCrossings(l frontierLayout, first, second frontierRouteID) []frontierPoint {
	firstCells := frontierRouteCells(l.routes[int(first)-1])
	secondCells := frontierRouteCells(l.routes[int(second)-1])
	var crossings []frontierPoint
	for at := range firstCells {
		if _, shared := secondCells[at]; shared && l.cells[at.y][at.x].r == '┼' {
			crossings = append(crossings, at)
		}
	}
	return crossings
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
		if got := l.rankOf[want.id]; got != want.column {
			t.Errorf("column of %s = %d, want %d", want.id, got, want.column)
		}
	}
}

// A Ghost Ticket's Links are never followed, so it has no blockers and always
// lands in column 0.
func TestFrontierLayoutPutsAGhostInColumnZero(t *testing.T) {
	tickets := []model.Ticket{blockingTicket("#1", model.StatusTodo)}
	_, l := layoutOf(tickets, map[model.TicketID][]model.Link{"#1": blockedBy("#ghost")})

	if got, ok := l.rankOf["#ghost"]; !ok || got != 0 {
		t.Errorf("Ghost column = %d (present %v), want 0", got, ok)
	}
	if l.rankOf["#1"] != 1 {
		t.Errorf("dependent column = %d, want 1", l.rankOf["#1"])
	}
}

func TestFrontierDeduplicatesBlocksFromDuplicateMembers(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("a", model.StatusTodo),
		blockingTicket("a", model.StatusTodo),
		blockingTicket("c", model.StatusTodo),
	}
	g := graphOf(tickets, map[model.TicketID][]model.Link{
		"a": {{
			Kind: model.LinkBlocks,
			Target: model.LinkTarget{
				ID: "c", Key: "c", Status: model.StatusTodo,
			},
		}},
		"c": {},
	})
	nodes := frontierNodes(g, tickets, true)
	index := make(map[model.TicketID]int, len(nodes))
	for i, node := range nodes {
		index[node.id] = i
	}

	edges := frontierEdges(g, index)
	want := frontierEdge{dependent: index["c"], blocker: index["a"]}
	if len(edges) != 1 || edges[0] != want {
		t.Errorf("frontierEdges() = %+v, want only %+v", edges, want)
	}

	for _, node := range nodes {
		if node.id == "c" {
			if node.emphasis.badge != "blocked by 1" {
				t.Errorf("c badge = %q, want %q", node.emphasis.badge, "blocked by 1")
			}
			return
		}
	}
	t.Fatal("c has no Frontier node")
}

func TestFrontierDeduplicatesBlockedByFromDuplicateDependentMembers(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("a", model.StatusTodo),
		blockingTicket("a", model.StatusTodo),
		blockingTicket("c", model.StatusTodo),
	}
	g := graphOf(tickets, map[model.TicketID][]model.Link{
		"a": blockedBy("c"),
		"c": nil,
	})

	plan := planFrontierRanks(g, frontierNodes(g, tickets, true))
	if len(plan.routes) != 1 {
		t.Fatalf("routes = %+v, want one canonical c -> a route", plan.routes)
	}
	if got := plan.routes[0]; got.blocker != "c" || got.dependent != "a" {
		t.Errorf("route = %+v, want blocker c and dependent a", got)
	}
	for _, id := range []model.TicketID{"a", "c"} {
		if got, want := plan.incident[id], []frontierRouteID{1}; !reflect.DeepEqual(got, want) {
			t.Errorf("incident[%s] = %v, want %v", id, got, want)
		}
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
	links := map[model.TicketID][]model.Link{
		"#1": blockedBy("#2"),
		"#2": blockedBy("#1"),
		"#3": blockedBy("#3"),
	}
	g := graphOf(tickets, links)
	if len(g.Cycles()) != 2 {
		t.Fatalf("Cycles() = %v, want the two-cycle and the self-loop", g.Cycles())
	}
	for _, direction := range []frontierRankDirection{frontierRanksHorizontal, frontierRanksVertical} {
		l := layoutFrontier(g, frontierNodes(g, tickets, true), frontierLayoutOptions{
			innerWidth: 120, direction: direction,
		})
		if l.rankOf["#1"] != l.rankOf["#2"] {
			t.Errorf("direction %v cycle members are in ranks %d and %d, want one rank",
				direction, l.rankOf["#1"], l.rankOf["#2"])
		}
		for _, id := range []model.TicketID{"#1", "#2", "#3"} {
			if _, ok := l.nodeAt[id]; !ok {
				t.Errorf("direction %v did not draw %s", direction, id)
			}
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

	if l.rankOf["#1"] != 2 || l.rankOf["#2"] != 1 || l.rankOf["#3"] != 0 {
		t.Fatalf("columns = %v, want #3 0, #2 1, #1 2", l.rankOf)
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

func TestFrontierRoutesRetainCanonicalIdentityGeometryAndCrossings(t *testing.T) {
	tickets, links := crossingFrontierFixture()
	g := graphOf(tickets, links)
	nodes := frontierNodes(g, tickets, true)
	plan := planFrontierRanks(g, nodes)
	wantRoutes := []struct {
		blocker, dependent model.TicketID
	}{
		{"B", "C"}, {"A", "D"}, {"D", "E"}, {"C", "F"},
		{"E", "G"}, {"F", "G"}, {"A", "G"},
	}
	if len(plan.routes) != len(wantRoutes) {
		t.Fatalf("routes = %d, want %d", len(plan.routes), len(wantRoutes))
	}
	for i, want := range wantRoutes {
		got := plan.routes[i]
		if got.blocker != want.blocker || got.dependent != want.dependent {
			t.Errorf("route %d endpoints = %s -> %s, want %s -> %s",
				i+1, got.blocker, got.dependent, want.blocker, want.dependent)
		}
	}
	wantIncident := map[model.TicketID][]frontierRouteID{
		"A": {2, 7}, "B": {1}, "C": {1, 4}, "D": {2, 3},
		"E": {3, 5}, "F": {4, 6}, "G": {5, 6, 7},
	}
	if !reflect.DeepEqual(plan.incident, wantIncident) {
		t.Errorf("incident index = %v, want canonical %v", plan.incident, wantIncident)
	}

	segments := 0
	for _, rank := range plan.segments {
		for _, segment := range rank {
			segments++
			if segment.routeID == 0 || int(segment.routeID) > len(plan.routes) {
				t.Errorf("segment %+v has no canonical route", segment)
			}
		}
	}
	if segments != 9 {
		t.Errorf("local segments = %d, want six adjacent plus three for the long route", segments)
	}
	dummies := 0
	for _, rank := range plan.slots {
		for _, slot := range rank {
			if slot.dummy {
				dummies++
				if slot.routeID != 7 {
					t.Errorf("long-span dummy route = %d, want canonical route 7", slot.routeID)
				}
			}
		}
	}
	if dummies != 2 {
		t.Errorf("long-span dummies = %d, want 2", dummies)
	}

	layout := layoutFrontier(g, nodes, frontierLayoutOptions{
		innerWidth: 200, direction: frontierRanksHorizontal, plan: &plan,
	})
	for i, route := range layout.routes {
		if route.blocker != wantRoutes[i].blocker || route.dependent != wantRoutes[i].dependent {
			t.Errorf("materialized route %d lost semantic endpoints: %+v", i+1, route)
		}
		if len(route.strokes) == 0 {
			t.Errorf("materialized route %d has no geometry", i+1)
		}
		for j := 1; j < len(route.strokes); j++ {
			previous, current := route.strokes[j-1], route.strokes[j]
			distance := absInt(previous.to.x-current.from.x) + absInt(previous.to.y-current.from.y)
			if distance != 1 {
				t.Errorf("route %d strokes %d and %d are not blocker-to-dependent neighbors: %+v then %+v",
					i+1, j-1, j, previous, current)
			}
		}
	}
	for _, pair := range [][2]frontierRouteID{{1, 2}, {3, 4}} {
		if crossings := sharedRouteCrossings(layout, pair[0], pair[1]); len(crossings) == 0 {
			t.Errorf("routes %d and %d have no preserved shared crossing", pair[0], pair[1])
		}
	}

	againPlan := planFrontierRanks(g, nodes)
	again := layoutFrontier(g, nodes, frontierLayoutOptions{
		innerWidth: 200, direction: frontierRanksHorizontal, plan: &againPlan,
	})
	if !reflect.DeepEqual(layout, again) {
		t.Error("identical graph input produced non-deterministic route metadata or materialization")
	}
}

func TestFocusedFrontierCrossingUsesEitherOwnerAndIncidentRoutesOnly(t *testing.T) {
	tickets, links := crossingFrontierFixture()
	_, layout := layoutOfDirection(tickets, links, frontierRanksHorizontal, 200)
	crossings := sharedRouteCrossings(layout, 1, 2)
	if len(crossings) == 0 {
		t.Fatal("crossing fixture has no shared route-1/route-2 junction")
	}
	crossing := crossings[0]
	glyphAt := func(focus model.TicketID) rune {
		t.Helper()
		lines := renderFrontierCanvas(layout, focus, true, 0, 0,
			layout.width, layout.height, DefaultStyles(true))
		row := []rune(ansi.Strip(lines[crossing.y]))
		if crossing.x >= len(row) {
			t.Fatalf("crossing x=%d is outside rendered row width %d", crossing.x, len(row))
		}
		return row[crossing.x]
	}
	if got := glyphAt("C"); got != '╋' {
		t.Errorf("dependent owner C crossing = %q, want heavy junction", got)
	}
	if got := glyphAt("D"); got != '╋' {
		t.Errorf("dependent owner D crossing = %q, want heavy junction", got)
	}
	if got := glyphAt("E"); got != '┼' {
		t.Errorf("unrelated focus E crossing = %q, want normal junction", got)
	}

	overlay := focusedFrontierOverlay(layout, "C", 0, 0, layout.width, layout.height)
	if got := layout.incident["C"]; !reflect.DeepEqual(got, []frontierRouteID{1, 4}) {
		t.Fatalf("C incident routes = %v, want inbound 1 and outbound 4", got)
	}
	for _, routeID := range []frontierRouteID{1, 4} {
		found := false
		for at := range frontierRouteCells(layout.routes[int(routeID)-1]) {
			if _, highlighted := overlay[[2]int{at.x, at.y}]; highlighted {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("incident route %d contributes no focused cells", routeID)
		}
	}
	for at := range frontierRouteCells(layout.routes[4]) {
		if _, highlighted := overlay[[2]int{at.x, at.y}]; highlighted {
			t.Errorf("nonincident route 5 highlighted at %+v", at)
			break
		}
	}
}

func TestFocusedFrontierOverlayClipsAndRejectsProtectedCells(t *testing.T) {
	cells := make([]frontierCell, 8)
	for i := range cells {
		cells[i] = frontierCell{r: '─'}
	}
	cells[4] = frontierCell{continuation: true}
	cells[5] = frontierCell{r: '─', link: 1}
	layout := frontierLayout{
		cells:  [][]frontierCell{cells},
		width:  8,
		height: 1,
		order:  []model.TicketID{"card"},
		nodeAt: map[model.TicketID]frontierRect{"card": {X: 2, W: 2, H: 1}},
		routes: []frontierRoute{
			{blocker: "focus", dependent: "dependent", strokes: []frontierStroke{{
				from: frontierPoint{x: -1_000_000}, to: frontierPoint{x: 5},
			}}},
			{blocker: "other", dependent: "elsewhere", strokes: []frontierStroke{{
				from: frontierPoint{x: 6}, to: frontierPoint{x: 7},
			}}},
		},
		incident: map[model.TicketID][]frontierRouteID{"focus": {1}},
	}
	overlay := focusedFrontierOverlay(layout, "focus", 0, 0, 8, 1)
	want := map[[2]int]frontierCell{
		{0, 0}: {r: '─', style: frontierFocusedEdgeStyle},
		{1, 0}: {r: '─', style: frontierFocusedEdgeStyle},
	}
	if !reflect.DeepEqual(overlay, want) {
		t.Errorf("protected/clipped overlay = %#v, want only visible unprotected incident cells %#v", overlay, want)
	}
}

func TestFocusedFrontierHeavyGlyphsSurviveANSIStripping(t *testing.T) {
	const light = "─│┌┐└┘┼▶▼"
	const heavy = "━┃┏┓┗┛╋▶▼"
	if got := heavyFrontierEdgeText(light); got != heavy {
		t.Errorf("heavy substitution = %q, want %q", got, heavy)
	}
	styled := DefaultStyles(true).FrontierBold.Render(heavyFrontierEdgeText(light))
	if got := ansi.Strip(styled); got != heavy {
		t.Errorf("ANSI-stripped heavy geometry = %q, want %q", got, heavy)
	}
}

func TestFrontierRouteIdentityPreservesGhostsLongSpansCyclesAndDirections(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("P", model.StatusTodo),
		blockingTicket("Q", model.StatusTodo),
		blockingTicket("X", model.StatusTodo),
		blockingTicket("Y", model.StatusTodo),
	}
	links := map[model.TicketID][]model.Link{
		"P": blockedBy("Q", "ghost"),
		"Q": blockedBy("ghost"),
		"X": blockedBy("Y"),
		"Y": blockedBy("X"),
	}
	for _, direction := range []frontierRankDirection{frontierRanksHorizontal, frontierRanksVertical} {
		t.Run(fmt.Sprintf("direction-%d", direction), func(t *testing.T) {
			g, layout := layoutOfDirection(tickets, links, direction, 160)
			wantRoutes := map[[2]model.TicketID]bool{
				{"Q", "P"}: true, {"ghost", "P"}: true, {"ghost", "Q"}: true,
			}
			for _, route := range layout.routes {
				delete(wantRoutes, [2]model.TicketID{route.blocker, route.dependent})
			}
			if len(wantRoutes) != 0 {
				t.Errorf("direction %d missing semantic routes %v", direction, wantRoutes)
			}
			if len(layout.incident["X"]) != 0 || len(layout.incident["Y"]) != 0 {
				t.Errorf("direction %d routed SCC-internal cycle edges: X=%v Y=%v",
					direction, layout.incident["X"], layout.incident["Y"])
			}
			if len(g.Cycles()) != 1 {
				t.Errorf("direction %d cycles = %v, want X/Y cycle truth", direction, g.Cycles())
			}
			longRoute := -1
			for i, route := range layout.routes {
				if route.blocker == "ghost" && route.dependent == "P" {
					longRoute = i
				}
			}
			if longRoute < 0 || len(layout.routes[longRoute].strokes) < 3 {
				t.Fatalf("direction %d long Ghost route has no dummy geometry", direction)
			}
			overlay := focusedFrontierOverlay(layout, "ghost", 0, 0, layout.width, layout.height)
			for at := range overlay {
				if layout.insideCard(at[0], at[1]) {
					t.Errorf("direction %d focused edge overwrote card at %v", direction, at)
				}
			}
			visible := ansi.Strip(strings.Join(renderFrontierCanvas(layout, "ghost", true,
				0, 0, layout.width, layout.height, DefaultStyles(true)), "\n"))
			if !strings.ContainsAny(visible, "━┃┏┓┗┛╋") {
				t.Errorf("direction %d rendered no heavy Ghost incident geometry", direction)
			}
			if direction == frontierRanksHorizontal {
				if !strings.ContainsRune(visible, '▶') || strings.ContainsRune(visible, '▼') {
					t.Errorf("horizontal route arrows lost direction:\n%s", visible)
				}
			} else if !strings.ContainsRune(visible, '▼') || strings.ContainsRune(visible, '▶') {
				t.Errorf("vertical route arrows lost direction:\n%s", visible)
			}
		})
	}
}

func TestFrontierCellsCarryNoRouteOwnershipMetadata(t *testing.T) {
	cellType := reflect.TypeOf(frontierCell{})
	for i := range cellType.NumField() {
		field := cellType.Field(i)
		name := strings.ToLower(field.Name)
		if strings.Contains(name, "route") || strings.Contains(name, "owner") {
			t.Errorf("frontierCell field %q stores topology ownership", field.Name)
		}
		if field.Type.Kind() == reflect.Slice {
			t.Errorf("frontierCell field %q is a per-cell slice", field.Name)
		}
	}
}

func TestClipFrontierStrokePreservesAxisAndTravelOrder(t *testing.T) {
	clip := frontierRect{X: 2, Y: 3, W: 4, H: 5}
	for _, test := range []struct {
		name   string
		stroke frontierStroke
		want   frontierStroke
		ok     bool
	}{
		{"right", frontierStroke{frontierPoint{x: -9, y: 4}, frontierPoint{x: 20, y: 4}}, frontierStroke{frontierPoint{x: 2, y: 4}, frontierPoint{x: 5, y: 4}}, true},
		{"left", frontierStroke{frontierPoint{x: 20, y: 4}, frontierPoint{x: -9, y: 4}}, frontierStroke{frontierPoint{x: 5, y: 4}, frontierPoint{x: 2, y: 4}}, true},
		{"down", frontierStroke{frontierPoint{x: 3, y: -9}, frontierPoint{x: 3, y: 20}}, frontierStroke{frontierPoint{x: 3, y: 3}, frontierPoint{x: 3, y: 7}}, true},
		{"up", frontierStroke{frontierPoint{x: 3, y: 20}, frontierPoint{x: 3, y: -9}}, frontierStroke{frontierPoint{x: 3, y: 7}, frontierPoint{x: 3, y: 3}}, true},
		{"outside", frontierStroke{frontierPoint{x: 1, y: 0}, frontierPoint{x: 1, y: 20}}, frontierStroke{}, false},
		{"diagonal", frontierStroke{frontierPoint{x: 2, y: 3}, frontierPoint{x: 3, y: 4}}, frontierStroke{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := clipFrontierStroke(test.stroke, clip)
			if ok != test.ok || got != test.want {
				t.Errorf("clip = %+v/%t, want %+v/%t", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestMergeGlyph(t *testing.T) {
	tests := []struct {
		existing, incoming, want rune
	}{
		{' ', '─', '─'},
		{0, '│', '│'},
		{'─', '▶', '▶'},
		{'▶', '─', '▶'},
		{'│', '▼', '▼'},
		{'▼', '│', '▼'},
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

func layoutOfDirection(tickets []model.Ticket, links map[model.TicketID][]model.Link,
	direction frontierRankDirection, innerWidth int) (model.BlockingGraph, frontierLayout) {
	g := graphOf(tickets, links)
	return g, layoutFrontier(g, frontierNodes(g, tickets, true), frontierLayoutOptions{
		innerWidth: innerWidth,
		direction:  direction,
	})
}

func TestFrontierVerticalLayoutKeepsCardsUprightAndRoutesDown(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusDone),
	}
	links := map[model.TicketID][]model.Link{
		"#1": blockedBy("#2", "#3"),
		"#2": blockedBy("#3"),
		"#3": nil,
	}
	g, layout := layoutOfDirection(tickets, links, frontierRanksVertical, 120)
	if layout.direction != frontierRanksVertical {
		t.Fatalf("direction = %v, want vertical", layout.direction)
	}
	if layout.nodeAt["#3"].Y >= layout.nodeAt["#2"].Y ||
		layout.nodeAt["#2"].Y >= layout.nodeAt["#1"].Y {
		t.Fatalf("vertical ranks = #3:%+v #2:%+v #1:%+v, want blockers above dependents",
			layout.nodeAt["#3"], layout.nodeAt["#2"], layout.nodeAt["#1"])
	}
	for id, rect := range layout.nodeAt {
		if rect.W != frontierCardWidth || rect.H != frontierCardHeight {
			t.Errorf("%s rect = %+v, want an upright %dx%d card", id, rect, frontierCardWidth, frontierCardHeight)
		}
	}

	arrows := 0
	for y, row := range layout.cells {
		for x, cell := range row {
			if cell.r == '▶' {
				t.Errorf("horizontal arrow at %d,%d in vertical layout", x, y)
			}
			if cell.r == '▼' {
				arrows++
			}
		}
	}
	if arrows == 0 {
		t.Fatal("vertical layout drew no downward arrow")
	}

	// The direct #3 -> #1 edge spans the real #2 rank. Its dummy must run
	// vertically through that rank in a peer slot no card occupies.
	middle := layout.nodeAt["#2"]
	waypointColumn := -1
	for x := range layout.width {
		if !insideAnyCard(layout, x, middle.Y) && layout.cells[middle.Y][x].r == '│' {
			waypointColumn = x
			break
		}
	}
	if waypointColumn < 0 {
		t.Error("the vertical long-span edge inserted no dummy waypoint")
	}

	borders := make(map[model.TicketID]frontierBorder)
	for _, node := range frontierNodes(g, tickets, true) {
		borders[node.id] = node.emphasis.border
	}
	for id, rect := range layout.nodeAt {
		border := borders[id]
		for x := rect.X; x < rect.X+rect.W; x++ {
			want := border.horizontal
			switch x {
			case rect.X:
				want = border.topLeft
			case rect.X + rect.W - 1:
				want = border.topRight
			}
			if got := layout.cells[rect.Y][x].r; got != want {
				t.Fatalf("vertical routing overwrote %s top border at %d with %q, want %q", id, x, got, want)
			}
		}
	}
}

func TestFrontierVerticalLayoutPlacesGhostBlockersAboveDependents(t *testing.T) {
	tickets := []model.Ticket{blockingTicket("#1", model.StatusTodo)}
	_, layout := layoutOfDirection(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#ghost"),
	}, frontierRanksVertical, 120)

	ghost, ghostDrawn := layout.nodeAt["#ghost"]
	dependent, dependentDrawn := layout.nodeAt["#1"]
	if !ghostDrawn || !dependentDrawn {
		t.Fatalf("drawn nodes = %v, want dependent and Ghost", layout.nodeAt)
	}
	if layout.rankOf["#ghost"] != 0 || ghost.Y >= dependent.Y {
		t.Errorf("Ghost rect/rank = %+v/%d, dependent = %+v; want rank-zero Ghost above",
			ghost, layout.rankOf["#ghost"], dependent)
	}
	if ghost.W != frontierCardWidth || ghost.H != frontierCardHeight {
		t.Errorf("Ghost rect = %+v, want upright %dx%d", ghost, frontierCardWidth, frontierCardHeight)
	}
}

func TestFrontierVerticalPhysicalNavigationAndCanonicalTie(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusDone),
	}
	_, layout := layoutOfDirection(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#3"), "#2": blockedBy("#3"), "#3": nil,
	}, frontierRanksVertical, 120)
	for _, test := range []struct {
		name   string
		from   model.TicketID
		dx, dy int
		want   model.TicketID
	}{
		{"right moves among peers", "#1", 1, 0, "#2"},
		{"left moves among peers", "#2", -1, 0, "#1"},
		{"up crosses to blocker rank", "#1", 0, -1, "#3"},
		{"down crosses to dependent rank", "#3", 0, 1, "#1"},
		{"unavailable rank retains focus", "#3", 0, -1, "#3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := layout.moveFocus(test.from, test.dx, test.dy); !ok || got != test.want {
				t.Errorf("move = %q/%t, want %q/true", got, ok, test.want)
			}
		})
	}

	// Handcrafted geometry makes the equal-distance rule observable: canonical
	// byRank order, not map order, breaks the tie.
	tie := frontierLayout{
		direction: frontierRanksVertical,
		rankOf:    map[model.TicketID]int{"from": 0, "first": 1, "second": 1},
		byRank:    [][]model.TicketID{{"from"}, {"first", "second"}},
		nodeAt: map[model.TicketID]frontierRect{
			"from": {X: 10}, "first": {X: 0}, "second": {X: 20},
		},
	}
	if got, _ := tie.moveFocus("from", 0, 1); got != "first" {
		t.Errorf("equal-distance tie focused %q, want first canonical peer", got)
	}
}

func TestFrontierDirectionChoiceUsesFitPhysicalRowsAndHorizontalTies(t *testing.T) {
	chain := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusTodo),
	}
	g := graphOf(chain, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2"), "#2": blockedBy("#3"), "#3": nil,
	})
	plan := planFrontierRanks(g, frontierNodes(g, chain, true))
	candidates := frontierCandidates(plan, 80)
	if candidates[frontierRanksHorizontal].fullyFits(80, 24) ||
		!candidates[frontierRanksVertical].fullyFits(80, 24) {
		t.Fatalf("chain candidates = %+v, want only vertical to fit 80x24", candidates)
	}
	if got := chooseFrontierDirection(candidates, 80, 24); got != frontierRanksVertical {
		t.Errorf("sole-fit direction = %v, want vertical", got)
	}

	probe := frontierLayoutCandidate{width: 3, height: 1}
	if got, want := frontierAspectError(probe, 80, 24), absInt(3*48-80); got != want {
		t.Errorf("80x24 aspect error = %d, want 80x48 cross-product %d", got, want)
	}

	// This metadata shape is the correction-sensitive boundary: both candidates
	// overflow, raw 80x24 would prefer vertical, but physical 80x48 prefers horizontal.
	boundary := [2]frontierLayoutCandidate{
		{direction: frontierRanksHorizontal, width: 96, height: 24},
		{direction: frontierRanksVertical, width: 112, height: 30},
	}
	if got := chooseFrontierDirection(boundary, 80, 24); got != frontierRanksHorizontal {
		t.Errorf("corrected boundary direction = %v, want horizontal", got)
	}
	rawError := func(candidate frontierLayoutCandidate) int {
		return absInt(candidate.width*24 - 80*candidate.height)
	}
	if rawError(boundary[frontierRanksVertical]) >= rawError(boundary[frontierRanksHorizontal]) {
		t.Fatal("boundary fixture does not change without the physical-row correction")
	}

	tie := [2]frontierLayoutCandidate{
		{direction: frontierRanksHorizontal, width: 10, height: 10},
		{direction: frontierRanksVertical, width: 10, height: 10},
	}
	if got := chooseFrontierDirection(tie, 80, 24); got != frontierRanksHorizontal {
		t.Errorf("exact tie direction = %v, want horizontal", got)
	}
}

func TestFrontierDirectionHysteresisIsStrictAndSeatLocal(t *testing.T) {
	bothFit := [2]frontierLayoutCandidate{{width: 20, height: 10}, {width: 10, height: 20}}
	if got := frontierDirectionAfterResize(frontierRanksVertical, bothFit, 30, 30); got != frontierRanksVertical {
		t.Errorf("both-fit resize changed to %v", got)
	}
	bothOverflow := [2]frontierLayoutCandidate{{width: 40, height: 10}, {width: 10, height: 40}}
	if got := frontierDirectionAfterResize(frontierRanksVertical, bothOverflow, 20, 20); got != frontierRanksVertical {
		t.Errorf("both-overflow resize changed to %v", got)
	}
	if got := frontierDirectionAfterResize(frontierRanksVertical, bothFit, 25, 15); got != frontierRanksHorizontal {
		t.Errorf("clear fit boundary retained %v, want horizontal", got)
	}
	if got := frontierDirectionAfterResize(frontierRanksHorizontal, bothFit, 30, 30); got != frontierRanksHorizontal {
		t.Errorf("ordinary both-fit resize flipped back to %v", got)
	}
}

func TestFrontierCandidateMeasurementAllocatesNoCellGrid(t *testing.T) {
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusTodo),
	}
	g := graphOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2", "#3"), "#2": blockedBy("#3"), "#3": nil,
	})
	plan := planFrontierRanks(g, frontierNodes(g, tickets, true))
	if allocations := testing.AllocsPerRun(100, func() {
		_ = frontierCandidates(plan, 80)
	}); allocations != 0 {
		t.Errorf("candidate measurement allocated %.0f objects, want metadata only", allocations)
	}
}

func TestFrontierInnerRectDegradesPerAxisAndChromeStaysInRing(t *testing.T) {
	for _, test := range []struct {
		width, height int
		want          frontierRect
	}{
		{0, 5, frontierRect{Y: 1, H: 3}},
		{5, 0, frontierRect{X: 1, W: 3}},
		{-1, 3, frontierRect{Y: 1, H: 1}},
		{1, 1, frontierRect{W: 1, H: 1}},
		{2, 2, frontierRect{X: 1, Y: 1, W: 1, H: 1}},
		{3, 3, frontierRect{X: 1, Y: 1, W: 1, H: 1}},
		{4, 5, frontierRect{X: 1, Y: 1, W: 2, H: 3}},
		{1, 3, frontierRect{X: 0, Y: 1, W: 1, H: 1}},
		{3, 2, frontierRect{X: 1, Y: 1, W: 1, H: 1}},
	} {
		if got := frontierInnerRect(test.width, test.height); got != test.want {
			t.Errorf("frontierInnerRect(%d,%d) = %+v, want %+v", test.width, test.height, got, test.want)
		}
	}

	if got := frontierCardWidthFor(28); got != 26 {
		t.Errorf("inner width 28 chose card width %d, want 26 (outer width must not leak into narrow input)", got)
	}

	layout := frontierLayout{
		width: 20, height: 20,
		styles: []frontierStyle{{Role: frontierRoleText}},
		cells:  make([][]frontierCell, 20),
	}
	for y := range layout.cells {
		layout.cells[y] = make([]frontierCell, 20)
		for x := range layout.cells[y] {
			layout.cells[y][x] = frontierCell{r: ' '}
		}
	}
	for _, size := range []struct {
		width, height int
		want          map[[2]int]rune
	}{
		{0, 5, map[[2]int]rune{}},
		{5, 0, map[[2]int]rune{}},
		{1, 1, map[[2]int]rune{}},
		{2, 2, map[[2]int]rune{{1, 0}: '▲', {0, 1}: '‹'}},
		{3, 3, map[[2]int]rune{{1, 0}: '▲', {1, 2}: '▼', {0, 1}: '‹', {2, 1}: '›'}},
		{8, 6, map[[2]int]rune{{4, 0}: '▲', {4, 5}: '▼', {0, 3}: '‹', {7, 3}: '›'}},
	} {
		inner := frontierInnerRect(size.width, size.height)
		chrome := frontierChromeGlyphs(layout, inner, 1, 1, size.width, size.height)
		if !reflect.DeepEqual(chrome, size.want) {
			t.Errorf("%dx%d chrome = %v, want fixed ring cues %v", size.width, size.height, chrome, size.want)
		}
		for at := range chrome {
			if at[0] >= inner.X && at[0] < inner.X+inner.W &&
				at[1] >= inner.Y && at[1] < inner.Y+inner.H {
				t.Errorf("%dx%d chrome %v overlaps inner %+v", size.width, size.height, at, inner)
			}
		}
		lines := renderFrontierBody(layout, "", false, 1, 1, size.width, size.height, DefaultStyles(true))
		if len(lines) != size.height {
			t.Errorf("%dx%d body has %d lines, want %d", size.width, size.height, len(lines), size.height)
		}
		for _, line := range lines {
			if got := lipgloss.Width(line); got != size.width {
				t.Errorf("%dx%d body line width = %d, want exactly %d", size.width, size.height, got, size.width)
			}
		}
	}
}

// Graph-derived emphasis is withheld until every Detail read has answered: a
// half-loaded Frontier visibly says PENDING instead of giving a premature graph
// conclusion.
func TestFrontierEmphasisIsWithheldUntilResolved(t *testing.T) {
	tickets := []model.Ticket{blockingTicket("#1", model.StatusTodo)}
	g := graphOf(tickets, map[model.TicketID][]model.Link{"#1": nil})

	loading := frontierNodes(g, tickets, false)
	if loading[0].emphasis.badge != "PENDING" {
		t.Errorf("badge while loading = %q, want PENDING", loading[0].emphasis.badge)
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

	if got := frontierBadgeLine(nodes[0], g, 60); got != "UNVERIFIED +2 unnamed blockers" {
		t.Errorf("badge line = %q, want an unverified card carrying the unnamed blocker count", got)
	}
}

// A Ghost reached as a dependent — a member's Blocks Link pointing out of the
// Watchlist — is drawn connected to the member that blocks it. The Ref-list
// fixture is the real shape: #112 blocks #116, which is not one of its members.
func TestFrontierLayoutConnectsAGhostReachedAsADependent(t *testing.T) {
	tickets := fake.FixtureRefListSnapshot().Tickets
	links := make(map[model.TicketID][]model.Link, len(tickets))
	for _, ticket := range tickets {
		links[ticket.ID] = fake.FixtureDetails()[ticket.ID].Links
	}
	g, l := layoutOf(tickets, links)

	const ghost = model.TicketID("acme/widgets#116")
	if _, drawn := l.nodeAt[ghost]; !drawn {
		t.Fatalf("%s is not on the canvas, so this test proves nothing", ghost)
	}
	if got := l.rankOf[ghost]; got != l.rankOf["acme/widgets#112"]+1 {
		t.Errorf("the Ghost sits in column %d, want the column right of the member that blocks it",
			got)
	}

	index := make(map[model.TicketID]int, len(l.order))
	for i, id := range l.order {
		index[id] = i
	}
	for _, edge := range frontierEdges(g, index) {
		if l.order[edge.dependent] == ghost && l.order[edge.blocker] == "acme/widgets#112" {
			return
		}
	}
	t.Errorf("no edge joins %s to the member that blocks it: it is drawn floating", ghost)
}

// Overflow chrome never shares an inner-canvas cell, including a continuation
// cell owned by a double-width rune.
func TestFrontierOverflowGlyphsNeverLandOnADoubleWidthRune(t *testing.T) {
	wide := func(id, title string) model.Ticket {
		return model.Ticket{
			ID: model.TicketID(id), Key: id, Title: title, Status: model.StatusTodo,
		}
	}
	tickets := []model.Ticket{
		wide("#1", "分散シャード再調整の計画"),
		wide("#2", "重み付けテーブルの移行"),
		wide("#3", "配置ヒューリスティクス"),
		wide("#4", "シャードの安全な退避"),
		wide("#5", "ロールアウト手順書の公開"),
	}
	_, l := layoutOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2"), "#2": blockedBy("#3"), "#3": nil,
		"#4": blockedBy("#3"), "#5": blockedBy("#3"),
	})

	const width, height = 40, 9
	inner := frontierInnerRect(width, height)
	if l.width <= inner.W || l.height <= inner.H {
		t.Fatalf("canvas is %dx%d, which fits inner %dx%d: no chrome is drawn",
			l.width, l.height, inner.W, inner.H)
	}
	placed := 0
	for offsetY := 0; offsetY <= l.height-inner.H; offsetY++ {
		for offsetX := 0; offsetX <= l.width-inner.W; offsetX++ {
			for at := range frontierChromeGlyphs(l, inner, offsetX, offsetY, width, height) {
				placed++
				if at[0] >= inner.X && at[0] < inner.X+inner.W &&
					at[1] >= inner.Y && at[1] < inner.Y+inner.H {
					t.Fatalf("chrome %v landed inside inner rect %+v", at, inner)
				}
			}
			for i, line := range renderFrontierBody(l, "", false,
				offsetX, offsetY, width, height, DefaultStyles(true)) {
				if got := lipgloss.Width(line); got != width {
					t.Fatalf("at offset %d,%d line %d is %d columns wide, want exactly %d: %q",
						offsetX, offsetY, i, got, width, line)
				}
			}
		}
	}
	if placed == 0 {
		t.Fatal("no ring chrome was placed anywhere, so this test asserts nothing")
	}
}

// On a card too narrow to hold both, the badge keeps its columns and the
// display-only Native Status gives way. A screen whose whole purpose is "what
// can be picked up" must not lose "blocked by 3" to a verbose Tracker label.
func TestFrontierBadgeLineGivesTheNativeStatusWhatIsLeft(t *testing.T) {
	blocked := model.Ticket{
		ID:           "#1",
		Key:          "#1",
		Title:        "title #1",
		Status:       model.StatusTodo,
		NativeStatus: "Selected for Development",
	}
	tickets := []model.Ticket{blocked,
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusTodo),
		blockingTicket("#4", model.StatusTodo),
	}
	g := graphOf(tickets, map[model.TicketID][]model.Link{
		"#1": blockedBy("#2", "#3", "#4"),
		"#2": nil,
		"#3": nil,
		"#4": nil,
	})
	node := frontierNodes(g, tickets, true)[0]
	const badge = "blocked by 3"
	if node.emphasis.badge != badge {
		t.Fatalf("badge = %q, want %q: this test asserts the wrong thing otherwise",
			node.emphasis.badge, badge)
	}

	// Wide enough for both: the Native Status leads, in full.
	if got := frontierBadgeLine(node, g, 60); got != "[Selected for Development] "+badge {
		t.Errorf("badge line at 60 columns = %q, want both fields", got)
	}
	// A default card's inner width holds only one of them whole.
	got := frontierBadgeLine(node, g, frontierCardWidth-2)
	if !strings.HasSuffix(got, badge) {
		t.Errorf("badge line on a default card = %q, want it to end in %q", got, badge)
	}
	if w := lipgloss.Width(got); w > frontierCardWidth-2 {
		t.Errorf("badge line is %d columns wide, want at most %d: %q", w, frontierCardWidth-2, got)
	}
	// Narrower still and the Native Status is dropped rather than reduced to an
	// ellipsis standing in for nothing.
	if got := frontierBadgeLine(node, g, len(badge)+2); got != badge {
		t.Errorf("badge line with no room to spare = %q, want just %q", got, badge)
	}
}
