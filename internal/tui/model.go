package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
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

	width, height int
	ready         bool

	keys     KeyMap
	help     help.Model
	styles   Styles
	quitting bool
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

	return Model{
		fetch:    func() (ListInput, error) { return src(ctx) },
		now:      now,
		interval: opts.Interval,
		// The first refresh is already on its way out of Init.
		generation:  1,
		refreshing:  true,
		lastAttempt: now(),
		keys:        DefaultKeyMap(),
		help:        help.New(),
		styles:      DefaultStyles(true),
	}
}

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
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		return m, nil

	case tea.BackgroundColorMsg:
		m.styles = DefaultStyles(msg.IsDark())
		return m, nil

	case heartbeatMsg:
		return m.onHeartbeat()

	case refreshedMsg:
		return m.onRefreshed(msg), nil

	case tea.KeyPressMsg:
		return m.onKey(msg)
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

	header := renderHeader(m.input, m.staleness(), m.hasData, m.width, m.styles)
	footer := m.renderFooter()
	v.SetContent(strings.Join([]string{header, m.renderBody(), footer}, "\n"))
	return v
}

// visibleTickets returns the Tickets the list is currently showing. Today that
// is all of them; a hide-done toggle or a fuzzy find replaces the body of this
// method and nothing else — which is why the header's progress is computed
// from m.input.Tickets instead, and cannot be moved by a filter.
func (m Model) visibleTickets() []model.Ticket { return m.input.Tickets }

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
func (m Model) rebuildRows() Model {
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

// onKey dispatches a key press.
func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
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
		// Reserved for the Ticket Detail drill-in: the binding is declared and
		// shown in help so the key is not claimed by anything else, and does
		// nothing until Detail exists.
		return m, nil

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

// renderFooter draws the always-visible bottom block: the refresh error when
// there is one, then the help line. The error is a single truncated line, not
// a modal and not a stack trace — the list behind it is still the point.
func (m Model) renderFooter() string {
	lines := []string{""}
	if m.lastErr != nil && m.hasData {
		remaining := m.interval - m.now().Sub(m.lastAttempt)
		lines = append(lines, m.styles.Error.Render(truncateLine(
			fmt.Sprintf("refresh failed: %v%sretrying in %s", m.lastErr, separator, countdown(remaining)), m.width)))
	}
	// The expanded help is several lines, so it is clipped line by line.
	return strings.Join(append(lines, truncateBlock(m.help.View(m.keys), m.width)), "\n")
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
	return max(m.height-headerHeight-lipgloss.Height(m.renderFooter()), 1)
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
