package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

// Every Frontier frame test drives a real tui.Model through Options and presses
// v. Constructing a FrontierInput literal would bypass the terminal-text
// boundary the screen sits behind (ADR-0006).
func frontierSession(t *testing.T, c *clock, p *fake.Provider) *session {
	t.Helper()
	return startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
}

// openFrontier waits for the Watchlist, presses v, and waits for the fan-out to
// finish — the header's counts line is what says it has.
//
// It then walks focus to the leftmost column, which puts the window at the
// canvas origin: the Frontier opens focused on the list's selection, and the
// fixture's selection sits in the rightmost column.
func openFrontier(t *testing.T, s *session) {
	t.Helper()
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")
	s.tm.Send(keyPress("g"))
	s.tm.Send(keyPress("h"))
}

// The headline frame: Status Category colour on every card, the emphasis
// channel's border weight and badge words, and edges reading left to right as
// "this must finish before that".
//
// The fixture's graph is deliberately larger than a 120x40 terminal, so this
// frame is the canvas origin and TestFrontierDrawsGhostTickets scrolls to the
// far end of it. That is the screen's real behaviour: it scrolls, and is never
// reflowed or scaled.
func TestFrontierFrame(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)

	m, got := s.finish(t)

	checkGolden(t, "frontier.golden.txt", got)
	if m.mode != modeFrontier {
		t.Errorf("mode = %v, want the Frontier", m.mode)
	}
	frame := string(got)
	// The other spelling of the same header field: a Watchlist where some
	// member's Links were readable gets a number, and it counts only those.
	if !strings.Contains(frame, separator+"3 actionable") {
		t.Errorf("the header does not carry a computed Actionable count:\n%s", frame)
	}
	for _, want := range []string{"ACTIONABLE", "blocked by 1", "CYCLE", "UNVERIFIED"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame is missing %q:\n%s", want, frame)
		}
	}
	// A Relates Link carries no ordering, so it is not an edge and names no
	// node. #207 is reachable from #212 only through Relates, and #212 sits in
	// column 0 with nothing pointing at it.
	if _, drawn := m.frontier.layout.nodeAt["acme/widgets#212"]; !drawn {
		t.Fatal("#212 is not on the canvas, so the Relates assertion below proves nothing")
	}
	if m.frontier.layout.columnOf["acme/widgets#212"] != 0 {
		t.Error("a Relates Link was drawn as a blocking edge")
	}
}

// A Ticket blocked by something outside the Watchlist is drawn as a Ghost
// Ticket, so it never looks Actionable.
func TestFrontierDrawsGhostTickets(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	s.tm.Send(keyPress("G"))
	s.waitFor(t, "GHOST")

	m, got := s.finish(t)

	checkGolden(t, "frontier_ghosts.golden.txt", got)
	if n := len(m.frontier.graph.Ghosts()); n != 3 {
		t.Errorf("Ghosts = %d, want the fixture's 3", n)
	}
	if !strings.Contains(string(got), "GHOST") {
		t.Errorf("the frame drew no Ghost Ticket:\n%s", got)
	}
}

// blockedDetailSource is a DetailSource that never answers until the test lets
// it. It is how the loading frame is made deterministic: no Detail has landed,
// so nothing on screen may claim to know anything.
func blockedDetailSource(release <-chan struct{}, inner DetailSource) DetailSource {
	return func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		select {
		case <-release:
			return inner(ctx, id)
		case <-ctx.Done():
			return model.Detail{}, model.Capabilities{}, ctx.Err()
		}
	}
}

type controlledFrontierReply struct {
	detail model.Detail
	caps   model.Capabilities
	err    error
}

type controlledFrontierCall struct {
	id    model.TicketID
	ctx   context.Context
	reply chan controlledFrontierReply
}

type controlledFrontierReads struct {
	calls  chan controlledFrontierCall
	active atomic.Int64
	peak   atomic.Int64
	total  atomic.Int64
}

func newControlledFrontierReads() *controlledFrontierReads {
	return &controlledFrontierReads{calls: make(chan controlledFrontierCall, 64)}
}

func (r *controlledFrontierReads) source(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
	active := r.active.Add(1)
	for peak := r.peak.Load(); active > peak && !r.peak.CompareAndSwap(peak, active); peak = r.peak.Load() {
	}
	r.total.Add(1)
	defer r.active.Add(-1)

	call := controlledFrontierCall{id: id, ctx: ctx, reply: make(chan controlledFrontierReply, 1)}
	select {
	case r.calls <- call:
	case <-ctx.Done():
		return model.Detail{}, model.Capabilities{}, ctx.Err()
	}
	select {
	case reply := <-call.reply:
		return reply.detail, reply.caps, reply.err
	case <-ctx.Done():
		return model.Detail{}, model.Capabilities{}, ctx.Err()
	}
}

func (r *controlledFrontierReads) next(t *testing.T) controlledFrontierCall {
	t.Helper()
	select {
	case call := <-r.calls:
		return call
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for controlled Frontier Detail read")
		return controlledFrontierCall{}
	}
}

func (r *controlledFrontierReads) batch(t *testing.T, n int) []controlledFrontierCall {
	t.Helper()
	calls := make([]controlledFrontierCall, n)
	for i := range n {
		calls[i] = r.next(t)
	}
	return calls
}

func assertFrontierCallsCanceled(t *testing.T, calls ...controlledFrontierCall) {
	t.Helper()
	for _, call := range calls {
		select {
		case <-call.ctx.Done():
			if !errors.Is(call.ctx.Err(), context.Canceled) {
				t.Errorf("Detail context for %s ended with %v, want context.Canceled", call.id, call.ctx.Err())
			}
		case <-time.After(waitTimeout):
			t.Fatalf("Detail context for %s was not cancelled", call.id)
		}
	}
}

func assertFrontierCallsLive(t *testing.T, calls ...controlledFrontierCall) {
	t.Helper()
	for _, call := range calls {
		select {
		case <-call.ctx.Done():
			t.Fatalf("Detail context for %s ended while its Frontier seat was still active: %v", call.id, call.ctx.Err())
		default:
		}
	}
}

// Emphasis is withheld until every Detail read has answered, then applied at
// once: one honest moment, no flipping. Asserted as two frames from the same
// session — absent, then present.
func TestFrontierWithholdsEmphasisUntilEveryDetailHasAnswered(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	release := make(chan struct{})
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: blockedDetailSource(release, TicketDetailSource(p)),
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "reading Detail")

	m, got := s.finish(t)
	close(release)

	checkGolden(t, "frontier_loading.golden.txt", got)
	frame := string(got)
	if !strings.Contains(frame, "reading Detail 0/13") {
		t.Errorf("the header does not report the fan-out's progress:\n%s", frame)
	}
	for _, badge := range []string{"ACTIONABLE", "UNVERIFIED", "blocked by"} {
		if strings.Contains(frame, badge) {
			t.Errorf("a half-loaded Frontier claimed %q:\n%s", badge, frame)
		}
	}
	if m.frontier.isResolved() {
		t.Error("the Frontier reported itself resolved with every read outstanding")
	}
}

// A Ticket whose Links could not be read is unverified rather than unblocked,
// and the footer says how many there are. Nothing is Actionable when nothing
// was verified: Actionable fails closed.
func TestFrontierUnverifiedWhenEveryDetailReadFails(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture(), fake.WithDetailError(errors.New("tracker said no")))
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "could not be read")
	s.tm.Send(keyPress("g"))

	m, got := s.finish(t)

	checkGolden(t, "frontier_unverified.golden.txt", got)
	frame := string(got)
	if !strings.Contains(frame, "UNVERIFIED") {
		t.Errorf("no card is marked unverified:\n%s", frame)
	}
	if !strings.Contains(frame, "13 Tickets' Links could not be read") {
		t.Errorf("the footer does not name the failure count:\n%s", frame)
	}
	// "0 actionable" is a computed answer. A Frontier where nothing could be
	// computed must not render byte-identically to one where every Ticket is
	// genuinely blocked.
	if !strings.Contains(frame, "actionable unknown") {
		t.Errorf("the header claims an Actionable count it could not compute:\n%s", frame)
	}
	if strings.Contains(frame, "ACTIONABLE") {
		t.Errorf("something looked Actionable with no Links verified:\n%s", frame)
	}
	for _, a := range m.frontier.graph.Members() {
		if a.Actionable {
			t.Fatalf("%s is Actionable with unreadable Links", a.TicketID)
		}
	}
}

func TestFrontierCancellationIsControlFlowButDeadlineIsFailure(t *testing.T) {
	in := ListInput{
		Header:       Header{Key: "W-1", Title: "one member"},
		Tickets:      []model.Ticket{{ID: "T-1", Key: "T-1", Title: "first", Status: model.StatusTodo}},
		Capabilities: model.Capabilities{BlockingLinks: true},
		FetchedAt:    newClock().now(),
	}
	read := func(t *testing.T, readErr error) Model {
		t.Helper()
		var received context.Context
		m := New(t.Context(), Options{
			Initial: &in,
			DetailSource: func(ctx context.Context, _ model.TicketID) (model.Detail, model.Capabilities, error) {
				received = ctx
				return model.Detail{}, in.Capabilities, readErr
			},
		})
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = updated.(Model)
		updated, _ = m.Update(keyPress("v"))
		m = updated.(Model)

		// Drive the same captured-context command that Bubble Tea's batch owns. The
		// ignored batch is never run in this unit test, so exactly one result lands.
		msg := m.frontierFetchCmd(m.frontierGeneration, "T-1")().(frontierDetailMsg)
		if received != m.frontierContext {
			t.Fatal("Frontier command did not pass its generation context to DetailSource")
		}
		updated, _ = m.onFrontierDetail(msg)
		return updated.(Model)
	}

	t.Run("wrapped cancellation", func(t *testing.T) {
		m := read(t, errors.Join(errors.New("provider wrapper"), context.Canceled))
		frame := m.View().Content
		if len(m.frontier.failed) != 0 || m.frontier.lastErr != nil {
			t.Errorf("cancellation became a failure: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
		}
		if strings.Contains(frame, "could not be read") || strings.Contains(frame, "provider wrapper") {
			t.Errorf("cancellation rendered a failure footer:\n%s", frame)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		m := read(t, context.DeadlineExceeded)
		frame := m.View().Content
		if _, failed := m.frontier.failed["T-1"]; !failed || !errors.Is(m.frontier.lastErr, context.DeadlineExceeded) {
			t.Errorf("deadline was hidden: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
		}
		if !strings.Contains(frame, "could not be read") || !strings.Contains(frame, context.DeadlineExceeded.Error()) {
			t.Errorf("deadline did not render as a retryable failure:\n%s", frame)
		}
	})
}

// Esc cancels every issued read, clears work not yet issued, and returns to a
// readable list. Cancellation outcomes are stale control flow: they cannot add
// progress, a failed member, or a footer error.
func TestFrontierFetchIsInterruptible(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	calls := reads.batch(t, detailfanout.Parallelism)
	s.waitFor(t, "reading Detail")
	s.tm.Send(escKey)
	s.waitFor(t, "TODO")
	assertFrontierCallsCanceled(t, calls...)

	m, got := s.finish(t)

	checkGolden(t, "frontier_interrupted.golden.txt", got)
	if m.mode != modeList {
		t.Errorf("mode = %v, want the list", m.mode)
	}
	if m.frontier.done != 0 || len(m.frontier.failed) != 0 || m.frontier.lastErr != nil {
		t.Errorf("cancellation landed as progress/failure: done=%d failed=%v err=%v",
			m.frontier.done, m.frontier.failed, m.frontier.lastErr)
	}
	if len(m.frontier.queued) != 0 {
		t.Errorf("abandoned Frontier retained %d queued reads", len(m.frontier.queued))
	}
	if got := reads.total.Load(); got != detailfanout.Parallelism {
		t.Errorf("issued reads = %d, want only the first batch of %d", got, detailfanout.Parallelism)
	}
}

func TestFrontierImmediateReentryUsesDistinctBoundedLifetime(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	oldCalls := reads.batch(t, detailfanout.Parallelism)
	s.tm.Send(escKey)
	s.waitFor(t, "TODO")
	s.tm.Send(keyPress("v"))
	assertFrontierCallsCanceled(t, oldCalls...)
	newCalls := reads.batch(t, detailfanout.Parallelism)

	if oldCalls[0].ctx == newCalls[0].ctx {
		t.Fatal("re-entered Frontier reused the abandoned generation context")
	}
	assertFrontierCallsLive(t, newCalls...)
	m, _ := s.finish(t)
	assertFrontierCallsCanceled(t, newCalls...)

	if peak := reads.peak.Load(); peak > detailfanout.Parallelism {
		t.Errorf("peak concurrent reads = %d, want at most %d", peak, detailfanout.Parallelism)
	}
	if m.frontier.done != 0 || len(m.frontier.failed) != 0 || m.frontier.lastErr != nil {
		t.Errorf("stale cancellations changed the new seat: done=%d failed=%v err=%v",
			m.frontier.done, m.frontier.failed, m.frontier.lastErr)
	}
}

// A Provider that cannot report blocking links opens a screen that explains
// itself, and pays for no Detail read at all.
func TestFrontierWithoutTheBlockingLinksCapability(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture(), fake.WithCapabilities(model.Capabilities{
		Hierarchy: true, Comments: true, PullRequests: true,
		Selectors: model.SelectorCapabilities{Epic: true, RefList: true, Query: true},
	}))
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "does not report blocking links")

	m, got := s.finish(t)

	checkGolden(t, "frontier_no_blocking_capability.golden.txt", got)
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls = %d, want 0: a screen that cannot draw a graph must not pay for one", n)
	}
	for _, a := range m.frontier.graph.Members() {
		if a.Actionable || a.LinksKnown {
			t.Errorf("%s = %+v: the graph claims blocking links the Provider does not report",
				a.TicketID, a)
		}
	}
	if strings.Contains(string(got), "ACTIONABLE") {
		t.Errorf("the Frontier claimed something with no blocking links to read:\n%s", got)
	}
}

// Filters are a list gesture. Hiding a node here would delete an edge, which
// can make a blocked Ticket look Actionable — so the Frontier renders the
// complete Watchlist and says plainly that it is doing so.
func TestFrontierIgnoresListFilters(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("d"))
	s.tm.Send(keyPress("/"))
	s.typeText("queue")
	s.tm.Send(enterKey)
	s.waitFor(t, "filter:")

	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")
	s.tm.Send(keyPress("g"))

	m, got := s.finish(t)

	checkGolden(t, "frontier_filtered.golden.txt", got)
	if visible := len(m.visibleTickets()); visible >= len(m.input.Tickets) {
		t.Fatalf("the filter hid nothing (%d of %d), so this test proves nothing",
			visible, len(m.input.Tickets))
	}
	if len(m.frontier.input.Tickets) != len(m.input.Tickets) {
		t.Errorf("the Frontier drew %d of %d Tickets, want the whole Watchlist",
			len(m.frontier.input.Tickets), len(m.input.Tickets))
	}
	if !strings.Contains(string(got), "filters do not apply here") {
		t.Errorf("the footer does not say the filter is ignored:\n%s", got)
	}
}

// An overflow indicator says "there is more that way". Punched into a card's
// border it says that and also unmakes the card, so it takes the nearest blank
// cell on its edge instead — and where the edge is full it is dropped, because
// the footer's scroll position reports the same fact.
func TestFrontierOverflowGlyphsNeverOverwriteContent(t *testing.T) {
	// A canvas whose top row is entirely card border and whose bottom row has
	// exactly one blank cell, off the midpoint.
	full := make([]frontierCell, 8)
	for i := range full {
		full[i] = frontierCell{r: '─'}
	}
	bottom := append([]frontierCell(nil), full...)
	bottom[1] = frontierCell{r: ' '}
	l := frontierLayout{cells: [][]frontierCell{full, bottom, full}, width: 8, height: 3}

	got := frontierOverflowGlyphs(l, nil, 0, 1, 8, 2)

	if len(got) != 1 {
		t.Fatalf("placed %v, want only the ▲ the free cell can carry", got)
	}
	if glyph, ok := got[[2]int{1, 0}]; !ok || glyph != '▲' {
		t.Errorf("placed %v, want ▲ on the one blank cell at x=1", got)
	}
}

// A zero-width rune modifies the cell before it rather than claiming a column
// of its own. Dropping it silently rewrites the Tracker's text: "café" on a
// card must be the bytes the list row draws.
func TestFrontierCardsKeepCombiningMarks(t *testing.T) {
	// "cafe" plus a combining acute: the mark is zero-width and claims no
	// column of its own, so a width-counting canvas can silently discard it.
	const title = "café latency"
	tickets := []model.Ticket{{ID: "T-1", Key: "#1", Title: title, Status: model.StatusTodo}}
	m := frontierMouseModel(t, tickets, nil, 120, 30)

	if !strings.Contains(m.View().Content, title) {
		t.Errorf("the card dropped the combining mark:\n%s", m.View().Content)
	}
}

// jsonout walks members positionally because a Ref-list Watchlist may name the
// same Ticket twice. The Frontier is a graph, where one Ticket is one node: a
// second card would give nodeAt and columnOf two answers and one winner, so
// clicks and focus would reach a card the eye is not on.
func TestFrontierDrawsOneNodePerTicket(t *testing.T) {
	ticket := model.Ticket{ID: "T-1", Key: "#1", Title: "twice", Status: model.StatusTodo}
	m := frontierMouseModel(t, []model.Ticket{ticket, ticket}, nil, 120, 30)

	if n := len(m.frontier.layout.order); n != 1 {
		t.Errorf("the canvas carries %d nodes for one Ticket named twice: %v",
			n, m.frontier.layout.order)
	}
	if strings.Count(m.View().Content, "#1") != 1 {
		t.Errorf("the Ticket was drawn twice:\n%s", m.View().Content)
	}
}

// A canvas bigger than the terminal scrolls rather than reflowing: the card
// width is fixed, and the edges say where there is more.
func TestFrontierNarrowTerminalScrolls(t *testing.T) {
	const (
		width  = 60
		height = 20
	)
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	tm := teatest.NewTestModel(t, New(t.Context(), Options{
		Source:       selectorSource(p, c),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	}), teatest.WithInitialTermSize(width, height))
	s := &session{tm: tm, clock: c}
	openFrontier(t, s)

	tm.Send(keyPress("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitTimeout))
	m, ok := tm.FinalModel(t).(Model)
	if !ok {
		t.Fatalf("final model is %T, want tui.Model", tm.FinalModel(t))
	}
	content := m.View().Content
	got := frame(content)

	checkGolden(t, "frontier_narrow_60x20.golden.txt", got)
	if h := lipgloss.Height(content); h != height {
		t.Errorf("frame height = %d, want %d", h, height)
	}
	for _, line := range strings.Split(content, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line width = %d, want at most %d: %q", w, width, line)
		}
	}
	if !strings.Contains(string(got), "x ") {
		t.Errorf("no scroll position is reported on a canvas larger than the body:\n%s", got)
	}
	// The one-line footer is cut from the right, so the order it is written in
	// is the order it survives in. These three are what a reader needs when the
	// screen is not doing what they expected — and r is the only remedy for the
	// failure banner this frame draws directly above the footer. Asserted here
	// so a -update run cannot accept their loss silently.
	footer := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	last := footer[len(footer)-1]
	for _, want := range []string{"q quit", "r re-read Details", "? help"} {
		if !strings.Contains(last, want) {
			t.Errorf("the %d-column footer dropped %q: %q", width, want, last)
		}
	}
}

// Selection survives the toggle in both directions: it is a mode toggle over
// one Watchlist, not a navigation stack.
func TestFrontierSelectionFollowsTheListIn(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("j"))
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")

	m, _ := s.finish(t)

	if m.selectedID == "" {
		t.Fatal("nothing was selected on the list, so this test proves nothing")
	}
	if m.frontier.focusID != m.selectedID {
		t.Errorf("focus = %q, want the list's selection %q", m.frontier.focusID, m.selectedID)
	}
}

func TestFrontierSelectionFollowsTheFocusOut(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	// The list opens on #207, the first In Progress Ticket; g on the Frontier
	// focuses the first node in canonical order, which is #201.
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")
	s.tm.Send(keyPress("g"))
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "TODO")

	m, _ := s.finish(t)

	if m.mode != modeList {
		t.Fatalf("mode = %v, want the list", m.mode)
	}
	if want := m.input.Tickets[0].ID; m.selectedID != want {
		t.Errorf("list selection = %q, want the focused node %q", m.selectedID, want)
	}
}

// A node the list is currently filtering out leaves the list selection alone:
// moving it somewhere the user cannot see is worse than leaving it put.
func TestFrontierLeavesAFilteredOutSelectionAlone(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	// #205 is Done, so hiding finished Tickets takes it off the list, and the
	// list's own selection moves to a Ticket that is still visible.
	s.tm.Send(keyPress("d"))
	s.waitFor(t, "done+cancelled hidden")

	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")
	// g focuses #201; h crosses to the blocker side, where #205 is the nearest
	// node.
	s.tm.Send(keyPress("g"))
	s.tm.Send(keyPress("h"))
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "TODO")

	m, _ := s.finish(t)
	if m.frontier.focusID != "acme/widgets#205" {
		t.Fatalf("focus = %q, want the hidden Done Ticket #205", m.frontier.focusID)
	}
	if m.selectedID == "acme/widgets#205" {
		t.Error("the list selection jumped to a Ticket its own filter hides")
	}
}

// enter opens a member's Detail exactly as the list does, and esc comes back to
// the Frontier rather than to the list — the Frontier is a second rendering of
// the Watchlist, not a Trail entry.
func TestFrontierOpenReturnsToTheFrontier(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)
	s.waitFor(t, "nodes")

	m, _ := s.finish(t)
	if m.mode != modeFrontier {
		t.Errorf("esc from a Frontier-opened Detail landed in %v, want the Frontier", m.mode)
	}
}

func TestFrontierFanoutLivesThroughDetailAndRootEscape(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	frontierCalls := reads.batch(t, detailfanout.Parallelism)

	s.tm.Send(enterKey)
	detailCall := reads.next(t)
	assertFrontierCallsLive(t, frontierCalls...)
	detailCall.reply <- controlledFrontierReply{
		detail: model.Detail{TicketID: detailCall.id, Description: "FRONTIER DETAIL BODY"},
		caps:   model.Capabilities{BlockingLinks: true},
	}
	s.waitFor(t, "FRONTIER DETAIL BODY")
	assertFrontierCallsLive(t, frontierCalls...)
	s.tm.Send(escKey)
	s.waitFor(t, "nodes")
	assertFrontierCallsLive(t, frontierCalls...)

	m, _ := s.finish(t)
	assertFrontierCallsCanceled(t, frontierCalls...)
	if m.detailReturn != modeFrontier {
		t.Errorf("Detail return mode = %v, want Frontier", m.detailReturn)
	}
}

func TestHiddenFrontierFanoutIsCancelledByWalkUp(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	frontierCalls := reads.batch(t, detailfanout.Parallelism)
	s.tm.Send(enterKey)
	detailCall := reads.next(t)
	detailCall.reply <- controlledFrontierReply{
		detail: model.Detail{TicketID: detailCall.id, Description: "HIDDEN FRONTIER DETAIL"},
		caps:   model.Capabilities{BlockingLinks: true},
	}
	s.waitFor(t, "HIDDEN FRONTIER DETAIL")

	s.tm.Send(keyPress("u"))
	s.waitFor(t, "TODO")
	assertFrontierCallsCanceled(t, frontierCalls...)
	m, _ := s.finish(t)
	if got := reads.total.Load(); got != detailfanout.Parallelism+1 {
		t.Errorf("u issued %d total Detail reads, want the first Frontier batch plus opened Detail", got)
	}
	if m.mode != modeList {
		t.Errorf("u from hidden Frontier landed in %v, want list", m.mode)
	}
}

func TestQuitCancelsHiddenFrontierAndDetailReads(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	frontierCalls := reads.batch(t, detailfanout.Parallelism)
	s.tm.Send(enterKey)
	detailCall := reads.next(t)

	m, _ := s.finish(t)
	assertFrontierCallsCanceled(t, frontierCalls...)
	assertFrontierCallsCanceled(t, detailCall)
	if got := reads.total.Load(); got != detailfanout.Parallelism+1 {
		t.Errorf("quit issued %d total Detail reads, want the first Frontier batch plus opened Detail", got)
	}
	if !m.quitting {
		t.Error("q did not quit from a Frontier-opened Detail")
	}
}

// u is the walk-up: the Watchlist is genuinely up from both screens.
func TestFrontierWalkUpGoesToTheList(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(keyPress("u"))
	s.waitFor(t, "TODO")

	m, _ := s.finish(t)
	if m.mode != modeList {
		t.Errorf("u landed in %v, want the list", m.mode)
	}
}

// enter on a Ghost Ticket seats a deliberately thin Detail: a Link target never
// borrows rich fields from a list row, even one with the same identity.
func TestFrontierOpensAGhostTicketThin(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	s.tm.Send(keyPress("G"))
	s.waitFor(t, "GHOST")

	// G focuses the last node in canonical order, which is the last Ghost the
	// fixture's Links reach.
	const ghost = model.TicketID("acme/widgets#403")
	s.tm.Send(enterKey)
	s.waitFor(t, "Could not read this Ticket's detail")

	m, _ := s.finish(t)
	if m.detail.ticket.ID != ghost {
		t.Fatalf("seated %q, want the focused Ghost %q", m.detail.ticket.ID, ghost)
	}
	seat := m.detail.ticket
	if len(seat.Assignees) > 0 || len(seat.PullRequests) > 0 || seat.Repository != "" {
		t.Errorf("the Ghost's seat carries rich list fields: %+v", seat)
	}
}

// ADR-0003, both halves: a session that never presses v issues no Detail read
// at all, and pressing v issues at most one per uncached Ticket.
func TestFrontierIsTheOnlyBulkDetailRead(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	for range 3 {
		s.clock.advance(time.Minute)
		s.beat()
		waitUntil(t, "the refresh to land", func() bool { return p.ResolveCalls() >= 2 })
	}
	if n := p.DetailCalls(); n != 0 {
		t.Fatalf("DetailCalls = %d before v was pressed, want 0", n)
	}

	// Open one Ticket first, so its Detail is warm before the fan-out plans.
	// The list opens on #207, the first In Progress Ticket.
	const warm = model.TicketID("acme/widgets#207")
	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)
	s.waitFor(t, "TODO")

	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")
	m, _ := s.finish(t)

	if n := p.DetailCallsFor(warm); n != 1 {
		t.Errorf("DetailCallsFor(%s) = %d, want the one read that warmed the cache", warm, n)
	}
	if n := p.DetailCalls(); n != len(m.input.Tickets) {
		t.Errorf("DetailCalls = %d, want one per Ticket (%d) counting the warm one",
			n, len(m.input.Tickets))
	}
}

// r re-issues only the reads that never succeeded: re-reading a warm cache
// would turn a recovery key into a fan-out.
func TestFrontierRefreshOnlyRetriesWhatFailed(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	before := p.DetailCalls()

	s.tm.Send(keyPress("r"))
	waitUntil(t, "the retry of #211", func() bool {
		return p.DetailCallsFor("acme/widgets#211") == 2
	})
	m, _ := s.finish(t)

	// #211 has no fixture Detail, so it is the only read left to retry.
	if got := p.DetailCalls() - before; got != 1 {
		t.Errorf("r issued %d reads, want only the one that never succeeded", got)
	}
	if n := p.DetailCallsFor("acme/widgets#211"); n != 2 {
		t.Errorf("DetailCallsFor(#211) = %d, want the original read and the retry", n)
	}
	if !m.frontier.isResolved() {
		t.Error("the retry left the Frontier unresolved")
	}
}

// g and G stay Home and Last on the list and in Detail: the Frontier took v,
// not them.
func TestHomeAndEndKeepTheirMeaningOffTheFrontier(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("G"))
	s.tm.Send(keyPress("g"))

	m, _ := s.finish(t)
	if m.mode != modeList {
		t.Fatalf("g or G left the list, landing in %v", m.mode)
	}
	// The list's first row is the first Ticket of the first Status Category
	// group, which the fixture makes #207.
	if m.selectedID != "acme/widgets#207" {
		t.Errorf("g on the list selected %q, want the first row", m.selectedID)
	}
}

// G on the list is still Last.
func TestEndStillSelectsTheLastListRow(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := frontierSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("G"))

	m, _ := s.finish(t)
	if m.mode != modeList {
		t.Fatalf("G left the list, landing in %v", m.mode)
	}
	if m.selectedID != m.rows[len(m.rows)-1].Ticket.ID {
		t.Errorf("G selected %q, want the last row", m.selectedID)
	}
}

// The Frontier is behind the same terminal-text boundary as every other screen:
// it is seated through the funnels, and nothing hostile survives the crossing.
func TestFrontierSeatIsSanitized(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	in := hostileListInput(now)
	m := New(t.Context(), Options{Initial: &in, Now: func() time.Time { return now }})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
	m = updated.(Model)
	updated, _ = m.Update(keyPress("v"))
	m = updated.(Model)

	if m.mode != modeFrontier {
		t.Fatalf("mode = %v, want the Frontier", m.mode)
	}
	termtexttest.AssertClean(t, "m.frontier.input.Header", m.frontier.input.Header)
	for _, ticket := range m.frontier.input.Tickets {
		termtexttest.AssertClean(t, "m.frontier.input.Tickets", ticket,
			termtexttest.Exempt("ID", "ParentID"))
	}
	assertNoInjectedControls(t, "frontier frame", m.View().Content)
}

// Links arriving through the fan-out never crossed a seat funnel: they are
// written straight into frontier.input.Links from a DetailSource answer. The
// boundary has to hold on that path too, or the one screen that fetches in bulk
// is the one screen that does not sanitize.
func TestFrontierFanOutLinksAreSanitized(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	in := hostileListInput(now)
	detail := hostileDetail("T-1")
	if termtexttest.IsClean(detail, detailValueOptions...) {
		t.Fatal("the hostile Detail fixture carries nothing hostile")
	}
	m := New(t.Context(), Options{
		Initial: &in,
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			return hostileDetail(id), in.Capabilities, nil
		},
		Now: func() time.Time { return now },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
	m = updated.(Model)
	updated, cmd := m.Update(keyPress("v"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("the Frontier issued no fan-out, so this test proves nothing")
	}
	// Entering batches a repaint with the fan-out's fetch commands.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("entering the Frontier produced %T, want a batch of fetches", cmd())
	}
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		updated, _ = m.Update(sub())
		m = updated.(Model)
	}

	if len(m.frontier.input.Links) == 0 {
		t.Fatal("no fan-out answer reached the seat, so this test proves nothing")
	}
	for id, links := range m.frontier.input.Links {
		for _, link := range links {
			termtexttest.AssertClean(t, "m.frontier.input.Links["+string(id)+"]", link,
				termtexttest.Exempt("Target.ID"))
		}
	}
	assertNoInjectedControls(t, "frontier frame", m.View().Content)
}

// frontierMouseModel seats a small Watchlist through Options.Initial, opens the
// Frontier and answers its fan-out, so the mouse tests below act on a resolved
// canvas without a live program.
func frontierMouseModel(t *testing.T, tickets []model.Ticket,
	links map[model.TicketID][]model.Link, width, height int) Model {
	t.Helper()
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	in := ListInput{
		Header:       Header{Key: "#0", Title: "mice"},
		Tickets:      tickets,
		Capabilities: model.Capabilities{BlockingLinks: true},
		FetchedAt:    now,
	}
	m := New(t.Context(), Options{
		Initial: &in,
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			return model.Detail{TicketID: id, Links: links[id]}, in.Capabilities, nil
		},
		Now: func() time.Time { return now },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = updated.(Model)
	updated, _ = m.Update(keyPress("v"))
	m = updated.(Model)

	for _, ticket := range tickets {
		updated, _ = m.Update(frontierDetailMsg{
			generation: m.frontierGeneration,
			id:         ticket.ID,
			detail:     model.Detail{TicketID: ticket.ID, Links: links[ticket.ID]},
			caps:       in.Capabilities,
		})
		m = updated.(Model)
	}
	if !m.frontier.isResolved() {
		t.Fatal("the fan-out never resolved")
	}
	return m
}

// One cache, one answer. A fan-out read that failed leaves no Links key, so the
// card is UNVERIFIED and the footer counts it; opening that Ticket by hand fills
// the session cache, and coming back must adopt it. Otherwise the Frontier keeps
// claiming a read failed that has since succeeded, while the list — which reads
// the same cache directly — draws the Ticket as Actionable, and r is a silent
// no-op because there is genuinely nothing left to plan.
func TestFrontierAdoptsADetailFetchedByHand(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	tickets := []model.Ticket{
		{ID: "T-1", Key: "#1", Title: "first", Status: model.StatusTodo},
		{ID: "T-2", Key: "#2", Title: "second", Status: model.StatusTodo},
	}
	in := ListInput{
		Header:       Header{Key: "#0", Title: "adopt"},
		Tickets:      tickets,
		Capabilities: model.Capabilities{BlockingLinks: true},
		FetchedAt:    now,
	}
	m := New(t.Context(), Options{
		Initial: &in,
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			return model.Detail{TicketID: id}, in.Capabilities, nil
		},
		Now: func() time.Time { return now },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(keyPress("v"))
	m = updated.(Model)

	// The fan-out answers: #2 lands, #1 fails. The Frontier opens focused on
	// the list's selection, which is #1.
	updated, _ = m.Update(frontierDetailMsg{
		generation: m.frontierGeneration, id: "T-2",
		detail: model.Detail{TicketID: "T-2"}, caps: in.Capabilities,
	})
	m = updated.(Model)
	updated, _ = m.Update(frontierDetailMsg{
		generation: m.frontierGeneration, id: "T-1", err: errors.New("context deadline exceeded"),
	})
	m = updated.(Model)

	if !strings.Contains(m.View().Content, "UNVERIFIED") {
		t.Fatalf("the failed read left no UNVERIFIED card:\n%s", m.View().Content)
	}
	if m.frontier.focusID != "T-1" {
		t.Fatalf("focus = %q, want the Ticket whose read failed", m.frontier.focusID)
	}

	// Open it by hand, let the read land, and come back.
	updated, cmd := m.Update(keyPress("enter"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("opening the unread Ticket issued no fetch")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	updated, _ = m.Update(escKey)
	m = updated.(Model)

	if m.mode != modeFrontier {
		t.Fatalf("esc landed in %v, want the Frontier", m.mode)
	}
	frame := m.View().Content
	if strings.Contains(frame, "UNVERIFIED") {
		t.Errorf("the card is still unverified after its Detail was read by hand:\n%s", frame)
	}
	if strings.Contains(frame, "could not be read") {
		t.Errorf("the footer still reports a failed read:\n%s", frame)
	}
	if strings.Contains(frame, "context deadline exceeded") {
		t.Errorf("the footer still shows the stale error:\n%s", frame)
	}
	if _, seated := m.frontier.input.Links["T-1"]; !seated {
		t.Error("the Frontier's Links map still lacks the key the session cache holds")
	}
}

// Cancellation does not release a fan-out slot until its command returns, so
// the in-flight count lives on the Model: re-seating frontierState must not
// reset it to zero and let a second toggle start a second full-width batch.
func TestFrontierTogglesKeepTheFanOutBounded(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	release := make(chan struct{})
	source := blockedDetailSource(release, TicketDetailSource(p))
	var peak, live int
	var mu sync.Mutex
	counting := func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		mu.Lock()
		live++
		peak = max(peak, live)
		mu.Unlock()
		defer func() {
			mu.Lock()
			live--
			mu.Unlock()
		}()
		return source(ctx, id)
	}
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: counting,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")

	for range 3 {
		s.tm.Send(keyPress("v"))
		s.waitFor(t, "reading Detail")
		s.tm.Send(keyPress("v"))
		s.waitFor(t, "TODO")
	}
	close(release)
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")

	s.finish(t)

	mu.Lock()
	defer mu.Unlock()
	if peak > detailfanout.Parallelism {
		t.Errorf("peak concurrent reads = %d, want at most %d: a toggle started a second batch",
			peak, detailfanout.Parallelism)
	}
}

// A completed answer remains a real Detail when the rest of its generation is
// abandoned: it warms the session cache, and re-entry plans around it instead
// of asking the Tracker again. The abandoned seat's bookkeeping stays out.
func TestCompletedFrontierAnswerSurvivesLaterCancellation(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	calls := reads.batch(t, detailfanout.Parallelism)
	completed := calls[0]
	completed.reply <- controlledFrontierReply{
		detail: model.Detail{TicketID: completed.id},
		caps:   model.Capabilities{BlockingLinks: true},
	}
	s.waitFor(t, "reading Detail 1/13")
	// The bounded scheduler immediately replaces the completed slot while this
	// generation is still live; that replacement belongs to the old lifetime too.
	replacement := reads.next(t)

	s.tm.Send(escKey)
	s.waitFor(t, "TODO")
	assertFrontierCallsCanceled(t, calls[1:]...)
	assertFrontierCallsCanceled(t, replacement)
	s.tm.Send(keyPress("v"))
	newCalls := reads.batch(t, detailfanout.Parallelism)
	for _, call := range newCalls {
		if call.id == completed.id {
			t.Errorf("re-entered Frontier fetched cached Ticket %s again", completed.id)
		}
	}
	m, _ := s.finish(t)
	assertFrontierCallsCanceled(t, newCalls...)

	if !m.haveDetail(completed.id) {
		t.Errorf("completed Detail %s was discarded when its peers were cancelled", completed.id)
	}
	if m.frontier.done != 0 {
		t.Errorf("frontier.done = %d, want 0 before the new generation answers", m.frontier.done)
	}
	if got := reads.total.Load(); got != 2*detailfanout.Parallelism+1 {
		t.Errorf("issued reads = %d, want the old batch, its replacement, and one new bounded batch", got)
	}
}

func frontierMouse(t *testing.T, m Model, msg tea.MouseMsg) tea.Msg {
	t.Helper()
	handler := m.View().OnMouse
	if handler == nil {
		t.Fatal("the Frontier view has no mouse handler")
	}
	cmd := handler(msg)
	if cmd == nil {
		t.Fatal("the mouse event produced no domain message")
	}
	return cmd()
}

// A click focuses the node under the pointer; a second click on the same node
// opens its Ticket.
func TestFrontierClickFocusesAndDoubleClickOpens(t *testing.T) {
	tickets := []model.Ticket{
		{ID: "T-1", Key: "#1", Title: "first", Status: model.StatusTodo},
		{ID: "T-2", Key: "#2", Title: "second", Status: model.StatusTodo},
	}
	m := frontierMouseModel(t, tickets, nil, 120, 30)
	rect, drawn := m.frontier.layout.nodeAt["T-2"]
	if !drawn {
		t.Fatal("#2 is not on the canvas")
	}
	click := tea.MouseClickMsg{X: rect.X + 1, Y: headerHeight + rect.Y + 1, Button: tea.MouseLeft}

	updated, _ := m.Update(frontierMouse(t, m, click))
	m = updated.(Model)
	if m.frontier.focusID != "T-2" {
		t.Fatalf("focus = %q, want the clicked node", m.frontier.focusID)
	}

	updated, cmd := m.Update(frontierMouse(t, m, click))
	m = updated.(Model)
	if m.mode != modeDetail || m.detail.ticket.ID != "T-2" {
		t.Errorf("double-click seated %v/%q, want #2's Detail", m.mode, m.detail.ticket.ID)
	}
	// The fan-out already warmed this Ticket, so opening it costs the Tracker
	// nothing.
	if cmd != nil || !m.detail.loaded {
		t.Errorf("opening a Ticket the fan-out already read issued a command (%v, loaded %v)",
			cmd != nil, m.detail.loaded)
	}
}

// A modified click is a terminal gesture, not a selection: shift-drag has to
// keep selecting text.
func TestFrontierModifiedClickIsTransparent(t *testing.T) {
	tickets := []model.Ticket{{ID: "T-1", Key: "#1", Title: "first", Status: model.StatusTodo}}
	m := frontierMouseModel(t, tickets, nil, 120, 30)
	rect := m.frontier.layout.nodeAt["T-1"]

	handler := m.View().OnMouse
	if cmd := handler(tea.MouseClickMsg{
		X: rect.X + 1, Y: headerHeight + rect.Y + 1, Button: tea.MouseLeft, Mod: tea.ModShift,
	}); cmd != nil {
		t.Error("a shift-click produced a domain message")
	}
}

// The wheel scrolls the canvas rather than moving focus: a graph's rows are six
// lines tall, and moving focus by wheel would feel broken.
func TestFrontierWheelScrollsTheCanvas(t *testing.T) {
	tickets := make([]model.Ticket, 0, 10)
	for i := range 10 {
		key := "#" + string(rune('0'+i))
		tickets = append(tickets, model.Ticket{
			ID: model.TicketID(key), Key: key, Title: key, Status: model.StatusTodo,
		})
	}
	m := frontierMouseModel(t, tickets, nil, 120, 20)
	if m.frontier.layout.height <= m.frontierBodyHeight() {
		t.Fatal("the canvas fits the body, so there is nothing to scroll")
	}
	focus := m.frontier.focusID

	updated, _ := m.Update(frontierMouse(t, m,
		tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelDown}))
	m = updated.(Model)

	if m.frontier.offsetY != 3 {
		t.Errorf("offsetY = %d, want three lines down", m.frontier.offsetY)
	}
	if m.frontier.focusID != focus {
		t.Errorf("the wheel moved focus to %q", m.frontier.focusID)
	}
}

// blockingSnapshotSwapping returns the blocking fixture with one member dropped
// and one appended, which is the shape a refresh has to survive: a node
// arrives, a node leaves.
func blockingSnapshotSwapping(t *testing.T, drop model.TicketID, add model.Ticket) model.WatchlistSnapshot {
	t.Helper()
	s := fake.FixtureBlockingSnapshot()
	kept := make([]model.Ticket, 0, len(s.Tickets))
	for _, member := range s.Tickets {
		if member.ID != drop {
			kept = append(kept, member)
		}
	}
	if len(kept) == len(s.Tickets) {
		t.Fatalf("%s is not in the fixture, so this test proves nothing", drop)
	}
	s.Tickets = append(kept, add)
	return s
}

// addedMember is deliberately absent from FixtureBlockingDetails: a member a
// refresh introduces has no Links until somebody reads them.
var addedMember = model.Ticket{
	ID:           "acme/widgets#214",
	Key:          "#214",
	Title:        "Audit the rebalancer rollout",
	URL:          "https://tracker.example.test/acme/widgets/214",
	Status:       model.StatusTodo,
	NativeStatus: "open",
	Repository:   "acme/widgets",
}

func successfulFrontierMembers(t *testing.T, count int) ([]model.Ticket, map[model.TicketID]model.Detail) {
	t.Helper()
	fixtureDetails := fake.FixtureBlockingDetails()
	members := make([]model.Ticket, 0, count)
	details := make(map[model.TicketID]model.Detail, count)
	for _, ticket := range fake.FixtureBlockingSnapshot().Tickets {
		detail, ok := fixtureDetails[ticket.ID]
		if !ok {
			continue
		}
		// #204 points at a Ghost whose native status is unknown and therefore
		// legitimately renders UNVERIFIED even after its own Links were read. These
		// tests isolate an absent seat from that separate graph condition.
		if ticket.ID == "acme/widgets#204" {
			continue
		}
		members = append(members, ticket)
		details[ticket.ID] = detail
		if len(members) == count {
			break
		}
	}
	if len(members) != count {
		t.Fatalf("blocking fixture has %d successful members, want %d", len(members), count)
	}
	return members, details
}

// frontierWithDeferredFanout opens a real Frontier but deliberately does not
// execute its returned fetch commands. Tests can then model a Provider that
// finishes after cancellation by injecting the messages those commands own.
func frontierWithDeferredFanout(t *testing.T, members []model.Ticket,
	details map[model.TicketID]model.Detail) (Model, *fake.Provider, int) {
	t.Helper()
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	p := fake.New(fake.WithDetails(details))
	in := ListInput{
		Header:       Header{Key: "#93", Title: "late cache answers"},
		Tickets:      members,
		Capabilities: p.Capabilities(),
		FetchedAt:    now,
	}
	m := New(t.Context(), Options{
		Initial:      &in,
		DetailSource: TicketDetailSource(p),
		Now:          func() time.Time { return now },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	updated, cmd := m.Update(keyPress("v"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("opening the Frontier issued no command")
	}
	if want := min(len(members), detailfanout.Parallelism); m.detailFanoutInflight != want {
		t.Fatalf("in-flight reads = %d, want the deferred first batch of %d", m.detailFanoutInflight, want)
	}
	if calls := p.DetailCalls(); calls != 0 {
		t.Fatalf("DetailCalls = %d before deferred commands ran, want 0", calls)
	}
	return m, p, m.frontierGeneration
}

func TestFrontierResolutionRequiresFullSeatCoverage(t *testing.T) {
	first := model.Ticket{ID: "T-1"}
	second := model.Ticket{ID: "T-2"}
	blocking := model.Capabilities{BlockingLinks: true}

	for _, tt := range []struct {
		name     string
		tickets  []model.Ticket
		links    map[model.TicketID][]model.Link
		failed   map[model.TicketID]struct{}
		caps     model.Capabilities
		planned  int
		done     int
		resolved bool
	}{
		{
			name:     "every member has a Links key including zero Links",
			tickets:  []model.Ticket{first, second},
			links:    map[model.TicketID][]model.Link{first.ID: nil, second.ID: {}},
			caps:     blocking,
			resolved: true,
		},
		{
			name:     "a recorded failure covers its member",
			tickets:  []model.Ticket{first, second},
			links:    map[model.TicketID][]model.Link{first.ID: nil},
			failed:   map[model.TicketID]struct{}{second.ID: {}},
			caps:     blocking,
			resolved: true,
		},
		{
			name:    "plan completion does not cover an omitted member",
			tickets: []model.Ticket{first, second},
			links:   map[model.TicketID][]model.Link{first.ID: nil},
			caps:    blocking,
			planned: 1,
			done:    1,
		},
		{
			name:     "duplicate rows add no obligation",
			tickets:  []model.Ticket{first, first},
			links:    map[model.TicketID][]model.Link{first.ID: nil},
			caps:     blocking,
			resolved: true,
		},
		{
			name:     "empty Watchlist is terminal",
			caps:     blocking,
			resolved: true,
		},
		{
			name:     "Capability absent is terminal",
			tickets:  []model.Ticket{first},
			resolved: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frontier := frontierState{
				input: FrontierInput{
					Tickets:      tt.tickets,
					Links:        tt.links,
					Capabilities: tt.caps,
				},
				failed:  tt.failed,
				planned: tt.planned,
				done:    tt.done,
			}
			if got := frontier.isResolved(); got != tt.resolved {
				t.Errorf("isResolved() = %v, want %v", got, tt.resolved)
			}
		})
	}
}

func TestFrontierPlanSettlementReleasesContextWithoutResolvingAnUncoveredSeat(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := Model{
		frontierContext: ctx,
		cancelFrontier:  cancel,
		frontier: frontierState{
			input: FrontierInput{
				Tickets: []model.Ticket{{ID: "T-seated"}, {ID: "T-uncovered"}},
				Links: map[model.TicketID][]model.Link{
					"T-seated": nil,
				},
				Capabilities: model.Capabilities{BlockingLinks: true},
			},
			planned: 1,
			done:    1,
		},
	}

	m = m.settleFrontier()

	if m.frontierContext != nil || m.cancelFrontier != nil {
		t.Error("settled plan retained its child context")
	}
	select {
	case <-ctx.Done():
	case <-time.After(waitTimeout):
		t.Fatal("settled plan did not cancel its child context")
	}
	if m.frontier.isResolved() {
		t.Error("plan-local completion resolved a seat with an uncovered member")
	}
}

func TestFrontierResolvedHeaderMatchesListAndRefreshRepairsAStaleSeat(t *testing.T) {
	members, details := successfulFrontierMembers(t, 3)
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	p := fake.New(fake.WithDetails(details))
	in := ListInput{
		Header:       Header{Key: "#104", Title: "resolution truth"},
		Tickets:      members,
		Capabilities: p.Capabilities(),
		FetchedAt:    now,
	}
	m := New(t.Context(), Options{
		Initial:      &in,
		DetailSource: TicketDetailSource(p),
		Now:          func() time.Time { return now },
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	updated, _ = m.Update(keyPress("v"))
	m = updated.(Model)
	for _, ticket := range members {
		updated, _ = m.Update(frontierDetailMsg{
			generation: m.frontierGeneration,
			id:         ticket.ID,
			detail:     details[ticket.ID],
			caps:       in.Capabilities,
		})
		m = updated.(Model)
	}

	markers := m.listMarkers()
	if !markers.active || !m.frontier.isResolved() {
		t.Fatalf("complete cache/seat is not resolved and list-active: markers=%+v resolved=%v",
			markers, m.frontier.isResolved())
	}
	wantTally := fmt.Sprintf("%s%d actionable", separator, markers.count)
	if header := m.frontierHeader(); !strings.Contains(header, wantTally) {
		t.Fatalf("resolved Frontier header does not agree with list count %d:\n%s", markers.count, header)
	}

	missing := members[0].ID
	delete(m.frontier.input.Links, missing)
	m = m.rebuildFrontier()
	if !m.listMarkers().active {
		t.Fatal("removing a seated key also cooled the independent session cache")
	}
	if m.frontier.isResolved() {
		t.Fatal("a member absent from the seat remained resolved")
	}
	if header := m.frontierHeader(); strings.Contains(header, " actionable") {
		t.Fatalf("unresolved Frontier published an Actionable tally:\n%s", header)
	}

	epoch := m.mouseEpoch
	before := p.DetailCalls()
	updated, cmd := m.Update(keyPress("r"))
	m = updated.(Model)
	if !carriesClearScreen(cmd) {
		t.Fatal("repairing the stale seat returned no full repaint")
	}
	if got := p.DetailCalls(); got != before {
		t.Fatalf("repair fetched %d cached Details, want 0", got-before)
	}
	if _, seated := m.frontier.input.Links[missing]; !seated {
		t.Fatalf("r did not adopt cached member %s", missing)
	}
	if m.mouseEpoch == epoch {
		t.Fatal("repair changed the seat without invalidating captured mouse callbacks")
	}
	markers = m.listMarkers()
	if !markers.active || !m.frontier.isResolved() {
		t.Fatalf("repaired cache/seat is not resolved and list-active: markers=%+v resolved=%v",
			markers, m.frontier.isResolved())
	}
	wantTally = fmt.Sprintf("%s%d actionable", separator, markers.count)
	if header := m.frontierHeader(); !strings.Contains(header, wantTally) {
		t.Fatalf("repaired Frontier header does not agree with list count %d:\n%s", markers.count, header)
	}
}

func TestFrontierRefreshAdoptsEveryLateCachedAnswerWithoutFetching(t *testing.T) {
	members, details := successfulFrontierMembers(t, detailfanout.Parallelism)
	m, p, oldGeneration := frontierWithDeferredFanout(t, members, details)
	focus := m.frontier.focusID

	m, _ = m.reseatFrontier()
	if m.frontierGeneration == oldGeneration {
		t.Fatal("reseating the Frontier did not retire its fan-out generation")
	}
	for _, ticket := range members {
		updated, _ := m.onFrontierDetail(frontierDetailMsg{
			generation: oldGeneration,
			id:         ticket.ID,
			detail:     details[ticket.ID],
			caps:       m.frontier.input.Capabilities,
		})
		m = updated.(Model)
		if _, cached := m.details[ticket.ID]; !cached {
			t.Errorf("late answer for %s was not cached", ticket.ID)
		}
		if _, seated := m.frontier.input.Links[ticket.ID]; seated {
			t.Errorf("stale-generation answer for %s mutated the replacement seat", ticket.ID)
		}
	}
	if m.detailFanoutInflight != 0 {
		t.Fatalf("shared in-flight reads = %d after every stale answer landed, want 0", m.detailFanoutInflight)
	}
	if m.frontier.isResolved() {
		t.Fatal("the pre-r frame resolved a replacement seat with no Links")
	}
	if header := m.frontierHeader(); !strings.Contains(header, "reading Detail 0/0") || strings.Contains(header, " actionable") {
		t.Fatalf("the pre-r frame does not expose the stranded seat as incomplete:\n%s", header)
	}
	staleTarget := members[0].ID
	if staleTarget == focus {
		staleTarget = members[1].ID
	}
	staleClick := frontierMouseClickMsg{epoch: m.mouseEpoch, id: staleTarget}

	before := p.DetailCalls()
	updated, cmd := m.Update(keyPress("r"))
	m = updated.(Model)
	if !carriesClearScreen(cmd) {
		t.Fatal("r adopted cached Links without returning a full repaint")
	}
	if got := p.DetailCalls(); got != before {
		t.Fatalf("r issued %d Provider calls on a cache-complete Frontier, want 0", got-before)
	}
	if m.mouseEpoch == staleClick.epoch {
		t.Fatal("adoption relaid the Frontier without retiring callbacks captured against the old layout")
	}
	updated, _ = m.Update(staleClick)
	m = updated.(Model)
	if m.frontier.focusID != focus {
		t.Errorf("stale click focused %s after adoption, want %s to remain focused", m.frontier.focusID, focus)
	}
	for _, ticket := range members {
		if _, seated := m.frontier.input.Links[ticket.ID]; !seated {
			t.Errorf("cached answer for %s was not seated by r", ticket.ID)
		}
		actionability, member := m.frontier.graph.For(ticket.ID)
		if !member || !actionability.LinksKnown {
			t.Errorf("rebuilt graph still treats %s as unverified: member=%v actionable=%+v",
				ticket.ID, member, actionability)
		}
	}
	if frame := m.View().Content; strings.Contains(frame, "UNVERIFIED") {
		t.Fatalf("the post-r frame still strands a successfully read Ticket:\n%s", frame)
	}
	if m.frontier.focusID != focus || !m.frontier.hasFocus {
		t.Errorf("focus = %q/%v after adoption, want %q/true", m.frontier.focusID, m.frontier.hasFocus, focus)
	}
	if _, visible := m.frontier.layout.nodeAt[focus]; !visible {
		t.Errorf("focused Ticket %s is absent from the rebuilt layout", focus)
	}
}

func TestFrontierRefreshAdoptsLateBatchBeforePlanningRemainder(t *testing.T) {
	members, details := successfulFrontierMembers(t, detailfanout.Parallelism+1)
	m, p, oldGeneration := frontierWithDeferredFanout(t, members, details)
	m, _ = m.reseatFrontier()

	for _, ticket := range members[:detailfanout.Parallelism] {
		updated, _ := m.onFrontierDetail(frontierDetailMsg{
			generation: oldGeneration,
			id:         ticket.ID,
			detail:     details[ticket.ID],
			caps:       m.frontier.input.Capabilities,
		})
		m = updated.(Model)
	}
	last := members[len(members)-1].ID
	for _, ticket := range members[:detailfanout.Parallelism] {
		if _, cached := m.details[ticket.ID]; !cached {
			t.Errorf("late answer for %s is not cached", ticket.ID)
		}
		if _, seated := m.frontier.input.Links[ticket.ID]; seated {
			t.Errorf("late answer for %s entered the replacement seat before r", ticket.ID)
		}
	}
	if _, cached := m.details[last]; cached {
		t.Fatalf("remainder %s is already cached, so retry planning proves nothing", last)
	}

	updated, cmd := m.Update(keyPress("r"))
	m = updated.(Model)
	for _, ticket := range members[:detailfanout.Parallelism] {
		if _, seated := m.frontier.input.Links[ticket.ID]; !seated {
			t.Errorf("r did not seat cached first-batch member %s before fetching", ticket.ID)
		}
	}
	if m.frontier.planned != 1 || m.frontier.done != 0 || len(m.frontier.queued) != 0 || m.detailFanoutInflight != 1 {
		t.Fatalf("retry state = planned %d done %d queued %d in-flight %d, want one issued remainder",
			m.frontier.planned, m.frontier.done, len(m.frontier.queued), m.detailFanoutInflight)
	}
	if calls := p.DetailCalls(); calls != 0 {
		t.Fatalf("DetailCalls = %d before the retry command ran, want 0", calls)
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("r returned %T, want a repaint and one Detail fetch", msg)
	}
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		updated, _ = m.Update(sub())
		m = updated.(Model)
	}
	if calls := p.DetailCalls(); calls != 1 {
		t.Fatalf("r issued %d Provider calls, want the one genuine cache miss", calls)
	}
	for _, ticket := range members[:detailfanout.Parallelism] {
		if calls := p.DetailCallsFor(ticket.ID); calls != 0 {
			t.Errorf("cached member %s was fetched %d times, want 0", ticket.ID, calls)
		}
	}
	if calls := p.DetailCallsFor(last); calls != 1 {
		t.Errorf("remainder %s was fetched %d times, want 1", last, calls)
	}
	if !m.frontier.isResolved() {
		t.Error("the one-Ticket retry left the Frontier unresolved")
	}
	for _, ticket := range members {
		if _, cached := m.details[ticket.ID]; !cached {
			t.Errorf("final cache is missing %s", ticket.ID)
		}
		if _, seated := m.frontier.input.Links[ticket.ID]; !seated {
			t.Errorf("final seat is missing %s", ticket.ID)
		}
	}
	if frame := m.View().Content; strings.Contains(frame, "UNVERIFIED") {
		t.Fatalf("the resolved frame still strands a successful read:\n%s", frame)
	}
}

func TestFrontierRefreshReconcilesBeforeEveryGuard(t *testing.T) {
	cached := model.Ticket{ID: "T-cached", Key: "T-cached", Title: "cached", Status: model.StatusTodo}
	missing := model.Ticket{ID: "T-missing", Key: "T-missing", Title: "missing", Status: model.StatusTodo}
	detail := model.Detail{TicketID: cached.ID}

	for _, tt := range []struct {
		name          string
		blockingLinks bool
		cacheMissing  bool
		inflight      int
		queued        bool
		preserveWork  bool
	}{
		{name: "Capability absent"},
		{name: "empty plan", blockingLinks: true, cacheMissing: true},
		{name: "shared slot busy", blockingLinks: true, inflight: 1, preserveWork: true},
		{name: "retry queue active", blockingLinks: true, queued: true, preserveWork: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
			allDetails := map[model.TicketID]model.Detail{
				cached.ID:  detail,
				missing.ID: {TicketID: missing.ID},
			}
			p := fake.New(fake.WithDetails(allDetails))
			in := ListInput{
				Header:       Header{Key: "#93", Title: tt.name},
				Tickets:      []model.Ticket{cached, missing},
				Capabilities: model.Capabilities{BlockingLinks: tt.blockingLinks},
				FetchedAt:    now,
			}
			m := New(t.Context(), Options{
				Initial:      &in,
				DetailSource: TicketDetailSource(p),
				Now:          func() time.Time { return now },
			})
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			m = updated.(Model)
			m.mode = modeFrontier
			m.frontier = frontierState{
				input:   FrontierFromList(in, nil),
				focusID: cached.ID,
			}
			m.details[cached.ID] = detailEntry{detail: detail, caps: in.Capabilities, fetchedAt: now}
			if tt.cacheMissing {
				m.details[missing.ID] = detailEntry{detail: allDetails[missing.ID], caps: in.Capabilities, fetchedAt: now}
			}
			m = m.rebuildFrontier()
			if actionability, member := m.frontier.graph.For(cached.ID); tt.blockingLinks && (!member || actionability.LinksKnown) {
				t.Fatalf("cached-but-unseated precondition failed: member=%v actionable=%+v", member, actionability)
			}

			m.frontierGeneration = 7
			m.detailFanoutInflight = tt.inflight
			if tt.queued {
				m.frontier.queued = []model.TicketID{missing.ID}
				m.frontier.planned = 1
			}
			if tt.preserveWork {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				m.frontierContext = ctx
				m.cancelFrontier = cancel
			}
			generation := m.frontierGeneration
			ctx := m.frontierContext
			queue := slices.Clone(m.frontier.queued)
			planned, done := m.frontier.planned, m.frontier.done

			updated, cmd := m.refreshFrontier()
			m = updated.(Model)
			if !carriesClearScreen(cmd) {
				t.Fatal("guard returned without repainting the reconciled seat")
			}
			if calls := p.DetailCalls(); calls != 0 {
				t.Fatalf("guard issued %d Provider calls, want 0", calls)
			}
			if _, seated := m.frontier.input.Links[cached.ID]; !seated {
				t.Error("guard ran before adopting the cached member")
			}
			if _, drawn := m.frontier.layout.nodeAt[cached.ID]; !drawn {
				t.Error("guard returned before rebuilding the Frontier layout")
			}
			if tt.blockingLinks {
				actionability, member := m.frontier.graph.For(cached.ID)
				if !member || !actionability.LinksKnown {
					t.Errorf("rebuilt graph did not credit the cached member: member=%v actionable=%+v",
						member, actionability)
				}
			}
			if tt.preserveWork {
				if m.frontierGeneration != generation || m.frontierContext != ctx || m.cancelFrontier == nil {
					t.Errorf("guard replaced active work: generation %d/%d context %v/%v cancel-nil=%v",
						m.frontierGeneration, generation, m.frontierContext, ctx, m.cancelFrontier == nil)
				}
				if !slices.Equal(m.frontier.queued, queue) || m.frontier.planned != planned ||
					m.frontier.done != done {
					t.Errorf("guard changed queue bookkeeping: queued=%v/%v planned=%d/%d done=%d/%d",
						m.frontier.queued, queue, m.frontier.planned, planned, m.frontier.done, done)
				}
			}
		})
	}
}

// ADR-0003 Amendment 4: a whole-Watchlist Detail fan-out is permitted only in
// response to an explicit user action. The --interval heartbeat is not one, so
// a refresh landing with the Frontier open re-seats the screen and issues
// nothing — a member the refresh introduced keeps the seat unresolved until r.
func TestFrontierRefreshIssuesNoDetailFanout(t *testing.T) {
	after := blockingSnapshotSwapping(t, "acme/widgets#213", addedMember)
	p := fake.New(fake.WithSnapshots(fake.FixtureBlockingSnapshot(), after),
		fake.WithDetails(fake.FixtureBlockingDetails()))
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)

	fetched := p.DetailCalls()
	if fetched == 0 {
		t.Fatal("the fan-out fetched nothing, so a fan-out on refresh would be invisible here")
	}

	s.clock.advance(61 * time.Second)
	s.beat()
	s.waitFor(t, "reading Detail 0/0")
	m, got := s.finish(t)

	if n := p.DetailCalls(); n != fetched {
		t.Errorf("DetailCalls = %d, want the fan-out's %d: a refresh must fan out no Detail", n, fetched)
	}
	if _, drawn := m.frontier.layout.nodeAt[addedMember.ID]; !drawn {
		t.Error("the member the refresh added is not on the canvas")
	}
	if _, drawn := m.frontier.layout.nodeAt["acme/widgets#213"]; drawn {
		t.Error("the member the refresh removed is still on the canvas")
	}
	if _, seated := m.frontier.input.Links[addedMember.ID]; seated {
		t.Error("the added member has Links, so something fetched them off a timer")
	}
	if m.frontier.isResolved() {
		t.Error("the re-seated Frontier resolved with an uncovered member")
	}
	header := m.frontierHeader()
	if strings.Contains(header, " actionable") {
		t.Errorf("the partial Frontier published an Actionable tally:\n%s", header)
	}
	if !strings.Contains(header, "reading Detail 0/0") {
		t.Errorf("the unresolved re-seat does not identify itself as incomplete:\n%s", header)
	}
	frame := string(got)
	for _, badge := range []string{"ACTIONABLE", "blocked by"} {
		if strings.Contains(frame, badge) {
			t.Errorf("the partial Frontier published resolved emphasis %q:\n%s", badge, frame)
		}
	}
}

// The re-seat preserves where the reader was. A Watchlist that changed shape
// under them must not also move the window and the focus.
func TestFrontierRefreshKeepsFocusAndOffsets(t *testing.T) {
	// #212 leaves rather than #213, so the node G lands on survives the refresh
	// and preserving focus is what the assertion below can see.
	after := blockingSnapshotSwapping(t, "acme/widgets#212", addedMember)
	p := fake.New(fake.WithSnapshots(fake.FixtureBlockingSnapshot(), after),
		fake.WithDetails(fake.FixtureBlockingDetails()))
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	s.tm.Send(keyPress("G"))

	s.clock.advance(61 * time.Second)
	s.beat()
	s.waitFor(t, "reading Detail 0/0")
	m, _ := s.finish(t)

	if m.frontier.focusID != "acme/widgets#403" {
		t.Errorf("focus = %q after the refresh, want the node G left it on", m.frontier.focusID)
	}
	if m.frontier.offsetX == 0 && m.frontier.offsetY == 0 {
		t.Fatal("the window sat at the canvas origin, so preserving the offsets proves nothing")
	}
	if _, drawn := m.frontier.layout.nodeAt[m.frontier.focusID]; !drawn {
		t.Errorf("focus = %q after the refresh, which is not a node on the canvas", m.frontier.focusID)
	}
}

// A refresh reseat cancels reads issued for the previous Watchlist instead of
// merely dropping their answers. Cancelled reads neither warm the cache nor
// become failures on the replacement seat.
func TestFrontierRefreshCancelsInFlightAnswers(t *testing.T) {
	// Nothing leaves here: with every read still held there are no Links and so
	// no Ghosts, and one added node is the only thing that moves the counts.
	after := fake.FixtureBlockingSnapshot()
	after.Tickets = append(after.Tickets, addedMember)
	p := fake.New(fake.WithSnapshots(fake.FixtureBlockingSnapshot(), after),
		fake.WithDetails(fake.FixtureBlockingDetails()))
	c := newClock()
	reads := newControlledFrontierReads()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: reads.source,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	calls := reads.batch(t, detailfanout.Parallelism)
	s.waitFor(t, "reading Detail")

	s.clock.advance(61 * time.Second)
	s.beat()
	s.waitFor(t, "14 nodes")
	assertFrontierCallsCanceled(t, calls...)
	m, _ := s.finish(t)

	if m.frontier.done != 0 {
		t.Errorf("the re-seated Frontier counted %d answers from the reading it replaced", m.frontier.done)
	}
	if len(m.frontier.input.Links) != 0 {
		t.Errorf("the re-seated Frontier seated %d Links from the reading it replaced", len(m.frontier.input.Links))
	}
	if len(m.details) != 0 {
		t.Errorf("cancelled reads warmed %d Detail cache entries", len(m.details))
	}
	if got := reads.total.Load(); got != detailfanout.Parallelism {
		t.Errorf("interval refresh issued %d reads, want only the original batch of %d",
			got, detailfanout.Parallelism)
	}
	if len(m.frontier.failed) != 0 || m.frontier.lastErr != nil {
		t.Errorf("cancellation became a Frontier failure: failed=%v err=%v", m.frontier.failed, m.frontier.lastErr)
	}
}

// An exact Ref list may name one Ticket twice. The canvas draws one card for
// it, so a header counting the graph's positional member rows would claim more
// Actionable Tickets than there are cards to point at.
func TestFrontierHeaderCountsCardsNotMemberRows(t *testing.T) {
	snap := fake.FixtureBlockingSnapshot()
	snap.Tickets = append(snap.Tickets, snap.Tickets[0])
	p := fake.New(fake.WithSnapshot(snap), fake.WithDetails(fake.FixtureBlockingDetails()))
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	m, got := s.finish(t)

	want := "16 nodes" + separator + "3 ghosts" + separator + "3 actionable"
	if !strings.Contains(string(got), want) {
		t.Errorf("the header does not read %q:\n%s", want, string(got))
	}
	if n := len(m.frontier.layout.order); n != 16 {
		t.Errorf("the canvas drew %d nodes, so the count above is asserting the wrong thing", n)
	}
}

// Adoption credits the Ticket it seated and nothing else. With more members
// than detailfanout.Parallelism a Ticket can still be queued when its Detail is
// read by hand, and a bare failure count cannot tell which Ticket it is
// clearing: it would absolve one that really did fail, dropping the footer's
// count and its "press r to retry" line while that card still says UNVERIFIED.
//
// The keyboard cannot reach this interleaving deterministically, so it is
// driven through the state the two paths share.
func TestAdoptCachedLinksCreditsOnlyTheTicketItSeats(t *testing.T) {
	const failed = model.TicketID("acme/widgets#211")
	const queued = model.TicketID("acme/widgets#212")
	m := Model{
		details: map[model.TicketID]detailEntry{
			queued: {detail: model.Detail{TicketID: queued}},
		},
		frontier: frontierState{
			input: FrontierInput{
				Tickets:      []model.Ticket{{ID: failed}, {ID: queued}},
				Capabilities: model.Capabilities{BlockingLinks: true},
			},
			failed:  map[model.TicketID]struct{}{failed: {}},
			lastErr: errors.New("tracker said no"),
		},
	}

	m = m.adoptCachedLinks()

	if _, seated := m.frontier.input.Links[queued]; !seated {
		t.Error("the hand-read Ticket's Links were not adopted")
	}
	if _, still := m.frontier.failed[failed]; !still {
		t.Errorf("adopting %s cleared the failure for %s, which never succeeded", queued, failed)
	}
	if m.frontier.lastErr == nil {
		t.Error("the retry line went away while a Ticket's Links are still unread")
	}

	// Seating the Ticket that actually failed is what clears it, and emptying
	// the set is what clears the error.
	m.details[failed] = detailEntry{detail: model.Detail{TicketID: failed}}
	m = m.adoptCachedLinks()
	if len(m.frontier.failed) != 0 {
		t.Errorf("failed = %v after every Ticket was seated, want empty", m.frontier.failed)
	}
	if m.frontier.lastErr != nil {
		t.Error("the retry line survived every Ticket being read")
	}
}

// The Frontier's expanded help is the only place its graph vocabulary is
// written down, and nothing rendered it. Two sizes: a normal terminal, and one
// small enough that model.go's keys.(KeyMap) type assertion fails and a
// FrontierKeyMap goes down the generic stacking branch instead of the list's
// compact one.
func TestFrontierExpandedHelp(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		golden        string
		// wants is the graph vocabulary the panel has room for. A terminal too
		// short for the whole listing still has to name the keys it does show
		// in the Frontier's own words.
		wants []string
	}{
		{"normal", 120, 40, "frontier_help_120x40.golden.txt", []string{
			"previous node", "next node", "blocker side", "dependent side",
			"first node", "last node", "v/esc", "open Ticket", "re-read Details",
		}},
		{"narrow", 42, 28, "frontier_help_42x28.golden.txt", []string{
			"v/esc", "open Ticket", "re-read Details", "select node",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := fake.New(fake.WithBlockingFixture())
			c := newClock()
			tm := teatest.NewTestModel(t, New(t.Context(), Options{
				Source:       selectorSource(p, c),
				DetailSource: TicketDetailSource(p),
				Interval:     time.Minute,
				Now:          c.now,
			}), teatest.WithInitialTermSize(tt.width, tt.height))
			s := &session{tm: tm, clock: c}
			s.waitFor(t, "Shard rebalancer rollout")
			tm.Send(keyPress("v"))
			// The counts line, not the word "actionable": at 42 columns the
			// header is clipped before it reaches that far.
			s.waitFor(t, "16 nodes")
			tm.Send(keyPress("?"))
			s.waitFor(t, "re-read Details")

			tm.Send(keyPress("q"))
			tm.WaitFinished(t, teatest.WithFinalTimeout(waitTimeout))
			m, ok := tm.FinalModel(t).(Model)
			if !ok {
				t.Fatalf("final model is %T, want tui.Model", tm.FinalModel(t))
			}
			content := m.View().Content
			got := frame(content)

			checkGolden(t, tt.golden, got)
			if h := lipgloss.Height(content); h != tt.height {
				t.Errorf("frame height = %d, want %d", h, tt.height)
			}
			for _, line := range strings.Split(content, "\n") {
				if w := lipgloss.Width(line); w > tt.width {
					t.Errorf("line width = %d, want at most %d: %q", w, tt.width, line)
				}
			}
			// The graph vocabulary, which is what this panel is for: the four
			// movement axes say what they move over, and esc is named beside v.
			for _, want := range tt.wants {
				if !strings.Contains(string(got), want) {
					t.Errorf("the expanded help omits %q:\n%s", want, got)
				}
			}
		})
	}
}

// r on a fully warm Frontier issues nothing. Re-reading a cache that already
// covers the Watchlist would turn a recovery key into a whole-Watchlist
// fan-out, which is the ADR-0003 Amendment 4 protection this guard exists for.
func TestFrontierRefreshOnAWarmFrontierIssuesNothing(t *testing.T) {
	// Every member has a Detail, so one fan-out leaves nothing outstanding --
	// the fixture deliberately omits #211's.
	details := fake.FixtureBlockingDetails()
	details["acme/widgets#211"] = model.Detail{TicketID: "acme/widgets#211"}
	p := fake.New(fake.WithSnapshot(fake.FixtureBlockingSnapshot()), fake.WithDetails(details))
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	warm := p.DetailCalls()
	if warm != len(fake.FixtureBlockingSnapshot().Tickets) {
		t.Fatalf("DetailCalls = %d, want one per member: the cache is not warm", warm)
	}

	s.tm.Send(keyPress("r"))
	s.tm.Send(keyPress("G"))
	s.waitFor(t, "GHOST")
	m, _ := s.finish(t)

	if n := p.DetailCalls(); n != warm {
		t.Errorf("r issued %d reads on a warm Frontier, want none", n-warm)
	}
	if !m.frontier.isResolved() {
		t.Error("r left a warm Frontier unresolved")
	}
}

// r while a fan-out is in flight issues nothing either: the reads it would
// re-issue are the reads already running, and a second batch behind the first
// is the fan-out paid for twice.
func TestFrontierRefreshDuringAFanOutIssuesNothing(t *testing.T) {
	release := make(chan struct{})
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	// The reads are held before they reach the Provider, so the Provider's own
	// counter never moves: this counts what the screen issued.
	var issued atomic.Int64
	held := blockedDetailSource(release, TicketDetailSource(p))
	counting := func(ctx context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
		issued.Add(1)
		return held(ctx, id)
	}
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: counting,
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "reading Detail")
	waitUntil(t, "the first batch to be issued", func() bool {
		return issued.Load() == detailfanout.Parallelism
	})

	// Key presses are delivered in order and handled synchronously, so r has
	// been through Update by the time q ends the session.
	s.tm.Send(keyPress("r"))
	m, _ := s.finish(t)
	close(release)

	if n := issued.Load(); n != detailfanout.Parallelism {
		t.Errorf("issued %d reads, want the %d already in flight: r started a second batch",
			n, detailfanout.Parallelism)
	}
	// quit retires the generation and clears its unissued queue. r must not have
	// started a second batch before that retirement.
	if got := len(m.frontier.queued); got != 0 {
		t.Errorf("queued = %d after quit, want abandoned work cleared", got)
	}
}

// Every movement binding is dispatched through a real key press, so swapping
// two cases in onFrontierKey's switch fails here. moveFocus itself is unit
// tested; the wiring from binding to direction was not.
func TestFrontierMovementKeysReachTheirDirections(t *testing.T) {
	// Three nodes: #3 blocks both #1 and #2, so #3 is alone on the blocker side
	// and #1 sits above #2 on the dependent side.
	tickets := []model.Ticket{
		blockingTicket("#1", model.StatusTodo),
		blockingTicket("#2", model.StatusTodo),
		blockingTicket("#3", model.StatusTodo),
	}
	links := map[model.TicketID][]model.Link{
		"#1": blockedBy("#3"), "#2": blockedBy("#3"), "#3": nil,
	}

	seat := func(t *testing.T, focus model.TicketID) Model {
		t.Helper()
		m := frontierMouseModel(t, tickets, links, 120, 40)
		m.frontier.focusID = focus
		m.frontier.hasFocus = true
		return m
	}
	press := func(t *testing.T, m Model, key tea.KeyPressMsg) model.TicketID {
		t.Helper()
		next, _ := m.onFrontierKey(key)
		return next.(Model).frontier.focusID
	}

	// The layout the assertions below read: #3 alone on the blocker side, #1
	// above #2 on the dependent side.
	l := seat(t, "#1").frontier.layout
	if l.columnOf["#3"] != 0 || l.columnOf["#1"] != 1 || l.columnOf["#2"] != 1 {
		t.Fatalf("columns = %v, want #3 left of #1 and #2", l.columnOf)
	}
	if l.nodeAt["#1"].Y >= l.nodeAt["#2"].Y {
		t.Fatalf("#1 is not above #2, so up and down assert nothing")
	}

	for _, tt := range []struct {
		name string
		from model.TicketID
		key  tea.KeyPressMsg
		want model.TicketID
		axis string
	}{
		{"down", "#1", keyPress("j"), "#2", "next node"},
		{"up", "#2", keyPress("k"), "#1", "previous node"},
		{"left", "#1", keyPress("h"), "#3", "blocker side"},
		{"right", "#3", keyPress("l"), "#1", "dependent side"},
		{"home", "#2", keyPress("g"), "#1", "first node"},
		{"end", "#1", keyPress("G"), "#3", "last node"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := press(t, seat(t, tt.from), tt.key); got != tt.want {
				t.Errorf("%s from %s focused %s, want %s (%s)",
					tt.name, tt.from, got, tt.want, tt.axis)
			}
		})
	}
}

// A read that failed is still failed after the Watchlist is read again. The
// re-seat issues nothing, so the retry remedy remains visible even while an
// introduced uncovered member keeps the settled failure count withheld.
func TestFrontierRefreshKeepsTheFailureNotice(t *testing.T) {
	after := blockingSnapshotSwapping(t, "acme/widgets#212", addedMember)
	p := fake.New(fake.WithSnapshots(fake.FixtureBlockingSnapshot(), after),
		fake.WithDetails(fake.FixtureBlockingDetails()))
	c := newClock()
	s := frontierSession(t, c, p)
	openFrontier(t, s)
	s.clock.advance(61 * time.Second)
	s.beat()
	s.waitFor(t, "reading Detail 0/0")
	m, got := s.finish(t)

	// #211 is the fixture's unreadable member and is still on the Watchlist.
	if _, still := m.frontier.failed["acme/widgets#211"]; !still {
		t.Errorf("failed = %v after a refresh, want the Ticket whose read failed", m.frontier.failed)
	}
	frame := string(got)
	if strings.Contains(frame, "Ticket's Links could not be read") {
		t.Errorf("an unresolved seat published a settled failure count:\n%s", frame)
	}
	if !strings.Contains(frame, "read failed:") || !strings.Contains(frame, "press r to retry") {
		t.Errorf("the carried failure remedy went away across a refresh:\n%s", frame)
	}
}
