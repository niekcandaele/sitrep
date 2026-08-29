package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// completeBlockingProvider serves the blocking Watchlist with a Detail for
// every member, including #211, whose Detail the shared fixture deliberately
// omits so that its read fails. That failure is what keeps the list cold when
// one Detail is unreadable, and TestOneUnreadableDetailKeepsTheListCold depends
// on it, so the complete fixture is composed here rather than in
// internal/provider/fake.
//
// #211's Detail is read and carries no Links, which is the tri-state's other
// side: known, and genuinely unblocked.
func completeBlockingProvider(opts ...fake.Option) *fake.Provider {
	d := fake.FixtureBlockingDetails()
	d["acme/widgets#211"] = model.Detail{TicketID: "acme/widgets#211"}
	return fake.New(append([]fake.Option{fake.WithBlockingFixture(), fake.WithDetails(d)}, opts...)...)
}

// listSession drives the whole monitor through Options, which is what keeps
// these tests behind the terminal-text boundary a hand-built ListInput would
// bypass (ADR-0006).
func listSession(t *testing.T, c *clock, p *fake.Provider) *session {
	t.Helper()
	return startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
}

// warmTheDetailCache opens the Frontier, waits for its fan-out to resolve, and
// returns to the list. That is the only route a user has to a warm cache, and
// it is the state every marker in this file depends on.
func warmTheDetailCache(t *testing.T, s *session) {
	t.Helper()
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "actionable")
	s.tm.Send(keyPress("v"))
}

// markedKeys reports, per Ticket key, whether that row carries the Actionable
// marker. It reads the stripped frame, because the glyph is the signal and the
// style is only decoration.
func markedKeys(frame string) map[string]bool {
	marks := make(map[string]bool)
	for _, line := range strings.Split(frame, "\n") {
		runes := []rune(line)
		if len(runes) <= selectionGutter {
			continue
		}
		fields := strings.Fields(string(runes[selectionGutter:]))
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "#") {
			continue
		}
		marks[fields[0]] = len(fields) > 1 && fields[1] == strings.TrimSpace(actionableMarker)
	}
	return marks
}

func assertMarked(t *testing.T, frame string, want ...string) {
	t.Helper()
	wanted := make(map[string]bool, len(want))
	for _, k := range want {
		wanted[k] = true
	}
	marks := markedKeys(frame)
	if len(marks) == 0 {
		t.Fatalf("no Ticket rows found in the frame:\n%s", frame)
	}
	for key, marked := range marks {
		if marked != wanted[key] {
			t.Errorf("%s marked = %v, want %v:\n%s", key, marked, wanted[key], frame)
		}
	}
	for key := range wanted {
		if _, drawn := marks[key]; !drawn {
			t.Errorf("%s is not on screen, so its marker proves nothing:\n%s", key, frame)
		}
	}
}

// assertNoMarkers is the cold assertion: not a partial answer, not a stale one,
// nothing at all — neither the row glyph nor the header's legend.
func assertNoMarkers(t *testing.T, frame string) {
	t.Helper()
	if strings.Contains(frame, strings.TrimSpace(actionableMarker)) {
		t.Errorf("a cold list drew an Actionable marker:\n%s", frame)
	}
	if strings.Contains(frame, "actionable") {
		t.Errorf("a cold list drew the header's Actionable count:\n%s", frame)
	}
}

// titleColumn is the column a Ticket row's title starts in, which is what the
// reserved marker column moves.
func titleColumn(t *testing.T, frame, key string) int {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		runes := []rune(line)
		if len(runes) <= selectionGutter {
			continue
		}
		rest := string(runes[selectionGutter:])
		if !strings.HasPrefix(strings.TrimSpace(rest), key+" ") {
			continue
		}
		title := "Ship the rebalancer CLI"
		if i := strings.Index(line, title); i >= 0 {
			return len([]rune(line[:i]))
		}
	}
	t.Fatalf("no row for %s in:\n%s", key, frame)
	return 0
}

// Before the Frontier has been opened, the list knows nothing about
// Actionability and pays nothing to find out: no marker column, no header
// segment, and not one FetchDetail call.
func TestListBeforeTheDetailCacheIsWarm(t *testing.T) {
	p := completeBlockingProvider()
	c := newClock()
	s := listSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")

	_, got := s.finish(t)

	checkGolden(t, "list_blocking.golden.txt", got)
	assertNoMarkers(t, string(got))
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls = %d, want 0: the list must never fetch Detail", n)
	}
}

// Once every member's Detail has been read, the list marks the Actionable rows
// and the header counts them. The marker survives leaving the Frontier: it is
// derived from the cache, not from Frontier state.
func TestListMarksActionableRowsOnceTheDetailCacheIsWarm(t *testing.T) {
	p := completeBlockingProvider()
	c := newClock()
	s := listSession(t, c, p)
	warmTheDetailCache(t, s)

	m, got := s.finish(t)

	checkGolden(t, "list_actionable.golden.txt", got)
	frame := string(got)
	if m.mode != modeList {
		t.Fatalf("mode = %v, want the list", m.mode)
	}
	assertMarked(t, frame, "#201", "#211", "#212", "#213")
	if !strings.Contains(frame, separator+"4 actionable") {
		t.Errorf("the header does not count the Actionable Tickets:\n%s", frame)
	}

	// The reserved column moves the title, and the meta line with it, so the
	// list does not look ragged.
	cold := listSession(t, newClock(), completeBlockingProvider())
	cold.waitFor(t, "Shard rebalancer rollout")
	_, coldFrame := cold.finish(t)
	warm, chilly := titleColumn(t, frame, "#201"), titleColumn(t, string(coldFrame), "#201")
	if warm-chilly != actionableColumn {
		t.Errorf("the title sits %d columns further right, want %d", warm-chilly, actionableColumn)
	}
}

// The ticket's central constraint: the 60s refresh loop never pays for the
// marker. A refresh re-reads Ticket.Status and rebuilds every row, and the
// markers follow the fresh Status without one FetchDetail call.
func TestListMarkersSurviveARefreshAndCostNoDetailCall(t *testing.T) {
	after := fake.FixtureBlockingSnapshot()
	// #211 is Actionable in the first reading and finished in the second, so
	// its marker has to go without anything being re-fetched.
	// Selected by ID: a fixture reorder must fail loudly rather than quietly
	// test a different Ticket.
	const finished = model.TicketID("acme/widgets#211")
	moved := false
	for i, member := range after.Tickets {
		if member.ID != finished {
			continue
		}
		after.Tickets[i].Status = model.StatusDone
		after.Tickets[i].NativeStatus = "closed"
		moved = true
	}
	if !moved {
		t.Fatalf("%s is not in the fixture, so this test proves nothing", finished)
	}

	p := completeBlockingProvider(fake.WithSnapshots(fake.FixtureBlockingSnapshot(), after))
	c := newClock()
	s := listSession(t, c, p)
	warmTheDetailCache(t, s)
	s.waitFor(t, separator+"4 actionable")
	fetched := p.DetailCalls()

	s.clock.advance(61 * time.Second)
	s.beat()
	s.waitFor(t, separator+"3 actionable")

	_, got := s.finish(t)

	frame := string(got)
	assertMarked(t, frame, "#201", "#212", "#213")
	if n := p.ResolveCalls(); n != 2 {
		t.Errorf("ResolveCalls = %d, want 2: one refresh is one Resolve", n)
	}
	if n := p.DetailCalls(); n != fetched {
		t.Errorf("DetailCalls = %d, want the fan-out's %d: a refresh must fetch no Detail", n, fetched)
	}
}

// A refresh that brings in a Ticket nobody has read makes the whole list cold
// again. Marking the twelve that are cached would be a partial answer, and a
// partial answer about blocking is a wrong one.
func TestARefreshThatAddsAnUncachedTicketDropsEveryMarker(t *testing.T) {
	after := fake.FixtureBlockingSnapshot()
	after.Tickets = append(after.Tickets, model.Ticket{
		ID:           "acme/widgets#214",
		Key:          "#214",
		Title:        "Audit the rebalancer rollout",
		URL:          "https://tracker.example.test/acme/widgets/214",
		Status:       model.StatusTodo,
		NativeStatus: "open",
		Repository:   "acme/widgets",
	})

	p := completeBlockingProvider(fake.WithSnapshots(fake.FixtureBlockingSnapshot(), after))
	c := newClock()
	s := listSession(t, c, p)
	warmTheDetailCache(t, s)
	s.waitFor(t, separator+"4 actionable")
	fetched := p.DetailCalls()

	s.clock.advance(61 * time.Second)
	s.beat()
	s.waitFor(t, "Audit the rebalancer rollout")

	_, got := s.finish(t)

	assertNoMarkers(t, string(got))
	if n := p.DetailCalls(); n != fetched {
		t.Errorf("DetailCalls = %d, want the fan-out's %d: the list fetched to fill the gap", n, fetched)
	}
}

// The shared fixture omits #211's Detail, so its read fails and it never gains
// a cache key. Twelve of thirteen members cached is still cold: one unreadable
// Ticket can block anything.
func TestOneUnreadableDetailKeepsTheListCold(t *testing.T) {
	p := fake.New(fake.WithBlockingFixture())
	c := newClock()
	s := listSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "could not be read")
	s.tm.Send(keyPress("v"))

	m, got := s.finish(t)

	assertNoMarkers(t, string(got))
	if _, cached := m.details["acme/widgets#211"]; cached {
		t.Fatal("#211's failed read was cached, so this test proves nothing")
	}
	if len(m.details) != 12 {
		t.Errorf("cached Details = %d, want the twelve that could be read", len(m.details))
	}
}

// Filtering hides rows, and a hidden row would delete an edge: #201's blockers
// #205 and #206 are exactly the finished Tickets `d` hides. The markers are
// computed over the whole reading, so hiding them changes nothing.
func TestListMarkersIgnoreTheFilter(t *testing.T) {
	p := completeBlockingProvider()
	c := newClock()
	s := listSession(t, c, p)
	warmTheDetailCache(t, s)
	s.tm.Send(keyPress("d"))

	_, got := s.finish(t)

	frame := string(got)
	if strings.Contains(frame, "#205") || strings.Contains(frame, "#206") {
		t.Fatalf("the filter did not hide #201's finished blockers:\n%s", frame)
	}
	assertMarked(t, frame, "#201", "#211", "#212", "#213")
	if !strings.Contains(frame, separator+"4 actionable") {
		t.Errorf("the filter moved the header's Actionable count:\n%s", frame)
	}
}

// The #41 guard. The marker lives on the key/title line every Ticket already
// has, so rowHeights knows nothing about it and cannot disagree with the
// renderer: a suppression the window arithmetic does not share is what makes
// the scroll window drift and mouse clicks land on the wrong row.
//
// finishWith enforces the other half — every frame is exactly the terminal's
// height, warm or cold.
func TestActionableMarkersDoNotChangeRowHeights(t *testing.T) {
	warm := listSession(t, newClock(), completeBlockingProvider())
	warmTheDetailCache(t, warm)
	warmModel, warmFrame := warm.finish(t)

	cold := listSession(t, newClock(), completeBlockingProvider())
	cold.waitFor(t, "Shard rebalancer rollout")
	coldModel, _ := cold.finish(t)

	if !warmModel.listMarkers().active {
		t.Fatal("the warm session is not warm, so this comparison proves nothing")
	}
	if coldModel.listMarkers().active {
		t.Fatal("the cold session is warm, so this comparison proves nothing")
	}
	assertMarked(t, string(warmFrame), "#201", "#211", "#212", "#213")

	warmHeights := rowHeights(warmModel.rows, warmModel.input.Capabilities)
	coldHeights := rowHeights(coldModel.rows, coldModel.input.Capabilities)
	if len(warmHeights) != len(coldHeights) {
		t.Fatalf("rows = %d warm, %d cold", len(warmHeights), len(coldHeights))
	}
	for i := range warmHeights {
		if warmHeights[i] != coldHeights[i] {
			t.Errorf("row %d is %d lines warm and %d cold", i, warmHeights[i], coldHeights[i])
		}
	}
}

// A Provider that cannot express "blocks" has nothing to say about direction,
// so the list claims nothing and the Frontier pays for no Detail at all.
func TestListMarksNothingWithoutTheBlockingLinksCapability(t *testing.T) {
	p := completeBlockingProvider(fake.WithCapabilities(model.Capabilities{
		Hierarchy: true, Comments: true, PullRequests: true,
		Selectors: model.SelectorCapabilities{Epic: true, RefList: true, Query: true},
	}))
	c := newClock()
	s := listSession(t, c, p)
	s.waitFor(t, "Shard rebalancer rollout")
	s.tm.Send(keyPress("v"))
	s.waitFor(t, "does not report blocking links")
	s.tm.Send(keyPress("v"))

	_, got := s.finish(t)

	assertNoMarkers(t, string(got))
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls = %d, want 0", n)
	}
}
