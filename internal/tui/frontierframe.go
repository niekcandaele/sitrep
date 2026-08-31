package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
	"github.com/niekcandaele/sitrep/internal/termtext"
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
	// frontierMinGutter reserves two cells on the rank axis: one clear of the
	// blocker's border and one for the arrowhead beside the dependent. Bounded
	// perpendicular channels sit between them.
	frontierMinGutter = 2
	// maxGutterChannels caps how many perpendicular channel runs one gutter
	// draws. Beyond it, channels are reused round-robin: junctions are drawn, so
	// an overlapping run is readable where a dropped edge would be a lie.
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

// frontierMarkerStyle is the render-time sentinel for the focus marker, which
// the layout does not own.
const frontierMarkerStyle = -1

// frontierCell is one canvas cell: what to draw, which style-table entry to
// draw it with, and which of the layout's URLs it hyperlinks to.
type frontierCell struct {
	r     rune
	style int
	link  int
	// continuation marks a cell covered by the double-width rune to its left.
	// It draws nothing of its own and is not blank: writing over it defaces the
	// glyph and leaves the assembled row one column too wide. It is a field
	// rather than an r == 0 test so the writer and renderer read one spelling of
	// the same fact.
	continuation bool
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
	// byRank is each rank's real nodes in canonical peer order. Rank 0 holds
	// the nodes nothing blocks; dummy waypoints are deliberately absent.
	byRank [][]model.TicketID
	// rankOf and peerOf make physical navigation independent of whether ranks
	// occupy columns or rows.
	rankOf map[model.TicketID]int
	peerOf map[model.TicketID]int
	// candidates retain the two allocation-free measurements used by resize
	// hysteresis. Only the direction below has cells materialized.
	candidates    [2]frontierLayoutCandidate
	direction     frontierRankDirection
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
// resolved says whether every identifiable current Watchlist member has seated
// Links or a recorded Detail failure. Until then, every member is visibly
// PENDING: fail-closed plus a progressive fetch means a partial Frontier gives
// wrong answers to anyone glancing at it, so the screen waits and says why.
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
	// same ID would give geometry, rank metadata and hit-testing two answers and one
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
		// GHOST identifies why this node exists; unlike a current member's
		// evidence badge, that identity stays true while the seat is unresolved.
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

// memberEmphasis picks one member's emphasis. Until the full seat resolves,
// provisional evidence outranks every graph-derived conclusion. Once resolved,
// a cycle is malformed data and outranks anything derived from it, an unverified
// Ticket outranks a claim about it, and only then does Actionable get the loudest
// treatment on screen.
func memberEmphasis(a model.Actionability, resolved bool) frontierEmphasis {
	normal := frontierEmphasis{
		border:    frontierLightBorder,
		titleRole: frontierRoleText,
		badgeRole: frontierRoleMuted,
	}
	switch {
	case !resolved:
		normal.badge = "PENDING"
		return normal
	case a.InCycle:
		return frontierEmphasis{
			border:    frontierDoubleBorder,
			titleRole: frontierRoleBold,
			badgeRole: frontierRoleBold,
			badge:     "CYCLE",
		}
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
// Watchlist order then Ghosts in theirs, each node's Blockers in order — which
// is the only tie-break the layout below uses.
//
// Ghosts are walked because a Ghost reached as a dependent — a member's Blocks
// Link pointing out of the Watchlist — carries its edge on the Ghost's own
// Blockers, and a Ghost card drawn connected to nothing says the opposite of
// what the Watchlist read.
func frontierEdges(g model.BlockingGraph, index map[model.TicketID]int) []frontierEdge {
	var edges []frontierEdge
	collect := func(id model.TicketID, blockers []model.Blocker) {
		from, ok := index[id]
		if !ok {
			return
		}
		for _, b := range blockers {
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
	for _, a := range g.Members() {
		collect(a.TicketID, a.Blockers)
	}
	for _, ghost := range g.Ghosts() {
		collect(ghost.Target.ID, ghost.Blockers)
	}
	return edges
}

// frontierRankDirection says which physical axis carries graph ranks. Cards
// themselves are always upright.
type frontierRankDirection uint8

const (
	frontierRanksHorizontal frontierRankDirection = iota
	frontierRanksVertical
)

func (d frontierRankDirection) other() frontierRankDirection {
	if d == frontierRanksHorizontal {
		return frontierRanksVertical
	}
	return frontierRanksHorizontal
}

// frontierSlot is one peer slot in a rank: a real node, or a pass-through
// waypoint carrying an edge across a rank it does not stop in.
type frontierSlot struct {
	node  int
	dummy bool
}

// frontierSegment is one edge segment between adjacent ranks, resolved to the
// peer slots at each end. After the waypoint pass every segment is local like
// this, which is why routing can never collide with a card.
type frontierSegment struct {
	// blockerPeer is in the earlier rank and dependentPeer in the rank
	// immediately after it. Direction maps that relation onto a physical axis.
	blockerPeer, dependentPeer int
}

// frontierRankPlan is the one deterministic, allocation-bearing graph plan.
// Candidate measurement below reads it twice, but neither candidate owns cells.
type frontierRankPlan struct {
	ranks    []int
	slots    [][]frontierSlot
	segments [][]frontierSegment
	peers    int
}

// frontierLayoutCandidate is allocation-free shape metadata for one direction.
type frontierLayoutCandidate struct {
	direction     frontierRankDirection
	width, height int
}

func (c frontierLayoutCandidate) fullyFits(width, height int) bool {
	return c.width <= width && c.height <= height
}

// frontierLayoutOptions is the private Model-owned layout seam. plan is set by
// rebuildFrontier so selection and materialization share one rank plan; direct
// pure callers may leave it nil.
type frontierLayoutOptions struct {
	innerWidth int
	direction  frontierRankDirection
	plan       *frontierRankPlan
}

// planFrontierRanks produces the sole deterministic rank plan. Real nodes are
// inserted in canonical member-then-Ghost order, followed by each long edge's
// dummy waypoints in canonical edge order.
func planFrontierRanks(g model.BlockingGraph, nodes []frontierNode) frontierRankPlan {
	if len(nodes) == 0 {
		return frontierRankPlan{}
	}
	index := make(map[model.TicketID]int, len(nodes))
	for i, node := range nodes {
		if _, duplicate := index[node.id]; !duplicate {
			index[node.id] = i
		}
	}
	edges := frontierEdges(g, index)
	ranks := frontierRanks(g, nodes, index, edges)
	rankCount := 1
	for _, rank := range ranks {
		rankCount = max(rankCount, rank+1)
	}

	plan := frontierRankPlan{
		ranks:    ranks,
		slots:    make([][]frontierSlot, rankCount),
		segments: make([][]frontierSegment, rankCount),
	}
	peerOf := make([]int, len(nodes))
	for i := range nodes {
		rank := ranks[i]
		peerOf[i] = len(plan.slots[rank])
		plan.slots[rank] = append(plan.slots[rank], frontierSlot{node: i})
	}
	for _, edge := range edges {
		dependentRank, blockerRank := ranks[edge.from], ranks[edge.to]
		if dependentRank <= blockerRank {
			// Both ends share an SCC rank. The CYCLE badge reports that relation;
			// there is no acyclic rank direction left to route.
			continue
		}
		peer := peerOf[edge.from]
		for rank := dependentRank - 1; rank > blockerRank; rank-- {
			next := len(plan.slots[rank])
			plan.slots[rank] = append(plan.slots[rank], frontierSlot{dummy: true})
			plan.segments[rank] = append(plan.segments[rank], frontierSegment{
				blockerPeer: next, dependentPeer: peer,
			})
			peer = next
		}
		plan.segments[blockerRank] = append(plan.segments[blockerRank], frontierSegment{
			blockerPeer: peerOf[edge.to], dependentPeer: peer,
		})
	}
	for _, rank := range plan.slots {
		plan.peers = max(plan.peers, len(rank))
	}
	return plan
}

func frontierCardWidthFor(innerWidth int) int {
	if innerWidth > 0 && innerWidth < frontierNarrowTerminal {
		return max(innerWidth-2, frontierMinCardWidth)
	}
	return frontierCardWidth
}

func frontierGutterSize(segments []frontierSegment) int {
	return frontierMinGutter + min(channelsNeeded(segments), maxGutterChannels)
}

// frontierCandidates measures both physical shapes and their logical rank-plan
// aspects without allocating either cell grid.
func frontierCandidates(plan frontierRankPlan, innerWidth int) [2]frontierLayoutCandidate {
	if len(plan.slots) == 0 {
		return [2]frontierLayoutCandidate{
			{direction: frontierRanksHorizontal},
			{direction: frontierRanksVertical},
		}
	}
	cardWidth := frontierCardWidthFor(innerWidth)
	gutterTotal := 0
	for rank := 0; rank < len(plan.slots)-1; rank++ {
		gutterTotal += frontierGutterSize(plan.segments[rank])
	}
	ranks, peers := len(plan.slots), max(plan.peers, 1)
	return [2]frontierLayoutCandidate{
		{
			direction: frontierRanksHorizontal,
			width:     ranks*cardWidth + gutterTotal,
			height:    peers * frontierSlotHeight,
		},
		{
			direction: frontierRanksVertical,
			width:     peers * cardWidth,
			height:    ranks*frontierSlotHeight + gutterTotal,
		},
	}
}

// layoutFrontier materializes exactly one selected candidate from one rank plan.
func layoutFrontier(g model.BlockingGraph, nodes []frontierNode, opts frontierLayoutOptions) frontierLayout {
	l := frontierLayout{
		styles:    []frontierStyle{{Role: frontierRoleText}},
		links:     []string{""},
		nodeAt:    make(map[model.TicketID]frontierRect, len(nodes)),
		rankOf:    make(map[model.TicketID]int, len(nodes)),
		peerOf:    make(map[model.TicketID]int, len(nodes)),
		direction: opts.direction,
	}
	if len(nodes) == 0 {
		return l
	}

	l.order = make([]model.TicketID, 0, len(nodes))
	for _, node := range nodes {
		l.order = append(l.order, node.id)
	}
	plan := opts.plan
	if plan == nil {
		owned := planFrontierRanks(g, nodes)
		plan = &owned
	}
	l.candidates = frontierCandidates(*plan, opts.innerWidth)
	selected := l.candidates[opts.direction]
	l.width, l.height = selected.width, selected.height
	l.cells = make([][]frontierCell, l.height)
	for y := range l.cells {
		l.cells[y] = make([]frontierCell, l.width)
		for x := range l.cells[y] {
			l.cells[y][x] = frontierCell{r: ' ', style: frontierBlankStyle}
		}
	}

	cardWidth := frontierCardWidthFor(opts.innerWidth)
	rankOrigin := make([]int, len(plan.slots))
	for rank := 1; rank < len(rankOrigin); rank++ {
		previous := rank - 1
		if opts.direction == frontierRanksHorizontal {
			rankOrigin[rank] = rankOrigin[previous] + cardWidth + frontierGutterSize(plan.segments[previous])
		} else {
			rankOrigin[rank] = rankOrigin[previous] + frontierSlotHeight + frontierGutterSize(plan.segments[previous])
		}
	}

	l.byRank = make([][]model.TicketID, len(plan.slots))
	edgeStyle := l.styleIndex(frontierStyle{Role: frontierRoleMuted})
	for rank, slots := range plan.slots {
		for peer, slot := range slots {
			if slot.dummy {
				if opts.direction == frontierRanksHorizontal {
					y := peer*frontierSlotHeight + 1
					for x := range cardWidth {
						l.merge(rankOrigin[rank]+x, y, '─', edgeStyle)
					}
				} else {
					x := peer*cardWidth + cardWidth/2
					for y := range frontierCardHeight {
						l.merge(x, rankOrigin[rank]+y, '│', edgeStyle)
					}
				}
				continue
			}

			node := nodes[slot.node]
			rect := frontierRect{W: cardWidth, H: frontierCardHeight}
			if opts.direction == frontierRanksHorizontal {
				rect.X, rect.Y = rankOrigin[rank], peer*frontierSlotHeight
			} else {
				rect.X, rect.Y = peer*cardWidth, rankOrigin[rank]
			}
			l.drawCard(node, g, rect)
			l.nodeAt[node.id] = rect
			l.rankOf[node.id] = rank
			l.peerOf[node.id] = peer
			l.byRank[rank] = append(l.byRank[rank], node.id)
		}
	}

	for rank := 0; rank < len(plan.slots)-1; rank++ {
		if opts.direction == frontierRanksHorizontal {
			l.routeHorizontalGutter(plan.segments[rank], rankOrigin[rank]+cardWidth-1,
				rankOrigin[rank+1], edgeStyle)
		} else {
			l.routeVerticalGutter(plan.segments[rank], rankOrigin[rank]+frontierCardHeight-1,
				rankOrigin[rank+1], cardWidth, edgeStyle)
		}
	}
	return l
}

func frontierAspectError(candidate frontierLayoutCandidate, width, height int) int {
	// A terminal row is approximately twice as tall as a column is wide. Candidate
	// dimensions are the logical canvas shape measured from rank metadata; no cell
	// grid is needed for this integer cross-product comparison.
	effectiveHeight := 2 * max(height, 1)
	return absInt(candidate.width*effectiveHeight - max(width, 1)*candidate.height)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// chooseFrontierDirection is first-seat selection: a sole fitting candidate
// wins, otherwise the closer effective aspect wins and exact ties stay
// horizontal.
func chooseFrontierDirection(candidates [2]frontierLayoutCandidate, width, height int) frontierRankDirection {
	horizontalFits := candidates[frontierRanksHorizontal].fullyFits(width, height)
	verticalFits := candidates[frontierRanksVertical].fullyFits(width, height)
	if horizontalFits != verticalFits {
		if verticalFits {
			return frontierRanksVertical
		}
		return frontierRanksHorizontal
	}
	horizontalError := frontierAspectError(candidates[frontierRanksHorizontal], width, height)
	verticalError := frontierAspectError(candidates[frontierRanksVertical], width, height)
	if verticalError < horizontalError {
		return frontierRanksVertical
	}
	return frontierRanksHorizontal
}

// frontierDirectionAfterResize is strict seat-local hysteresis. It never
// re-runs ideal selection: only current overflow paired with an alternative
// that fully fits can move the graph.
func frontierDirectionAfterResize(current frontierRankDirection,
	candidates [2]frontierLayoutCandidate, width, height int) frontierRankDirection {
	if !candidates[current].fullyFits(width, height) && candidates[current.other()].fullyFits(width, height) {
		return current.other()
	}
	return current
}

// frontierRanks assigns every node a logical rank. Blockers receive lower
// ranks than dependents; materialization maps ranks onto either physical axis.
// A Ghost Ticket has no blockers of its own, so it starts at rank 0.
func frontierRanks(g model.BlockingGraph, nodes []frontierNode,
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

	ranks := make([]int, len(nodes))
	for i := range nodes {
		ranks[i] = walk(component[i])
	}
	return ranks
}

// channelsNeeded counts the segments in one rank gutter that cannot be drawn as
// a straight run and therefore need a perpendicular channel of their own.
func channelsNeeded(segments []frontierSegment) int {
	n := 0
	for _, segment := range segments {
		if segment.blockerPeer != segment.dependentPeer {
			n++
		}
	}
	return n
}

// routeHorizontalGutter preserves the original left-to-right routing: blockers
// are on the left and arrowheads point into dependents on the right.
func (l *frontierLayout) routeHorizontalGutter(segments []frontierSegment, bx, dx, style int) {
	channels := max(min(channelsNeeded(segments), maxGutterChannels), 1)
	next := 0
	for _, segment := range segments {
		by := segment.blockerPeer*frontierSlotHeight + 1
		dy := segment.dependentPeer*frontierSlotHeight + 1
		if by == dy {
			for x := bx + 1; x <= dx-2; x++ {
				l.merge(x, by, '─', style)
			}
			l.merge(dx-1, by, '▶', style)
			continue
		}

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

// routeVerticalGutter maps the same bounded, canonical channel allocation onto
// rows. Blockers are above and arrowheads point down into dependents.
func (l *frontierLayout) routeVerticalGutter(segments []frontierSegment, by, dy, cardWidth, style int) {
	channels := max(min(channelsNeeded(segments), maxGutterChannels), 1)
	next := 0
	for _, segment := range segments {
		bx := segment.blockerPeer*cardWidth + cardWidth/2
		dx := segment.dependentPeer*cardWidth + cardWidth/2
		if bx == dx {
			for y := by + 1; y <= dy-2; y++ {
				l.merge(bx, y, '│', style)
			}
			l.merge(bx, dy-1, '▼', style)
			continue
		}

		cy := by + 2 + next%channels
		next++
		for y := by + 1; y <= cy-1; y++ {
			l.merge(bx, y, '│', style)
		}
		if dx > bx {
			l.merge(bx, cy, '└', style)
			l.merge(dx, cy, '┐', style)
		} else {
			l.merge(bx, cy, '┘', style)
			l.merge(dx, cy, '┌', style)
		}
		for x := min(bx, dx) + 1; x < max(bx, dx); x++ {
			l.merge(x, cy, '─', style)
		}
		for y := cy + 1; y <= dy-2; y++ {
			l.merge(dx, y, '│', style)
		}
		l.merge(dx, dy-1, '▼', style)
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
	case existing == '▼' || incoming == '▼':
		return '▼'
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
	l.write(rect.X+1, rect.Y+3, frontierBadgeLine(n, g, inner), badge, 0, rect.X+rect.W-1)
}

// frontierBadgeLine is a card's fourth line, fitted to width: the Native Status
// where it says something the Status Category does not, then the emphasis badge,
// then a count of blocking Links that named no Ticket.
//
// The badge and the unnamed-blocker count answer the question this screen exists
// to answer, so they are assembled first and keep their columns. The Native
// Status is display-only and takes what is left; where nothing useful is left it
// is dropped outright. Truncating the joined line instead lets a long Native
// Status eat "blocked by 3".
func frontierBadgeLine(n frontierNode, g model.BlockingGraph, width int) string {
	var carried []string
	if n.emphasis.badge != "" {
		carried = append(carried, n.emphasis.badge)
	}
	if a, ok := g.For(n.id); ok {
		if anonymous := anonymousBlockers(a); anonymous > 0 {
			noun := "blocker"
			if anonymous > 1 {
				noun = "blockers"
			}
			carried = append(carried, fmt.Sprintf("+%d unnamed %s", anonymous, noun))
		}
	}
	load := strings.Join(carried, " ")
	switch {
	case n.native == "":
		return balancedTruncate(load, width, "…")
	case load == "":
		return balancedTruncate(n.native, width, "…")
	}
	// One column for the separating space, and at least two for the Native
	// Status itself: a lone ellipsis where a status used to be says less than
	// the blank it replaces.
	budget := width - lipgloss.Width(load) - 1
	if budget < 2 {
		return balancedTruncate(load, width, "…")
	}
	return balancedTruncate(n.native, budget, "…") + " " + load
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
				l.cells[y][x+i] = frontierCell{style: style, link: link, continuation: true}
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

// frontierInnerRect reserves a deterministic fixed ring inside the outer body.
// Every axis keeps at least one inner cell, degrading from both sides to the
// leading side and finally to no chrome as space disappears.
func frontierInnerRect(width, height int) frontierRect {
	axis := func(dimension int) (origin, size int) {
		switch {
		case dimension >= 3:
			return 1, dimension - 2
		case dimension == 2:
			return 1, 1
		default:
			return 0, 1
		}
	}
	x, w := axis(width)
	y, h := axis(height)
	return frontierRect{X: x, Y: y, W: w, H: h}
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

// renderFrontierCanvas draws only the inner window of the canvas at the given
// offsets. Focus is an inner-canvas overlay; overflow chrome is composed later
// in the reserved outer ring.
//
// Cells are grouped into maximal runs of one style and one hyperlink before
// rendering, which keeps the frame small enough that the incremental renderer
// stays cheap.
func renderFrontierCanvas(l frontierLayout, focus model.TicketID, hasFocus bool,
	offsetX, offsetY, width, height int, s Styles) []string {
	overlay := make(map[[2]int]frontierCell, 1)
	if rect, ok := l.nodeAt[focus]; ok && hasFocus {
		overlay[[2]int{rect.X + 1 - offsetX, rect.Y + 1 - offsetY}] =
			frontierCell{r: '▸', style: frontierMarkerStyle}
	}

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
			if cell.style != runStyle || cell.link != runLink {
				flush()
				runStyle, runLink = cell.style, cell.link
			}
			if !cell.continuation {
				run.WriteRune(cell.r)
				run.WriteString(cell.trailing)
			}
		}
		flush()
		// The canvas cuts by column, not by line: a horizontal scroll slices
		// every card in the leftmost and rightmost columns mid-field, which can
		// leave a bidirectional opener or its terminator on the far side of the
		// window. Balancing the assembled line is the same obligation
		// balancedTruncate carries for a field it clips (ADR-0006).
		lines = append(lines, termtext.Balance(
			truncateLine(strings.TrimRight(line.String(), " "), width)))
	}
	return lines
}

// frontierChromeGlyphs places directional overflow cues in fixed cells of the
// reserved ring. Insert order is the collision precedence: top, bottom, left,
// right. A missing side is omitted rather than overlaid on the canvas.
func frontierChromeGlyphs(l frontierLayout, inner frontierRect,
	offsetX, offsetY, outerWidth, outerHeight int) map[[2]int]rune {
	out := make(map[[2]int]rune, 4)
	place := func(at [2]int, glyph rune) {
		if _, occupied := out[at]; !occupied {
			out[at] = glyph
		}
	}
	if inner.Y > 0 && offsetY > 0 {
		place([2]int{inner.X + inner.W/2, inner.Y - 1}, '▲')
	}
	if inner.Y+inner.H < outerHeight && offsetY+inner.H < l.height {
		place([2]int{inner.X + inner.W/2, inner.Y + inner.H}, '▼')
	}
	if inner.X > 0 && offsetX > 0 {
		place([2]int{inner.X - 1, inner.Y + inner.H/2}, '‹')
	}
	if inner.X+inner.W < outerWidth && offsetX+inner.W < l.width {
		place([2]int{inner.X + inner.W, inner.Y + inner.H/2}, '›')
	}
	return out
}

// renderFrontierBody composes the already-rendered inner canvas with inert
// ring-only chrome. It never slices a styled or hyperlinked canvas line.
func renderFrontierBody(l frontierLayout, focus model.TicketID, hasFocus bool,
	offsetX, offsetY, outerWidth, outerHeight int, s Styles) []string {
	inner := frontierInnerRect(outerWidth, outerHeight)
	canvas := renderFrontierCanvas(l, focus, hasFocus, offsetX, offsetY, inner.W, inner.H, s)
	chrome := frontierChromeGlyphs(l, inner, offsetX, offsetY, outerWidth, outerHeight)

	renderRing := func(y, from, to int) string {
		var line strings.Builder
		for x := from; x < to; x++ {
			if glyph, ok := chrome[[2]int{x, y}]; ok {
				line.WriteString(s.Muted.Render(string(glyph)))
			} else {
				line.WriteByte(' ')
			}
		}
		return line.String()
	}

	lines := make([]string, outerHeight)
	for y := range outerHeight {
		var line strings.Builder
		if y >= inner.Y && y < inner.Y+inner.H {
			line.WriteString(renderRing(y, 0, inner.X))
			inside := canvas[y-inner.Y]
			line.WriteString(inside)
			line.WriteString(strings.Repeat(" ", max(inner.W-lipgloss.Width(inside), 0)))
			line.WriteString(renderRing(y, inner.X+inner.W, outerWidth))
		} else {
			line.WriteString(renderRing(y, 0, outerWidth))
		}
		lines[y] = line.String()
	}
	return lines
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

// moveFocus interprets keys physically. Along-rank motion steps through canonical
// peers; cross-rank motion chooses the nearest orthogonal card and preserves
// canonical peer order on an exact tie.
func (l frontierLayout) moveFocus(id model.TicketID, dx, dy int) (model.TicketID, bool) {
	rank, ok := l.rankOf[id]
	if !ok {
		return "", false
	}

	peerDelta, rankDelta := dy, dx
	orthogonal := func(rect frontierRect) int { return rect.Y }
	if l.direction == frontierRanksVertical {
		peerDelta, rankDelta = dx, dy
		orthogonal = func(rect frontierRect) int { return rect.X }
	}
	if peerDelta != 0 {
		peers := l.byRank[rank]
		if next := l.peerOf[id] + peerDelta; next >= 0 && next < len(peers) {
			return peers[next], true
		}
		return id, true
	}
	if rankDelta == 0 {
		return id, true
	}

	target := rank + rankDelta
	if target < 0 || target >= len(l.byRank) || len(l.byRank[target]) == 0 {
		return id, true
	}
	want := orthogonal(l.nodeAt[id])
	best, bestDistance := l.byRank[target][0], -1
	for _, candidate := range l.byRank[target] {
		position := orthogonal(l.nodeAt[candidate])
		distance := absInt(position - want)
		if bestDistance < 0 || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best, true
}
