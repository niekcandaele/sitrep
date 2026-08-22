package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// mode is which screen owns the terminal.
type mode int

const (
	// modeList is the grouped Ticket list.
	modeList mode = iota
	// modeDetail is one Ticket's Detail.
	//
	// The Detail screen is entered from the list today, and it is deliberately
	// not written that way: it renders a DetailInput seated on the Model, so a
	// future caller — a bare Ticket Ref decoded straight into Detail — can seat
	// one before the first frame instead of teaching this screen a second way to
	// find its data. That is why the breadcrumb is a Header field on DetailInput
	// rather than a read of the list's own Header.
	modeDetail
)

// DetailHeader identifies the Ticket a Detail belongs to: everything the Detail
// screen shows above the fold, carried as data. The Detail screen never reaches
// back into the list for it, so a Ticket opened without a list behind it — a
// bare Ticket Ref decoded straight into this screen — fills the same fields.
type DetailHeader struct {
	// Key is the Ticket's display identity, e.g. "#112".
	Key string
	// Title is the Ticket's one-line summary.
	Title string
	// URL points at the Ticket in its Tracker. May be empty.
	URL string
	// Status is the Ticket's normalized lifecycle bucket.
	Status model.StatusCategory
	// NativeStatus is the Tracker's own label. Display-only.
	NativeStatus string
	// Assignees are the people the Ticket is assigned to.
	Assignees []model.User
	// PullRequests are the pull requests moving the Ticket.
	PullRequests []model.PullRequest
	// Repository is where the Ticket lives, e.g. "acme/widgets".
	Repository string
}

// DetailInput is everything the Detail screen renders.
type DetailInput struct {
	// Ticket identifies what is being read.
	Ticket DetailHeader
	// Parent is the Watchlist this Ticket was reached through, drawn as a
	// breadcrumb above the Ticket's own identity. The zero Header draws no
	// breadcrumb.
	Parent Header
	// Detail is the expensive per-ticket data, exactly as the Provider returned
	// it.
	Detail model.Detail
	// Capabilities decide which sections may be drawn; an undeclared Capability
	// is silently absent, never an error.
	Capabilities model.Capabilities
	// FetchedAt is when this Detail was read, from the caller's clock.
	FetchedAt time.Time
}

// DetailFromTicket adapts one Ticket and the Detail just fetched for it to the
// Detail-view contract. It is the only adapter this ticket needs; a later caller
// that never had a Ticket in a list builds a DetailInput of its own.
func DetailFromTicket(t model.Ticket, d model.Detail, caps model.Capabilities,
	parent Header, fetchedAt time.Time) DetailInput {
	return DetailInput{
		Ticket: DetailHeader{
			Key:          t.Key,
			Title:        t.Title,
			URL:          t.URL,
			Status:       t.Status,
			NativeStatus: t.NativeStatus,
			Assignees:    t.Assignees,
			PullRequests: t.PullRequests,
			Repository:   t.Repository,
		},
		Parent:       parent,
		Detail:       d,
		Capabilities: caps,
		FetchedAt:    fetchedAt,
	}
}

// OpenTicket is the decoder's entry: the Ticket to open, and the Watchlist it
// was reached through. A zero Parent means no breadcrumb and no walk-up — a
// Ticket with no parent opens in Detail like any other.
type OpenTicket struct {
	// Ticket is the Ticket being decoded, in list-model form.
	Ticket model.Ticket
	// Parent is the Watchlist drawn as the breadcrumb above it.
	Parent Header
	// Capabilities decide which Detail sections may be drawn.
	Capabilities model.Capabilities
}

// DetailSource fetches one Ticket's Detail. It is the seam between the Detail
// screen and the Provider, held as a plain function so the screen knows nothing
// about auth, GraphQL or Refs. One call is exactly one FetchDetail and
// never a Resolve (ADR-0003).
type DetailSource func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error)

// TicketDetailSource returns a DetailSource reading from p.
func TicketDetailSource(p provider.Provider) DetailSource {
	return func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		d, err := p.FetchDetail(ctx, id)
		if err != nil {
			return model.Detail{}, model.Capabilities{}, err
		}
		return d, p.Capabilities(), nil
	}
}

// detailEntry is one Ticket's cached Detail, stamped with when it was read. The
// stamp is what makes a cached reading say "read 4m ago" rather than looking as
// fresh as the list beside it.
type detailEntry struct {
	detail    model.Detail
	caps      model.Capabilities
	fetchedAt time.Time
}

// detailState is the Detail screen's own state, kept in one struct so that it
// can be seated whole — the list's state is never consulted to build it.
type detailState struct {
	// ticket is the Ticket being read. It is a copy, and it is the only place
	// the Detail screen's identity comes from once the screen is open, so a
	// refreshed list dropping the Ticket cannot blank the screen.
	ticket model.Ticket
	// input is what the screen renders. Its header is filled the moment Detail
	// opens; its Detail arrives later.
	input DetailInput
	// loaded reports whether a Detail has actually landed. Before it has, input
	// carries a header and nothing else.
	loaded bool
	// loading reports whether a fetch is in flight.
	loading bool
	// lastErr is the last failed read. With a Detail already loaded it is a
	// footer line; without one it is the body.
	lastErr error
	// offset is the first body line on screen.
	offset int
}

// detailFetchedMsg carries the outcome of one DetailSource call. It is guarded
// twice — by generation and by TicketID — because a Detail read is started by a
// keystroke rather than a timer: open #112, esc, open #113, and #112's slow
// answer would otherwise paint over #113.
type detailFetchedMsg struct {
	generation int
	id         model.TicketID
	detail     model.Detail
	caps       model.Capabilities
	err        error
}

// detailStaleness renders how old the Detail on screen is: "read 4s ago". It is
// worded differently from the list's indicator on purpose — the two ages are
// different readings, taken at different moments, and a cached Detail beside a
// freshly refreshed list must not look as new as the list does.
func detailStaleness(fetchedAt, now time.Time, loading bool) string {
	switch {
	case loading:
		return "reading…"
	case fetchedAt.IsZero():
		return "never read"
	}
	if age := now.Sub(fetchedAt); age >= time.Second {
		return "read " + humanAge(age) + " ago"
	}
	return "read just now"
}

// selectedTicket returns the Ticket the cursor is on. A list with no selectable
// row — an empty Watchlist, or a filter matching nothing — has none, and that
// is an ordinary state rather than an error.
func (m Model) selectedTicket() (model.Ticket, bool) {
	if m.selected < 0 || m.selected >= len(m.rows) || !m.rows[m.selected].Selectable() {
		return model.Ticket{}, false
	}
	return m.rows[m.selected].Ticket, true
}

// openDetail switches to the Detail screen for the selected Ticket, fetching its
// Detail at that moment and never before (ADR-0003).
//
// Nothing above m.mode is touched here: the selection, the scroll offset and the
// session's Filter are simply left alone, which is why esc puts the screen back
// exactly as it was without any code putting it there.
func (m Model) openDetail() (tea.Model, tea.Cmd) {
	t, ok := m.selectedTicket()
	if !ok {
		return m, nil
	}

	m = m.clearPendingClick()
	m.mode = modeDetail
	m.detail = detailState{ticket: t}

	// A Ticket read once this session opens instantly and costs the Tracker
	// nothing. Its own "read Ns ago" stamp keeps the reading honest rather than
	// letting a cached Detail look as fresh as the list beside it.
	if cached, hit := m.details[t.ID]; hit {
		m.detail.input = DetailFromTicket(t, cached.detail, cached.caps, m.input.Header, cached.fetchedAt)
		m.detail.loaded = true
		return m.syncDetailKeys(), nil
	}

	// The header is drawn from the Ticket the list already had, so the loading
	// screen can say which Ticket it is loading.
	m.detail.input = DetailFromTicket(t, model.Detail{}, m.input.Capabilities, m.input.Header, time.Time{})
	next, cmd := m.syncDetailKeys().startDetailFetch()
	return next, cmd
}

// syncDetailKeys keeps the Detail keyboard in step with what is actually behind
// this screen. It is one place with one rule, so the help line and the matcher
// can never disagree: u is offered when there is a Watchlist to walk up into
// and the caller named it, and esc says "back" only when there is a list to go
// back to.
func (m Model) syncDetailKeys() Model {
	m.detailKeys.Parent.SetEnabled(m.hasSource && m.detail.input.Parent != (Header{}))
	if m.listArmed {
		m.detailKeys.Back.SetHelp("esc", "back")
	} else {
		m.detailKeys.Back.SetHelp("esc", "quit")
	}
	return m
}

// walkUp leaves the Detail for the Watchlist the Ticket belongs to. In a
// decoder session it is where the list is first fetched — nothing has read it
// until now — and from a drill-in it is esc by another name, because both mean
// "the Watchlist this Ticket belongs to".
func (m Model) walkUp() (tea.Model, tea.Cmd) {
	m = m.clearPendingClick()
	m.mode = modeList
	m.detail = detailState{}
	m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())

	if m.listArmed {
		return m, nil
	}
	next, cmd := m.startRefresh()
	return next, cmd
}

// startDetailFetch begins one read of the Ticket on screen, or does nothing when
// one is already in flight.
func (m Model) startDetailFetch() (Model, tea.Cmd) {
	if m.detail.loading || m.detail.ticket.ID == "" {
		return m, nil
	}
	m.detailGeneration++
	m.detail.loading = true
	return m, m.detailFetchCmd(m.detailGeneration, m.detail.ticket.ID)
}

// detailFetchCmd reads the DetailSource on Bubble Tea's goroutine pool, tagging
// the answer with the generation and the Ticket that asked for it.
func (m Model) detailFetchCmd(generation int, id model.TicketID) tea.Cmd {
	fetch := m.fetchDetail
	return func() tea.Msg {
		d, caps, err := fetch(id)
		return detailFetchedMsg{generation: generation, id: id, detail: d, caps: caps, err: err}
	}
}

// onDetailFetched folds one Detail reading in, dropping any answer the screen is
// no longer waiting for.
func (m Model) onDetailFetched(msg detailFetchedMsg) Model {
	if msg.generation != m.detailGeneration || msg.id != m.detail.ticket.ID {
		// Open #112, esc, open #113: #112's slow answer must not paint over
		// #113, and must not clear the in-flight flag of the read that is.
		return m
	}
	m.detail.loading = false

	if msg.err != nil {
		// A cached Detail stays on screen behind the error: stale detail beats a
		// blank screen.
		m.detail.lastErr = msg.err
		return m
	}
	m.detail.lastErr = nil

	at := m.now()
	m.details[msg.id] = detailEntry{detail: msg.detail, caps: msg.caps, fetchedAt: at}
	m.detail.input.Detail = msg.detail
	m.detail.input.Capabilities = msg.caps
	m.detail.input.FetchedAt = at
	m.detail.loaded = true
	m.detail.offset = m.clampDetail(m.detail.offset)
	return m
}

// onDetailKey dispatches a key press while a Ticket's Detail is open.
//
// esc goes back rather than quitting: after the find box and the filter, "one
// level up" is what esc means everywhere in this program. q and ctrl+c quit
// outright, from here as from the list, so nobody is trapped in a Detail.
//
// u goes up to the Watchlist this Ticket belongs to. From a drill-in that is
// where esc already lands, which is coherent rather than surprising; from a
// decoded Ticket it is the way into the full monitor.
func (m Model) onDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.detailKeys.Quit):
		return m.quit(msg), tea.Quit

	case key.Matches(msg, m.detailKeys.Parent):
		return m.walkUp()

	case key.Matches(msg, m.detailKeys.Back):
		if !m.listArmed {
			// The esc ladder's last rung: a decoded Ticket has no list behind it,
			// so "one level up" is out of the program. q and ctrl+c still quit
			// from everywhere, so nobody is trapped either way.
			return m.quit(msg), tea.Quit
		}
		m = m.clearPendingClick()
		m.mode = modeList
		m.detail = detailState{}
		// The list's own state was never touched, but the help listing may have
		// been expanded while Detail was open, which changes how much room the
		// list has. Re-measuring is idempotent when nothing moved.
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		return m, nil

	case key.Matches(msg, m.detailKeys.Refresh):
		// Only this Ticket, and only its Detail: r here never touches the list.
		next, cmd := m.startDetailFetch()
		return next, cmd

	case key.Matches(msg, m.detailKeys.ToggleMouse):
		return m.toggleMouse(), nil

	case key.Matches(msg, m.detailKeys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.detail.offset = m.clampDetail(m.detail.offset)
		return m, nil

	case key.Matches(msg, m.detailKeys.Up):
		return m.scrollDetail(-1), nil
	case key.Matches(msg, m.detailKeys.Down):
		return m.scrollDetail(1), nil
	case key.Matches(msg, m.detailKeys.PageUp):
		return m.scrollDetail(-m.detailBodyHeight()), nil
	case key.Matches(msg, m.detailKeys.PageDown):
		return m.scrollDetail(m.detailBodyHeight()), nil
	case key.Matches(msg, m.detailKeys.Home):
		return m.scrollDetailTo(0), nil
	case key.Matches(msg, m.detailKeys.End):
		return m.scrollDetailTo(len(m.detailBodyLines())), nil
	}
	return m, nil
}

// scrollDetail moves the window by delta lines, staying inside the document.
func (m Model) scrollDetail(delta int) Model { return m.scrollDetailTo(m.detail.offset + delta) }

// scrollDetailTo puts the window at offset, clamped into the document.
func (m Model) scrollDetailTo(offset int) Model {
	m.detail.offset = m.clampDetail(offset)
	return m
}

// clampDetail keeps a scroll offset inside the Detail currently on screen.
func (m Model) clampDetail(offset int) int {
	return clampDetailOffset(offset, len(m.detailBodyLines()), m.detailBodyHeight())
}

// detailBodyLines is the whole Detail document as terminal lines: the Detail
// itself once it has landed, and the state that stands in for it until then.
//
// The header is drawn either way, so every one of these bodies appears beneath a
// screen that says which Ticket it is about.
func (m Model) detailBodyLines() []string {
	switch {
	case m.detail.loaded:
		return detailLines(m.detail.input, m.width, m.styles)
	case m.detail.lastErr != nil:
		return []string{
			m.styles.Error.Render(truncateLine(
				"Could not read this Ticket's detail: "+m.detail.lastErr.Error(), m.width)),
			"",
			m.styles.Muted.Render("Press r to try again, esc to go back."),
		}
	default:
		return []string{m.styles.Muted.Render("Reading Ticket detail…")}
	}
}

// detailFrame renders the whole Detail screen: a header that never scrolls, the
// windowed body, and a footer carrying the help line, the scroll position and
// any failed re-read.
func (m Model) detailFrame() string {
	footer := m.detailFooterLines()
	header := renderDetailHeader(m.detail.input,
		detailStaleness(m.detail.input.FetchedAt, m.now(), m.detail.loading), m.width, m.styles)
	body := renderDetailBody(m.detailBodyLines(), m.detail.offset, m.detailBodyHeight(), m.width)

	return strings.Join(append([]string{header, body}, footer...), "\n")
}

// detailFooterLines is the Detail screen's bottom block, line by line: a blank
// spacer, the failed-re-read line when there is one, then the help line with the
// scroll position against the right-hand edge.
func (m Model) detailFooterLines() []string {
	lines := []string{""}
	// A read that failed with a Detail already on screen is a footer line, not a
	// body: what the reader has is still worth reading.
	if m.detail.lastErr != nil && m.detail.loaded {
		lines = append(lines, m.styles.Error.Render(truncateLine(
			"could not re-read this Ticket's detail: "+m.detail.lastErr.Error(), m.width)))
	}

	help := strings.Split(m.help.View(m.detailHelpKeys()), "\n")
	// The indicator is drawn only when there is somewhere to scroll: a Detail
	// that fits on screen has no position to report.
	if pos := scrollIndicator(m.detail.offset, len(m.detailBodyLines()), m.detailBodyHeight()); pos != "" {
		last := len(help) - 1
		help[last] = pairLine(help[last], m.styles.Staleness.Render(pos), m.width)
	}
	for _, line := range help {
		lines = append(lines, truncateLine(line, m.width))
	}
	return lines
}

// detailBodyHeight is the room left for the Detail body once the header and the
// footer have taken theirs, floored at one line so a tiny terminal still renders.
//
// The footer's *height* does not depend on the scroll indicator, which is what
// keeps this out of a loop with detailFooterLines: the indicator only decides
// what the last help line says, never how many lines there are.
func (m Model) detailBodyHeight() int {
	lines := 1
	if m.detail.lastErr != nil && m.detail.loaded {
		lines++
	}
	lines += len(strings.Split(m.help.View(m.detailHelpKeys()), "\n"))
	return max(m.height-detailHeaderHeight-lines, 1)
}
