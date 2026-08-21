package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

var (
	escKey    = tea.KeyPressMsg{Code: tea.KeyEsc}
	enterKey  = tea.KeyPressMsg{Code: tea.KeyEnter}
	ctrlCKey  = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	downKey   = tea.KeyPressMsg{Code: tea.KeyDown}
	pageDnKey = tea.KeyPressMsg{Code: tea.KeyPgDown}
	homeKey   = tea.KeyPressMsg{Code: tea.KeyHome}
	endKey    = tea.KeyPressMsg{Code: tea.KeyEnd}
)

// startFixture runs the monitor over the fixture Epic with the first reading
// already on screen, which is where every filtering session begins.
func startFixture(t *testing.T) (*fake.Provider, *session) {
	t.Helper()

	p := fake.New()
	c := newClock()
	s := start(t, c, epicSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")
	return p, s
}

// checkSameFrameAs asserts a frame is byte-identical to an existing golden. It
// is a stronger statement than a golden of its own: "this ends up exactly where
// it started" cannot be quietly re-recorded.
func checkSameFrameAs(t *testing.T, name string, got []byte) {
	t.Helper()

	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	if string(got) != string(want) {
		t.Errorf("frame is not the unfiltered %s.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// The hide-done criterion: Done and Cancelled leave the list entirely, headings
// and all, while the progress header goes on counting the whole Epic.
func TestFrameWithFinishedTicketsHidden(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("d"))
	s.waitFor(t, "6 of 10 Tickets")

	m, got := s.finish(t)

	checkGolden(t, "hide_done.golden.txt", got)
	if !m.filter.HideFinished {
		t.Error("d did not turn the hide toggle on")
	}
	// The header reports the collection, not the view.
	if !strings.Contains(string(got), "3/9 done · 1 cancelled · 33%") {
		t.Errorf("the progress header moved when the list was filtered:\n%s", got)
	}
	for _, absent := range []string{"DONE (", "CANCELLED (", "#118", "#119", "#120", "#121"} {
		if strings.Contains(string(got), absent) {
			t.Errorf("the frame still shows %q with finished Tickets hidden:\n%s", absent, got)
		}
	}
}

// The toggle really toggles: pressing d twice puts the screen back exactly
// where it started, footer included.
func TestHideFinishedTwiceRestoresTheUnfilteredFrame(t *testing.T) {
	_, s := startFixture(t)

	// Nothing is waited on between the presses: Bubble Tea folds messages in
	// the order they were sent, so the frame the final model renders has
	// seen both.
	s.tm.Send(keyPress("d"))
	s.tm.Send(keyPress("d"))

	m, got := s.finish(t)

	checkSameFrameAs(t, "initial.golden.txt", got)
	if m.filter.Active() {
		t.Errorf("the filter is still active: %+v", m.filter)
	}
}

// The find box, open and narrowing live: hits across several Status Categories
// with their group counts recut, the header's progress untouched.
func TestFrameWhileSearching(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("/"))
	s.typeText("shard")

	m, got := s.finishWith(t, ctrlCKey)

	// The list is narrowed with no enter ever sent, and the box is still
	// open: that is live narrowing, asserted on the frame rather than on a
	// substring of an incremental repaint.
	checkGolden(t, "search_active.golden.txt", got)
	if !m.searching {
		t.Error("the find box closed on its own")
	}
	if m.filter.Query != "shard" {
		t.Errorf("the applied query is %q, want it in step with the box", m.filter.Query)
	}
	if !strings.Contains(string(got), "3/9 done · 1 cancelled · 33%") {
		t.Errorf("the progress header moved when the list was filtered:\n%s", got)
	}
}

// enter commits: the box closes, the list stays narrowed, and the footer says
// what is filtered and how to clear it.
func TestFrameAfterCommittingTheQuery(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("/"))
	s.typeText("shard")
	s.tm.Send(enterKey)

	m, got := s.finish(t)

	checkGolden(t, "search_committed.golden.txt", got)
	if m.searching {
		t.Error("enter did not close the find box")
	}
	if m.filter.Query != "shard" {
		t.Errorf("enter dropped the query: %q", m.filter.Query)
	}
}

// esc clears the filter and restores the screen exactly as it was, which is the
// acceptance criterion verbatim.
func TestEscapeClearsTheQueryAndRestoresTheUnfilteredFrame(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("/"))
	s.typeText("shard")
	s.tm.Send(escKey)

	m, got := s.finish(t)

	checkSameFrameAs(t, "initial.golden.txt", got)
	if m.searching || m.filter.Active() {
		t.Errorf("esc left searching=%v filter=%+v", m.searching, m.filter)
	}
}

// A filter that admits nothing says so where the list would be, tells the user
// how to undo it, and does not take the program down with it.
func TestFrameWhenNothingMatches(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("/"))
	s.typeText("zzz")

	m, got := s.finishWith(t, ctrlCKey)

	checkGolden(t, "search_no_match.golden.txt", got)
	if !strings.Contains(string(got), "0 of 10 Tickets") {
		t.Errorf("the frame does not report an empty view:\n%s", got)
	}
	if !strings.Contains(string(got), "3/9 done · 1 cancelled · 33%") {
		t.Errorf("the progress header moved when the list emptied:\n%s", got)
	}
	if m.lastErr != nil {
		t.Errorf("filtering produced an error: %v", m.lastErr)
	}
}

// The two criteria compose with AND, and the footer names both.
func TestFrameWithBothFiltersOn(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("d"))
	s.tm.Send(keyPress("/"))
	s.typeText("shard")
	s.tm.Send(enterKey)

	_, got := s.finish(t)

	checkGolden(t, "filters_combined.golden.txt", got)
	// The unfinished "shard" Tickets stay; the finished ones are gone.
	for _, want := range []string{"#112", "#115", "done+cancelled hidden", `"shard"`, "2 of 10 Tickets"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the frame does not contain %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"#118", "#121", "#113"} {
		if strings.Contains(string(got), absent) {
			t.Errorf("the frame still shows %q:\n%s", absent, got)
		}
	}
}

// The filter survives an auto-refresh and re-applies itself to the new reading
// without a second Tracker call: a Ticket that has just finished disappears
// from the list while the header's progress advances.
func TestFilterSurvivesAutoRefresh(t *testing.T) {
	before := fake.FixtureSnapshot()
	after := fake.FixtureSnapshot()
	// #113 finishes, which moves the progress bar and, with d on, takes the
	// Ticket off the screen.
	after.Tickets[1].Status = model.StatusDone
	after.Tickets[1].NativeStatus = "closed"

	p := fake.New(fake.WithSnapshots(before, after))
	c := newClock()
	s := start(t, c, epicSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(keyPress("d"))

	s.clock.advance(61 * time.Second)
	s.beat()
	// "4/9 done" is text only the second reading can produce.
	s.waitFor(t, "4/9 done")

	m, got := s.finishWith(t, keyPress("q"))

	checkGolden(t, "hide_done_refreshed.golden.txt", got)
	if !m.filter.HideFinished {
		t.Error("the refresh cleared the filter")
	}
	if strings.Contains(string(got), "#113") {
		t.Errorf("#113 finished but is still listed under a hide-finished filter:\n%s", got)
	}
	if n := p.EpicCalls(); n != 2 {
		t.Errorf("EpicCalls() = %d, want 2: filtering adds no fetch of its own", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0", n)
	}
}

// ADR-0003: filtering is arithmetic over Tickets already in memory. Neither
// criterion, however hard it is exercised, reaches a Provider.
func TestFilteringNeverRefetches(t *testing.T) {
	p, s := startFixture(t)

	s.tm.Send(keyPress("d"))
	s.tm.Send(keyPress("d"))
	s.tm.Send(keyPress("/"))
	s.typeText("shard sync")
	s.tm.Send(enterKey)

	s.finish(t)

	if n := p.EpicCalls(); n != 1 {
		t.Errorf("EpicCalls() = %d, want 1: filtering never refetches", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: this ticket adds no Detail call", n)
	}
}

// The find box owns the keyboard: the list's own commands are text while it is
// open. A box that quits the program when you search for "queue" is the classic
// bug here.
func TestTheFindBoxOwnsTheKeyboard(t *testing.T) {
	p, s := startFixture(t)

	s.tm.Send(keyPress("/"))
	s.typeText("queue drift")

	m, got := s.finishWith(t, ctrlCKey)

	if m.search.Value() != "queue drift" {
		t.Errorf("the box holds %q, want the literal text typed into it", m.search.Value())
	}
	if !m.quitting {
		t.Error("ctrl+c did not quit from inside the find box")
	}
	if !strings.Contains(string(got), "queue drift") {
		t.Errorf("the frame does not show the typed query:\n%s", got)
	}
	// The r in "drift" must not have forced a refresh.
	if n := p.EpicCalls(); n != 1 {
		t.Errorf("EpicCalls() = %d, want 1: r is a letter inside the find box", n)
	}
}

// The esc ladder: out of the box, out of the filter, out of the program.
func TestEscapeLadder(t *testing.T) {
	_, s := startFixture(t)

	s.tm.Send(keyPress("/"))
	s.typeText("shard")

	// Rung one: out of the box and the query with it, program alive.
	s.tm.Send(escKey)

	// Rung two: d, then esc clears the filter without quitting.
	s.tm.Send(keyPress("d"))
	s.tm.Send(escKey)

	// Rung three: with nothing left to clear, esc quits.
	m, _ := s.finishWith(t, escKey)

	if !m.quitting {
		t.Error("esc with no filter active did not quit")
	}
	if m.filter.Active() {
		t.Errorf("the filter survived its esc: %+v", m.filter)
	}
}

// A refresh landing while the user is mid-query must not clobber the draft or
// the applied filter.
func TestRefreshDoesNotClobberTheDraftQuery(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := start(t, c, epicSource(p, c), time.Minute)
	s.waitFor(t, "Widget sync v2")

	s.tm.Send(keyPress("/"))
	s.typeText("sha")

	s.clock.advance(61 * time.Second)
	s.beat()
	waitUntil(t, "the auto-refresh to land", func() bool { return p.EpicCalls() >= 2 })

	s.typeText("rd")

	m, got := s.finishWith(t, ctrlCKey)

	if m.search.Value() != "shard" {
		t.Errorf("the box holds %q, want the draft the refresh landed on top of", m.search.Value())
	}
	if m.filter.Query != "shard" {
		t.Errorf("the applied query is %q, want it still in force", m.filter.Query)
	}
	if strings.Contains(string(got), "#119") {
		t.Errorf("a Ticket the query excludes came back with the refresh:\n%s", got)
	}
}

// Navigation over a list a filter has emptied is a no-op, not a panic and not a
// spin.
func TestNavigatingAnEmptyFilteredList(t *testing.T) {
	m := listModel(t, fixtureTickets())
	m = m.setFilter(Filter{Query: "zzz"})

	if len(m.rows) != 0 {
		t.Fatalf("the filter left %d rows, want none", len(m.rows))
	}
	for _, msg := range []tea.KeyPressMsg{downKey, pageDnKey, homeKey, endKey, enterKey} {
		next, _ := m.onKey(msg)
		got := next.(Model)
		if got.selected != 0 || got.selectedID != "" || got.offset != 0 {
			t.Errorf("%v moved the cursor on an empty filtered list: row %d (%q) offset %d",
				msg, got.selected, got.selectedID, got.offset)
		}
	}
}

// The cursor sitting on a Ticket the filter is about to hide lands on a visible
// one instead of pointing at nothing.
func TestSelectionSurvivesTheListShrinking(t *testing.T) {
	m := listModel(t, fixtureTickets())
	m = m.jump(len(m.rows)-1, -1)
	if m.selectedID != "acme/widgets#121" {
		t.Fatalf("selected %q, want the Cancelled Ticket at the end", m.selectedID)
	}
	deepOffset := m.offset

	m = m.setFilter(Filter{HideFinished: true})

	if m.selectedID == "" {
		t.Error("hiding the selected Ticket left the cursor on nothing")
	}
	if m.rows[m.selected].Ticket.Status.IsFinished() {
		t.Errorf("the cursor is on %q, a Ticket the filter hides", m.selectedID)
	}
	if m.offset >= len(m.rows) {
		t.Errorf("offset %d points past the %d rows left (was %d)", m.offset, len(m.rows), deepOffset)
	}
}

// The find box narrows the view, never the reading: clearing it restores every
// Ticket without a refetch, because m.input was never touched.
func TestFilteringLeavesTheReadingAlone(t *testing.T) {
	m := listModel(t, fixtureTickets())
	want := len(m.input.Tickets)

	m = m.setFilter(Filter{HideFinished: true, Query: "shard"})
	if got := len(m.input.Tickets); got != want {
		t.Errorf("the reading holds %d Tickets, want the untouched %d", got, want)
	}

	m = m.setFilter(Filter{})
	if got := len(m.visibleTickets()); got != want {
		t.Errorf("clearing the filter left %d Tickets visible, want %d", got, want)
	}
}
