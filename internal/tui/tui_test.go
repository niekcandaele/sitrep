package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// A golden frame is a screenshot of a terminal, so the terminal's size is part
// of the input and is pinned here.
const (
	termWidth  = 120
	termHeight = 40
)

// waitTimeout bounds every wait in this file. Nothing here sleeps: the waits
// are on what the program has drawn, and the timeout only decides how long a
// hung program takes to fail the test.
const waitTimeout = 10 * time.Second

// clock is the monitor's injected clock. Tests advance it explicitly; nothing
// in the package reads the wall clock, which is what makes "updated 12s ago"
// assertable at all.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// session drives the whole monitor program against an injected Source.
type session struct {
	tm    *teatest.TestModel
	clock *clock
}

// start runs the monitor with an explicit interval, driven by c. The Source
// must read the same clock: a reading stamped from a different one would make
// the staleness indicator report an age nobody lived through.
func start(t *testing.T, c *clock, src Source, interval time.Duration) *session {
	t.Helper()
	return startWith(t, c, Options{Source: src, Interval: interval, Now: c.now})
}

// startWith runs the monitor with explicit Options, for the sessions that need a
// second seam — the Detail source — beside the list's.
func startWith(t *testing.T, c *clock, opts Options) *session {
	t.Helper()

	return &session{
		tm: teatest.NewTestModel(t, New(t.Context(), opts),
			teatest.WithInitialTermSize(termWidth, termHeight)),
		clock: c,
	}
}

// waitFor blocks until the program has drawn want.
func (s *session) waitFor(t *testing.T, want string) {
	t.Helper()

	teatest.WaitFor(t, s.tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), want)
	}, teatest.WithDuration(waitTimeout))
}

// waitUntil blocks until cond holds. It polls a fact the test owns — a
// Provider's call count — the same way teatest.WaitFor polls the program's
// output, and for the same reason: nothing here may assume how long a
// goroutine takes. It is not a sleep-and-hope; a condition that never holds
// fails the test rather than passing slowly.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// beat delivers one heartbeat, which is the deterministic lever the monitor's
// single timer gives a test: advance the clock, beat, and the refresh decision
// and the staleness label both move exactly as they would after that long.
func (s *session) beat() {
	s.tm.Send(heartbeatMsg(s.clock.now()))
}

// typeText sends one key press per rune, the way a human types.
func (s *session) typeText(text string) {
	for _, r := range text {
		s.tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// finish quits the program with q and returns the frame it settled on.
func (s *session) finish(t *testing.T) (Model, []byte) {
	t.Helper()
	return s.finishWith(t, keyPress("q"))
}

// finishWith quits the program with a specific key and returns the frame it
// settled on. The final model's own View is the frame: the accumulated output
// of an alternate-screen session is every repaint concatenated, and a golden of
// that asserts the history of the screen rather than the screen.
//
// A session that ends inside the find box has to quit with ctrl+c, because
// there a q is a q.
func (s *session) finishWith(t *testing.T, quit tea.KeyPressMsg) (Model, []byte) {
	t.Helper()

	s.tm.Send(quit)
	s.tm.WaitFinished(t, teatest.WithFinalTimeout(waitTimeout))

	m, ok := s.tm.FinalModel(t).(Model)
	if !ok {
		t.Fatalf("final model is %T, want tui.Model", s.tm.FinalModel(t))
	}

	content := m.View().Content
	// Every state has to fill the screen exactly. One line too many scrolls the
	// alternate screen and the header walks off the top; one too few leaves the
	// footer floating.
	if h := lipgloss.Height(content); h != termHeight {
		t.Errorf("the frame is %d lines, want exactly the terminal's %d", h, termHeight)
	}
	return m, frame(content)
}

// selectorSource reads the fake Provider through the same seam production uses.
func selectorSource(p *fake.Provider, c *clock) Source {
	return SelectorSource(p, provider.EpicSelector{Ref: ref.Ref{Raw: "111"}}, c.now)
}

func refListSelectorSource(p *fake.Provider, c *clock) Source {
	refs := make([]ref.Ref, 0, 4)
	for _, number := range []int{112, 115, 118, 121} {
		refs = append(refs, ref.Ref{
			Tracker: ref.TrackerGitHub,
			Host:    "github.com",
			Owner:   "acme",
			Repo:    "widgets",
			Number:  number,
		})
	}
	return SelectorSource(p, provider.RefListSelector{Refs: refs}, c.now)
}

func querySelectorSource(p *fake.Provider, c *clock, query string) Source {
	return SelectorSource(p, provider.QuerySelector{Query: query}, c.now)
}

// The headline frame: the header and its progress bar, every Status Category
// grouped with its count, assignees, all four pull request shapes, the unicode
// title, the cross-repo Ticket, the selected-row marker and the footer help.
func TestInitialFrame(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := start(t, c, selectorSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	m, got := s.finish(t)

	checkGolden(t, "initial.golden.txt", got)
	if !m.hasData {
		t.Error("the first reading never landed")
	}
	// ADR-0003: a list refresh is one batched fetch and never touches Detail.
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1 batched fetch", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: the monitor never fetches Detail", n)
	}
}

func TestRefListInitialFrame(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := start(t, c, refListSelectorSource(p, c), time.Minute)
	s.waitFor(t, "4 tickets")

	m, got := s.finish(t)

	checkGolden(t, "ref_list.golden.txt", got)
	if m.input.Header != (Header{Title: "4 tickets"}) {
		t.Errorf("Header = %+v, want 4 tickets", m.input.Header)
	}
	if len(m.input.Tickets) != 4 {
		t.Errorf("Tickets = %d, want 4", len(m.input.Tickets))
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 0 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 0", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestLongQueryHeaderFitsANarrowFrameWithoutChangingTheQuery(t *testing.T) {
	const (
		width  = 42
		height = 18
	)
	query := "  project = FOO AND statusCategory != Done ORDER BY updated DESC  "
	p := fake.New()
	c := newClock()
	tm := teatest.NewTestModel(t, New(t.Context(), Options{
		Source:   querySelectorSource(p, c, query),
		Interval: time.Minute,
		Now:      c.now,
	}), teatest.WithInitialTermSize(width, height))
	s := &session{tm: tm, clock: c}
	s.waitFor(t, "project = FOO")

	tm.Send(keyPress("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitTimeout))
	m, ok := tm.FinalModel(t).(Model)
	if !ok {
		t.Fatalf("final model is %T, want tui.Model", tm.FinalModel(t))
	}
	content := m.View().Content
	got := frame(content)
	checkGolden(t, "query_narrow.golden.txt", got)

	if h := lipgloss.Height(content); h != height {
		t.Errorf("frame height = %d, want %d", h, height)
	}
	for _, line := range strings.Split(content, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("line width = %d, want at most %d: %q", got, width, line)
		}
	}
	if first := strings.SplitN(string(got), "\n", 2)[0]; !strings.HasSuffix(strings.TrimRight(first, " "), "…") {
		t.Errorf("narrow Query Header = %q, want ellipsis", first)
	}
	if m.input.Header.Title != query {
		t.Errorf("stored Header Title = %q, want full Query %q", m.input.Header.Title, query)
	}
	selector, ok := p.LastSelector().(provider.QuerySelector)
	if !ok || selector.Query != query {
		t.Errorf("last selector = %#v, want exact Query %q", p.LastSelector(), query)
	}
}

func TestQueryDetailUsesTheQueryBreadcrumb(t *testing.T) {
	query := "label:bug assignee:@me"
	p := fake.New()
	c := newClock()
	s := startWith(t, c, Options{
		Source:       querySelectorSource(p, c, query),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, query)

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	m, got := s.finish(t)

	if m.mode != modeDetail {
		t.Error("enter did not open Detail from the Query Watchlist")
	}
	if m.detail.input.Parent != (Header{Title: query}) {
		t.Errorf("Detail Parent = %+v, want Query Header", m.detail.input.Parent)
	}
	if !strings.Contains(string(got), query) {
		t.Errorf("Detail frame omitted Query breadcrumb:\n%s", got)
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 1 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 1", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestQueryLimitNoticeSurvivesFilterAndDetailRoundTrip(t *testing.T) {
	query := "state=opened"
	p := fake.New(fake.WithMaxTickets(4))
	c := newClock()
	s := startWith(t, c, Options{
		Source:       querySelectorSource(p, c, query),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Limit reached — showing 4 tickets.")

	s.tm.Send(keyPress("/"))
	s.typeText("shard")
	s.tm.Send(enterKey)
	s.waitFor(t, "2 of 4 Tickets")
	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)

	m, got := s.finish(t)
	if m.mode != modeList || m.filter.Query != "shard" || !m.input.LimitReached {
		t.Errorf("returned list state = mode %v filter %q LimitReached=%t", m.mode, m.filter.Query, m.input.LimitReached)
	}
	for _, want := range []string{"Limit reached — showing 4 tickets.", "2 of 4 Tickets"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("returned list omitted %q:\n%s", want, got)
		}
	}
	if p.ResolveCalls() != 1 || p.DetailCalls() != 1 {
		t.Errorf("calls = Resolve %d Detail %d, want 1 and 1", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestQueryEscapeAndRefreshReplaceMembershipWithTheSameSelector(t *testing.T) {
	query := "label:changing-membership"
	before := fake.FixtureSnapshot()
	before.Tickets = append([]model.Ticket(nil), before.Tickets[0])
	after := fake.FixtureSnapshot()
	after.Tickets = append([]model.Ticket(nil), after.Tickets[1])
	p := fake.New(fake.WithSnapshots(before, after))
	c := newClock()
	s := startWith(t, c, Options{
		Source:       querySelectorSource(p, c, query),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Draft the shard sync protocol")

	s.tm.Send(enterKey)
	s.waitFor(t, "DESCRIPTION")
	s.tm.Send(escKey)
	s.tm.Send(keyPress("r"))
	waitUntil(t, "the Query refresh", func() bool { return p.ResolveCalls() == 2 })
	s.waitFor(t, "Retry & backoff for the sync worker")

	m, got := s.finish(t)
	if m.mode != modeList {
		t.Error("esc did not return to the Query Watchlist")
	}
	if m.input.Header != (Header{Title: query}) {
		t.Errorf("Header after refresh = %+v, want Query Header", m.input.Header)
	}
	if len(m.input.Tickets) != 1 || m.input.Tickets[0].ID != "acme/widgets#113" {
		t.Errorf("membership after refresh = %+v, want only #113", m.input.Tickets)
	}
	if strings.Contains(string(got), "Draft the shard sync protocol") {
		t.Errorf("former Query member remained in refreshed frame:\n%s", got)
	}
	selector, ok := p.LastSelector().(provider.QuerySelector)
	if !ok || selector.Query != query {
		t.Errorf("last selector = %#v, want exact Query %q", p.LastSelector(), query)
	}
	if p.ResolveCalls() != 2 || p.DetailCalls() != 1 {
		t.Errorf("calls = Resolve %d Detail %d, want 2 and 1", p.ResolveCalls(), p.DetailCalls())
	}
}

func TestRefListFilteringDrillInAndRefreshUseTheCommonListPath(t *testing.T) {
	before := fake.FixtureSnapshot()
	after := fake.FixtureSnapshot()
	after.Tickets[0].Status = model.StatusDone
	after.Tickets[0].NativeStatus = "closed"
	after.Tickets = append(after.Tickets, model.Ticket{ID: "acme/widgets#999", Key: "#999", Title: "unrequested"})
	p := fake.New(fake.WithSnapshots(before, after))
	c := newClock()
	s := startWith(t, c, Options{
		Source:       refListSelectorSource(p, c),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "4 tickets")

	// Hide-finished operates on the same rows while whole-Watchlist progress stays
	// based on all four members.
	s.tm.Send(keyPress("d"))
	s.waitFor(t, "2 of 4 Tickets")
	s.tm.Send(keyPress("d"))

	// Fuzzy find selects #112, and opening it is the only Detail read.
	s.tm.Send(keyPress("/"))
	s.typeText("Draft shard")
	s.tm.Send(enterKey)
	s.tm.Send(enterKey)
	waitUntil(t, "the selected Ref-list Ticket detail", func() bool { return p.DetailCallsFor("acme/widgets#112") == 1 })
	s.waitFor(t, "The shard sync protocol")
	s.tm.Send(escKey)
	s.waitFor(t, "4 tickets")

	// Clear the query before refreshing so the changed status is visible.
	s.tm.Send(escKey)
	s.tm.Send(keyPress("r"))
	waitUntil(t, "the Ref-list refresh", func() bool { return p.ResolveCalls() == 2 })

	m, _ := s.finish(t)
	if m.input.Header != (Header{Title: "4 tickets"}) {
		t.Errorf("Header after refresh = %+v", m.input.Header)
	}
	if len(m.input.Tickets) != 4 {
		t.Fatalf("membership after refresh = %d, want 4", len(m.input.Tickets))
	}
	for _, ticket := range m.input.Tickets {
		if ticket.ID == "acme/widgets#999" {
			t.Error("refresh added an unrequested Ticket")
		}
	}
	if p.ResolveCalls() != 2 || p.DetailCalls() != 1 {
		t.Errorf("calls = Resolve %d Detail %d, want 2 and 1", p.ResolveCalls(), p.DetailCalls())
	}
}

// The auto-refresh criterion: once the interval has elapsed the second reading
// is on screen, the group counts and the progress bar have moved, and the
// staleness indicator has reset.
func TestFrameAfterAutoRefresh(t *testing.T) {
	before := fake.FixtureSnapshot()
	after := fake.FixtureSnapshot()
	// #113 finishes: it leaves In Progress for Done, which moves both counts
	// and the bar.
	after.Tickets[1].Status = model.StatusDone
	after.Tickets[1].NativeStatus = "closed"

	p := fake.New(fake.WithSnapshots(before, after))
	c := newClock()
	s := start(t, c, selectorSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	// A beat before the interval has elapsed must not fetch.
	s.clock.advance(30 * time.Second)
	s.beat()
	s.waitFor(t, "updated 30s ago")
	if n := p.ResolveCalls(); n != 1 {
		t.Fatalf("ResolveCalls() = %d after 30s of a 60s interval, want 1", n)
	}

	s.clock.advance(31 * time.Second)
	s.beat()
	// 44% is text only the second reading can produce, so seeing it drawn is
	// proof the refresh landed and repainted.
	s.waitFor(t, "44%")

	m, got := s.finish(t)

	checkGolden(t, "after_refresh.golden.txt", got)
	if n := p.ResolveCalls(); n != 2 {
		t.Errorf("ResolveCalls() = %d, want 2: one refresh is one Resolve", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0", n)
	}
	if m.lastErr != nil {
		t.Errorf("lastErr = %v, want none", m.lastErr)
	}
}

// The failure criterion: the last good list is still fully rendered, the
// footer carries the error, and the staleness indicator still counts from the
// last *successful* fetch — because the data really is that old.
func TestFrameAfterFailedRefresh(t *testing.T) {
	p := fake.New()
	c := newClock()
	var calls atomic.Int32
	src := func(ctx context.Context) (ListInput, error) {
		if calls.Add(1) > 1 {
			return ListInput{}, errors.New("dial tcp: lookup tracker.example.test: no such host")
		}
		return selectorSource(p, c)(ctx)
	}

	s := start(t, c, src, time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.clock.advance(45 * time.Second)
	s.tm.Send(keyPress("r"))
	s.waitFor(t, "refresh failed")

	m, got := s.finish(t)

	checkGolden(t, "refresh_failed.golden.txt", got)
	if m.lastErr == nil {
		t.Error("the failed refresh was swallowed")
	}
	if len(m.rows) == 0 {
		t.Error("the last good list was blanked by a failed refresh")
	}
}

// An undeclared Capability is silently absent: no pull request text anywhere,
// no error, no placeholder.
func TestFrameWithoutThePullRequestCapability(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{
		Hierarchy: true,
		Selectors: model.SelectorCapabilities{Epic: true},
	}))
	c := newClock()
	s := start(t, c, selectorSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	_, got := s.finish(t)

	checkGolden(t, "no_pr_capability.golden.txt", got)
	for _, absent := range []string{"ci ok", "ci FAIL", "ci ...", "approved", "changes req", "review pending"} {
		if strings.Contains(string(got), absent) {
			t.Errorf("the frame mentions %q without the PullRequests capability", absent)
		}
	}
}

// A Watchlist with no Tickets renders its header without dividing by zero and
// says so rather than trailing off into an empty void.
func TestFrameWithNoTickets(t *testing.T) {
	empty := model.WatchlistSnapshot{
		Header: model.WatchlistHeader{
			Key:   "#900",
			Title: "Widget sync v3: nothing planned yet",
			URL:   "https://tracker.example.test/acme/widgets/900",
		},
		Epic: model.Epic{
			ID:     "acme/widgets#900",
			Key:    "#900",
			Title:  "Widget sync v3: nothing planned yet",
			URL:    "https://tracker.example.test/acme/widgets/900",
			Status: model.StatusTodo,
		},
	}
	p := fake.New(fake.WithSnapshot(empty))
	c := newClock()
	s := start(t, c, selectorSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v3")

	_, got := s.finish(t)

	checkGolden(t, "empty.golden.txt", got)
}

// A failed *first* fetch shows an error state with a retry instruction and
// does not exit: on an SSH box the network comes and goes, and a monitor that
// dies on one bad lookup is not a monitor.
func TestFrameWhenTheFirstFetchFails(t *testing.T) {
	src := func(context.Context) (ListInput, error) {
		return ListInput{}, errors.New("dial tcp: lookup tracker.example.test: no such host")
	}
	s := start(t, newClock(), src, time.Minute)
	s.waitFor(t, "Could not read the collection")

	m, got := s.finish(t)

	checkGolden(t, "first_fetch_failed.golden.txt", got)
	if m.hasData {
		t.Error("the model claims data it never received")
	}
}

// The list screen consumes a Watchlist of Tickets plus a header, not an Epic.
// This is the executable form of that contract: a hand-built ListInput that
// was never a WatchlistSnapshot renders.
func TestFrameFromAHandBuiltListInput(t *testing.T) {
	in := ListInput{
		Header:  Header{Key: "query", Title: "Everything assigned to @tobias"},
		Tickets: []model.Ticket{ticket("PROJ-7", model.StatusInProgress)},
	}

	s := start(t, newClock(), func(context.Context) (ListInput, error) { return in, nil }, time.Minute)
	s.waitFor(t, "Everything assigned to @tobias")

	_, got := s.finish(t)

	for _, want := range []string{"query", "Everything assigned to @tobias", "IN PROGRESS (1)", "PROJ-7"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the frame does not contain %q:\n%s", want, got)
		}
	}
}

// Pressing r during an in-flight refresh does not start a second one: that is
// the rate limiter that stops a user leaning on the key.
func TestRefreshDoesNotOverlap(t *testing.T) {
	p := fake.New(fake.WithDelay(50 * time.Millisecond))
	c := newClock()
	s := start(t, c, selectorSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(keyPress("r"))
	s.tm.Send(keyPress("r"))
	s.waitFor(t, "refreshing…")
	waitUntil(t, "the forced refresh to finish", func() bool { return p.ResolveCalls() >= 2 })

	m, _ := s.finish(t)

	if n := p.ResolveCalls(); n != 2 {
		t.Errorf("ResolveCalls() = %d, want 2: two rapid refreshes are one fetch", n)
	}
	if !m.quitting {
		t.Error("the final model does not report quitting")
	}
}

// Expanding the help listing takes room from the list rather than pushing the
// frame past the bottom of the screen.
func TestFullHelpKeepsTheFrameOnScreen(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := start(t, c, selectorSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(keyPress("?"))
	s.waitFor(t, "page down")

	m, got := s.finish(t)

	if !m.help.ShowAll {
		t.Error("? did not expand the help")
	}
	for _, want := range []string{"pgdn", "enter", "open"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the expanded help does not list %q:\n%s", want, got)
		}
	}
}

// Both quit keys end the program, and only ctrl+c is an interrupt. README
// states ctrl+c prints nothing and exits 130; raw mode delivers it as a key
// press rather than a signal, so the fact has to be carried out of the program
// on the final model.
func TestQuitKeys(t *testing.T) {
	tests := []struct {
		name            string
		key             tea.KeyPressMsg
		wantInterrupted bool
	}{
		{name: "q", key: keyPress("q")},
		{name: "ctrl+c", key: tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, wantInterrupted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fake.New()
			c := newClock()
			s := start(t, c, selectorSource(p, c), time.Minute)
			s.waitFor(t, "Widget sync v2")

			s.tm.Send(tt.key)
			s.tm.WaitFinished(t, teatest.WithFinalTimeout(waitTimeout))

			m, ok := s.tm.FinalModel(t).(Model)
			if !ok || !m.quitting {
				t.Fatalf("%v did not quit the program", tt.key)
			}
			if m.Interrupted() != tt.wantInterrupted {
				t.Errorf("Interrupted() = %v, want %v", m.Interrupted(), tt.wantInterrupted)
			}
		})
	}
}

// esc from the list quits too, and it is not an interrupt: only ctrl+c is.
func TestEscapeIsNotAnInterrupt(t *testing.T) {
	m := Model{}

	quit := m.quit(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !quit.quitting || quit.Interrupted() {
		t.Errorf("esc: quitting=%v interrupted=%v, want true/false", quit.quitting, quit.Interrupted())
	}

	quit = m.quit(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !quit.quitting || !quit.Interrupted() {
		t.Errorf("ctrl+c: quitting=%v interrupted=%v, want true/true", quit.quitting, quit.Interrupted())
	}
}
