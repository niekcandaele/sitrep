package tui

import (
	"context"
	"errors"
	"fmt"
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

// frontierState is the Frontier screen's own state, kept in one struct so that
// it can be seated whole: the list's state is never consulted to draw it.
type frontierState struct {
	input  FrontierInput
	graph  model.BlockingGraph
	layout frontierLayout
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
		m = m.settleFrontier().rebuildFrontier()
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
	// The badges, and therefore the geometry a queued mouse callback captured,
	// may have changed.
	m.mouseEpoch++
	m = m.rebuildFrontier()
	return m.issueFrontierFetches()
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

// rebuildFrontier recomputes everything derived from the seated input: the
// blocking graph, the nodes, the canvas, and where focus and the window sit on
// it.
func (m Model) rebuildFrontier() Model {
	previous := indexOfNode(m.frontier.layout.order, m.frontier.focusID)
	g := model.BuildBlockingGraph(m.frontier.input.Tickets, m.frontier.input.Links,
		m.frontier.input.Capabilities)
	m.frontier.graph = g
	m.frontier.layout = layoutFrontier(g,
		frontierNodes(g, m.frontier.input.Tickets, m.frontier.isResolved()), m.width)

	l := m.frontier.layout
	if _, drawn := l.nodeAt[m.frontier.focusID]; !drawn {
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

// reconcileFrontier clamps the window into the canvas, optionally bringing the
// focused card back into view first.
func (m Model) reconcileFrontier(ensureFocus bool) Model {
	width, height := m.width, m.frontierBodyHeight()
	if ensureFocus && m.frontier.hasFocus {
		if rect, ok := m.frontier.layout.nodeAt[m.frontier.focusID]; ok {
			m.frontier.offsetX, m.frontier.offsetY = ensureNodeVisible(
				rect, m.frontier.offsetX, m.frontier.offsetY, width, height)
		}
	}
	m.frontier.offsetX = clampFrontierOffset(m.frontier.offsetX, m.frontier.layout.width, width)
	m.frontier.offsetY = clampFrontierOffset(m.frontier.offsetY, m.frontier.layout.height, height)
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

// pageFrontier moves the canvas window by current visible body-height pages.
func (m Model) pageFrontier(pages int) Model {
	m.frontier.offsetY += pages * m.frontierBodyHeight()
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

	var counts string
	switch {
	case !f.input.Capabilities.BlockingLinks:
		counts = "frontier" + separator + "this Provider reports no blocking links"
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
	case len(m.frontier.input.Tickets) == 0:
		return padLines([]string{m.styles.Muted.Render("This Watchlist has no Tickets.")}, height)
	}
	// A graph with nodes but no edges is not an error state: every Todo node
	// whose Links were readable is then Actionable, which is the truth.
	return renderFrontierCanvas(m.frontier.layout, m.frontier.focusID, m.frontier.hasFocus,
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
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:max(height, 1)]
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
		// Whichever parallel read failed last, labelled as one failure and
		// pointed at its remedy: bare, it reads as the cause of everything on
		// screen. The text is the Tracker's, so the cut is re-balanced.
		lines = append(lines, m.styles.Error.Render(balancedTruncate(fmt.Sprintf(
			"read failed: %s — press r to retry the Tickets that failed",
			m.frontier.lastErr.Error()), m.width, "…")))
	}

	return append(lines, strings.Split(truncateBlock(m.help.View(m.helpKeys()), m.width), "\n")...)
}

// frontierShowsCanvas reports whether the body is the graph rather than one of
// the states that stands in for it.
func (m Model) frontierShowsCanvas() bool {
	return m.frontier.input.Capabilities.BlockingLinks && len(m.frontier.input.Tickets) > 0
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
	width, height := m.width, m.frontierBodyHeight()
	var parts []string
	if l.width > width {
		parts = append(parts, fmt.Sprintf("x %d/%d", m.frontier.offsetX, l.width-width))
	}
	if l.height > height {
		parts = append(parts, fmt.Sprintf("y %d/%d", m.frontier.offsetY, l.height-height))
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

// frontierBodyHeight is the room left for the canvas once the header and footer
// have taken theirs, floored at one line so a tiny terminal still renders.
func (m Model) frontierBodyHeight() int {
	return max(m.height-headerHeight-m.frontierFooterHeight(), 1)
}
