package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

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
	if m.frontier.resolved {
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
	if strings.Contains(frame, "ACTIONABLE") {
		t.Errorf("something looked Actionable with no Links verified:\n%s", frame)
	}
	for _, a := range m.frontier.graph.Members() {
		if a.Actionable {
			t.Fatalf("%s is Actionable with unreadable Links", a.TicketID)
		}
	}
}

// esc mid-fetch returns to a readable list, and a late answer from the
// abandoned generation changes nothing: that is what makes the fan-out
// interruptible.
func TestFrontierFetchIsInterruptible(t *testing.T) {
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
	s.tm.Send(escKey)
	s.waitFor(t, "TODO")

	// A late answer tagged with the abandoned generation.
	s.tm.Send(frontierDetailMsg{generation: 0, id: "acme/widgets#201"})

	m, got := s.finish(t)
	close(release)

	checkGolden(t, "frontier_interrupted.golden.txt", got)
	if m.mode != modeList {
		t.Errorf("mode = %v, want the list", m.mode)
	}
	if m.frontier.done != 0 || m.frontier.failed != 0 {
		t.Errorf("a stale answer landed: done %d failed %d", m.frontier.done, m.frontier.failed)
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
	if m.frontier.graph.BlockingLinksSupported() {
		t.Error("the graph claims blocking links the Provider does not report")
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
	if !strings.Contains(string(got), "col ") {
		t.Errorf("no scroll position is reported on a canvas larger than the body:\n%s", got)
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
	s.waitFor(t, "actionable")
	m, _ := s.finish(t)

	// #211 has no fixture Detail, so it is the only read left to retry.
	if got := p.DetailCalls() - before; got != 1 {
		t.Errorf("r issued %d reads, want only the one that never succeeded", got)
	}
	if n := p.DetailCallsFor("acme/widgets#211"); n != 2 {
		t.Errorf("DetailCallsFor(#211) = %d, want the original read and the retry", n)
	}
	if !m.frontier.resolved {
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
	for i, ticket := range m.frontier.input.Tickets {
		termtexttest.AssertClean(t, "m.frontier.input.Tickets", ticket,
			termtexttest.Exempt("ID", "ParentID"))
		if i > 2 {
			break
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
	if !m.frontier.resolved {
		t.Fatal("the fan-out never resolved")
	}
	return m
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
