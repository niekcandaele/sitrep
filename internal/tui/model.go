package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// Model is the monitor's state: the last good reading of the collection, the
// rows derived from it, and where the cursor sits.
type Model struct {
	fetch    func() (ListInput, error)
	now      func() time.Time
	interval time.Duration

	input       ListInput
	hasData     bool
	lastErr     error
	refreshing  bool
	generation  int
	lastAttempt time.Time

	rows       []Row
	selected   int
	selectedID model.TicketID
	offset     int

	// filter is the session's view filter. It is applied on the way to the
	// screen and never persisted: #12 owns configuration, and this is a
	// keystroke, not a setting.
	filter Filter
	// searching is true while the find box owns the keyboard.
	searching bool
	// search holds the draft query; filter.Query holds what the list is
	// actually narrowed by. They are kept in step on every keystroke, and are
	// separate so that esc can drop both in one move.
	search textinput.Model

	// mode is which screen owns the terminal. Opening and closing Detail
	// touches nothing above this line — which is why the selection, the scroll
	// offset and the filters survive it without any code restoring them.
	//
	// A later caller may start the program in modeDetail by seating a
	// DetailInput before the first frame — a bare Ticket Ref decoded straight
	// into Detail — which is why detailState is a struct and why the Detail
	// screen takes its breadcrumb as a Header field rather than reading the
	// list's.
	mode   mode
	detail detailState
	// details caches the Details read this session, per Ticket. It holds Detail
	// and nothing else: no list data migrates in here, and nothing here migrates
	// onto a Ticket (ADR-0003). It is never persisted — #12 owns configuration.
	details          map[model.TicketID]detailEntry
	detailGeneration int
	fetchDetail      func(model.TicketID) (model.Detail, model.Capabilities, error)

	width, height int
	ready         bool

	keys       KeyMap
	searchKeys SearchKeyMap
	detailKeys DetailKeyMap
	help       help.Model
	styles     Styles
	quitting   bool
}

// New returns the monitor's Model reading from opts.Source.
//
// ctx is the program's lifetime, bound into the refresh command here: a Bubble
// Tea command is handed no context of its own, and a context field on a model
// outlives every update it was correct for. Binding it once means quitting
// cancels an in-flight FetchEpic instead of holding the process open for the
// Tracker's HTTP timeout.
func New(ctx context.Context, opts Options) Model {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	src := opts.Source

	// The Detail seam is bound to the same lifetime as the refresh, and for the
	// same reason: quitting while a Detail read is in flight cancels it instead
	// of holding the process open for the Tracker's HTTP timeout. A Provider
	// that cannot serve Detail leaves this nil, and enter says so rather than
	// panicking.
	detailSrc := opts.DetailSource
	fetchDetail := func(id model.TicketID) (model.Detail, model.Capabilities, error) {
		if detailSrc == nil {
			return model.Detail{}, model.Capabilities{}, errNoDetailSource
		}
		return detailSrc(ctx, id)
	}

	// The box draws no cursor of its own: the real terminal cursor is placed
	// on it from View, which keeps the frame free of a blinking glyph that a
	// golden would have to guess the phase of.
	search := textinput.New()
	search.Prompt = searchPrompt
	search.CharLimit = 0
	search.SetVirtualCursor(false)

	return Model{
		fetch:    func() (ListInput, error) { return src(ctx) },
		now:      now,
		interval: opts.Interval,
		// The first refresh is already on its way out of Init.
		generation:  1,
		refreshing:  true,
		lastAttempt: now(),
		search:      search,
		fetchDetail: fetchDetail,
		details:     make(map[model.TicketID]detailEntry),
		keys:        DefaultKeyMap(),
		searchKeys:  DefaultSearchKeyMap(),
		detailKeys:  DefaultDetailKeyMap(),
		help:        help.New(),
		styles:      DefaultStyles(true),
	}
}

// errNoDetailSource explains a monitor opened without a way to read Detail. It
// is a wiring mistake rather than a Tracker failure, so it says which.
var errNoDetailSource = errors.New("this monitor was opened without a Detail source")

// Init starts the first refresh, the heartbeat, and the background-colour
// query that decides the palette.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(m.generation), heartbeat(), requestBackgroundColor)
}

// Update folds one message into the model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.help.SetWidth(msg.Width)
		m.search.SetWidth(searchBoxWidth(msg.Width))
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		// The Detail body is a function of width, so a resize re-wraps it and
		// the offset has to come back inside the re-wrapped document. A resize
		// that scrolls the reader into the void is the bug this clamp exists
		// for.
		m.detail.offset = m.clampDetail(m.detail.offset)
		return m, nil

	case tea.BackgroundColorMsg:
		m.styles = DefaultStyles(msg.IsDark())
		return m, nil

	case heartbeatMsg:
		return m.onHeartbeat()

	case refreshedMsg:
		return m.onRefreshed(msg), nil

	case detailFetchedMsg:
		return m.onDetailFetched(msg), nil

	case tea.KeyPressMsg:
		// The mode decides who owns the keyboard, before any binding is
		// consulted: while the box is open every list command is text, and while
		// Detail is open the list's commands are not on this screen at all.
		switch {
		case m.searching:
			return m.onSearchKey(msg)
		case m.mode == modeDetail:
			return m.onDetailKey(msg)
		}
		return m.onKey(msg)

	case tea.PasteMsg:
		// A pasted Ticket key is the obvious way to use this box, and a paste
		// arrives as its own message rather than as key presses.
		if m.searching {
			return m.updateSearch(msg)
		}
	}
	return m, nil
}

// View renders the whole screen: a header that never scrolls, the windowed
// list, and a footer carrying the help line and any refresh error.
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	if !m.ready {
		// Before the terminal has reported its size there is nothing honest to
		// draw: a frame guessed at 80x24 flashes the wrong layout for one
		// repaint, which reads worse than a blank one.
		return v
	}

	if m.mode == modeDetail {
		v.SetContent(m.detailFrame())
		return v
	}

	header := renderHeader(m.input, m.staleness(), m.hasData, m.width, m.styles)
	v.SetContent(strings.Join(append([]string{header, m.renderBody()}, m.footerLines()...), "\n"))
	v.Cursor = m.cursor()
	return v
}

// cursor places the real terminal cursor inside the find box, and nowhere else:
// the list marks its selection with "▸", and a second cursor parked in the
// top-left corner only draws the eye away from it.
func (m Model) cursor() *tea.Cursor {
	if !m.searching {
		return nil
	}
	c := m.search.Cursor()
	if c == nil {
		return nil
	}
	// The box is drawn at column 0 of the filter line, so only the row needs
	// shifting: past the header, past the body, and past the footer's blank
	// spacer and any refresh error above it.
	c.Y += headerHeight + m.bodyHeight() + m.filterLineIndex()
	return c
}

// visibleTickets returns the Tickets the list is currently showing: the last
// good reading narrowed by the session's Filter. This is the single call site
// filtering happens at — the rows, and only the rows, are built from it. The
// header's progress is deliberately computed from m.input.Tickets instead, so
// no filter can move the bar.
func (m Model) visibleTickets() []model.Ticket { return m.filter.Apply(m.input.Tickets) }

// onHeartbeat re-arms the timer and starts a refresh when the interval has
// elapsed. The beat is also what makes the staleness indicator count up
// without a data change.
func (m Model) onHeartbeat() (tea.Model, tea.Cmd) {
	if m.refreshing || m.now().Sub(m.lastAttempt) < m.interval {
		return m, heartbeat()
	}
	next, cmd := m.startRefresh()
	return next, tea.Batch(cmd, heartbeat())
}

// startRefresh begins one refresh, or does nothing when one is already in
// flight. Refusing to overlap is what stops a user leaning on `r` from
// stacking requests at a rate-limited API.
func (m Model) startRefresh() (Model, tea.Cmd) {
	if m.refreshing {
		return m, nil
	}
	m.generation++
	m.refreshing = true
	m.lastAttempt = m.now()
	return m, m.fetchCmd(m.generation)
}

// fetchCmd reads the Source on Bubble Tea's goroutine pool, tagging the result
// with the generation that asked for it.
func (m Model) fetchCmd(generation int) tea.Cmd {
	fetch := m.fetch
	return func() tea.Msg {
		in, err := fetch()
		return refreshedMsg{generation: generation, input: in, err: err}
	}
}

// onRefreshed folds one reading in. A failed refresh keeps the last good data
// on screen: the list still renders, and the staleness indicator keeps
// counting from the last *successful* fetch, because the data really is that
// old.
func (m Model) onRefreshed(msg refreshedMsg) Model {
	if msg.generation != m.generation {
		// A slow auto-refresh answering after a manual one must not land on
		// top of the fresher reading.
		return m
	}
	m.refreshing = false

	if msg.err != nil {
		m.lastErr = msg.err
		return m
	}
	m.lastErr = nil
	m.input = msg.input
	m.hasData = true
	return m.rebuildRows()
}

// rebuildRows derives the list from the current reading and puts the cursor
// back on the Ticket the user was looking at. Auto-refresh rebuilds the rows
// under a live cursor every interval: following the Ticket by its ID rather
// than its position is what stops the selection sliding onto whatever moved
// into that slot.
// The same path runs after a filter change: a Filter narrows the list under a
// live cursor exactly the way a refresh does, so the two share the clamp rather
// than each keeping their own opinion of where the cursor should land.
func (m Model) rebuildRows() Model {
	// esc means "clear the filter" only while there is one to clear;
	// otherwise it falls through to Quit. Keeping the binding's enabled state
	// in step with the Filter is what makes both the matching and the help
	// line say the same thing.
	m.keys.ClearFilter.SetEnabled(m.filter.Active())

	m.rows = BuildRows(m.visibleTickets())

	if found, ok := rowOf(m.rows, m.selectedID); ok {
		m.selected = found
	} else {
		m.selected = nearestSelectable(m.rows, m.selected)
	}
	m.selectedID = selectedTicketID(m.rows, m.selected)
	m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	return m
}

// onKey dispatches a key press in list mode.
//
// ClearFilter is matched before Quit because both answer to esc: the ladder is
// escape the box, then escape the filter, then escape the program. q and ctrl+c
// still quit unconditionally, so nobody is trapped by a filter.
func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.ClearFilter):
		return m.setFilter(Filter{}), nil

	case key.Matches(msg, m.keys.HideFinished):
		m.filter.HideFinished = !m.filter.HideFinished
		return m.setFilter(m.filter), nil

	case key.Matches(msg, m.keys.Find):
		m.searching = true
		// The box re-opens holding the applied query, so / is how you edit
		// what you last searched for rather than only how you start again.
		m.search.SetValue(m.filter.Query)
		m.search.CursorEnd()
		cmd := m.search.Focus()
		// Opening the box adds a footer line, which takes one from the body.
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		return m, cmd

	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		// The expanded listing eats body height, so the window has to be
		// re-measured before the next frame.
		m.help.ShowAll = !m.help.ShowAll
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		next, cmd := m.startRefresh()
		return next, cmd

	case key.Matches(msg, m.keys.Open):
		return m.openDetail()

	case key.Matches(msg, m.keys.Up):
		return m.move(-1), nil
	case key.Matches(msg, m.keys.Down):
		return m.move(1), nil
	case key.Matches(msg, m.keys.PageUp):
		return m.page(-1), nil
	case key.Matches(msg, m.keys.PageDown):
		return m.page(1), nil
	case key.Matches(msg, m.keys.Home):
		return m.jump(0, 1), nil
	case key.Matches(msg, m.keys.End):
		return m.jump(len(m.rows)-1, -1), nil
	}
	return m, nil
}

// onSearchKey dispatches a key press while the find box is open. Only the four
// bindings SearchKeyMap declares are intercepted; everything else — including
// q, d, r and ? — is text, because a find box that quits the program when you
// search for "queue" is worse than no find box.
func (m Model) onSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.searchKeys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.searchKeys.Cancel):
		// Abandon: the draft and the applied query go together. HideFinished
		// survives — it was not part of this interaction.
		m.searching = false
		m.search.Blur()
		return m.setFilter(Filter{HideFinished: m.filter.HideFinished}), nil

	case key.Matches(msg, m.searchKeys.Apply):
		// Commit: the box closes and the list stays narrowed. The query is
		// already applied — it has been narrowing live on every keystroke —
		// so this only hands the keyboard back.
		m.searching = false
		m.search.Blur()
		return m.setFilter(m.filter), nil

	case key.Matches(msg, m.searchKeys.Move):
		return m.moveList(msg), nil
	}
	return m.updateSearch(msg)
}

// moveList steps the list selection from inside the find box, so a query can be
// narrowed and one of its hits picked without leaving the box.
//
// The list's own bindings answer to k and j as well as the arrows, and inside
// the box a letter is text — so movement here is the arrow keys SearchKeyMap
// names, and this only reads the direction off the one that matched.
func (m Model) moveList(msg tea.KeyPressMsg) Model {
	switch msg.String() {
	case "up":
		return m.move(-1)
	case "down":
		return m.move(1)
	case "pgup":
		return m.page(-1)
	default:
		return m.page(1)
	}
}

// updateSearch feeds one message to the find box and re-applies the draft as
// the live query, which is what narrows the list on every keystroke rather than
// only on enter.
func (m Model) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.filter.Query = m.search.Value()
	return m.rebuildRows(), cmd
}

// setFilter applies f and rebuilds, keeping the find box's draft in step with
// the query that is actually in force.
func (m Model) setFilter(f Filter) Model {
	m.filter = f
	m.search.SetValue(f.Query)
	m.search.CursorEnd()
	return m.rebuildRows()
}

// move steps the selection by one selectable row in the given direction,
// skipping group headings. At either end it stays put rather than wrapping:
// wrapping a list this short is disorienting.
func (m Model) move(delta int) Model {
	for i := m.selected + delta; i >= 0 && i < len(m.rows); i += delta {
		if m.rows[i].Selectable() {
			return m.selectRow(i)
		}
	}
	return m
}

// page moves the selection roughly one screenful in the given direction.
func (m Model) page(direction int) Model {
	target := m.selected
	remaining := m.bodyHeight()
	heights := rowHeights(m.rows, m.input.Capabilities)

	for i := m.selected + direction; i >= 0 && i < len(m.rows) && remaining > 0; i += direction {
		remaining -= heights[i]
		if m.rows[i].Selectable() {
			target = i
		}
	}
	return m.selectRow(target)
}

// jump selects the first selectable row scanning from start in the given
// direction, which is how home and end reach the ends of the list without
// landing on a heading.
func (m Model) jump(start, direction int) Model {
	for i := start; i >= 0 && i < len(m.rows); i += direction {
		if m.rows[i].Selectable() {
			return m.selectRow(i)
		}
	}
	return m
}

// selectRow moves the cursor to i and scrolls just enough to keep it visible.
func (m Model) selectRow(i int) Model {
	if i < 0 || i >= len(m.rows) || !m.rows[i].Selectable() {
		return m
	}
	m.selected = i
	m.selectedID = m.rows[i].Ticket.ID
	m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	return m
}

// renderBody draws the list, or the state that stands in for it: a collection
// with no Tickets, or a first fetch that has not landed yet.
func (m Model) renderBody() string {
	height := m.bodyHeight()

	switch {
	case m.hasData && len(m.rows) > 0:
		return renderRows(m.rows, m.selected, m.offset, height, m.width, m.input.Capabilities, m.styles)
	case m.hasData && m.filter.Active() && len(m.input.Tickets) > 0:
		// Distinct from the empty collection below on purpose: "there is
		// nothing here" and "you are hiding everything" look identical on
		// screen, and a user who cannot tell them apart thinks sitrep is
		// broken.
		return pad(m.styles.EmptyFilter.Render(truncateLine(
			"No Tickets match this filter.  Press esc to clear it.", m.width)), height)
	case m.hasData:
		return pad(m.styles.Muted.Render("This collection has no Tickets."), height)
	case m.lastErr != nil:
		// A monitor that exits on one bad DNS lookup is useless on an SSH box:
		// the screen says what went wrong and how to try again, and waits.
		return pad(strings.Join([]string{
			m.styles.Error.Render(truncateLine("Could not read the collection: "+m.lastErr.Error(), m.width)),
			"",
			m.styles.Muted.Render("Press r to try again, q to quit."),
		}, "\n"), height)
	default:
		return pad(m.styles.Muted.Render("Reading…"), height)
	}
}

// footerLines is the always-visible bottom block, line by line: a blank
// spacer, the refresh error when there is one, the filter state when there is
// one, then the help line. The error is a single truncated line, not a modal
// and not a stack trace — the list behind it is still the point.
//
// It is returned as lines rather than a block because the footer's height is
// what the body is measured against, and because the find box needs to know
// which row it was drawn on to put the cursor there.
func (m Model) footerLines() []string {
	lines := []string{""}
	if m.lastErr != nil && m.hasData {
		remaining := m.interval - m.now().Sub(m.lastAttempt)
		lines = append(lines, m.styles.Error.Render(truncateLine(
			fmt.Sprintf("refresh failed: %v%sretrying in %s", m.lastErr, separator, countdown(remaining)), m.width)))
	}
	if filter := m.renderFilterLine(); filter != "" {
		lines = append(lines, filter)
	}
	// The expanded help is several lines, so it is clipped line by line.
	return append(lines, strings.Split(truncateBlock(m.help.View(m.helpKeys()), m.width), "\n")...)
}

// renderFilterLine draws the footer's filter line: the find box while it is
// open, the filter's state while one is on, and nothing at all otherwise — an
// unfiltered screen is exactly the screen it was before this existed.
//
// Whichever it draws, it carries the "X of Y Tickets" count. That count is the
// most important thing on the line: it is how a user reconciles a header that
// says nine with a list showing six, and hidden work that looks like missing
// work is this feature's whole failure mode.
func (m Model) renderFilterLine() string {
	if !m.searching && !m.filter.Active() {
		return ""
	}

	count := fmt.Sprintf("%d of %d Tickets", len(m.visibleTickets()), len(m.input.Tickets))
	if m.searching {
		return pairLine(m.styles.SearchBox.Render(m.search.View()), m.styles.FilterLine.Render(count), m.width)
	}

	right := m.styles.FilterLine.Render("esc clear")
	var parts []string
	if m.filter.HideFinished {
		// The key is labelled "hide finished"; the line spells out what
		// actually left the screen, because Cancelled went with Done.
		parts = append(parts, "done+cancelled hidden")
	}
	if query := m.filter.Query; query != "" {
		// The query is what gets clipped when the line will not fit — the
		// count is what carries the meaning, and the user typed the query and
		// already knows it.
		budget := m.width - lipgloss.Width(right) - len(filterPrefix) - len(count) - 3*len(separator)
		parts = append(parts, strconv.Quote(plain.Truncate(query, max(budget, minQueryWidth))))
	}
	parts = append(parts, count)

	return pairLine(m.styles.FilterLine.Render(filterPrefix+strings.Join(parts, separator)), right, m.width)
}

// filterPrefix labels the footer's filter line.
const filterPrefix = "filter: "

// minQueryWidth is how much of the query survives on a terminal too narrow for
// the whole filter line. Below this the line says nothing useful at all.
const minQueryWidth = 8

// searchPrompt is the find box's prompt, matching the key that opens it.
const searchPrompt = "/"

// searchBoxWidth is how wide the find box may grow: enough to hold a real
// query, never so much that it pushes the hit count off its own line.
func searchBoxWidth(terminalWidth int) int {
	return max(terminalWidth/2, minQueryWidth)
}

// filterLineIndex is which footer row the filter line occupies: after the blank
// spacer and after the refresh error, when there is one.
func (m Model) filterLineIndex() int {
	if m.lastErr != nil && m.hasData {
		return 2
	}
	return 1
}

// helpKeys is the keyboard surface the *list's* footer describes, which is the
// one the keyboard is on whenever that footer is drawn. A footer offering list
// commands while the find box holds the keyboard would be describing a program
// the user is not in.
//
// The Detail screen's footer names m.detailKeys directly rather than going
// through here: the list's body height is measured against this footer, and a
// list whose window silently re-measured itself because another screen is open
// would not come back the way it left.
func (m Model) helpKeys() help.KeyMap {
	if m.searching {
		return m.searchKeys
	}
	return m.keys
}

// staleness is the header's age indicator, read from the injected clock. It is
// the only place in the TUI a clock reaches the screen.
func (m Model) staleness() string {
	return Staleness(m.input.FetchedAt, m.now(), m.refreshing)
}

// bodyHeight is the room left for the list once the header and footer have
// taken theirs, floored at one line so a tiny terminal still renders.
//
// The footer is measured rather than counted: it grows by an error line, and
// the expanded help listing is as tall as its longest column. A constant here
// would be a number to keep in step with a layout, and the frame that overflows
// by one line is the frame that scrolls the alternate screen.
func (m Model) bodyHeight() int {
	return max(m.height-headerHeight-len(m.footerLines()), 1)
}

// headerHeight is what renderHeader always draws: identity, progress, blank.
const headerHeight = 3

// pad grows a block to exactly height lines so the footer stays at the bottom
// of the screen.
func pad(s string, height int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:max(height, 1)], "\n")
}

// rowOf finds the row holding a Ticket by its ID.
func rowOf(rows []Row, id model.TicketID) (int, bool) {
	if id == "" {
		return 0, false
	}
	for i, r := range rows {
		if r.Kind == RowTicket && r.Ticket.ID == id {
			return i, true
		}
	}
	return 0, false
}

// nearestSelectable clamps an index into the row list and snaps it to the
// closest Ticket row, searching forwards first. A rebuild can shrink the list
// under a live cursor, so this runs after every one.
func nearestSelectable(rows []Row, want int) int {
	if len(rows) == 0 {
		return 0
	}
	want = min(max(want, 0), len(rows)-1)
	for i := want; i < len(rows); i++ {
		if rows[i].Selectable() {
			return i
		}
	}
	for i := want; i >= 0; i-- {
		if rows[i].Selectable() {
			return i
		}
	}
	return 0
}

// selectedTicketID reports which Ticket the cursor is on, or "" when it is on
// nothing — a list with no Ticket rows at all is a legitimate state.
func selectedTicketID(rows []Row, selected int) model.TicketID {
	if selected < 0 || selected >= len(rows) || !rows[selected].Selectable() {
		return ""
	}
	return rows[selected].Ticket.ID
}
