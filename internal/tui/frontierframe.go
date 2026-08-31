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

const (
	// frontierMarkerStyle is the render-time sentinel for the focus marker, which
	// the layout does not own.
	frontierMarkerStyle = -1
	// frontierFocusedEdgeStyle is the render-time sentinel for incident edge
	// geometry. Its heavy glyph substitution survives monochrome and ANSI stripping.
	frontierFocusedEdgeStyle = -2
)

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
	// routes retain compact endpoint identity and ranges into the selected
	// candidate's single stroke arena. The incident index preserves canonical
	// route order for render-only focus.
	routes   []frontierRoute
	strokes  []frontierStroke
	incident map[model.TicketID][]frontierRouteID
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

// frontierEdge is one drawn blocking relation over displayed node indices.
// BuildBlockingGraph deduplicates graph-node pairs before duplicate Ticket IDs
// collapse onto their first displayed node, so frontierEdges deduplicates again
// after that mapping. Blocks Links are already folded into this direction, and
// nothing here reads Detail.Links.
type frontierEdge struct {
	dependent, blocker int
}

// frontierEdges collects the drawable edges in canonical order — members in
// Watchlist order then Ghosts in theirs, each node's Blockers in order — which
// is the only tie-break the layout below uses. The first occurrence of a
// displayed semantic relation wins when duplicate members collapse together.
//
// Ghosts are walked because a Ghost reached as a dependent — a member's Blocks
// Link pointing out of the Watchlist — carries its edge on the Ghost's own
// Blockers, and a Ghost card drawn connected to nothing says the opposite of
// what the Watchlist read.
func frontierEdges(g model.BlockingGraph, index map[model.TicketID]int) []frontierEdge {
	var edges []frontierEdge
	seen := make(map[frontierEdge]struct{})
	collect := func(id model.TicketID, blockers []model.Blocker) {
		dependent, ok := index[id]
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
			blocker, ok := index[b.Target.ID]
			if !ok || blocker == dependent {
				// A self-blocking Ticket routes nothing: it is a cycle of one and
				// carries the CYCLE badge.
				continue
			}
			edge := frontierEdge{dependent: dependent, blocker: blocker}
			if _, duplicate := seen[edge]; duplicate {
				continue
			}
			seen[edge] = struct{}{}
			edges = append(edges, edge)
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

// frontierRouteID is one-based so a zero-valued segment or dummy cannot
// accidentally claim the first canonical route.
type frontierRouteID uint32

// frontierPoint and frontierStroke retain ordered, axis-aligned physical
// geometry without attaching ownership to canvas cells.
type frontierPoint struct {
	x, y int
}

type frontierStroke struct {
	from, to frontierPoint
}

// frontierRoute is one canonical blocker-to-dependent relation. Its geometry is
// an owned range in frontierLayout.strokes; strokeCapacity reserves the proven
// 7*rankSpan-1 routing bound without a per-route backing allocation.
type frontierRoute struct {
	blocker, dependent                       model.TicketID
	strokeStart, strokeCount, strokeCapacity int
}

// A local gutter segment emits at most six strokes. Each intermediate dummy
// adds one, so a route spanning k ranks needs at most 6k+(k-1) slots.
func frontierRouteStrokeCapacity(rankSpan int) int {
	return 7*rankSpan - 1
}

// frontierSlot is one peer slot in a rank: a real node, or a pass-through
// waypoint carrying an edge across a rank it does not stop in.
type frontierSlot struct {
	node    int
	dummy   bool
	routeID frontierRouteID
}

// frontierSegment is one edge segment between adjacent ranks, resolved to the
// peer slots at each end. After the waypoint pass every segment is local like
// this, which is why routing can never collide with a card.
type frontierSegment struct {
	// blockerPeer is in the earlier rank and dependentPeer in the rank
	// immediately after it. Direction maps that relation onto a physical axis.
	blockerPeer, dependentPeer int
	routeID                    frontierRouteID
}

// frontierRankPlan is the one deterministic, allocation-bearing graph plan.
// Candidate measurement below reads it twice, but neither candidate owns cells.
type frontierRankPlan struct {
	ranks    []int
	slots    [][]frontierSlot
	segments [][]frontierSegment
	routes   []frontierRoute
	incident map[model.TicketID][]frontierRouteID
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
		incident: make(map[model.TicketID][]frontierRouteID, len(nodes)),
	}
	peerOf := make([]int, len(nodes))
	for i := range nodes {
		rank := ranks[i]
		peerOf[i] = len(plan.slots[rank])
		plan.slots[rank] = append(plan.slots[rank], frontierSlot{node: i})
	}
	for _, edge := range edges {
		dependentRank, blockerRank := ranks[edge.dependent], ranks[edge.blocker]
		if dependentRank <= blockerRank {
			// Both ends share an SCC rank. The CYCLE badge reports that relation;
			// there is no acyclic rank direction left to route.
			continue
		}

		routeID := frontierRouteID(len(plan.routes) + 1)
		blockerID, dependentID := nodes[edge.blocker].id, nodes[edge.dependent].id
		plan.routes = append(plan.routes, frontierRoute{
			blocker:        blockerID,
			dependent:      dependentID,
			strokeCapacity: frontierRouteStrokeCapacity(dependentRank - blockerRank),
		})
		plan.incident[blockerID] = append(plan.incident[blockerID], routeID)
		plan.incident[dependentID] = append(plan.incident[dependentID], routeID)

		blockerPeer := peerOf[edge.blocker]
		for rank := blockerRank; rank < dependentRank; rank++ {
			dependentPeer := peerOf[edge.dependent]
			if rank+1 < dependentRank {
				dependentPeer = len(plan.slots[rank+1])
				plan.slots[rank+1] = append(plan.slots[rank+1], frontierSlot{
					dummy: true, routeID: routeID,
				})
			}
			plan.segments[rank] = append(plan.segments[rank], frontierSegment{
				blockerPeer: blockerPeer, dependentPeer: dependentPeer, routeID: routeID,
			})
			blockerPeer = dependentPeer
		}
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
		incident:  make(map[model.TicketID][]frontierRouteID, len(nodes)),
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
	l.routes = append(l.routes, plan.routes...)
	strokeSlots := 0
	for i := range l.routes {
		l.routes[i].strokeStart = strokeSlots
		l.routes[i].strokeCount = 0
		strokeSlots += l.routes[i].strokeCapacity
	}
	l.strokes = make([]frontierStroke, strokeSlots)
	l.incident = plan.incident
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
		// A long route crosses the next rank after entering its dummy slot. Drawing
		// it here retains blocker-to-dependent stroke order without another route
		// pass or any canvas-cell ownership.
		for peer, slot := range plan.slots[rank+1] {
			if !slot.dummy {
				continue
			}
			if opts.direction == frontierRanksHorizontal {
				y := peer*frontierSlotHeight + 1
				l.mergeRouteStroke(slot.routeID,
					frontierPoint{x: rankOrigin[rank+1], y: y},
					frontierPoint{x: rankOrigin[rank+1] + cardWidth - 1, y: y}, '─', edgeStyle)
			} else {
				x := peer*cardWidth + cardWidth/2
				l.mergeRouteStroke(slot.routeID,
					frontierPoint{x: x, y: rankOrigin[rank+1]},
					frontierPoint{x: x, y: rankOrigin[rank+1] + frontierCardHeight - 1}, '│', edgeStyle)
			}
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
	for _, edge := range edges {
		dependent, blocker := component[edge.dependent], component[edge.blocker]
		if dependent != blocker {
			succ[dependent] = append(succ[dependent], blocker)
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
			if bx+1 <= dx-2 {
				l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx + 1, y: by},
					frontierPoint{x: dx - 2, y: by}, '─', style)
			}
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: dx - 1, y: by},
				frontierPoint{x: dx - 1, y: by}, '▶', style)
			continue
		}

		cx := bx + 2 + next%channels
		next++
		l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx + 1, y: by},
			frontierPoint{x: cx - 1, y: by}, '─', style)
		if dy > by {
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx, y: by},
				frontierPoint{x: cx, y: by}, '┐', style)
			if by+1 <= dy-1 {
				l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx, y: by + 1},
					frontierPoint{x: cx, y: dy - 1}, '│', style)
			}
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx, y: dy},
				frontierPoint{x: cx, y: dy}, '└', style)
		} else {
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx, y: by},
				frontierPoint{x: cx, y: by}, '┘', style)
			if by-1 >= dy+1 {
				l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx, y: by - 1},
					frontierPoint{x: cx, y: dy + 1}, '│', style)
			}
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx, y: dy},
				frontierPoint{x: cx, y: dy}, '┌', style)
		}
		if cx+1 <= dx-2 {
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: cx + 1, y: dy},
				frontierPoint{x: dx - 2, y: dy}, '─', style)
		}
		l.mergeRouteStroke(segment.routeID, frontierPoint{x: dx - 1, y: dy},
			frontierPoint{x: dx - 1, y: dy}, '▶', style)
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
			if by+1 <= dy-2 {
				l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx, y: by + 1},
					frontierPoint{x: bx, y: dy - 2}, '│', style)
			}
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx, y: dy - 1},
				frontierPoint{x: bx, y: dy - 1}, '▼', style)
			continue
		}

		cy := by + 2 + next%channels
		next++
		l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx, y: by + 1},
			frontierPoint{x: bx, y: cy - 1}, '│', style)
		if dx > bx {
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx, y: cy},
				frontierPoint{x: bx, y: cy}, '└', style)
			if bx+1 <= dx-1 {
				l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx + 1, y: cy},
					frontierPoint{x: dx - 1, y: cy}, '─', style)
			}
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: dx, y: cy},
				frontierPoint{x: dx, y: cy}, '┐', style)
		} else {
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx, y: cy},
				frontierPoint{x: bx, y: cy}, '┘', style)
			if bx-1 >= dx+1 {
				l.mergeRouteStroke(segment.routeID, frontierPoint{x: bx - 1, y: cy},
					frontierPoint{x: dx + 1, y: cy}, '─', style)
			}
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: dx, y: cy},
				frontierPoint{x: dx, y: cy}, '┌', style)
		}
		if cy+1 <= dy-2 {
			l.mergeRouteStroke(segment.routeID, frontierPoint{x: dx, y: cy + 1},
				frontierPoint{x: dx, y: dy - 2}, '│', style)
		}
		l.mergeRouteStroke(segment.routeID, frontierPoint{x: dx, y: dy - 1},
			frontierPoint{x: dx, y: dy - 1}, '▼', style)
	}
}

func (l frontierLayout) routeStrokes(route frontierRoute) []frontierStroke {
	end := route.strokeStart + route.strokeCount
	limit := route.strokeStart + route.strokeCapacity
	if route.strokeStart < 0 || route.strokeCount < 0 || route.strokeCount > route.strokeCapacity || limit > len(l.strokes) {
		panic(fmt.Sprintf("frontier route has invalid stroke range [%d:%d] with limit %d in arena %d",
			route.strokeStart, end, limit, len(l.strokes)))
	}
	return l.strokes[route.strokeStart:end]
}

// mergeRouteStroke stores one ordered, axis-aligned piece in a canonical
// route's arena range while materializing its normal-weight cells through
// mergeGlyph.
func (l *frontierLayout) mergeRouteStroke(routeID frontierRouteID, from, to frontierPoint, r rune, style int) {
	index := int(routeID) - 1
	if index < 0 || index >= len(l.routes) {
		panic(fmt.Sprintf("frontier route ID %d is out of range", routeID))
	}
	if from.x != to.x && from.y != to.y {
		panic(fmt.Sprintf("frontier route %d has diagonal stroke %+v -> %+v", routeID, from, to))
	}
	route := &l.routes[index]
	if route.strokeCount >= route.strokeCapacity {
		panic(fmt.Sprintf("frontier route %d exceeded its %d-stroke arena range", routeID, route.strokeCapacity))
	}
	l.strokes[route.strokeStart+route.strokeCount] = frontierStroke{from: from, to: to}
	route.strokeCount++

	dx, dy := 0, 0
	if from.x < to.x {
		dx = 1
	} else if from.x > to.x {
		dx = -1
	}
	if from.y < to.y {
		dy = 1
	} else if from.y > to.y {
		dy = -1
	}
	for at := from; ; at.x, at.y = at.x+dx, at.y+dy {
		l.merge(at.x, at.y, r, style)
		if at == to {
			break
		}
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
// Each positive axis keeps at least one inner cell, degrading from both sides to
// the leading side and finally to no chrome. A non-positive outer axis has no
// drawable inner extent.
func frontierInnerRect(width, height int) frontierRect {
	axis := func(dimension int) (origin, size int) {
		switch {
		case dimension <= 0:
			return 0, 0
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

func heavyFrontierEdgeGlyph(r rune) rune {
	switch r {
	case '─':
		return '━'
	case '│':
		return '┃'
	case '┌':
		return '┏'
	case '┐':
		return '┓'
	case '└':
		return '┗'
	case '┘':
		return '┛'
	case '┼':
		return '╋'
	default:
		// Arrowheads retain their physical direction; the adjoining heavy stroke
		// supplies the usual monochrome focus distinction. Isolated clipped
		// terminals receive a directional focus glyph before rendering.
		return r
	}
}

func heavyFrontierEdgeText(text string) string {
	return strings.Map(heavyFrontierEdgeGlyph, text)
}

func clipFrontierStroke(stroke frontierStroke, clip frontierRect) (frontierStroke, bool) {
	if clip.W <= 0 || clip.H <= 0 || (stroke.from.x != stroke.to.x && stroke.from.y != stroke.to.y) {
		return frontierStroke{}, false
	}
	right, bottom := clip.X+clip.W-1, clip.Y+clip.H-1
	switch {
	case stroke.from.y == stroke.to.y:
		if stroke.from.y < clip.Y || stroke.from.y > bottom {
			return frontierStroke{}, false
		}
		low := max(min(stroke.from.x, stroke.to.x), clip.X)
		high := min(max(stroke.from.x, stroke.to.x), right)
		if low > high {
			return frontierStroke{}, false
		}
		if stroke.from.x <= stroke.to.x {
			stroke.from.x, stroke.to.x = low, high
		} else {
			stroke.from.x, stroke.to.x = high, low
		}
	case stroke.from.x == stroke.to.x:
		if stroke.from.x < clip.X || stroke.from.x > right {
			return frontierStroke{}, false
		}
		low := max(min(stroke.from.y, stroke.to.y), clip.Y)
		high := min(max(stroke.from.y, stroke.to.y), bottom)
		if low > high {
			return frontierStroke{}, false
		}
		if stroke.from.y <= stroke.to.y {
			stroke.from.y, stroke.to.y = low, high
		} else {
			stroke.from.y, stroke.to.y = high, low
		}
	}
	return stroke, true
}

func frontierRectContains(rect frontierRect, x, y int) bool {
	return x >= rect.X && x < rect.X+rect.W && y >= rect.Y && y < rect.Y+rect.H
}

func frontierRectsIntersect(a, b frontierRect) bool {
	return a.W > 0 && a.H > 0 && b.W > 0 && b.H > 0 &&
		a.X < b.X+b.W && b.X < a.X+a.W &&
		a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// visibleFrontierCardRects retains defensive card rejection without scanning
// every laid-out node for every expanded incident cell.
func visibleFrontierCardRects(l frontierLayout, clip frontierRect) []frontierRect {
	var visible []frontierRect
	for _, id := range l.order {
		rect, ok := l.nodeAt[id]
		if ok && frontierRectsIntersect(rect, clip) {
			visible = append(visible, rect)
		}
	}
	return visible
}

func insideFrontierCardRects(rects []frontierRect, x, y int) bool {
	for _, rect := range rects {
		if frontierRectContains(rect, x, y) {
			return true
		}
	}
	return false
}

func (l frontierLayout) insideCard(x, y int) bool {
	for _, id := range l.order {
		rect, ok := l.nodeAt[id]
		if ok && frontierRectContains(rect, x, y) {
			return true
		}
	}
	return false
}

// distinguishClippedFrontierArrows gives an isolated focused terminal a
// monochrome-visible identity. Ordinarily the adjoining heavy stroke carries
// focus, so the established arrow remains unchanged.
func distinguishClippedFrontierArrows(overlay map[[2]int]frontierCell) {
	for at, cell := range overlay {
		predecessor := at
		var focusedArrow rune
		switch cell.r {
		case '▶':
			predecessor[0]--
			focusedArrow = '▸'
		case '▼':
			predecessor[1]--
			focusedArrow = '▾'
		default:
			continue
		}
		if prior, visible := overlay[predecessor]; visible && heavyFrontierEdgeGlyph(prior.r) != prior.r {
			continue
		}
		cell.r = focusedArrow
		overlay[at] = cell
	}
}

// focusedFrontierOverlay expands only the focused node's indexed routes. Each
// stroke is clipped before expansion, and output preserves the selected
// canvas's already-merged glyph so a shared crossing is highlighted for any
// incident owner. Only an isolated clipped terminal receives a directional
// monochrome focus glyph.
func focusedFrontierOverlay(l frontierLayout, focus model.TicketID,
	offsetX, offsetY, width, height int) map[[2]int]frontierCell {
	left, top := max(offsetX, 0), max(offsetY, 0)
	right, bottom := min(offsetX+width, l.width), min(offsetY+height, l.height)
	clip := frontierRect{X: left, Y: top, W: max(right-left, 0), H: max(bottom-top, 0)}
	if clip.W == 0 || clip.H == 0 {
		return nil
	}
	visibleCards := visibleFrontierCardRects(l, clip)

	var overlay map[[2]int]frontierCell
	for _, routeID := range l.incident[focus] {
		index := int(routeID) - 1
		if index < 0 || index >= len(l.routes) {
			continue
		}
		route := l.routes[index]
		if route.blocker != focus && route.dependent != focus {
			continue
		}
		for _, stroke := range l.routeStrokes(route) {
			stroke, visible := clipFrontierStroke(stroke, clip)
			if !visible {
				continue
			}
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
				cell := l.cells[at.y][at.x]
				if !insideFrontierCardRects(visibleCards, at.x, at.y) && !cell.continuation && cell.link == 0 {
					if overlay == nil {
						overlay = make(map[[2]int]frontierCell)
					}
					cell.style = frontierFocusedEdgeStyle
					overlay[[2]int{at.x - offsetX, at.y - offsetY}] = cell
				}
				if at == stroke.to {
					break
				}
			}
		}
	}
	distinguishClippedFrontierArrows(overlay)
	return overlay
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
	var overlay map[[2]int]frontierCell
	if hasFocus {
		overlay = focusedFrontierOverlay(l, focus, offsetX, offsetY, width, height)
		if rect, ok := l.nodeAt[focus]; ok {
			if overlay == nil {
				overlay = make(map[[2]int]frontierCell, 1)
			}
			// The focused-card marker wins even if malformed route geometry ever
			// reaches its card.
			overlay[[2]int{rect.X + 1 - offsetX, rect.Y + 1 - offsetY}] =
				frontierCell{r: '▸', style: frontierMarkerStyle}
		}
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
			case runStyle == frontierFocusedEdgeStyle:
				line.WriteString(s.FrontierBold.Render(heavyFrontierEdgeText(text)))
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
	if inner.W <= 0 || inner.H <= 0 {
		return out
	}
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

	lines := make([]string, max(outerHeight, 0))
	for y := range lines {
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
