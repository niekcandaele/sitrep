package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// Model is the monitor's state: the last good reading of the Watchlist, the
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
	// rateHold blocks refreshes after a current rate-limit refusal or an exhausted
	// successful budget. An empty reset is the deliberate manual-only hold for a
	// refusal whose headers supplied no usable deadline.
	rateHold rateHold
	// lowBudget widens only automatic cadence after a successful low-budget read.
	lowBudget lowBudgetSchedule
	// heartbeatCmd is injectable so scheduler tests advance only their controlled
	// clock and deliver heartbeatMsg themselves.
	heartbeatCmd tea.Cmd
	// terminalFocused is optimistic for terminals that do not emit focus reports.
	// It is session-local because focus is a property of this terminal session,
	// not of the Watchlist or its refresh policy.
	terminalFocused bool
	// listArmed reports whether the list holds a reading or has a fetch in
	// flight. It starts false in decoder mode — a session opened straight on one
	// Ticket's Detail — so the heartbeat never fetches a Watchlist the user has
	// not asked to see, and it is what the walk-up key turns on.
	listArmed bool
	// hasSource reports whether there is a Watchlist to monitor at all. A
	// decoded Ticket with no parent has none, and the walk-up key is then not
	// offered rather than offered and broken.
	hasSource bool

	rows       []Row
	selected   int
	selectedID model.TicketID
	offset     int

	// filter is the session's view filter. It is applied on the way to the
	// screen: it is a keystroke, not a setting, so nothing here is persisted.
	filter Filter
	// searching is true while the find box owns the keyboard.
	searching bool
	// legendVisible is session-local display state. It deliberately has no
	// relationship to Help and never participates in primary layout.
	legendVisible bool
	// search holds the draft query; filter.Query holds what the list is
	// actually narrowed by. They are kept in step on every keystroke, and are
	// separate so that esc can drop both in one move.
	search textinput.Model

	// mode is which screen owns the terminal. Detail Trail navigation leaves the
	// list state above untouched. Returning from a Detail root to the list may
	// reconcile its selection and scroll geometry before drawing.
	//
	// Decoder startup can seat modeDetail before the first frame, with no list
	// behind it. detailState therefore carries the Detail's own rendering context,
	// including its breadcrumb, rather than reading list state.
	mode   mode
	detail detailState
	// trail is the ordered path of prior Detail seats followed through explicit
	// Links. It is session-local and deliberately permits cycles and repeats.
	trail []detailTrailEntry
	// details caches the Details read this session, per Ticket. It holds Detail
	// and nothing else: no list data migrates in here, and nothing here migrates
	// onto a Ticket (ADR-0003). The cache is per-session and never persisted.
	details          map[model.TicketID]detailEntry
	detailGeneration int
	// detailEvidenceVersion orders accepted singular Detail reads against Frontier
	// fan-out commands, which capture its value when issued.
	detailEvidenceVersion int
	// frontier is the Frontier screen's own state. Like detailState it is seated
	// whole; the list's state is never consulted to draw it.
	frontier frontierState
	// frontierGeneration rejects answers from a Frontier seat that has been
	// left or replaced. Its generation-scoped context separately cancels the
	// Provider work; the generation remains the correctness guard when a
	// Provider races with or ignores cancellation.
	frontierGeneration int
	// layoutFrontierFn is an instance-owned test seam. Its nil fallback keeps
	// model-literal fixtures equivalent to production Models.
	layoutFrontierFn func(model.BlockingGraph, []frontierNode, frontierLayoutOptions) frontierLayout
	// frontierRebuildVersion identifies the newest evidence or width request.
	// A pending target is serviced by at most one physical Bubble Tea tick.
	frontierRebuildVersion           int
	frontierRebuildPendingVersion    int
	frontierRebuildPendingGeneration int
	frontierRebuildTimerID           int
	frontierRebuildNextTimerID       int
	// detailFanoutInflight counts issued bulk reads across Frontier generations.
	// Cancellation does not release a slot until the command returns, so rapid
	// leave/re-entry cycles can never exceed detailfanout.Parallelism.
	detailFanoutInflight int
	// detailReturn is the mode a root Detail seat returns to on esc. The
	// Frontier is a second rendering of the list, not a Trail entry, so a Ticket
	// opened from it goes back to it.
	detailReturn mode
	// mouseEpoch identifies the current capture lifetime and Detail seat for queued
	// mouse callbacks. Rereads leave it stable so callbacks can re-resolve facts
	// from the same seat; navigation and mouse toggles advance it so an older frame
	// cannot act after capture changes or after leaving and returning to the same ID.
	mouseEpoch int

	fetchDetail DetailSource
	// newReadContext derives one independently cancellable Detail or Frontier
	// generation from the monitor session. The cancel functions live on Model,
	// never in the render seats restored by Detail Trail or Frontier reseats.
	newReadContext  func() (context.Context, context.CancelFunc)
	cancelSession   context.CancelFunc
	detailContext   context.Context
	cancelDetail    context.CancelFunc
	frontierContext context.Context
	cancelFrontier  context.CancelFunc

	width, height int
	ready         bool

	mouseEnabled bool
	lastClickID  model.TicketID
	lastClickAt  time.Time

	keys          KeyMap
	searchKeys    SearchKeyMap
	detailKeys    DetailKeyMap
	frontierKeys  FrontierKeyMap
	help          help.Model
	styles        Styles
	markdownTheme markdownTheme
	markdown      detailMarkdownRenderers
	quitting      bool
	// interrupted reports that the quit was a ctrl+c rather than a q or an esc.
	// README states ctrl+c prints nothing and exits 130, which is true of the
	// one-shot path because a signal reaches it; in the monitor raw mode
	// delivers ctrl+c as an ordinary key press, so the program has to carry the
	// fact out itself. See tui.Run and cli.runMonitor.
	interrupted bool
}

type rateHold struct {
	err       error
	resetAt   time.Time
	exhausted bool
	manual    bool
}

func (h rateHold) active(now time.Time) bool {
	return h.manual || now.Before(h.resetAt)
}

type lowBudgetSchedule struct {
	resetAt time.Time
	nextAt  time.Time
	cadence time.Duration
}

func (s lowBudgetSchedule) active(now time.Time) bool {
	return !s.resetAt.IsZero() && now.Before(s.resetAt)
}

// interruptKey is the key press that means "the user interrupted this", as
// opposed to the other keys bound to quitting. It is compared by name because
// that is what the Model has: raw mode gives a key press, not a signal.
const interruptKey = "ctrl+c"

// New returns the monitor's Model reading from opts.Source.
//
// ctx belongs to the caller. New derives one monitor-session context from it:
// key-driven quit cancels that child without cancelling the caller, while caller
// cancellation still reaches every Provider request. Each single-Detail read and
// Frontier fan-out generation gets a further child context. Cancelling those
// children stops abandoned work; generation checks remain the guard that rejects
// an answer from a Provider that races with or ignores cancellation.
func New(ctx context.Context, opts Options) Model {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	heartbeatCmd := opts.Heartbeat
	if heartbeatCmd == nil {
		heartbeatCmd = heartbeat()
	}
	src := opts.Source
	sessionCtx, cancelSession := context.WithCancel(ctx)

	// Detail remains context-aware here so each screen generation can choose its
	// own lifetime. Intake sanitization still happens at this one funnel.
	detailSrc := opts.DetailSource
	fetchDetail := func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		if detailSrc == nil {
			return model.Detail{}, model.Capabilities{}, errNoDetailSource
		}
		d, caps, err := detailSrc(ctx, id)
		return safeDetail(d), caps, safeErr(err)
	}

	// The box draws no cursor of its own: the real terminal cursor is placed
	// on it from View, which keeps the frame free of a blinking glyph that a
	// golden would have to guess the phase of.
	search := textinput.New()
	search.Prompt = searchPrompt
	search.CharLimit = 0
	search.SetVirtualCursor(false)

	m := Model{
		fetch: func() (ListInput, error) {
			if src == nil {
				return ListInput{}, errNoSource
			}
			in, err := src(sessionCtx)
			return safeListInput(in), safeErr(err)
		},
		now:          now,
		interval:     opts.Interval,
		heartbeatCmd: heartbeatCmd,
		// The first refresh is already on its way out of Init.
		generation:      1,
		refreshing:      true,
		lastAttempt:     now(),
		terminalFocused: true,
		listArmed:       true,
		hasSource:       src != nil,
		legendVisible:   true,
		search:          search,
		fetchDetail:     fetchDetail,
		cancelSession:   cancelSession,
		newReadContext: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(sessionCtx)
		},
		mouseEnabled:     !opts.NoMouse,
		details:          make(map[model.TicketID]detailEntry),
		keys:             DefaultKeyMap(),
		searchKeys:       DefaultSearchKeyMap(),
		detailKeys:       DefaultDetailKeyMap(),
		frontierKeys:     DefaultFrontierKeyMap(),
		layoutFrontierFn: layoutFrontier,
		help:             help.New(),
		styles:           DefaultStyles(true),
		markdownTheme:    markdownDark,
		markdown:         newDetailMarkdownRenderers(0, markdownDark),
	}
	m = m.syncMouseKeys()

	if opts.InitialError != nil && provider.KindOf(opts.InitialError) == provider.KindRateLimit {
		m.lastErr = safeErr(opts.InitialError)
		m.rateHold = m.rateLimitHold(m.lastErr)
		if m.rateHold.manual || !m.rateHold.resetAt.IsZero() {
			m.refreshing = false
			m.generation = 0
		}
	}

	if opts.Initial != nil {
		// A reading the caller already took is folded in exactly the way a
		// successful refresh is folded in, and nothing is in flight: the refresh
		// clock runs from when the reading was *taken*, so the first auto-refresh
		// lands one interval after that rather than one interval after startup.
		m.input = safeListInput(*opts.Initial)
		m = m.applySuccessfulBudgetPolicy(m.input)
		m.hasData = true
		m.refreshing = false
		m.generation = 0
		m.lastAttempt = opts.Initial.FetchedAt
		m = m.rebuildRows()
	}

	if opts.Open != nil {
		// Decoder mode seats the same Detail state as a list-open, but leaves the
		// command for Init so Bubble Tea owns startup I/O.
		if opts.Initial == nil {
			m.refreshing = false
			m.generation = 0
			m.listArmed = false
		}
		entry := safeOpen(*opts.Open)
		next, _ := m.seatDetail(entry.Ticket, entry.Parent, entry.Capabilities, false)
		m = next.(Model)
	}
	return m
}

// errNoDetailSource explains a monitor opened without a way to read Detail. It
// is a wiring mistake rather than a Tracker failure, so it says which.
var errNoDetailSource = errors.New("this monitor was opened without a Detail source")

// errNoSource explains a refresh attempted with no Watchlist behind the
// screen. A decoded Ticket with no parent has none, and the walk-up key is
// disabled rather than offered — so reaching this is a wiring mistake.
var errNoSource = errors.New("this monitor was opened without a Watchlist to watch")

// retireDetailRead ends the current single-Detail lifetime and advances its
// correctness guard. Every transition that abandons or replaces a Detail seat
// goes through this helper.
func (m Model) retireDetailRead() Model {
	if m.cancelDetail != nil {
		m.cancelDetail()
	}
	m.detailContext = nil
	m.cancelDetail = nil
	m.detailGeneration++
	return m
}

// startDetailRead gives the current Detail generation its own session child.
// Callers invoke it only when they will issue a Provider command.
func (m Model) startDetailRead() Model {
	m.detailContext, m.cancelDetail = m.newReadContext()
	return m
}

// finishDetailRead releases a naturally completed child without advancing the
// generation that just landed.
func (m Model) finishDetailRead() Model {
	if m.cancelDetail != nil {
		m.cancelDetail()
	}
	m.detailContext = nil
	m.cancelDetail = nil
	return m
}

// retireFrontierFanout cancels all issued reads owned by the current Frontier
// seat, abandons work not yet issued, and advances the stale-answer guard. The
// shared in-flight count is deliberately untouched until those commands return.
func (m Model) retireFrontierFanout() Model {
	m = m.discardPendingFrontierRebuild()
	if m.cancelFrontier != nil {
		m.cancelFrontier()
	}
	m.frontierContext = nil
	m.cancelFrontier = nil
	m.frontierGeneration++
	m.frontier.queued = nil
	return m
}

// startFrontierFanout gives an accepted non-empty plan one session child.
func (m Model) startFrontierFanout() Model {
	m.frontierContext, m.cancelFrontier = m.newReadContext()
	return m
}

// finishFrontierFanout releases a settled child without invalidating the seat.
func (m Model) finishFrontierFanout() Model {
	if m.cancelFrontier != nil {
		m.cancelFrontier()
	}
	m.frontierContext = nil
	m.cancelFrontier = nil
	return m
}

// Init starts the heartbeat, the background-colour query that decides the
// palette, and whichever first read this session is for: the list's, the
// decoded Ticket's Detail, or — for a seeded monitor — neither.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.heartbeatCmd, requestBackgroundColor}
	if m.mode == modeDetail && m.detail.loading {
		cmds = append(cmds, m.detailFetchCmd(m.detailGeneration, m.detail.ticket.ID))
	}
	if m.refreshing {
		cmds = append(cmds, m.fetchCmd(m.generation))
	}
	return tea.Batch(cmds...)
}

// Update folds one message into the model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.FocusMsg:
		if m.terminalFocused {
			return m, nil
		}
		m.terminalFocused = true
		next, allowed, _ := m.automaticRefreshAdmission()
		if !allowed {
			return next, nil
		}
		return next.startRefresh()

	case tea.BlurMsg:
		m.terminalFocused = false
		return m, nil

	case tea.WindowSizeMsg:
		widthChanged := msg.Width != m.width
		heightChanged := msg.Height != m.height
		if widthChanged || heightChanged {
			m.mouseEpoch++
		}
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.help.SetWidth(msg.Width)
		m.search.SetWidth(searchBoxWidth(msg.Width))
		m = m.rebuildMarkdownRenderers()
		m = m.invalidateDetailMarkdownSections()
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		if m.mode == modeDetail {
			m = m.reconcileDetail(true)
		}
		if m.mode == modeFrontier {
			// The displayed canvas remains atomic until the existing trailing-edge
			// replacement settles. Width changes keep #101's behavior; a height-only
			// change schedules work only at the strict #109 flip boundary.
			m = m.reconcileFrontier(true)
			if widthChanged || (heightChanged && m.frontierResizeNeedsDirectionFlip()) {
				m, cmd := m.requestFrontierRebuild()
				return m, cmd
			}
		}
		return m, nil

	case frontierRebuildMsg:
		return m.onFrontierRebuild(msg)

	case frontierDetailMsg:
		return m.onFrontierDetail(msg)

	case frontierMouseClickMsg:
		if m.mode != modeFrontier || !m.mouseEnabled || msg.epoch != m.mouseEpoch {
			return m, nil
		}
		return m.onFrontierMouseClick(msg)

	case frontierMouseWheelMsg:
		if m.mode != modeFrontier || !m.mouseEnabled || msg.epoch != m.mouseEpoch {
			return m, nil
		}
		return m.onFrontierMouseWheel(msg), nil

	case tea.BackgroundColorMsg:
		isDark := msg.IsDark()
		m.styles = DefaultStyles(isDark)
		if isDark {
			m.markdownTheme = markdownDark
		} else {
			m.markdownTheme = markdownLight
		}
		m = m.rebuildMarkdownRenderers()
		m = m.invalidateDetailMarkdownSections()
		if m.mode == modeDetail {
			m = m.reconcileDetail(true)
		}
		return m, nil

	case heartbeatMsg:
		return m.onHeartbeat()

	case refreshedMsg:
		return m.applyRefresh(msg)

	case detailFetchedMsg:
		return m.onDetailFetchedWithFrontierEvidence(msg)

	case listMouseClickMsg:
		if m.mode != modeList || !m.mouseEnabled || msg.epoch != m.mouseEpoch {
			return m, nil
		}
		return m.onListMouseClick(msg)

	case listMouseWheelMsg:
		if m.mode != modeList || !m.mouseEnabled || msg.epoch != m.mouseEpoch {
			return m, nil
		}
		return m.onListMouseWheel(msg), nil

	case detailMouseWheelMsg:
		if m.mode != modeDetail {
			return m, nil
		}
		return m.onDetailMouseWheel(msg), nil

	case detailMouseLinkMsg:
		if m.mode != modeDetail {
			return m, nil
		}
		return m.onDetailMouseLink(msg)

	case tea.KeyPressMsg:
		// The mode decides who owns the keyboard, before any binding is
		// consulted: while the box is open every list command is text, and while
		// Detail is open the list's commands are not on this screen at all.
		switch {
		case m.searching:
			return m.clearPendingClick().onSearchKey(msg)
		case m.mode == modeDetail:
			return m.clearPendingClick().onDetailKey(msg)
		case m.mode == modeFrontier:
			return m.clearPendingClick().onFrontierKey(msg)
		}
		return m.clearPendingClick().onKey(msg)

	case tea.PasteMsg:
		// A pasted Ticket key is the obvious way to use this box, and a paste
		// arrives as its own message rather than as key presses.
		if m.searching {
			return m.clearPendingClick().updateSearch(msg)
		}
	}
	return m, nil
}

// View renders the whole screen: a header that never scrolls, the windowed
// list, and a footer carrying the help line and any refresh error.
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.ReportFocus = true
	v.AltScreen = true
	if m.mouseEnabled {
		v.MouseMode = tea.MouseModeCellMotion
	} else {
		v.MouseMode = tea.MouseModeNone
	}
	if !m.ready {
		// Before the terminal has reported its size there is nothing honest to
		// draw: a frame guessed at 80x24 flashes the wrong layout for one
		// repaint, which reads worse than a blank one.
		return v
	}

	if m.mode == modeDetail {
		doc := m.detailDocument()
		v.SetContent(m.detailFrame(doc))
		if m.mouseEnabled {
			v.OnMouse = m.detailMouseHandler(doc)
		}
		return v
	}

	if m.mode == modeFrontier {
		v.SetContent(m.frontierFrame())
		if m.mouseEnabled {
			v.OnMouse = m.frontierMouseHandler()
		}
		return v
	}

	// Keep timing-derived status and geometry internally consistent when a reset
	// boundary falls during one render.
	now := m.now()
	frame := m
	frame.now = func() time.Time { return now }
	markers := frame.listMarkers()
	header := renderHeader(frame.input, frame.staleness(), frame.hasData, frame.width, markers, frame.styles)
	v.SetContent(strings.Join(append([]string{header, frame.renderBody(markers)}, frame.footerLines()...), "\n"))
	v.Cursor = frame.cursor()
	if m.mouseEnabled {
		v.OnMouse = frame.listMouseHandler()
	}
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
	// spacer plus any cutoff notice or refresh error above it.
	c.Y += headerHeight + m.bodyHeight() + m.filterLineIndex()
	return c
}

// visibleTickets returns the Tickets the list is currently showing: the last
// good reading narrowed by the session's Filter. This is the single call site
// filtering happens at — the rows, and only the rows, are built from it. The
// header's progress is deliberately computed from m.input.Tickets instead, so
// no filter can move the bar.
func (m Model) visibleTickets() []model.Ticket { return m.filter.Apply(m.input.Tickets) }

// onHeartbeat re-arms the timer and starts a refresh only when the shared
// automatic admission gate says the effective cadence is due.
func (m Model) onHeartbeat() (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	next, allowed, _ := m.automaticRefreshAdmission()
	if !allowed {
		return next, next.heartbeatCmd
	}
	next, cmd := next.startRefresh()
	return next, tea.Batch(cmd, next.heartbeatCmd)
}

// automaticRefreshAdmission is the single automatic-refresh gate. Future
// scheduler policy extends it rather than copying its rate-limit decisions.
func (m Model) automaticRefreshAdmission() (Model, bool, time.Time) {
	now := m.now()
	holdExpired := !m.rateHold.manual && !m.rateHold.resetAt.IsZero() && !now.Before(m.rateHold.resetAt)
	m = m.expireRatePolicy(now)
	if !m.listArmed || m.refreshing || !m.terminalFocused || m.rateHold.active(now) {
		return m, false, time.Time{}
	}
	due := m.effectiveAutomaticDue(now)
	if holdExpired {
		due = now
	}
	return m, !now.Before(due), due
}

// manualRefreshAdmission deliberately bypasses only a positive low-budget
// widening. A known refusal and an exhausted budget remain hard holds.
func (m Model) manualRefreshAdmission() (Model, bool) {
	now := m.now()
	m = m.expireRatePolicy(now)
	if m.refreshing || (m.rateHold.active(now) && !m.rateHold.manual) {
		return m, false
	}
	return m, true
}

func (m Model) expireRatePolicy(now time.Time) Model {
	if !m.rateHold.manual && !m.rateHold.resetAt.IsZero() && !now.Before(m.rateHold.resetAt) {
		m.rateHold = rateHold{}
	}
	if !m.lowBudget.resetAt.IsZero() && !now.Before(m.lowBudget.resetAt) {
		m.lowBudget = lowBudgetSchedule{}
	}
	return m
}

func (m Model) effectiveAutomaticDue(now time.Time) time.Time {
	due := m.lastAttempt.Add(m.interval)
	if m.lowBudget.active(now) && m.lowBudget.nextAt.After(due) {
		return m.lowBudget.nextAt
	}
	return due
}

// startRefresh is the sole outbound Source launch point. Callers must pass
// automatic or manual admission before reaching it.
func (m Model) startRefresh() (Model, tea.Cmd) {
	if m.refreshing {
		return m, nil
	}
	m.generation++
	m.refreshing = true
	m.listArmed = true
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

// applyRefresh folds one reading in and, when the Frontier is the screen on
// display, reseats it on the new reading. A refresh that failed or answered a
// superseded generation changes nothing, so it reseats nothing either.
func (m Model) applyRefresh(msg refreshedMsg) (tea.Model, tea.Cmd) {
	landed := msg.generation == m.generation && msg.err == nil
	next := m.onRefreshed(msg)
	if !landed || next.mode != modeFrontier {
		return next, nil
	}
	return next.reseatFrontier()
}

// reseatFrontier rebuilds the Frontier on a Watchlist that has just changed
// shape. It cancels the old seat's Provider reads and advances the generation,
// so no old answer can mutate the replacement. FrontierFromList reads the
// session Detail cache again, so every Ticket cached at the instant of reseating
// stays verified. Adopting here would be redundant and cannot catch an old-
// generation success that lands afterward: that answer remains cacheable but
// cannot mutate this seat, and the next explicit Frontier r reconciles it. Until
// then, a current member without seated Links or a carried failure keeps the
// Frontier derived-unresolved.
//
// It issues no fetch. A refresh reaches here from the --interval timer as
// readily as from r, and ADR-0003 Amendment 4 permits a whole-Watchlist Detail
// fan-out only in response to an explicit user action. A member the refresh
// introduced therefore stays uncovered and keeps the Frontier unresolved until
// the user presses r: a timer is not a user action. enterFrontier and
// refreshFrontier are the only two fan-out doors, and both are keypresses.
func (m Model) reseatFrontier() (Model, tea.Cmd) {
	previous := m.frontier
	m = m.retireFrontierFanout()
	m.mouseEpoch++
	m.frontier = frontierState{
		input:   FrontierFromList(m.input, m.linksFromCache()),
		focusID: previous.focusID,
		offsetX: previous.offsetX,
		offsetY: previous.offsetY,
	}
	// A read that failed is still failed, so the footer keeps saying so and
	// keeps pointing at r. Dropping the record because the Watchlist was read
	// again would leave cards marked UNVERIFIED with nothing on screen saying
	// why or what to press. Members the refresh removed leave the set and their
	// error provenance with them, while retained members keep their matching
	// error so the footer never attributes a removed failure to one still shown.
	for id := range previous.failed {
		if _, seated := m.frontier.input.Links[id]; seated {
			continue
		}
		if !m.hasMember(id) {
			continue
		}
		if m.frontier.failed == nil {
			m.frontier.failed = make(map[model.TicketID]struct{}, len(previous.failed))
		}
		m.frontier.failed[id] = struct{}{}
		if err := previous.failureErrors[id]; err != nil {
			if m.frontier.failureErrors == nil {
				m.frontier.failureErrors = make(map[model.TicketID]error, len(previous.failureErrors))
			}
			m.frontier.failureErrors[id] = err
		}
	}
	if len(m.frontier.failed) > 0 {
		m.frontier.lastErr = previous.lastErr
		m = m.reconcileFrontierFailureNotice()
	}
	return m.rebuildFrontier(), repaint
}

// hasMember reports whether id names a Ticket in the seated Frontier's
// Watchlist.
func (m Model) hasMember(id model.TicketID) bool {
	for _, t := range m.frontier.input.Tickets {
		if t.ID == id {
			return true
		}
	}
	return false
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
		if provider.KindOf(msg.err) == provider.KindRateLimit {
			m.lowBudget = lowBudgetSchedule{}
			m.rateHold = m.rateLimitHold(msg.err)
		} else if m.lowBudget != (lowBudgetSchedule{}) {
			m = m.advanceLowBudgetAfterAttempt()
		}
		return m
	}
	m.lastErr = nil
	m = m.applySuccessfulBudgetPolicy(msg.input)
	m.input = msg.input
	m.hasData = true
	return m.rebuildRows()
}

func (m Model) rateLimitHold(err error) rateHold {
	metadata, ok := provider.RateLimitMetadataOf(err)
	if !ok {
		return rateHold{err: err, manual: true}
	}
	now := m.now()
	resetAt, hasDeadline := metadata.Deadline(now)
	if !hasDeadline || !now.Before(resetAt) {
		return rateHold{}
	}
	return rateHold{err: err, resetAt: resetAt}
}

func (m Model) applySuccessfulBudgetPolicy(in ListInput) Model {
	m.rateHold = rateHold{}
	m.lowBudget = lowBudgetSchedule{}
	budget := in.RateLimitBudget
	if !in.Capabilities.RateLimitBudget || !budget.Valid() {
		return m
	}
	now := m.now()
	if !now.Before(budget.ResetsAt) {
		return m
	}
	if budget.Remaining == 0 {
		m.rateHold = rateHold{resetAt: budget.ResetsAt, exhausted: true}
		return m
	}
	if budget.Remaining > 100 {
		return m
	}
	window := budget.ResetsAt.Sub(now)
	spread := window / time.Duration(budget.Remaining)
	if window%time.Duration(budget.Remaining) != 0 {
		spread++
	}
	m.lowBudget = lowBudgetSchedule{
		resetAt: budget.ResetsAt,
		nextAt:  now.Add(max(2*m.interval, spread)),
		cadence: max(2*m.interval, spread),
	}
	return m
}

// advanceLowBudgetAfterAttempt retains the established low-budget cadence after
// a failed automatic request. A manual retry before its scheduled turn leaves
// that turn intact.
func (m Model) advanceLowBudgetAfterAttempt() Model {
	now := m.now()
	if !m.lowBudget.active(now) || m.lowBudget.cadence <= 0 || m.lastAttempt.Before(m.lowBudget.nextAt) {
		return m
	}
	nextAt := m.lastAttempt.Add(m.lowBudget.cadence)
	if !now.Before(nextAt) {
		nextAt = now.Add(m.lowBudget.cadence)
	}
	m.lowBudget.nextAt = nextAt
	return m
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
	// The Frontier is offered only when there is a Watchlist to draw.
	m.keys.Frontier.SetEnabled(m.hasData)

	m.rows = BuildRows(m.visibleTickets())

	if found, ok := rowOf(m.rows, m.selectedID); ok {
		m.selected = found
	} else {
		m.selected = nearestSelectable(m.rows, m.selected)
	}
	m.selectedID = selectedTicketID(m.rows, m.selected)
	if m.ready {
		// Before the terminal has reported its size there is no window to fit the
		// cursor into: measuring against the one-line floor would scroll the first
		// group heading off a screen that has not been drawn yet. The size message
		// re-measures, so nothing is lost by waiting for it.
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	}
	return m
}

// quit is the single key-driven shutdown funnel. It invalidates both read
// generations, cancels a visible or hidden Frontier fan-out, then cancels the
// monitor session so Source and every remaining Provider child can return before
// Bubble Tea waits for its command pool. q and esc exit 0; ctrl+c exits 130.
func (m Model) quit(msg tea.KeyPressMsg) Model {
	m = m.retireDetailRead()
	m = m.retireFrontierFanout()
	if m.cancelSession != nil {
		m.cancelSession()
	}
	m.quitting = true
	m.interrupted = msg.String() == interruptKey
	return m
}

// Interrupted reports whether the session ended with ctrl+c. tui.Run reads it
// off the final model to decide which error, if any, the caller sees.
func (m Model) Interrupted() bool { return m.interrupted }

// onKey dispatches a key press in list mode.
//
// ClearFilter is matched before Quit because both answer to esc: the ladder is
// escape the box, then escape the filter, then escape the program. q and ctrl+c
// still quit unconditionally, so nobody is trapped by a filter.
// repaint forces the next frame to be drawn whole rather than diffed against
// the one before it.
//
// The rule: every keystroke that changes whether the filter line is drawn, or
// what it is, returns this. The footer grows and shrinks a row on those
// transitions, and the incremental renderer must not diff across a frame of a
// different shape — the result on a real terminal is stale rows left standing
// under fresh ones, up to and including a Ticket wearing another Ticket's
// status. Movement, typing inside the box and refreshes leave the frame's
// shape alone and are diffed as usual, which is what keeps a 60s poll cheap.
var repaint tea.Cmd = tea.ClearScreen

func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.ClearFilter):
		return m.setFilter(Filter{}), repaint

	case key.Matches(msg, m.keys.HideFinished):
		m.filter.HideFinished = !m.filter.HideFinished
		return m.setFilter(m.filter), repaint

	case key.Matches(msg, m.keys.Find):
		m.searching = true
		// The box re-opens holding the applied query, so / is how you edit
		// what you last searched for rather than only how you start again.
		m.search.SetValue(m.filter.Query)
		m.search.CursorEnd()
		cmd := m.search.Focus()
		// Opening the box adds a footer line, which takes one from the body.
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		return m, tea.Batch(cmd, repaint)

	case key.Matches(msg, m.keys.Quit):
		return m.quit(msg), tea.Quit

	case key.Matches(msg, m.keys.ToggleMouse):
		return m.toggleMouse(), nil

	case key.Matches(msg, m.keys.Legend):
		m.legendVisible = !m.legendVisible
		return m, repaint

	case key.Matches(msg, m.keys.Help):
		// The expanded listing eats body height, so the window has to be
		// re-measured before the next frame.
		m.help.ShowAll = !m.help.ShowAll
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		next, allowed := m.manualRefreshAdmission()
		if !allowed {
			return next, nil
		}
		next, cmd := next.startRefresh()
		return next, cmd

	case key.Matches(msg, m.keys.Open):
		return m.openDetail()

	case key.Matches(msg, m.keys.Frontier):
		return m.enterFrontier()

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
		return m.quit(msg), tea.Quit

	case key.Matches(msg, m.searchKeys.Cancel):
		// Abandon: the draft and the applied query go together. HideFinished
		// survives — it was not part of this interaction.
		m.searching = false
		m.search.Blur()
		return m.setFilter(Filter{HideFinished: m.filter.HideFinished}), repaint

	case key.Matches(msg, m.searchKeys.Apply):
		// Commit: the box closes and the list stays narrowed. The query is
		// already applied — it has been narrowing live on every keystroke —
		// so this only hands the keyboard back.
		m.searching = false
		m.search.Blur()
		return m.setFilter(m.filter), repaint

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

// renderBody draws the list, or the state that stands in for it: a Watchlist
// with no Tickets, or a first fetch that has not landed yet. The markers reach
// the rows and nothing else: an empty, failed or loading body has no row to
// mark.
func (m Model) renderBody(markers listMarkers) string {
	height := m.bodyHeight()

	switch {
	case m.hasData && len(m.rows) > 0:
		body := renderRows(m.rows, m.selected, m.offset, height, m.width, m.input.Capabilities,
			markers, m.styles)
		if !m.legendVisible || m.offset != 0 || m.filter.Active() {
			return body
		}
		used := 0
		for _, rowHeight := range rowHeights(m.rows, m.input.Capabilities) {
			used += rowHeight
		}
		return replaceTrailingBodyLines(body, used, listLegendLines(markers, m.width), m.styles.Muted.Render)
	case m.hasData && m.filter.Active() && len(m.input.Tickets) > 0:
		// Distinct from the empty Watchlist below on purpose: "there is
		// nothing here" and "you are hiding everything" look identical on
		// screen, and a user who cannot tell them apart thinks sitrep is
		// broken.
		return pad(m.styles.EmptyFilter.Render(truncateLine(
			"No Tickets match this filter.  Press esc to clear it.", m.width)), height)
	case m.hasData:
		return pad(m.styles.Muted.Render("This Watchlist has no Tickets."), height)
	case m.lastErr != nil:
		// A monitor that exits on one bad DNS lookup is useless on an SSH box:
		// the screen says what went wrong and how to try again, and waits.
		return pad(strings.Join([]string{
			m.styles.Error.Render(truncateLine("Could not read the Watchlist: "+m.lastErr.Error(), m.width)),
			"",
			m.styles.Muted.Render(m.retryBodyHint()),
		}, "\n"), height)
	default:
		return pad(m.styles.Muted.Render("Reading…"), height)
	}
}

func (m Model) retryBodyHint() string {
	now := m.now()
	if !m.terminalFocused {
		if m.rateHold.active(now) && !m.rateHold.manual {
			return "Automatic refresh is paused while terminal is unfocused; r is held. Press q to quit."
		}
		return "Automatic refresh is paused while terminal is unfocused. Press r to try again, q to quit."
	}
	if m.rateHold.active(now) && !m.rateHold.manual {
		return "Automatic refresh resumes at " + m.rateHold.resetAt.Local().Format(time.Kitchen) + "; r is held. Press q to quit."
	}
	return "Press r to try again, q to quit."
}

// footerLines is the always-visible bottom block, line by line: a blank
// spacer, the Query cutoff notice when there is one, the refresh error when
// there is one, the filter state when there is one, then the help line. The
// notice and error are single truncated lines, not modals — the list behind
// them is still the point.
//
// It is returned as lines rather than a block because the footer's height is
// what the body is measured against, and because the find box needs to know
// which row it was drawn on to put the cursor there.
func (m Model) footerLines() []string {
	now := m.now()
	lines := []string{""}
	if m.hasData && m.input.LimitReached {
		lines = append(lines, m.styles.Muted.Render(truncateLine(
			plain.LimitNotice(len(m.input.Tickets)), m.width)))
	}
	if m.lastErr != nil && m.hasData && (!m.refreshing || m.terminalFocused) && provider.KindOf(m.lastErr) != provider.KindRateLimit {
		if !m.terminalFocused {
			lines = append(lines, m.styles.Error.Render(truncateLine(
				fmt.Sprintf("refresh failed: %v%sautomatic refresh paused: terminal unfocused", m.lastErr, separator), m.width)))
		} else {
			remaining := m.effectiveAutomaticDue(now).Sub(now)
			lines = append(lines, m.styles.Error.Render(truncateLine(
				fmt.Sprintf("refresh failed: %v%sretrying in %s", m.lastErr, separator, countdown(remaining)), m.width)))
		}
	}
	if policy := m.ratePolicyFooter(); policy != "" {
		lines = append(lines, m.styles.Error.Render(truncateLine(policy, m.width)))
	}
	if pause := m.focusPauseFooter(); pause != "" {
		lines = append(lines, m.styles.Muted.Render(truncateLine(pause, m.width)))
	}
	if filter := m.renderFilterLine(); filter != "" {
		lines = append(lines, filter)
	}
	// The expanded help is several lines, so it is clipped line by line.
	return append(lines, strings.Split(truncateBlock(m.help.View(m.helpKeys()), m.width), "\n")...)
}

func (m Model) ratePolicyFooter() string {
	if m.refreshing && !m.terminalFocused {
		return ""
	}
	now := m.now()
	prefix := ""
	if !m.terminalFocused {
		prefix = "paused: terminal unfocused; "
	}
	if m.rateHold.active(now) {
		if m.rateHold.exhausted {
			if prefix != "" {
				return prefix + "refresh held: rate limit budget exhausted · r held"
			}
			return "refresh held: rate limit budget exhausted · retrying at " + m.rateHold.resetAt.Local().Format(time.Kitchen) + " · r held"
		}
		if m.rateHold.manual {
			return prefix + "refresh held: " + m.rateHold.err.Error() + " · press r to try again"
		}
		if prefix != "" {
			return prefix + "refresh held: " + m.rateHold.err.Error() + " · r held"
		}
		return "refresh held: " + m.rateHold.err.Error() + " · retrying at " + m.rateHold.resetAt.Local().Format(time.Kitchen) + " · r held"
	}
	if m.lowBudget.active(now) {
		if prefix != "" {
			return prefix + "refresh slowed: low rate limit budget"
		}
		return "refresh slowed: low rate limit budget · next automatic refresh in " + countdown(m.lowBudget.nextAt.Sub(now))
	}
	return ""
}

func (m Model) focusPauseFooter() string {
	if m.terminalFocused || m.refreshing {
		return ""
	}
	return "paused: terminal unfocused · " + m.staleness()
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
// spacer, the optional cutoff notice and the optional refresh error.
func (m Model) filterLineIndex() int {
	index := 1
	if m.hasData && m.input.LimitReached {
		index++
	}
	if m.lastErr != nil && m.hasData && (!m.refreshing || m.terminalFocused) && provider.KindOf(m.lastErr) != provider.KindRateLimit {
		index++
	}
	if m.ratePolicyFooter() != "" {
		index++
	}
	if m.focusPauseFooter() != "" {
		index++
	}
	return index
}

// helpKeys is the keyboard surface the *list's* footer describes, which is the
// one the keyboard is on whenever that footer is drawn. A footer offering list
// commands while the find box holds the keyboard would be describing a program
// the user is not in.
func (m Model) helpKeys() help.KeyMap {
	if m.searching {
		return m.responsiveHelpKeys(m.searchKeys)
	}
	if m.mode == modeFrontier {
		return m.responsiveHelpKeys(m.effectiveFrontierKeys())
	}
	return m.responsiveHelpKeys(m.keys)
}

// effectiveFrontierKeys is the Frontier's help and dispatch surface. Without
// BlockingLinks, or when the selected canvas was refused, graph interactions
// are disabled in one place so advertised bindings and every input path agree.
func (m Model) effectiveFrontierKeys() FrontierKeyMap {
	keys := m.frontierKeys
	if m.frontier.direction == frontierRanksVertical {
		keys.Up.SetHelp(keys.Up.Help().Key, "blocker side")
		keys.Down.SetHelp(keys.Down.Help().Key, "dependent side")
		keys.Left.SetHelp(keys.Left.Help().Key, "previous node")
		keys.Right.SetHelp(keys.Right.Help().Key, "next node")
	}
	if m.frontier.input.Capabilities.BlockingLinks && m.frontier.refusal == nil {
		return keys
	}
	for _, binding := range []*key.Binding{&keys.Open, &keys.Refresh,
		&keys.Up, &keys.Down, &keys.Left, &keys.Right, &keys.PageUp, &keys.PageDown, &keys.Home, &keys.End,
		&keys.MouseSelect, &keys.MouseOpen, &keys.MouseWheel} {
		binding.SetEnabled(false)
	}
	return keys
}

// compactMouseHelp returns the next shorter spelling of enabled mouse help.
// Capture is already one word and needs no shorter spelling.
func compactMouseHelp(binding key.Binding) (string, bool) {
	if binding.Help().Key != "m" {
		return "", false
	}
	switch binding.Help().Desc {
	case mouseEnabledHelp:
		return mouseEnabledCompactHelp, true
	case mouseEnabledCompactHelp:
		return mouseEnabledTerseHelp, true
	}
	return "", false
}

// shortenHelpItem owns the narrow fallback: it shortens the first item until
// the first three short-help roles fit. Wider list discovery has a separate
// projection that also protects Help.
func shortenHelpItem(short []key.Binding, renderer help.Model, width int) {
	if len(short) < 3 {
		return
	}
	for range 3 {
		if lipgloss.Width(renderer.ShortHelpView(short[:3])) <= width {
			return
		}
		desc, ok := shorterHelpDesc(short[0])
		if !ok {
			return
		}
		short[0].SetHelp(short[0].Help().Key, desc)
	}
}

func shorterHelpDesc(binding key.Binding) (string, bool) {
	if binding.Help().Key == "shift-drag" && binding.Help().Desc == searchMouseHintHelp {
		return searchMouseHintCompactHelp, true
	}
	return compactMouseHelp(binding)
}

// listHelpDiscoveryWidth is the narrowest list width that reserves the mouse,
// open, quit, and Help roles. Below it the existing three-control fallback wins
// because both mouse states cannot guarantee all four. Expanded help remains
// complete at every supported width.
const listHelpDiscoveryWidth = 60

// projectListShortHelp spends a list footer's width without changing the source
// priority order. The escape hatches and Help are selected first; optional
// bindings then compete for the remaining cells in their declared order.
func projectListShortHelp(short []key.Binding, keys KeyMap, renderer help.Model, width int) []key.Binding {
	fallback := append([]key.Binding(nil), short...)
	if width <= 0 || width < listHelpDiscoveryWidth {
		shortenHelpItem(fallback, renderer, width)
		return fallback
	}

	helpIndex := helpBindingIndex(short, keys.Help)
	mouseIndex := helpBindingIndex(short, keys.ToggleMouse)
	if helpIndex < 0 || mouseIndex < 0 || !short[helpIndex].Enabled() {
		shortenHelpItem(fallback, renderer, width)
		return fallback
	}
	// Keep byte-stable wide output when the source prefix already exposes the
	// complete Help segment. The renderer may still mark a trailing optional
	// binding as clipped; that established suffix does not hide discovery.
	if lipgloss.Width(renderer.ShortHelpView(short[:helpIndex+1])) <= width {
		return fallback
	}

	selected := make([]bool, len(short))
	protected := 0
	for i := range short {
		if short[i].Enabled() && protected < 3 {
			selected[i] = true
			protected++
		}
	}
	selected[helpIndex] = true

	for lipgloss.Width(renderer.ShortHelpView(selectedHelpBindings(short, selected))) > width {
		desc, ok := shorterHelpDesc(short[mouseIndex])
		if !ok {
			shortenHelpItem(fallback, renderer, width)
			return fallback
		}
		short[mouseIndex].SetHelp(short[mouseIndex].Help().Key, desc)
	}

	for i := range short {
		if i == helpIndex {
			continue
		}
		if i > helpIndex {
			break
		}
		if !short[i].Enabled() || selected[i] {
			continue
		}
		selected[i] = true
		if lipgloss.Width(renderer.ShortHelpView(selectedHelpBindings(short, selected))) > width {
			selected[i] = false
		}
	}
	for i := helpIndex + 1; i < len(short); i++ {
		if !short[i].Enabled() {
			continue
		}
		selected[i] = true
		if lipgloss.Width(renderer.ShortHelpView(selectedHelpBindings(short, selected))) > width {
			selected[i] = false
		}
	}

	return selectedHelpBindings(short, selected)
}

func helpBindingIndex(bindings []key.Binding, role key.Binding) int {
	for i, binding := range bindings {
		if binding.Help() == role.Help() && slices.Equal(binding.Keys(), role.Keys()) {
			return i
		}
	}
	return -1
}

func selectedHelpBindings(bindings []key.Binding, selected []bool) []key.Binding {
	result := make([]key.Binding, 0, len(bindings))
	for i := range bindings {
		if selected[i] {
			result = append(result, bindings[i])
		}
	}
	return result
}

type responsiveHelpKeyMap struct {
	short []key.Binding
	full  [][]key.Binding
}

func (k responsiveHelpKeyMap) ShortHelp() []key.Binding  { return k.short }
func (k responsiveHelpKeyMap) FullHelp() [][]key.Binding { return k.full }

// responsiveHelpKeys keeps list and search help actionable at narrow widths.
// Detail owns its richer role-aware layout in detailhelp.go.
func (m Model) responsiveHelpKeys(keys help.KeyMap) help.KeyMap {
	short := append([]key.Binding(nil), keys.ShortHelp()...)
	groups := keys.FullHelp()
	full := make([][]key.Binding, len(groups))
	for i := range groups {
		full[i] = append([]key.Binding(nil), groups[i]...)
	}

	// The expanded panel has two columns to spend, so only the longest item --
	// the capture-on wording, which carries the shift-drag recovery -- gives way
	// there.
	if m.width > 0 && m.width <= 42 {
		for i := range full {
			for j := range full[i] {
				if full[i][j].Help().Desc == mouseEnabledHelp {
					full[i][j].SetHelp("m", mouseEnabledCompactHelp)
				}
			}
		}
	}

	unbounded := m.help
	unbounded.SetWidth(0)
	if listKeys, ok := keys.(KeyMap); ok {
		short = projectListShortHelp(short, listKeys, unbounded, m.width)
	} else {
		shortenHelpItem(short, unbounded, m.width)
	}
	if listKeys, ok := keys.(KeyMap); ok && m.help.ShowAll &&
		m.width > 0 && m.width <= 42 && m.height > 0 && m.height <= 16 {
		full = compactListFullHelp(listKeys)
	} else if m.width > 0 && lipgloss.Width(unbounded.FullHelpView(full)) > m.width {
		stacked := make([]key.Binding, 0)
		for _, group := range full {
			stacked = append(stacked, group...)
		}
		full = [][]key.Binding{stacked}
	}
	return responsiveHelpKeyMap{short: short, full: full}
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
