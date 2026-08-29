package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// This file is the Frontier's layout: nodes and BlockedBy edges in, a canvas of
// cells out. Everything here is pure — no Model, no clock, no lipgloss.Style —
// so the part of this screen most likely to go wrong is testable without a
// terminal, and is.

// Card and canvas geometry. The card width is fixed rather than derived from
// the terminal, so the canvas is stable and scrolling — not reflow — handles a
// narrow terminal: a graph that re-flows on every resize makes "where was that
// node" a question the reader has to answer again each time.
const (
	frontierCardWidth  = 28
	frontierCardHeight = 5
	// frontierSlotHeight is a card plus the blank spacer under it.
	frontierSlotHeight = frontierCardHeight + 1
	// frontierNarrowTerminal is the width below which the fixed card no longer
	// fits at all and the card shrinks to what there is.
	frontierNarrowTerminal = 30
	frontierMinCardWidth   = 12
	// frontierMinGutter is the two columns every gutter reserves beside the
	// cards: one blank beside the blocker's right border, so a vertical channel
	// never runs flush against a card it has nothing to do with, and one for the
	// arrowhead beside the dependent's left border. The channels sit between
	// them.
	frontierMinGutter = 2
	// maxGutterChannels caps how many vertical runs one gutter draws. Beyond it
	// channels are reused round-robin: junctions are drawn, so an overlapping
	// run is readable where a dropped edge would be a lie.
	maxGutterChannels = 6
)

// frontierRole is one visual role a canvas cell can take. The layout assigns
// roles; Styles.frontierStyle maps a role to a lipgloss.Style at render time,
// which is what keeps the layout free of any style decision.
type frontierRole int

const (
	// frontierRoleText is ordinary card text.
	frontierRoleText frontierRole = iota
	// frontierRoleMuted is secondary card text and every edge cell. Edges carry
	// no colour meaning at all.
	frontierRoleMuted
	// frontierRoleCategory is the Status Category channel: card border and Key.
	frontierRoleCategory
	// frontierRoleBold is the loud half of the emphasis channel — Actionable and
	// cycle nodes. Emphasis is weight, never a second hue.
	frontierRoleBold
	// frontierRoleFaint is the quiet half of the emphasis channel — a Ticket
	// that is blocked.
	frontierRoleFaint
)

// frontierStyle is one entry in a layout's style table: a role, plus the Status
// Category the role is coloured by where the role is the category channel.
type frontierStyle struct {
	Role     frontierRole
	Category model.StatusCategory
}

// frontierBlankStyle is the style-table entry every layout interns first, so a
// cell outside the canvas has a valid style without a special case.
const frontierBlankStyle = 0

// Render-time style sentinels, used for cells the layout does not own: the
// focus marker and the overflow indicators are drawn by the renderer.
const (
	frontierMarkerStyle   = -1
	frontierOverflowStyle = -2
)

// frontierCell is one canvas cell: what to draw, which style-table entry to
// draw it with, and which of the layout's URLs it hyperlinks to. A zero rune is
// the second half of a double-width rune and draws nothing of its own.
type frontierCell struct {
	r     rune
	style int
	link  int
	// trailing holds the zero-width runes that belong to r — combining marks,
	// variation selectors, ZWJ joiners. They occupy no column of their own, so
	// they ride along with the cell they modify rather than claiming one or
	// being dropped: "café" on a card must be the same bytes as "café" on a
	// list row.
	trailing string
}

// frontierRect is one node's card on the canvas.
type frontierRect struct {
	X, Y, W, H int
}

// frontierLayout is the drawn Frontier: a canvas of cells plus everything focus
// movement, mouse hit-testing and scrolling need to find a node on it.
type frontierLayout struct {
	cells  [][]frontierCell
	styles []frontierStyle
	// links is the layout's URL table; index 0 is "no hyperlink".
	links []string
	// nodeAt is every drawn node's card rect, for focus, hit-testing and
	// ensureNodeVisible. Pass-through waypoints are deliberately absent: they
	// are not focusable and not clickable.
	nodeAt map[model.TicketID]frontierRect
	// order is canonical focus order: members in Watchlist order, then Ghosts.
	order []model.TicketID
	// byColumn is each column's nodes in row order, for blocker/dependent focus
	// movement. Column 0 holds the nodes nothing blocks.
	byColumn [][]model.TicketID
	// columnOf answers which column a node sits in without scanning byColumn.
	columnOf      map[model.TicketID]int
	width, height int
}

// frontierBorder is one card's box-drawing set.
type frontierBorder struct {
	horizontal, vertical                       rune
	topLeft, topRight, bottomLeft, bottomRight rune
}

var (
	frontierLightBorder  = frontierBorder{'─', '│', '┌', '┐', '└', '┘'}
	frontierHeavyBorder  = frontierBorder{'━', '┃', '┏', '┓', '┗', '┛'}
	frontierDoubleBorder = frontierBorder{'═', '║', '╔', '╗', '╚', '╝'}
	frontierGhostBorder  = frontierBorder{'╌', '╎', '╭', '╮', '╰', '╯'}
)

// frontierEmphasis is the second visual channel: how loudly a node is drawn and
// what its badge says. Colour carries Status Category and nothing else, so this
// channel takes border weight, text weight and a word — an InProgress Ticket
// that is also blocked has to show both facts at once.
type frontierEmphasis struct {
	border    frontierBorder
	titleRole frontierRole
	badgeRole frontierRole
	badge     string
}

// frontierNode is one drawable node: a Watchlist member or a Ghost Ticket, with
// its display fields and the emphasis its blocking state earns.
//
// The node's TicketID is its identity and is never drawn (ADR-0006); the card
// shows the Key.
type frontierNode struct {
	id       model.TicketID
	key      string
	title    string
	url      string
	status   model.StatusCategory
	native   string
	emphasis frontierEmphasis
}

// frontierNodes derives every drawable node from the graph and the Watchlist
// that produced it: members in canonical order, then Ghost Tickets.
//
// resolved says whether every planned Detail read has answered. Until it has,
// no Actionable or blocked emphasis is drawn at all: fail-closed plus a
// progressive fetch means a half-loaded Frontier gives wrong answers to anyone
// glancing at it, so the screen waits and says why.
func frontierNodes(g model.BlockingGraph, tickets []model.Ticket, resolved bool) []frontierNode {
	byID := make(map[model.TicketID]model.Ticket, len(tickets))
	for _, t := range tickets {
		if _, dup := byID[t.ID]; !dup {
			byID[t.ID] = t
		}
	}

	members := g.Members()
	ghosts := g.Ghosts()
	nodes := make([]frontierNode, 0, len(members)+len(ghosts))
	// A Ref-list Watchlist may legitimately name the same Ticket twice, and
	// jsonout walks its members positionally for exactly that reason. The
	// Frontier is a graph, where one Ticket is one node: a second card for the
	// same ID would give nodeAt, columnOf and hit-testing two answers and one
	// winner, so focus and clicks would reach a card the eye is not on.
	drawn := make(map[model.TicketID]bool, len(members))
	for _, a := range members {
		if drawn[a.TicketID] {
			continue
		}
		drawn[a.TicketID] = true
		t := byID[a.TicketID]
		nodes = append(nodes, frontierNode{
			id:       a.TicketID,
			key:      t.Key,
			title:    t.Title,
			url:      t.URL,
			status:   a.Status,
			native:   nativeStatusFor(t),
			emphasis: memberEmphasis(a, resolved),
		})
	}
	for _, ghost := range ghosts {
		target := ghost.Target
		nodes = append(nodes, frontierNode{
			id:     target.ID,
			key:    target.Key,
			title:  target.Title,
			url:    target.URL,
			status: target.Status,
			native: nativeStatusFor(ticketFromLinkTarget(target)),
			emphasis: frontierEmphasis{
				border:    frontierGhostBorder,
				titleRole: frontierRoleText,
				badgeRole: frontierRoleMuted,
				badge:     "GHOST",
			},
		})
	}
	return nodes
}

// nativeStatusFor returns the Native Status a card prints, or "" where it only
// restates the Status Category the card already supplies (#41).
func nativeStatusFor(t model.Ticket) string {
	if !plain.ShowsNativeStatus(t) {
		return ""
	}
	return "[" + t.NativeStatus + "]"
}

// memberEmphasis picks one member's emphasis. The states are a precedence
// ladder and the first match wins: a cycle is malformed data and outranks
// anything derived from it, an unverified Ticket outranks a claim about it, and
// only then does Actionable get the loudest treatment on screen.
func memberEmphasis(a model.Actionability, resolved bool) frontierEmphasis {
	normal := frontierEmphasis{
		border:    frontierLightBorder,
		titleRole: frontierRoleText,
		badgeRole: frontierRoleMuted,
	}
	switch {
	case a.InCycle:
		return frontierEmphasis{
			border:    frontierDoubleBorder,
			titleRole: frontierRoleBold,
			badgeRole: frontierRoleBold,
			badge:     "CYCLE",
		}
	case !resolved:
		return normal
	case a.HasUnknown():
		normal.badge = "UNVERIFIED"
		return normal
	case a.Actionable:
		return frontierEmphasis{
			border:    frontierHeavyBorder,
			titleRole: frontierRoleBold,
			badgeRole: frontierRoleBold,
			badge:     "ACTIONABLE",
		}
	case len(a.Unmet()) > 0:
		return frontierEmphasis{
			border:    frontierLightBorder,
			titleRole: frontierRoleFaint,
			badgeRole: frontierRoleFaint,
			badge:     fmt.Sprintf("blocked by %d", len(a.Unmet())),
		}
	}
	return normal
}

// anonymousBlockers counts one member's blocking Links that carried no Ticket
// identity. They block but have no node to draw, so the card says how many
// there are rather than looking unblocked.
func anonymousBlockers(a model.Actionability) int {
	n := 0
	for _, b := range a.Blockers {
		if b.Target.ID == "" {
			n++
		}
	}
	return n
}

// frontierEdge is one drawn blocking relation over node indices: from is
// BlockedBy to. BuildBlockingGraph already deduplicated on (blocked, blocker)
// and already folded Blocks Links into this direction, so nothing here dedupes
// again and nothing here reads Detail.Links.
type frontierEdge struct {
	from, to int
}

// frontierEdges collects the drawable edges in canonical order — members in
// Watchlist order, each member's Blockers in order — which is the only
// tie-break the layout below uses.
func frontierEdges(g model.BlockingGraph, index map[model.TicketID]int) []frontierEdge {
	var edges []frontierEdge
	for _, a := range g.Members() {
		from, ok := index[a.TicketID]
		if !ok {
			continue
		}
		for _, b := range a.Blockers {
			// An anonymous blocker has no node, so it has no edge. It is already
			// unsatisfied, so its member is already not Actionable; the card's
			// badge is where it is accounted for.
			if b.Target.ID == "" {
				continue
			}
			to, ok := index[b.Target.ID]
			if !ok || to == from {
				// A self-blocking Ticket routes nothing: it is a cycle of one and
				// carries the CYCLE badge.
				continue
			}
			edges = append(edges, frontierEdge{from: from, to: to})
		}
	}
	return edges
}

// frontierSlot is one row slot in a column: a real node, or a pass-through
// waypoint carrying an edge across a column it does not stop in.
type frontierSlot struct {
	node  int
	dummy bool
}

// frontierSegment is one edge segment between adjacent columns, resolved to the
// slot rows at each end. After the waypoint pass every segment is local like
// this, which is why routing can never collide with a card. It is the single
// most important simplification in this layout.
type frontierSegment struct {
	// blockerRow is the row in the left-hand column and dependentRow the row in
	// the column immediately right of it. The arrow points from blocker to
	// dependent: reading left to right is "this must finish before that".
	blockerRow, dependentRow int
}

// layoutFrontier turns a blocking graph into a canvas.
//
// It is pure and takes no height: the canvas is sized by its content and the
// viewport clamps against it separately, so the same Watchlist draws the same
// canvas on any terminal and only the window into it moves.
func layoutFrontier(g model.BlockingGraph, nodes []frontierNode, width int) frontierLayout {
	l := frontierLayout{
		styles:   []frontierStyle{{Role: frontierRoleText}},
		links:    []string{""},
		nodeAt:   make(map[model.TicketID]frontierRect, len(nodes)),
		columnOf: make(map[model.TicketID]int, len(nodes)),
	}
	if len(nodes) == 0 {
		return l
	}

	index := make(map[model.TicketID]int, len(nodes))
	l.order = make([]model.TicketID, 0, len(nodes))
	for i, n := range nodes {
		if _, dup := index[n.id]; !dup {
			index[n.id] = i
		}
		l.order = append(l.order, n.id)
	}

	edges := frontierEdges(g, index)
	columns := frontierColumns(g, nodes, index, edges)

	columnCount := 1
	for _, c := range columns {
		columnCount = max(columnCount, c+1)
	}
	// Slots: real nodes first, in canonical order — members before Ghosts —
	// then the waypoints each multi-column edge needs. There is deliberately no
	// crossing minimisation in v1: canonical order is decided, not forgotten,
	// because it is what keeps a golden frame stable.
	slots := make([][]frontierSlot, columnCount)
	rowOf := make([]int, len(nodes))
	for i := range nodes {
		c := columns[i]
		rowOf[i] = len(slots[c])
		slots[c] = append(slots[c], frontierSlot{node: i})
	}

	segments := make([][]frontierSegment, columnCount)
	for _, e := range edges {
		a, b := columns[e.from], columns[e.to]
		if a <= b {
			// Both ends share a component, so they share a column: the two sit on
			// a cycle and the CYCLE badge is what reports it. There is no
			// left-to-right ordering left to draw.
			continue
		}
		row := rowOf[e.from]
		for c := a - 1; c > b; c-- {
			// A pass-through waypoint (a classic Sugiyama dummy node) takes a
			// full row slot after every real node in its column.
			next := len(slots[c])
			slots[c] = append(slots[c], frontierSlot{dummy: true})
			segments[c] = append(segments[c], frontierSegment{blockerRow: next, dependentRow: row})
			row = next
		}
		segments[b] = append(segments[b], frontierSegment{blockerRow: rowOf[e.to], dependentRow: row})
	}

	cardWidth := frontierCardWidth
	if width > 0 && width < frontierNarrowTerminal {
		cardWidth = max(width-2, frontierMinCardWidth)
	}

	originX := make([]int, columnCount)
	x := 0
	for c := range columnCount {
		originX[c] = x
		x += cardWidth
		if c < columnCount-1 {
			x += frontierMinGutter + min(channelsNeeded(segments[c]), maxGutterChannels)
		}
	}

	rows := 0
	for _, s := range slots {
		rows = max(rows, len(s))
	}
	l.width = x
	l.height = rows * frontierSlotHeight
	l.cells = make([][]frontierCell, l.height)
	for y := range l.cells {
		l.cells[y] = make([]frontierCell, l.width)
		for i := range l.cells[y] {
			l.cells[y][i] = frontierCell{r: ' ', style: frontierBlankStyle}
		}
	}

	l.byColumn = make([][]model.TicketID, columnCount)
	edgeStyle := l.styleIndex(frontierStyle{Role: frontierRoleMuted})
	for c, column := range slots {
		for row, slot := range column {
			y := row * frontierSlotHeight
			if slot.dummy {
				// A waypoint is a horizontal run across its slot's anchor row and
				// nothing else.
				for i := range cardWidth {
					l.merge(originX[c]+i, y+1, '─', edgeStyle)
				}
				continue
			}
			n := nodes[slot.node]
			rect := frontierRect{X: originX[c], Y: y, W: cardWidth, H: frontierCardHeight}
			l.drawCard(n, g, rect)
			l.nodeAt[n.id] = rect
			l.columnOf[n.id] = c
			l.byColumn[c] = append(l.byColumn[c], n.id)
		}
	}

	for c := range columnCount - 1 {
		l.routeGutter(segments[c], originX[c]+cardWidth-1, originX[c+1], edgeStyle)
	}
	return l
}

// frontierColumns assigns every node a column. Blockers land left of their
// dependents, so reading left to right is "this must finish before that". A
// Ghost Ticket has no blockers of its own — its Links are never followed — so
// it always lands in column 0.
func frontierColumns(g model.BlockingGraph, nodes []frontierNode,
	index map[model.TicketID]int, edges []frontierEdge) []int {
	// Condense: every node on a cycle joins one component, every other node is
	// its own. Tarjan already did the hard part in BuildBlockingGraph, so there
	// is no second SCC pass here.
	component := make([]int, len(nodes))
	for i := range component {
		component[i] = i
	}
	for _, cycle := range g.Cycles() {
		root := -1
		for _, id := range cycle {
			i, ok := index[id]
			if !ok {
				continue
			}
			if root < 0 {
				root = i
			}
			component[i] = root
		}
	}

	// The condensation of a graph by its strongly connected components is
	// acyclic by construction, so the longest-path recursion below cannot
	// revisit a component. That is why layout terminates on cyclic input — not
	// a guard someone has to remember to keep.
	succ := make(map[int][]int, len(nodes))
	for _, e := range edges {
		from, to := component[e.from], component[e.to]
		if from != to {
			succ[from] = append(succ[from], to)
		}
	}

	const (
		unvisited = iota
		visiting
		done
	)
	mark := make([]int, len(nodes))
	depth := make([]int, len(nodes))
	var walk func(c int) int
	walk = func(c int) int {
		switch mark[c] {
		case done:
			return depth[c]
		case visiting:
			// Unreachable: the condensation is acyclic. Kept as a structural
			// backstop so an edge source that one day forgets to condense
			// degrades into a flat layout rather than a hang.
			return 0
		}
		mark[c] = visiting
		d := 0
		for _, s := range succ[c] {
			d = max(d, walk(s)+1)
		}
		mark[c] = done
		depth[c] = d
		return d
	}

	columns := make([]int, len(nodes))
	for i := range nodes {
		columns[i] = walk(component[i])
	}
	return columns
}

// channelsNeeded counts the segments in one gutter that cannot be drawn as a
// straight run and therefore need a vertical channel column of their own.
func channelsNeeded(segments []frontierSegment) int {
	n := 0
	for _, s := range segments {
		if s.blockerRow != s.dependentRow {
			n++
		}
	}
	return n
}

// routeGutter draws one gutter's segments. bx is the blocker cards' right edge
// column and dx the dependent cards' left edge column.
func (l *frontierLayout) routeGutter(segments []frontierSegment, bx, dx, style int) {
	channels := max(min(channelsNeeded(segments), maxGutterChannels), 1)
	next := 0
	for _, s := range segments {
		by := s.blockerRow*frontierSlotHeight + 1
		dy := s.dependentRow*frontierSlotHeight + 1
		if by == dy {
			for x := bx + 1; x <= dx-2; x++ {
				l.merge(x, by, '─', style)
			}
			l.merge(dx-1, by, '▶', style)
			continue
		}

		// Channels are assigned in segment order and reused round-robin past the
		// cap; overlapping runs are acceptable because junctions are drawn.
		// Channel 0 starts one column clear of the blocker's border: a trunk
		// drawn in the column touching the cards reads as part of every card in
		// that column, related or not.
		cx := bx + 2 + next%channels
		next++
		for x := bx + 1; x <= cx-1; x++ {
			l.merge(x, by, '─', style)
		}
		if dy > by {
			l.merge(cx, by, '┐', style)
			l.merge(cx, dy, '└', style)
		} else {
			l.merge(cx, by, '┘', style)
			l.merge(cx, dy, '┌', style)
		}
		for y := min(by, dy) + 1; y < max(by, dy); y++ {
			l.merge(cx, y, '│', style)
		}
		for x := cx + 1; x <= dx-2; x++ {
			l.merge(x, dy, '─', style)
		}
		l.merge(dx-1, dy, '▶', style)
	}
}

// merge writes one cell through mergeGlyph, so two edges crossing draw a
// junction rather than one silently erasing the other.
func (l *frontierLayout) merge(x, y int, r rune, style int) {
	if y < 0 || y >= len(l.cells) || x < 0 || x >= l.width {
		return
	}
	l.cells[y][x] = frontierCell{r: mergeGlyph(l.cells[y][x].r, r), style: style}
}

// mergeGlyph combines what is already on a cell with what is being drawn over
// it. ┼ is the deliberate catch-all: an unreadable junction is better than a
// silently dropped edge.
func mergeGlyph(existing, incoming rune) rune {
	switch {
	case existing == ' ' || existing == 0:
		return incoming
	case existing == '▶' || incoming == '▶':
		return '▶'
	case existing == incoming:
		return existing
	default:
		return '┼'
	}
}

// styleIndex interns one style-table entry. The table is built in draw order,
// so it is deterministic and no map iteration reaches the canvas.
func (l *frontierLayout) styleIndex(s frontierStyle) int {
	for i, existing := range l.styles {
		if existing == s {
			return i
		}
	}
	l.styles = append(l.styles, s)
	return len(l.styles) - 1
}

// linkIndex interns one URL. Index 0 is "no hyperlink".
func (l *frontierLayout) linkIndex(url string) int {
	if url == "" {
		return 0
	}
	for i, existing := range l.links {
		if existing == url {
			return i
		}
	}
	l.links = append(l.links, url)
	return len(l.links) - 1
}

// drawCard blits one node's card. Line 2 carries the Key and the Status
// Category word — the category is printed, not only coloured, so a monochrome
// terminal still reads it. Line 3 is the Title and line 4 the badge line. The
// Key and the Title are OSC 8 hyperlinks to the node's URL, as list rows are.
func (l *frontierLayout) drawCard(n frontierNode, g model.BlockingGraph, rect frontierRect) {
	border := l.styleIndex(frontierStyle{Role: frontierRoleCategory, Category: n.status})
	muted := l.styleIndex(frontierStyle{Role: frontierRoleMuted})
	title := l.styleIndex(frontierStyle{Role: n.emphasis.titleRole})
	badge := l.styleIndex(frontierStyle{Role: n.emphasis.badgeRole})
	link := l.linkIndex(n.url)
	b := n.emphasis.border
	inner := rect.W - 2

	l.write(rect.X, rect.Y, string(b.topLeft)+strings.Repeat(string(b.horizontal), inner)+string(b.topRight),
		border, 0, rect.X+rect.W)
	l.write(rect.X, rect.Y+rect.H-1,
		string(b.bottomLeft)+strings.Repeat(string(b.horizontal), inner)+string(b.bottomRight),
		border, 0, rect.X+rect.W)
	for i := 1; i < rect.H-1; i++ {
		l.write(rect.X, rect.Y+i, string(b.vertical), border, 0, rect.X+1)
		l.write(rect.X+rect.W-1, rect.Y+i, string(b.vertical), border, 0, rect.X+rect.W)
	}

	// The focus marker's two columns are left blank here and filled in by the
	// renderer: focus moves far more often than the canvas changes.
	category := plain.CategoryLabel(n.status)
	categoryWidth := lipgloss.Width(category)
	keyStart := rect.X + 1 + len(unselectedMarker)
	keyBudget := max(inner-len(unselectedMarker)-categoryWidth-1, 1)
	l.write(keyStart, rect.Y+1, balancedTruncate(n.key, keyBudget, "…"), border, link, keyStart+keyBudget)
	if x := rect.X + 1 + inner - categoryWidth; x > keyStart {
		l.write(x, rect.Y+1, category, muted, 0, rect.X+rect.W-1)
	}

	l.write(rect.X+1, rect.Y+2, balancedTruncate(n.title, inner, "…"), title, link, rect.X+rect.W-1)
	l.write(rect.X+1, rect.Y+3, balancedTruncate(frontierBadgeLine(n, g), inner, "…"), badge, 0, rect.X+rect.W-1)
}

// frontierBadgeLine is a card's fourth line: the Native Status where it says
// something the Status Category does not, then the emphasis badge, then a count
// of blocking Links that named no Ticket.
func frontierBadgeLine(n frontierNode, g model.BlockingGraph) string {
	var parts []string
	if n.native != "" {
		parts = append(parts, n.native)
	}
	if n.emphasis.badge != "" {
		parts = append(parts, n.emphasis.badge)
	}
	if a, ok := g.For(n.id); ok {
		if anonymous := anonymousBlockers(a); anonymous > 0 {
			noun := "blocker"
			if anonymous > 1 {
				noun = "blockers"
			}
			parts = append(parts, fmt.Sprintf("+%d unnamed %s", anonymous, noun))
		}
	}
	return strings.Join(parts, " ")
}

// write blits a string onto the canvas from x, stopping at limit. Card text
// overwrites rather than merging: a card is drawn once, onto cells no edge can
// reach. A double-width rune takes two cells so the card's right border stays
// in its column whatever the Tracker put in a title.
func (l *frontierLayout) write(x, y int, s string, style, link, limit int) {
	if y < 0 || y >= len(l.cells) {
		return
	}
	limit = min(limit, l.width)
	last := -1
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if w <= 0 {
			// A zero-width rune modifies the cell before it. A leading one
			// modifies nothing, so it is dropped.
			if last >= 0 {
				l.cells[y][last].trailing += string(r)
			}
			continue
		}
		if x+w > limit {
			return
		}
		if x >= 0 {
			l.cells[y][x] = frontierCell{r: r, style: style, link: link}
			for i := 1; i < w; i++ {
				l.cells[y][x+i] = frontierCell{style: style, link: link}
			}
			last = x
		}
		x += w
	}
}

// frontierStyle maps one style-table entry to the style it draws with. Colour
// comes from the Status Category and nothing else, so the Frontier and the list
// can never disagree about what colour a Ticket is.
func (s Styles) frontierStyle(entry frontierStyle) lipgloss.Style {
	switch entry.Role {
	case frontierRoleCategory:
		return s.groupHeader(entry.Category)
	case frontierRoleMuted:
		return s.Muted
	case frontierRoleBold:
		return s.FrontierBold
	case frontierRoleFaint:
		return s.FrontierFaint
	default:
		return s.TicketTitle
	}
}

// clampFrontierOffset keeps a scroll offset inside a canvas larger than the
// window, and pins it to zero when the whole canvas fits.
func clampFrontierOffset(offset, canvas, window int) int {
	return min(max(offset, 0), max(canvas-window, 0))
}

// ensureNodeVisible moves the window as little as possible to bring rect fully
// into a body of the given size, scrolling to the card's left or top edge when
// the card is bigger than the body.
func ensureNodeVisible(rect frontierRect, offsetX, offsetY, width, height int) (int, int) {
	return axisVisible(rect.X, rect.W, offsetX, width), axisVisible(rect.Y, rect.H, offsetY, height)
}

func axisVisible(start, size, offset, window int) int {
	if start < offset || size >= window {
		return start
	}
	if end := start + size; end > offset+window {
		return end - window
	}
	return offset
}

// renderFrontierCanvas draws the window of the canvas at the given offsets,
// marking the focused card and reporting with an edge glyph wherever content
// continues off-screen.
//
// Cells are grouped into maximal runs of one style and one hyperlink before
// rendering, which keeps the frame small enough that the incremental renderer
// stays cheap.
func renderFrontierCanvas(l frontierLayout, focus model.TicketID, hasFocus bool,
	offsetX, offsetY, width, height int, s Styles) []string {
	overlay := make(map[[2]int]frontierCell, 1)
	if rect, ok := l.nodeAt[focus]; ok && hasFocus {
		// The marker is drawn here rather than baked into the canvas because
		// focus moves far more often than the graph does.
		overlay[[2]int{rect.X + 1 - offsetX, rect.Y + 1 - offsetY}] =
			frontierCell{r: '▸', style: frontierMarkerStyle}
	}
	overflow := frontierOverflowGlyphs(l, overlay, offsetX, offsetY, width, height)

	lines := make([]string, 0, height)
	for wy := range height {
		var line, run strings.Builder
		runStyle, runLink := frontierBlankStyle, 0
		flush := func() {
			if run.Len() == 0 {
				return
			}
			text := run.String()
			run.Reset()
			switch {
			case runStyle == frontierMarkerStyle:
				line.WriteString(s.FrontierBold.Render(text))
			case runStyle == frontierOverflowStyle:
				line.WriteString(s.Muted.Render(text))
			case runLink > 0:
				line.WriteString(renderHyperlink(s.frontierStyle(l.styles[runStyle]), text, l.links[runLink]))
			default:
				line.WriteString(s.frontierStyle(l.styles[runStyle]).Render(text))
			}
		}
		for wx := range width {
			cell := frontierCell{r: ' ', style: frontierBlankStyle}
			if y, x := wy+offsetY, wx+offsetX; y >= 0 && y < l.height && x >= 0 && x < l.width {
				cell = l.cells[y][x]
			}
			if o, ok := overlay[[2]int{wx, wy}]; ok {
				cell = o
			}
			if glyph, ok := overflow[[2]int{wx, wy}]; ok {
				cell = frontierCell{r: glyph, style: frontierOverflowStyle}
			}
			if cell.style != runStyle || cell.link != runLink {
				flush()
				runStyle, runLink = cell.style, cell.link
			}
			if cell.r != 0 {
				run.WriteRune(cell.r)
				run.WriteString(cell.trailing)
			}
		}
		flush()
		lines = append(lines, truncateLine(strings.TrimRight(line.String(), " "), width))
	}
	return lines
}

// frontierOverflowGlyphs places one edge indicator per direction that has
// content off-screen, on a cell that is blank in the visible window. It walks
// outwards from the middle of the edge, because the middle is where the eye
// looks first; where the whole edge is occupied it draws nothing rather than
// punching a hole in a card's border, and the footer's scroll position still
// reports the same fact.
//
// It is a pure function of the window: the same offsets and the same canvas
// always place the same glyphs.
func frontierOverflowGlyphs(l frontierLayout, overlay map[[2]int]frontierCell,
	offsetX, offsetY, width, height int) map[[2]int]rune {
	out := make(map[[2]int]rune, 4)
	free := func(wx, wy int) bool {
		if _, taken := overlay[[2]int{wx, wy}]; taken {
			return false
		}
		if _, taken := out[[2]int{wx, wy}]; taken {
			return false
		}
		y, x := wy+offsetY, wx+offsetX
		if y < 0 || y >= l.height || x < 0 || x >= l.width {
			return true
		}
		r := l.cells[y][x].r
		return r == 0 || r == ' '
	}
	// place searches one edge for a free cell, nearest the middle first. across
	// is the coordinate that varies; fixed is the edge itself.
	place := func(glyph rune, alongRow bool, fixed, span int) {
		mid := span / 2
		for d := range span {
			for _, across := range [2]int{mid - d, mid + d} {
				if across < 0 || across >= span {
					continue
				}
				wx, wy := across, fixed
				if !alongRow {
					wx, wy = fixed, across
				}
				if free(wx, wy) {
					out[[2]int{wx, wy}] = glyph
					return
				}
			}
		}
	}

	if offsetY > 0 {
		place('▲', true, 0, width)
	}
	if offsetY+height < l.height {
		place('▼', true, height-1, width)
	}
	if offsetX > 0 {
		place('‹', false, 0, height)
	}
	if offsetX+width < l.width {
		place('›', false, width-1, height)
	}
	return out
}

// nodeAtPoint reports which node's card covers a canvas coordinate.
func (l frontierLayout) nodeAtPoint(x, y int) (model.TicketID, bool) {
	for _, id := range l.order {
		rect, ok := l.nodeAt[id]
		if !ok {
			continue
		}
		if x >= rect.X && x < rect.X+rect.W && y >= rect.Y && y < rect.Y+rect.H {
			return id, true
		}
	}
	return "", false
}

// moveFocus returns the node focus lands on when moving from id. Up and Down
// step within the focused node's column in row order and stay put at the ends,
// matching the list. Left moves to the blocker side and Right to the dependent
// side, landing on the node whose row is nearest the current one, ties to the
// smaller row index.
func (l frontierLayout) moveFocus(id model.TicketID, dx, dy int) (model.TicketID, bool) {
	column, ok := l.columnOf[id]
	if !ok {
		return "", false
	}
	if dy != 0 {
		nodes := l.byColumn[column]
		for i, candidate := range nodes {
			if candidate != id {
				continue
			}
			if next := i + dy; next >= 0 && next < len(nodes) {
				return nodes[next], true
			}
			return id, true
		}
		return id, true
	}

	target := column + dx
	if target < 0 || target >= len(l.byColumn) || len(l.byColumn[target]) == 0 {
		return id, true
	}
	want := l.nodeAt[id].Y
	best, bestDistance := l.byColumn[target][0], -1
	for _, candidate := range l.byColumn[target] {
		distance := max(l.nodeAt[candidate].Y-want, want-l.nodeAt[candidate].Y)
		if bestDistance < 0 || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best, true
}
