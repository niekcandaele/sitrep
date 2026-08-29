package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	// List drill-in and direct Ticket-Ref decoding both seat a DetailInput before
	// rendering. That is why the breadcrumb is carried on DetailInput rather than
	// read from list state.
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

// DetailInput contains the current Detail seat's Provider-backed rendering data.
type DetailInput struct {
	// Ticket identifies what is being read.
	Ticket DetailHeader
	// Parent is the root Watchlist crumb shown before any Trail entries. A zero
	// Header omits that root crumb; prior Trail Tickets may still be shown.
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

// DetailFromTicket adapts a Ticket and its Detail to the Detail-view contract.
// Rich list Tickets and deliberately thin Link targets both enter through this
// boundary, so the Detail screen never enriches a seat from list state.
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
	// linkFocus is the stable identity of the focused relationship. The separate
	// bit distinguishes an absent focus from a valid zero-value identity.
	linkFocus        detailLinkIdentity
	hasLinkFocus     bool
	markdownSections detailMarkdownSections
}

// detailTrailEntry is a stable snapshot of one prior Detail seat. In-flight
// state and generations are intentionally absent: navigation invalidates that
// work, and popping must never restore a reading state whose command was lost.
// Width and body height distinguish an exact round trip from a seat reflowed
// while a child was open.
type detailTrailEntry struct {
	ticket       model.Ticket
	input        DetailInput
	loaded       bool
	lastErr      error
	offset       int
	linkFocus    detailLinkIdentity
	hasLinkFocus bool
	width        int
	bodyHeight   int
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

// openDetail switches to the Detail screen for the selected list Ticket. A
// successful session-cache entry seats immediately; only a miss fetches Detail
// (ADR-0003). The transition leaves hidden list state untouched; root esc
// reconciles it before drawing the list again.
func (m Model) openDetail() (tea.Model, tea.Cmd) {
	t, ok := m.selectedTicket()
	if !ok {
		return m, nil
	}

	m = m.clearPendingClick()
	m.trail = nil
	return m.seatDetail(t, m.input.Header, m.input.Capabilities)
}

// seatDetail is the shared transition for a rich list Ticket and a thin Link
// target. Every seat advances the generation, including cache hits and cycles.
func (m Model) seatDetail(t model.Ticket, parent Header, caps model.Capabilities) (tea.Model, tea.Cmd) {
	m.detailGeneration++
	m.mouseEpoch++
	m.mode = modeDetail
	m.detail = detailState{
		ticket: t,
		input:  DetailFromTicket(t, model.Detail{}, caps, parent, time.Time{}),
	}

	if t.ID == "" {
		m.detail.lastErr = errEmptyLinkTargetID
		return m.reconcileDetail(false), nil
	}
	if cached, hit := m.details[t.ID]; hit {
		m.detail.input = DetailFromTicket(t, cached.detail, cached.caps, parent, cached.fetchedAt)
		m.detail.loaded = true
		return m.reconcileDetail(true), nil
	}

	m.detail.loading = true
	m = m.reconcileDetail(false)
	return m, m.detailFetchCmd(m.detailGeneration, t.ID)
}

// ticketFromLinkTarget is deliberately thin. A Link target never borrows rich
// fields from a visible or hidden list row, even when the same ID is present.
func ticketFromLinkTarget(target model.LinkTarget) model.Ticket {
	return model.Ticket{
		ID:           target.ID,
		Key:          target.Key,
		Title:        sanitizeTerminalText(target.Title),
		URL:          target.URL,
		Status:       target.Status,
		NativeStatus: sanitizeTerminalText(target.NativeStatus),
	}
}

var errEmptyLinkTargetID = errors.New("this Link target has no Ticket identity")

// syncDetailKeys keeps Detail matching and help in step with the rendered
// document. Relationship focus is re-resolved by its composite identity, never
// by a stale row or target ID.
func (m Model) syncDetailKeys() Model {
	return m.syncDetailKeysFor(m.detailDocument())
}

func (m Model) detailBackDescription() string {
	if len(m.trail) > 0 || m.listArmed {
		return "back"
	}
	return "quit"
}

func (m Model) syncDetailKeysFor(doc detailDocument) Model {
	m.detailKeys.Parent.SetEnabled(m.hasSource && m.detail.input.Parent != (Header{}))
	m.detailKeys.Back.SetHelp("esc", m.detailBackDescription())

	hasLinks := len(doc.LinkRows) > 0
	m.detailKeys.NextLink.SetEnabled(hasLinks)
	m.detailKeys.PreviousLink.SetEnabled(hasLinks)
	m.detailKeys.MouseFollow.SetEnabled(m.mouseEnabled && hasLinks)
	_, focused := detailLinkRowByIdentity(doc, m.detail.linkFocus)
	focused = focused && m.detail.hasLinkFocus
	if !focused {
		m.detail.linkFocus = detailLinkIdentity{}
		m.detail.hasLinkFocus = false
	}
	m.detailKeys.Follow.SetEnabled(focused)
	return m
}

// reconcileDetail synchronizes dynamic keys before calculating the footer's
// height, then clamps the body window. Callers that changed the document's
// geometry may ask to bring a retained focus minimally back into view.
func (m Model) reconcileDetail(ensureFocus bool) Model {
	m = m.ensureDetailMarkdownSections()
	doc := m.detailDocument()
	m = m.syncDetailKeysFor(doc)
	m.detail.offset = clampDetailOffset(m.detail.offset, len(doc.Lines), m.detailBodyHeight())
	if ensureFocus && m.detail.hasLinkFocus {
		if row, ok := detailLinkRowByIdentity(doc, m.detail.linkFocus); ok {
			m.detail.offset = ensureDocumentLineVisible(row.Line, m.detail.offset, len(doc.Lines), m.detailBodyHeight())
		}
	}
	return m
}

func (m Model) ensureDetailMarkdownSections() Model {
	if !m.detail.loaded {
		return m
	}
	markdown := m.effectiveMarkdownRenderers()
	if !m.detail.markdownSections.matches(m.detail.input, m.width, markdown) {
		m.detail.markdownSections = renderDetailMarkdownSections(m.detail.input, m.width, m.styles, markdown)
	}
	return m
}

func (m Model) invalidateDetailMarkdownSections() Model {
	m.detail.markdownSections.valid = false
	return m
}

func (m Model) moveDetailLinkFocus(direction int) Model {
	doc := m.detailDocument()
	if len(doc.LinkRows) == 0 {
		return m.syncDetailKeysFor(doc)
	}

	index := -1
	if m.detail.hasLinkFocus {
		for i, row := range doc.LinkRows {
			if row.Identity == m.detail.linkFocus {
				index = i
				break
			}
		}
	}
	if index < 0 {
		if direction < 0 {
			index = len(doc.LinkRows) - 1
		} else {
			index = 0
		}
	} else {
		index = (index + direction + len(doc.LinkRows)) % len(doc.LinkRows)
	}

	m.detail.linkFocus = doc.LinkRows[index].Identity
	m.detail.hasLinkFocus = true
	m = m.syncDetailKeysFor(doc)
	m.detail.offset = ensureDocumentLineVisible(doc.LinkRows[index].Line, m.detail.offset, len(doc.Lines), m.detailBodyHeight())
	return m
}

func (m Model) focusedDetailLink(doc detailDocument) (detailLinkRow, bool) {
	if !m.detail.hasLinkFocus {
		return detailLinkRow{}, false
	}
	return detailLinkRowByIdentity(doc, m.detail.linkFocus)
}

func (m Model) detailTrailSnapshot() detailTrailEntry {
	return detailTrailEntry{
		ticket:       m.detail.ticket,
		input:        m.detail.input,
		loaded:       m.detail.loaded,
		lastErr:      m.detail.lastErr,
		offset:       m.detail.offset,
		linkFocus:    m.detail.linkFocus,
		hasLinkFocus: m.detail.hasLinkFocus,
		width:        m.width,
		bodyHeight:   m.detailBodyHeight(),
	}
}

// followDetailLink re-resolves identity against the current document, archives
// the stable source seat, then uses the same cache/fetch transition as list-open.
func (m Model) followDetailLink(identity detailLinkIdentity) (tea.Model, tea.Cmd) {
	doc := m.detailDocument()
	row, ok := detailLinkRowByIdentity(doc, identity)
	if !ok {
		return m.syncDetailKeysFor(doc), nil
	}

	m.detail.linkFocus = identity
	m.detail.hasLinkFocus = true
	m = m.syncDetailKeysFor(doc)
	m.trail = append(m.trail, m.detailTrailSnapshot())
	return m.seatDetail(ticketFromLinkTarget(row.Link.Target), m.detail.input.Parent, m.detail.input.Capabilities)
}

func (m Model) followFocusedDetailLink() (tea.Model, tea.Cmd) {
	doc := m.detailDocument()
	row, ok := m.focusedDetailLink(doc)
	if !ok {
		return m.syncDetailKeysFor(doc), nil
	}
	return m.followDetailLink(row.Identity)
}

func (m Model) popDetailTrail() Model {
	entry := m.trail[len(m.trail)-1]
	m.trail = m.trail[:len(m.trail)-1]
	m.detailGeneration++
	m.mouseEpoch++
	m.mode = modeDetail
	m.detail = detailState{
		ticket:       entry.ticket,
		input:        entry.input,
		loaded:       entry.loaded,
		lastErr:      entry.lastErr,
		offset:       entry.offset,
		linkFocus:    entry.linkFocus,
		hasLinkFocus: entry.hasLinkFocus,
	}
	m = m.reconcileDetail(false)
	if m.width != entry.width || m.detailBodyHeight() != entry.bodyHeight {
		return m.reconcileDetail(true)
	}
	return m
}

// walkUp leaves the entire Detail Trail for its root Watchlist. It returns to an
// armed list immediately or fetches the list for the first time in a decoder
// session.
func (m Model) walkUp() (tea.Model, tea.Cmd) {
	m = m.clearPendingClick()
	m.detailGeneration++
	m.mouseEpoch++
	m.trail = nil
	m.mode = modeList
	m.detail = detailState{}
	m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())

	if m.listArmed {
		return m, nil
	}
	next, cmd := m.startRefresh()
	return next, cmd
}

// startDetailFetch begins one explicit re-read. Even a malformed empty ID
// advances the generation and repeats its local failure without issuing a
// Provider command, so an abandoned command can never land merely because the
// same Ticket is seated again later.
func (m Model) startDetailFetch() (Model, tea.Cmd) {
	if m.detail.loading {
		return m, nil
	}
	m.detailGeneration++
	if m.detail.ticket.ID == "" {
		m.detail.loading = false
		m.detail.lastErr = errEmptyLinkTargetID
		return m.reconcileDetail(false), nil
	}
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
		return m.reconcileDetail(false)
	}
	m.detail.lastErr = nil
	m = m.invalidateDetailMarkdownSections()

	at := m.now()
	m.details[msg.id] = detailEntry{detail: msg.detail, caps: msg.caps, fetchedAt: at}
	m.detail.input.Detail = msg.detail
	m.detail.input.Capabilities = msg.caps
	m.detail.input.FetchedAt = at
	m.detail.loaded = true
	return m.reconcileDetail(true)
}

// onDetailKey dispatches a key press while a Ticket's Detail is open. Esc pops
// one Trail entry, returns an untrailed root to the armed list, or quits a
// decoded root with no list. q and ctrl+c always quit. u clears the Trail and
// jumps to the root Watchlist, fetching it first in a decoder session.
func (m Model) onDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.detailKeys.Quit):
		return m.quit(msg), tea.Quit

	case key.Matches(msg, m.detailKeys.NextLink):
		return m.moveDetailLinkFocus(1), nil

	case key.Matches(msg, m.detailKeys.PreviousLink):
		return m.moveDetailLinkFocus(-1), nil

	case key.Matches(msg, m.detailKeys.Follow):
		return m.followFocusedDetailLink()

	case key.Matches(msg, m.detailKeys.Parent):
		return m.walkUp()

	case key.Matches(msg, m.detailKeys.Back):
		if len(m.trail) > 0 {
			return m.popDetailTrail(), nil
		}
		m.detailGeneration++
		m.mouseEpoch++
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
		return m.reconcileDetail(true), nil

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
		return m.scrollDetailTo(len(m.detailDocument().Lines)), nil
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
	return clampDetailOffset(offset, len(m.detailDocument().Lines), m.detailBodyHeight())
}

// detailDocument is the complete Detail body and its explicit Link metadata for
// the model's current state. Loading and first-read failures are documents too,
// but deliberately have no Link rows.
func (m Model) detailDocument() detailDocument {
	switch {
	case m.detail.loaded:
		markdown := m.effectiveMarkdownRenderers()
		sections := m.detail.markdownSections
		if !sections.matches(m.detail.input, m.width, markdown) {
			// Hand-built loaded models used by embedding callers and focused tests may
			// not have passed through a production reconciliation yet.
			sections = renderDetailMarkdownSections(m.detail.input, m.width, m.styles, markdown)
		}
		return composeDetailDocumentWithSections(m.detail.input, m.width, m.styles, m.detail.linkFocus,
			m.detail.hasLinkFocus, sections)
	case m.detail.lastErr != nil:
		return initialDetailErrorDocument(m.detail.lastErr, m.width, m.styles, m.detailBackDescription())
	default:
		return detailDocument{Lines: truncateDocumentLines(
			[]string{m.styles.Muted.Render("Reading Ticket detail…")}, m.width)}
	}
}

func initialDetailErrorDocument(err error, width int, styles Styles, backDescription string) detailDocument {
	message := "Could not read this Ticket's detail: " + err.Error()
	messageLines := wrapText(message, width)
	if len(messageLines) == 0 {
		messageLines = []string{""}
	}
	lines := make([]string, 0, len(messageLines)+3)
	for _, line := range messageLines {
		lines = append(lines, styles.Error.Render(line))
	}
	lines = append(lines, "")

	exitAction := backDescription
	if exitAction == "back" {
		exitAction = "go back"
	}
	guidance := "Press r to try again, esc to " + exitAction + "."
	for _, line := range wrapText(guidance, width) {
		lines = append(lines, styles.Muted.Render(line))
	}
	return detailDocument{Lines: truncateDocumentLines(lines, width)}
}

// detailBodyLines remains a convenient read-only seam for focused tests.
func (m Model) detailBodyLines() []string { return m.detailDocument().Lines }

// detailFrame renders the whole Detail screen: a header that never scrolls, the
// windowed body, and a footer carrying the help line, the scroll position and
// any failed re-read.
func (m Model) detailFrame(doc detailDocument) string {
	footer := m.detailFooterLines(doc)
	header := renderDetailHeader(m.detail.input,
		"current · "+detailStaleness(m.detail.input.FetchedAt, m.now(), m.detail.loading),
		m.width, m.styles, m.trail)
	body := renderDetailBody(doc.Lines, m.detail.offset, m.detailBodyHeight(), m.width)

	return strings.Join(append([]string{header, body}, footer...), "\n")
}

// detailFooterLines is the Detail screen's bottom block, line by line: a
// spacer, the failed-re-read line when there is one, then the help. The scroll
// position uses the last help line when it fits and otherwise the spacer, so it
// never clips a priority action.
func (m Model) detailFooterLines(doc detailDocument) []string {
	lines := []string{""}
	// A read that failed with a Detail already on screen is a footer line, not a
	// body: what the reader has is still worth reading.
	if m.detail.lastErr != nil && m.detail.loaded {
		lines = append(lines, m.styles.Error.Render(truncateLine(
			"could not re-read this Ticket's detail: "+m.detail.lastErr.Error(), m.width)))
	}

	help := strings.Split(m.detailHelpView(), "\n")
	// The indicator is drawn only when there is somewhere to scroll: a Detail
	// that fits on screen has no position to report.
	if pos := scrollIndicator(m.detail.offset, len(doc.Lines), m.detailBodyHeight()); pos != "" {
		last := len(help) - 1
		position := m.styles.Staleness.Render(pos)
		if lipgloss.Width(help[last])+1+lipgloss.Width(position) <= m.width {
			help[last] = pairLineReserved(help[last], position, m.width)
		} else {
			lines[0] = pairLineReserved(lines[0], position, m.width)
		}
	}
	for _, line := range help {
		lines = append(lines, truncateLine(line, m.width))
	}
	return lines
}

// detailBodyHeight is the room left for the Detail body once the header and the
// footer have taken theirs, floored at one line so a tiny terminal still renders.
//
// The footer's *height* does not depend on the scroll indicator, which keeps
// this out of a loop with detailFooterLines: the indicator reuses either the
// spacer or a help line and never adds a line.
func (m Model) detailBodyHeight() int {
	lines := 1
	if m.detail.lastErr != nil && m.detail.loaded {
		lines++
	}
	lines += len(strings.Split(m.detailHelpView(), "\n"))
	return max(m.height-detailHeaderHeight-lines, 1)
}
