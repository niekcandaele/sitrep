package tui

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
)

// FrontierInput is everything the Frontier screen renders: the complete
// Watchlist, the Links read for it so far, and the Capabilities that decide
// whether there is a blocking graph at all.
//
// Tickets is the whole Watchlist and never the filtered subset: hiding a node
// deletes an edge, and a deleted edge can make a blocked Ticket look
// Actionable — the precise failure Actionable exists to prevent.
//
// The BlockingGraph and the layout are deliberately absent. Both are functions
// of these fields, and storing them here would invite the two to drift, exactly
// as ListInput refuses to carry Progress.
type FrontierInput struct {
	// Header identifies the Watchlist being drawn.
	Header Header
	// Tickets are the Watchlist's members in the Provider's own order, which is
	// the graph's canonical order.
	Tickets []model.Ticket
	// Links are the members' Links as read so far. Key presence is the
	// tri-state: a Ticket absent from this map had its Links fetch fail, get
	// interrupted or never get issued, so it is never Actionable.
	Links map[model.TicketID][]model.Link
	// Capabilities decide whether there is a blocking graph at all.
	Capabilities model.Capabilities
	// FetchedAt is when the Watchlist behind this screen was read.
	FetchedAt time.Time
}

// FrontierFromList adapts the list's reading and the session's Detail cache to
// the Frontier's contract. Like DetailFromTicket it is a state-entry funnel, so
// it crosses the terminal-visible-text boundary here (ADR-0006); the walkers
// are idempotent, so values already cleaned at intake pay nothing for crossing
// again.
func FrontierFromList(in ListInput, links map[model.TicketID][]model.Link) FrontierInput {
	return safeFrontierInput(FrontierInput{
		Header:       in.Header,
		Tickets:      in.Tickets,
		Links:        links,
		Capabilities: in.Capabilities,
		FetchedAt:    in.FetchedAt,
	})
}

type frontierCanvasRefusal struct {
	direction          frontierRankDirection
	width, height      int
	projectedCells     string
	arithmeticOverflow bool
	nodes, ghosts      int
}

func refuseFrontierCandidate(candidate frontierLayoutCandidate, nodes, ghosts int) *frontierCanvasRefusal {
	if candidate.withinCellLimit(frontierCanvasCellLimit) {
		return nil
	}
	cells, ok := candidate.projectedCells()
	projected := "overflowed checked arithmetic"
	if ok {
		projected = fmt.Sprintf("%d", cells)
	} else if !candidate.overflow && candidate.width >= 0 && candidate.height >= 0 {
		projected = new(big.Int).Mul(
			big.NewInt(int64(candidate.width)), big.NewInt(int64(candidate.height))).String()
	}
	return &frontierCanvasRefusal{
		direction:          candidate.direction,
		width:              candidate.width,
		height:             candidate.height,
		projectedCells:     projected,
		arithmeticOverflow: candidate.overflow,
		nodes:              nodes,
		ghosts:             ghosts,
	}
}

// frontierState is the Frontier screen's own state, kept in one struct so that
// it can be seated whole: the list's state is never consulted to draw it.
type frontierState struct {
	input   FrontierInput
	graph   model.BlockingGraph
	layout  frontierLayout
	refusal *frontierCanvasRefusal
	// direction is seat-local. A fresh entry or Watchlist reseat leaves it unset;
	// every other rebuild applies strict hysteresis and retains it.
	direction    frontierRankDirection
	directionSet bool
	// focusID is the focused node and may name a Ghost Ticket.
	focusID          model.TicketID
	hasFocus         bool
	offsetX, offsetY int
	// queued are the Ticket IDs not yet issued. The in-flight count lives on
	// Model, not here: re-seating frontierState on every entry would reset it
	// to zero while the previous entry's fetches are still running, and N
	// toggles would then run N concurrent Parallelism-sized batches.
	queued  []model.TicketID
	planned int
	done    int
	// failed names the Tickets whose Links could not be read. Their dependents
	// are not Actionable, and the footer says so rather than guessing. It is a
	// set rather than a count because adoption has to credit the one Ticket it
	// seated: a count cannot tell which Ticket it is clearing, so adopting a
	// still-queued Ticket's Detail would silently absolve one that really did
	// fail while its card still says UNVERIFIED.
	failed  map[model.TicketID]struct{}
	lastErr error
}

// isResolved reports whether the current Watchlist seat is complete enough to
// publish Actionability. Plan counters describe one fan-out; resolution covers
// every identifiable current member, including successful reads with no Links.
// An empty ID is not fetchable and stays visibly unverified rather than keeping
// the whole seat in an inescapable loading state.
func (f frontierState) isResolved() bool {
	if !f.input.Capabilities.BlockingLinks || len(f.input.Tickets) == 0 {
		return true
	}
	for _, ticket := range f.input.Tickets {
		if ticket.ID == "" {
			continue
		}
		if _, seated := f.input.Links[ticket.ID]; seated {
			continue
		}
		if _, failed := f.failed[ticket.ID]; failed {
			continue
		}
		return false
	}
	return true
}

// frontierDetailMsg carries one answer from the bulk fan-out. Cancellation stops
// Provider work when a seat ends; the generation guard separately prevents a
// raced or cancellation-ignoring answer from mutating its replacement.
type frontierDetailMsg struct {
	generation int
	id         model.TicketID
	detail     model.Detail
	caps       model.Capabilities
	err        error
}

const frontierRebuildDelay = time.Second / 60

// frontierRebuildMsg is immutable ownership evidence for the sole outstanding
// deferred canvas replacement tick.
type frontierRebuildMsg struct {
	timerID         int
	observedVersion int
}

// frontierLayoutForModel keeps layout observation instance-local. Nil is the
// production default for model-literal tests.
func (m Model) frontierLayoutForModel(g model.BlockingGraph, nodes []frontierNode,
	opts frontierLayoutOptions) frontierLayout {
	if m.layoutFrontierFn == nil {
		return layoutFrontier(g, nodes, opts)
	}
	return m.layoutFrontierFn(g, nodes, opts)
}

// discardPendingFrontierRebuild invalidates deferred work but deliberately
// retains the physical tick owner. Bubble Tea ticks cannot be cancelled; keeping
// ownership prevents a replacement request from stacking another one behind it.
func (m Model) discardPendingFrontierRebuild() Model {
	m.frontierRebuildVersion++
	m.frontierRebuildPendingVersion = 0
	m.frontierRebuildPendingGeneration = 0
	return m
}

func (m Model) armFrontierRebuildTimer() (Model, tea.Cmd) {
	if m.frontierRebuildTimerID != 0 || m.frontierRebuildPendingVersion == 0 {
		return m, nil
	}
	m.frontierRebuildNextTimerID++
	m.frontierRebuildTimerID = m.frontierRebuildNextTimerID
	timerID := m.frontierRebuildTimerID
	observedVersion := m.frontierRebuildPendingVersion
	return m, tea.Tick(frontierRebuildDelay, func(time.Time) tea.Msg {
		return frontierRebuildMsg{timerID: timerID, observedVersion: observedVersion}
	})
}

// requestFrontierRebuild records the newest visible incomplete evidence or
// width. It never materialises the canvas synchronously.
func (m Model) requestFrontierRebuild() (Model, tea.Cmd) {
	if m.mode != modeFrontier {
		return m.discardPendingFrontierRebuild(), nil
	}
	m.frontierRebuildVersion++
	m.frontierRebuildPendingVersion = m.frontierRebuildVersion
	m.frontierRebuildPendingGeneration = m.frontierGeneration
	return m.armFrontierRebuildTimer()
}

// onFrontierRebuild services exactly the quiet trailing edge of a request.
func (m Model) onFrontierRebuild(msg frontierRebuildMsg) (tea.Model, tea.Cmd) {
	if msg.timerID != m.frontierRebuildTimerID {
		return m, nil
	}
	m.frontierRebuildTimerID = 0
	if m.frontierRebuildPendingVersion == 0 ||
		m.frontierRebuildPendingGeneration != m.frontierGeneration ||
		m.mode != modeFrontier {
		return m.discardPendingFrontierRebuild(), nil
	}
	if msg.observedVersion != m.frontierRebuildPendingVersion {
		next, cmd := m.armFrontierRebuildTimer()
		return next, cmd
	}
	m.frontierRebuildPendingVersion = 0
	m.frontierRebuildPendingGeneration = 0
	m = m.rebuildFrontier()
	return m, repaint
}

// frontierEvidenceChanged preserves immediate state updates but chooses whether
// the expensive stored canvas is replaced now or at a quiet frame boundary.
func (m Model) frontierEvidenceChanged() (Model, tea.Cmd) {
	if m.mode != modeFrontier {
		return m, nil
	}
	if m.frontier.isResolved() {
		return m.rebuildFrontier(), repaint
	}
	return m.requestFrontierRebuild()
}

// linksFromCache builds the Links map from the Details read this session. It
// delegates the key-presence contract to detailfanout rather than open-coding
// it: only a Ticket whose Detail was actually read gets a key.
func (m Model) linksFromCache() map[model.TicketID][]model.Link {
	details := make(map[model.TicketID]model.Detail, len(m.details))
	for id, entry := range m.details {
		details[id] = entry.detail
	}
	return detailfanout.Links(details)
}

// enterFrontier seats the Frontier on the current reading and starts the bulk
// Detail fan-out ADR-0003's Amendment 4 permits: an explicit user action, with
// a visible cost whose generation-scoped context the user can cancel.
func (m Model) enterFrontier() (tea.Model, tea.Cmd) {
	if !m.hasData {
		return m, nil
	}
	m = m.clearPendingClick()
	m = m.retireFrontierFanout()
	m.mouseEpoch++
	m.mode = modeFrontier
	m.frontier = frontierState{
		input:   FrontierFromList(m.input, m.linksFromCache()),
		focusID: m.selectedID,
	}

	if m.frontier.input.Capabilities.BlockingLinks {
		m.frontier.queued = detailfanout.Plan(m.frontier.input.Tickets, m.haveDetail)
		m.frontier.planned = len(m.frontier.queued)
	}
	if len(m.frontier.queued) > 0 {
		m = m.startFrontierFanout()
	}
	m = m.rebuildFrontier()
	// The footer changes shape on the way in, so the next frame is drawn whole.
	return m.issueFrontierFetches()
}

// haveDetail reports whether this session already read one Ticket's Detail. A
// Ticket opened earlier costs the Tracker nothing here.
func (m Model) haveDetail(id model.TicketID) bool {
	_, hit := m.details[id]
	return hit
}

// issueFrontierFetches keeps up to detailfanout.Parallelism commands in flight.
//
// It repaints because the footer and the badges change the frame's shape as
// answers land, and the incremental renderer must not diff across frames of
// different shapes. A fan-out still running behind an open Detail repaints
// nothing: that screen's shape did not move.
func (m Model) issueFrontierFetches() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.mode == modeFrontier {
		cmds = append(cmds, repaint)
	}
	adopted := false
	for len(m.frontier.queued) > 0 && m.detailFanoutInflight < detailfanout.Parallelism {
		id := m.frontier.queued[0]
		m.frontier.queued = m.frontier.queued[1:]
		// The plan was made before these answers landed. A Ticket read since —
		// by a fan-out this seat replaced, or by hand — is already paid for,
		// and paying twice is exactly what this screen's cost discipline is
		// about.
		if entry, cached := m.details[id]; cached {
			m = m.seatFanoutLinks(id, entry.detail.Links)
			m.frontier.done++
			adopted = true
			continue
		}
		m.detailFanoutInflight++
		cmds = append(cmds, m.frontierFetchCmd(m.frontierGeneration, id))
	}
	if adopted {
		m = m.settleFrontier()
		var rebuildCmd tea.Cmd
		m, rebuildCmd = m.frontierEvidenceChanged()
		cmds = append(cmds, rebuildCmd)
	}
	return m, tea.Batch(cmds...)
}

// seatFanoutLinks writes one Ticket's Links into the seated input. Key presence
// is the tri-state that makes fail-closed Actionable real, so this is the only
// way a Ticket becomes verified.
func (m Model) seatFanoutLinks(id model.TicketID, links []model.Link) Model {
	if m.frontier.input.Links == nil {
		m.frontier.input.Links = make(map[model.TicketID][]model.Link)
	}
	m.frontier.input.Links[id] = links
	return m
}

// settleFrontier releases the child context once this seat's own plan has been
// answered in full, from the Tracker or session cache. Plan completion is not
// resolution: the latter is derived from full Watchlist Links-or-failure
// coverage. It counts seat bookkeeping rather than the shared in-flight counter
// because cancelled reads from an older generation may still be unwinding.
func (m Model) settleFrontier() Model {
	if m.frontier.done == m.frontier.planned && len(m.frontier.queued) == 0 {
		m = m.finishFrontierFanout()
	}
	return m
}

// frontierFetchCmd reads one Ticket's Detail with the child context owned by
// this Frontier generation. The command captures both values so replacing the
// Model cannot redirect an issued read.
func (m Model) frontierFetchCmd(generation int, id model.TicketID) tea.Cmd {
	fetch := m.fetchDetail
	ctx := m.frontierContext
	return func() tea.Msg {
		d, caps, err := fetch(ctx, id)
		return frontierDetailMsg{generation: generation, id: id, detail: d, caps: caps, err: err}
	}
}

// onFrontierDetail folds one fan-out answer in. Every issued command frees its
// shared slot, and every success warms the session cache before the generation
// guard: a Provider may complete just before cancellation and that paid-for
// answer should prevent a duplicate read. Only the current seat's answer changes
// progress or rendering. A failure writes no Links key, preserving fail-closed
// Actionable; local cancellation is control flow and never a Ticket failure.
func (m Model) onFrontierDetail(msg frontierDetailMsg) (tea.Model, tea.Cmd) {
	m.detailFanoutInflight--
	if msg.err == nil {
		m.details[msg.id] = detailEntry{detail: msg.detail, caps: msg.caps, fetchedAt: m.now()}
	}
	if msg.generation != m.frontierGeneration {
		// An abandoned command has released a shared slot. A re-entered current
		// generation may now issue from its own distinct queue and context.
		return m.issueFrontierFetches()
	}
	m.frontier.done++

	switch {
	case errors.Is(msg.err, context.Canceled):
		// Cancellation never becomes a failed member or footer error. Normally the
		// generation has already advanced; this protects against reordered local
		// control messages and wrapped cancellation errors too.
	case msg.err != nil:
		if m.frontier.failed == nil {
			m.frontier.failed = make(map[model.TicketID]struct{})
		}
		m.frontier.failed[msg.id] = struct{}{}
		m.frontier.lastErr = msg.err
	default:
		m = m.seatFanoutLinks(msg.id, msg.detail.Links)
	}
	m = m.settleFrontier()
	m, rebuildCmd := m.frontierEvidenceChanged()
	next, fetchCmd := m.issueFrontierFetches()
	return next, tea.Batch(rebuildCmd, fetchCmd)
}

// adoptCachedLinks folds Details read since the fan-out into the seated input.
// One Ticket, one answer: a member whose fan-out read failed and was then
// opened by hand has Links in the session cache, and leaving the Frontier's own
// map without the key would keep the card UNVERIFIED and the footer claiming a
// read that has since succeeded could not be made — while the list, which reads
// the cache directly, draws the Ticket as Actionable.
func (m Model) adoptCachedLinks() Model {
	for _, t := range m.frontier.input.Tickets {
		if _, seated := m.frontier.input.Links[t.ID]; seated {
			continue
		}
		entry, cached := m.details[t.ID]
		if !cached {
			continue
		}
		m = m.seatFanoutLinks(t.ID, entry.detail.Links)
		delete(m.frontier.failed, t.ID)
	}
	if len(m.frontier.failed) == 0 {
		m.frontier.lastErr = nil
	}
	return m
}

// rebuildFrontier recomputes everything derived from the seated input. It owns
// the only production installation of a materialized canvas, and admission at
// this boundary precedes every dummy, route, stroke, hit-map, and grid allocation.
func (m Model) rebuildFrontier() Model {
	m = m.discardPendingFrontierRebuild()
	preferredFocus := m.frontier.focusID
	previous := indexOfNode(m.frontier.layout.order, preferredFocus)
	if preferredFocus == "" && !m.frontier.hasFocus {
		preferredFocus = m.selectedID
	}
	g := model.BuildBlockingGraph(m.frontier.input.Tickets, m.frontier.input.Links,
		m.frontier.input.Capabilities)
	nodes := frontierNodes(g, m.frontier.input.Tickets, m.frontier.isResolved())
	inner := m.frontierCanvasRect()
	projection := projectFrontierRanks(g, nodes, inner.W)
	candidates := projection.candidates
	direction := m.frontier.direction
	if !m.frontier.directionSet {
		direction = chooseFrontierDirection(candidates, inner.W, inner.H)
	} else {
		direction = frontierDirectionAfterResize(direction, candidates, inner.W, inner.H)
	}

	m.frontier.graph = g
	m.frontier.direction = direction
	m.frontier.directionSet = true
	m.mouseEpoch++
	if refusal := refuseFrontierCandidate(candidates[direction], len(nodes), len(g.Ghosts())); refusal != nil {
		m = m.clearPendingClick()
		m.frontier.layout = frontierLayout{}
		m.frontier.refusal = refusal
		m.frontier.focusID = ""
		m.frontier.hasFocus = false
		m.frontier.offsetX = 0
		m.frontier.offsetY = 0
		return m
	}

	m.frontier.refusal = nil
	m.frontier.layout = m.frontierLayoutForModel(g, nodes, frontierLayoutOptions{
		innerWidth: inner.W,
		direction:  direction,
		projection: &projection,
	})

	l := m.frontier.layout
	m.frontier.focusID = preferredFocus
	if _, drawn := l.nodeAt[preferredFocus]; !drawn {
		// The focused Ticket left the Watchlist under a refresh. Focus lands on
		// the nearest node in canonical order rather than jumping to the top.
		m.frontier.hasFocus = false
		if len(l.order) > 0 {
			m.frontier.focusID = l.order[min(max(previous, 0), len(l.order)-1)]
			m.frontier.hasFocus = true
		}
	} else {
		m.frontier.hasFocus = true
	}
	if !m.effectiveFrontierKeys().Open.Enabled() {
		m.frontier.hasFocus = false
		return m.reconcileFrontier(false)
	}
	return m.reconcileFrontier(true)
}

func (m Model) frontierCanvasRect() frontierRect {
	return frontierInnerRect(m.width, m.frontierBodyHeight())
}

func (m Model) frontierResizeNeedsDirectionFlip() bool {
	if !m.frontier.directionSet {
		return false
	}
	if m.frontier.refusal != nil {
		// A refused seat retains no candidate layout metadata. Re-projecting on a
		// height change is the only honest way to discover a newly fitting admitted
		// orientation, and still settles through #101's replacement boundary.
		return true
	}
	if !m.frontierShowsCanvas() {
		return false
	}
	inner := m.frontierCanvasRect()
	return frontierDirectionAfterResize(m.frontier.direction, m.frontier.layout.candidates,
		inner.W, inner.H) != m.frontier.direction
}

// reconcileFrontier clamps the window into the canvas, optionally bringing the
// focused card back into view first.
func (m Model) reconcileFrontier(ensureFocus bool) Model {
	inner := m.frontierCanvasRect()
	if ensureFocus && m.frontier.hasFocus {
		if rect, ok := m.frontier.layout.nodeAt[m.frontier.focusID]; ok {
			m.frontier.offsetX, m.frontier.offsetY = ensureNodeVisible(
				rect, m.frontier.offsetX, m.frontier.offsetY, inner.W, inner.H)
		}
	}
	m.frontier.offsetX = clampFrontierOffset(m.frontier.offsetX, m.frontier.layout.width, inner.W)
	m.frontier.offsetY = clampFrontierOffset(m.frontier.offsetY, m.frontier.layout.height, inner.H)
	return m
}

// indexOfNode is where id sits in canonical order, or -1.
func indexOfNode(order []model.TicketID, id model.TicketID) int {
	for i, candidate := range order {
		if candidate == id {
			return i
		}
	}
	return -1
}

// leaveFrontier returns to the list, cancels every issued read owned by this
// seat, and clears its unissued queue. The generation advance separately rejects
// any Provider outcome that races with cancellation.
func (m Model) leaveFrontier() (tea.Model, tea.Cmd) {
	m = m.clearPendingClick()
	m = m.retireFrontierFanout()
	m.mouseEpoch++
	m.mode = modeList
	// Selection survives the toggle in both directions. Only visible focus may
	// replace it: the no-Capability screen retains a hidden layout for internal
	// consistency, but must not move selection to a node the user never saw.
	if m.frontier.hasFocus {
		if row, ok := rowOf(m.rows, m.frontier.focusID); ok {
			m = m.selectRow(row)
		}
	}
	m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	return m, repaint
}

// openFrontierNode opens the focused node's Detail: a member's exactly as the
// list does, and a Ghost Ticket's through the same deliberately thin seat a
// followed Link gets.
//
// The Frontier is a second rendering of the Watchlist rather than a Trail
// entry, so opening from it clears the Trail exactly as a list-open does and
// records the Frontier as where root esc returns to.
func (m Model) openFrontierNode() (tea.Model, tea.Cmd) {
	if !m.frontier.hasFocus {
		return m, nil
	}
	t, ok := m.frontierTicket(m.frontier.focusID)
	if !ok {
		return m, nil
	}
	m = m.clearPendingClick()
	m.trail = nil
	m.detailReturn = modeFrontier
	return m.seatDetail(t, m.frontier.input.Header, m.frontier.input.Capabilities)
}

// frontierTicket resolves a node to the Ticket its Detail is seated from.
func (m Model) frontierTicket(id model.TicketID) (model.Ticket, bool) {
	for _, t := range m.frontier.input.Tickets {
		if t.ID == id {
			return t, true
		}
	}
	for _, ghost := range m.frontier.graph.Ghosts() {
		if ghost.Target.ID == id {
			return ticketFromLinkTarget(ghost.Target), true
		}
	}
	return model.Ticket{}, false
}

// refreshFrontier first reconciles paid-for cached reads into the current seat,
// then re-issues only the reads that never succeeded. Re-reading a warm cache
// would turn a recovery key into a fan-out, which is what Amendment 4 says must
// be deliberate; one Ticket's r in Detail remains the way to re-read one Ticket.
func (m Model) refreshFrontier() (tea.Model, tea.Cmd) {
	seated := len(m.frontier.input.Links)
	m = m.adoptCachedLinks()
	if len(m.frontier.input.Links) > seated {
		m.mouseEpoch++
	}
	m = m.rebuildFrontier()
	if !m.frontier.input.Capabilities.BlockingLinks {
		return m, repaint
	}
	outstanding := detailfanout.Plan(m.frontier.input.Tickets, m.haveDetail)
	if len(outstanding) == 0 || m.detailFanoutInflight > 0 || len(m.frontier.queued) > 0 {
		return m, repaint
	}
	m = m.retireFrontierFanout()
	m.frontier.queued = outstanding
	m.frontier.planned = len(outstanding)
	m.frontier.done = 0
	m.frontier.failed = nil
	m.frontier.lastErr = nil
	m = m.startFrontierFanout()
	m = m.rebuildFrontier()
	return m.issueFrontierFetches()
}

// onFrontierKey dispatches a key press on the Frontier. Neither d nor / is
// bound here: filters do not apply to this screen and the footer says so.
func (m Model) onFrontierKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keys := m.effectiveFrontierKeys()
	switch {
	case key.Matches(msg, keys.Quit):
		return m.quit(msg), tea.Quit

	case key.Matches(msg, keys.Toggle):
		return m.leaveFrontier()

	case key.Matches(msg, keys.Open):
		return m.openFrontierNode()

	case key.Matches(msg, keys.Refresh):
		return m.refreshFrontier()

	case key.Matches(msg, keys.ToggleMouse):
		return m.toggleMouse(), nil

	case key.Matches(msg, keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		return m.reconcileFrontier(true), nil

	case key.Matches(msg, keys.Up):
		return m.moveFrontierFocus(0, -1), nil
	case key.Matches(msg, keys.Down):
		return m.moveFrontierFocus(0, 1), nil
	case key.Matches(msg, keys.Left):
		return m.moveFrontierFocus(-1, 0), nil
	case key.Matches(msg, keys.Right):
		return m.moveFrontierFocus(1, 0), nil
	case key.Matches(msg, keys.PageUp):
		return m.pageFrontier(-1), nil
	case key.Matches(msg, keys.PageDown):
		return m.pageFrontier(1), nil
	case key.Matches(msg, keys.Home):
		return m.focusFrontierNodeAt(0), nil
	case key.Matches(msg, keys.End):
		return m.focusFrontierNodeAt(len(m.frontier.layout.order) - 1), nil
	}
	return m, nil
}

func (m Model) moveFrontierFocus(dx, dy int) Model {
	if !m.frontier.hasFocus {
		return m
	}
	next, ok := m.frontier.layout.moveFocus(m.frontier.focusID, dx, dy)
	if !ok {
		return m
	}
	m.frontier.focusID = next
	return m.reconcileFrontier(true)
}

// pageFrontier moves the canvas window by the current inner-canvas height.
func (m Model) pageFrontier(pages int) Model {
	m.frontier.offsetY += pages * m.frontierCanvasRect().H
	return m.reconcileFrontier(false)
}

func (m Model) focusFrontierNodeAt(i int) Model {
	if i < 0 || i >= len(m.frontier.layout.order) {
		return m
	}
	m.frontier.focusID = m.frontier.layout.order[i]
	m.frontier.hasFocus = true
	return m.reconcileFrontier(true)
}

// frontierFrame renders the whole screen: a header that never scrolls, the
// canvas window, and a footer that says what this screen is not showing.
func (m Model) frontierFrame() string {
	footer := m.frontierFooterLines()
	body := m.frontierBody(m.frontierBodyHeight())
	return strings.Join(append([]string{m.frontierHeader()}, append(body, footer...)...), "\n")
}

// frontierHeader is the same three lines the list's header occupies, so the
// body arithmetic is shared: identity, the graph's counts, and a blank.
func (m Model) frontierHeader() string {
	f := m.frontier
	nodes := len(f.layout.order)
	ghosts := len(f.graph.Ghosts())
	if f.refusal != nil {
		nodes = f.refusal.nodes
		ghosts = f.refusal.ghosts
	}

	var counts string
	switch {
	case !f.input.Capabilities.BlockingLinks:
		counts = "frontier" + separator + "this Provider reports no blocking links"
	case f.refusal != nil:
		counts = "frontier" + separator + plural(nodes, "node", "nodes")
		if ghosts > 0 {
			counts += separator + plural(ghosts, "ghost", "ghosts")
		}
		counts += separator + "canvas refused"
		switch {
		case f.isResolved():
			counts += separator + "Details complete"
		case m.frontierContext != nil:
			counts += fmt.Sprintf("%sreading Detail %d/%d", separator, f.done, f.planned)
		default:
			counts += separator + "Details pending"
		}
	case f.isResolved():
		// Counted over the layout's order rather than the graph's members: a
		// Watchlist may name one Ticket twice, and a header claiming more
		// Actionable Tickets than there are cards is not a count of anything.
		//
		// Only members whose Links were readable are counted, and when no
		// member's were the count is withheld outright. "0 actionable" is a
		// computed answer, and a Frontier where nothing could be computed must
		// not be byte-identical to one where everything is genuinely blocked.
		actionable, known := 0, 0
		for _, id := range f.layout.order {
			a, member := f.graph.For(id)
			if !member || !a.LinksKnown {
				continue
			}
			known++
			if a.Actionable {
				actionable++
			}
		}
		tally := fmt.Sprintf("%d actionable", actionable)
		if known == 0 {
			tally = "actionable unknown"
		}
		counts = "frontier" + separator + plural(nodes, "node", "nodes")
		if ghosts > 0 {
			counts += separator + plural(ghosts, "ghost", "ghosts")
		}
		counts += separator + tally
	case m.frontierContext != nil:
		counts = fmt.Sprintf("frontier%s%s%sreading Detail %d/%d",
			separator, plural(nodes, "node", "nodes"), separator, f.done, f.planned)
	default:
		counts = fmt.Sprintf("frontier%s%s%sDetails pending · press r",
			separator, plural(nodes, "node", "nodes"), separator)
	}

	return strings.Join([]string{
		headerIdentity(f.input.Header, m.width, m.styles),
		// The staleness clock is reserved rather than truncated away: how old
		// this reading is answers "why does it say that", and a counts line
		// clipped mid-word answers nothing. The list header makes the same
		// trade.
		pairLineReserved(m.styles.Counts.Render(counts),
			m.styles.Staleness.Render(m.staleness()), m.width),
		"",
	}, "\n")
}

// frontierBody draws the canvas window, or the state that stands in for it.
func (m Model) frontierBody(height int) []string {
	switch {
	case !m.frontier.input.Capabilities.BlockingLinks:
		// No fetch was issued at all: a screen that cannot draw a graph must not
		// pay for one.
		return padLines(m.wrappedMuted(
			"This Provider does not report blocking links, so sitrep cannot tell which "+
				"Tickets are blocked or which can be picked up. The Watchlist itself is "+
				"unaffected — press v or esc to go back to it."), height)
	case m.frontier.refusal != nil:
		refusal := m.frontier.refusal
		return padLines(m.wrappedMuted(fmt.Sprintf(
			"The selected Frontier canvas projects to %d × %d cells (%s projected cells), "+
				"above the permitted 500,000 cells. sitrep refused the entire canvas without "+
				"clipping: no Frontier graph or subset was drawn, while the fetched Watchlist "+
				"evidence remains intact. On linux/amd64, the raw frontierCell grid payload for "+
				"500,000 cells is 24,000,000 bytes only. Row slices, metadata, routes, "+
				"Go/runtime overhead, and total process memory are additional and "+
				"architecture-dependent. Press v or esc to return to the Watchlist.",
			refusal.width, refusal.height, refusal.projectedCells)), height)
	case len(m.frontier.input.Tickets) == 0:
		return padLines([]string{m.styles.Muted.Render("This Watchlist has no Tickets.")}, height)
	}
	// A graph with nodes but no edges is not an error state: every Todo node
	// whose Links were readable is then Actionable, which is the truth.
	return renderFrontierBody(m.frontier.layout, m.frontier.focusID, m.frontier.hasFocus,
		m.frontier.offsetX, m.frontier.offsetY, m.width, height, m.styles)
}

func (m Model) wrappedMuted(text string) []string {
	lines := wrapText(text, m.width)
	for i, line := range lines {
		lines[i] = m.styles.Muted.Render(truncateLine(line, m.width))
	}
	return lines
}

// padLines grows a block to exactly height lines so the footer stays at the
// bottom of the screen.
func padLines(lines []string, height int) []string {
	if height <= 0 {
		return nil
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// frontierFooterLines is the Frontier's bottom block with the scroll position
// written onto it. The position is an overlay rather than a line of its own,
// which is what lets frontierFooterHeight measure the block instead of
// hand-counting it.
func (m Model) frontierFooterLines() []string {
	lines := m.frontierFooterBlock()
	if pos := m.frontierScrollPosition(); pos != "" {
		last := len(lines) - 1
		position := m.styles.Staleness.Render(pos)
		if lipgloss.Width(lines[last])+1+lipgloss.Width(position) <= m.width {
			lines[last] = pairLineReserved(lines[last], position, m.width)
		} else {
			lines[0] = pairLineReserved(lines[0], position, m.width)
		}
	}
	return lines
}

// frontierFooterBlock is every footer line the screen's own state produces: the
// spacer, the notices, and the help listing. It never consults the canvas, so
// frontierBodyHeight can measure it without asking the canvas how tall it is.
//
// The filter notice is mandatory whenever a filter is on: hidden work that looks
// like missing work is this feature's failure mode, and here it would also
// delete edges.
func (m Model) frontierFooterBlock() []string {
	lines := []string{""}
	if m.filter.Active() {
		lines = append(lines, m.styles.Muted.Render(truncateLine(
			"filters do not apply here: the Frontier renders the whole Watchlist", m.width)))
	}
	if failed := len(m.frontier.failed); m.frontier.isResolved() && failed > 0 {
		notice := detailfanout.UnreadableLinksNotice(failed)
		lines = append(lines, m.styles.Error.Render(balancedTruncate(notice, m.width, "…")))
	}
	if m.frontier.lastErr != nil {
		// Whichever parallel read failed last, labelled as one failure rather than
		// the cause of everything on screen. A refused canvas disables Refresh, so
		// its footer reports evidence without advertising an inert remedy.
		message := fmt.Sprintf("read failed: %s — press r to retry the Tickets that failed",
			m.frontier.lastErr.Error())
		if m.frontier.refusal != nil {
			message = fmt.Sprintf("read failed: %s — the fetched evidence remains recorded",
				m.frontier.lastErr.Error())
		}
		lines = append(lines, m.styles.Error.Render(balancedTruncate(message, m.width, "…")))
	}

	return append(lines, strings.Split(truncateBlock(m.help.View(m.helpKeys()), m.width), "\n")...)
}

// frontierShowsCanvas reports whether the body is the graph rather than one of
// the states that stands in for it.
func (m Model) frontierShowsCanvas() bool {
	return m.frontier.input.Capabilities.BlockingLinks &&
		len(m.frontier.input.Tickets) > 0 && m.frontier.refusal == nil
}

// frontierScrollPosition reports where the window sits on a canvas bigger than
// it, one axis per direction that actually has somewhere to go. A canvas that
// fits, or a body that is not a canvas at all, reports nothing.
//
// The axes are labelled x and y, in character cells. "col" would collide with
// the graph columns the blocker-side and dependent-side keys move between,
// which are counted in nodes and are not what this reports.
func (m Model) frontierScrollPosition() string {
	if !m.frontierShowsCanvas() {
		return ""
	}
	l := m.frontier.layout
	inner := m.frontierCanvasRect()
	var parts []string
	if l.width > inner.W {
		parts = append(parts, fmt.Sprintf("x %d/%d", m.frontier.offsetX, l.width-inner.W))
	}
	if l.height > inner.H {
		parts = append(parts, fmt.Sprintf("y %d/%d", m.frontier.offsetY, l.height-inner.H))
	}
	return strings.Join(parts, separator)
}

// frontierFooterHeight is how many lines the footer occupies. Measuring the
// block would recurse — the canvas needs the height to know its own size, and
// the scroll position needs the canvas — so the block is built without the
// scroll-position overlay, which is what needs the canvas and which never adds
// a line: it is written onto a line the footer already has.
func (m Model) frontierFooterHeight() int {
	return len(m.frontierFooterBlock())
}

// frontierBodyHeight is the non-negative room left for the canvas once the
// header and footer have taken theirs. A positive remainder keeps its full
// extent; no synthetic body row is created when none remains.
func (m Model) frontierBodyHeight() int {
	return max(m.height-headerHeight-m.frontierFooterHeight(), 0)
}
