package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
)

// listModel returns a Model already holding one reading, sized like a
// terminal, so cursor behaviour can be exercised without a program.
func listModel(t *testing.T, tickets []model.Ticket) Model {
	t.Helper()

	m := New(t.Context(), Options{Interval: time.Minute, Now: func() time.Time { return time.Time{} }})
	m.width, m.height = 120, 40
	m.ready = true
	m.input = ListInput{Tickets: tickets}
	m.hasData = true
	return m.rebuildRows()
}

func keyPress(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// Movement skips group headings: down from the last Ticket of a group lands on
// the first Ticket of the next, never on the heading between them.
func TestCursorSkipsGroupHeadings(t *testing.T) {
	m := listModel(t, []model.Ticket{
		ticket("#1", model.StatusInProgress),
		ticket("#2", model.StatusTodo),
	})

	// rows: [In Progress] #1 [Todo] #2
	if m.selected != 1 {
		t.Fatalf("initial selection is row %d, want the first Ticket at row 1", m.selected)
	}

	down := m.move(1)
	if down.selected != 3 || down.rows[down.selected].Ticket.Key != "#2" {
		t.Errorf("down selected row %d (%s), want the next group's first Ticket #2",
			down.selected, down.selectedID)
	}

	up := down.move(-1)
	if up.rows[up.selected].Ticket.Key != "#1" {
		t.Errorf("up selected %s, want #1", up.rows[up.selected].Ticket.Key)
	}
}

// At either end the cursor stays put rather than wrapping: wrapping a list
// this short is disorienting.
func TestCursorStopsAtTheEnds(t *testing.T) {
	m := listModel(t, []model.Ticket{ticket("#1", model.StatusTodo), ticket("#2", model.StatusTodo)})

	if got := m.move(-1); got.selected != m.selected {
		t.Errorf("up from the first Ticket moved to row %d, want it to stay at %d", got.selected, m.selected)
	}
	last := m.jump(len(m.rows)-1, -1)
	if got := last.move(1); got.selected != last.selected {
		t.Errorf("down from the last Ticket moved to row %d, want it to stay at %d", got.selected, last.selected)
	}
}

// A collection with no Tickets at all has no selectable row. Navigating it
// must not spin or panic.
func TestCursorOnAnEmptyList(t *testing.T) {
	m := listModel(t, nil)

	for _, step := range []Model{m.move(1), m.move(-1), m.page(1), m.jump(0, 1)} {
		if step.selected != 0 || step.selectedID != "" {
			t.Errorf("navigating an empty list selected row %d (%q), want nothing",
				step.selected, step.selectedID)
		}
	}
}

// A refresh rebuilds the rows under a live cursor. The selection follows the
// Ticket the user was reading, by ID, rather than staying on whatever slid
// into that row.
func TestSelectionFollowsItsTicketAcrossARefresh(t *testing.T) {
	m := listModel(t, []model.Ticket{
		ticket("#1", model.StatusTodo),
		ticket("#2", model.StatusTodo),
		ticket("#3", model.StatusTodo),
	})
	m = m.jump(len(m.rows)-1, -1)
	if m.selectedID != "#3" {
		t.Fatalf("selected %q, want #3", m.selectedID)
	}

	// #1 disappears and the rest shift up a row.
	m.input = ListInput{Tickets: []model.Ticket{ticket("#2", model.StatusTodo), ticket("#3", model.StatusTodo)}}
	m = m.rebuildRows()

	if m.selectedID != "#3" {
		t.Errorf("after the refresh the cursor is on %q, want it still on #3", m.selectedID)
	}
}

// When the selected Ticket is gone the cursor falls back to the clamped index
// rather than to nothing.
func TestSelectionFallsBackWhenItsTicketDisappears(t *testing.T) {
	m := listModel(t, []model.Ticket{ticket("#1", model.StatusTodo), ticket("#2", model.StatusTodo)})
	m = m.jump(len(m.rows)-1, -1)

	m.input = ListInput{Tickets: []model.Ticket{ticket("#1", model.StatusTodo)}}
	m = m.rebuildRows()

	if m.selectedID != "#1" {
		t.Errorf("cursor is on %q, want it clamped onto the surviving Ticket", m.selectedID)
	}
}

// A slow refresh answering after a newer one must not land on top of the
// fresher reading: the generation guard is a correctness rule, not an
// implementation detail.
func TestStaleRefreshIsDropped(t *testing.T) {
	m := listModel(t, []model.Ticket{ticket("#1", model.StatusTodo)})
	m.generation = 7

	stale := m.onRefreshed(refreshedMsg{
		generation: 6,
		input:      ListInput{Tickets: []model.Ticket{ticket("#old", model.StatusTodo)}},
	})

	if stale.rows[1].Ticket.Key != "#1" {
		t.Errorf("the stale reading replaced the list: %q", stale.rows[1].Ticket.Key)
	}
	if !stale.refreshing {
		t.Error("a dropped message cleared the in-flight flag; the real refresh is still running")
	}
}

// A failed refresh keeps the last good data on screen and does not reset the
// staleness clock: the data really is that old.
func TestFailedRefreshKeepsTheLastGoodReading(t *testing.T) {
	fetchedAt := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	m := listModel(t, []model.Ticket{ticket("#1", model.StatusTodo)})
	m.input.FetchedAt = fetchedAt
	m.generation = 1

	failed := m.onRefreshed(refreshedMsg{generation: 1, err: errors.New("boom")})

	if failed.lastErr == nil {
		t.Error("the error was swallowed")
	}
	if len(failed.rows) != 2 || failed.rows[1].Ticket.Key != "#1" {
		t.Errorf("the list was blanked: %v", failed.rows)
	}
	if !failed.input.FetchedAt.Equal(fetchedAt) {
		t.Errorf("FetchedAt moved to %s, want it left at the last successful fetch %s",
			failed.input.FetchedAt, fetchedAt)
	}
	if failed.refreshing {
		t.Error("the in-flight flag survived a failed refresh")
	}

	// A successful refresh clears the error.
	ok := failed.onRefreshed(refreshedMsg{generation: 1, input: ListInput{Tickets: []model.Ticket{ticket("#1", model.StatusTodo)}}})
	if ok.lastErr != nil {
		t.Errorf("lastErr = %v, want it cleared by a successful refresh", ok.lastErr)
	}
}

// Enter is declared and shown in help so nothing else claims it, and does
// nothing until the Ticket Detail drill-in exists.
func TestEnterIsAReservedNoOp(t *testing.T) {
	m := listModel(t, []model.Ticket{ticket("#1", model.StatusTodo)})

	next, cmd := m.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Error("enter issued a command; it is reserved, not wired")
	}
	if next.(Model).selected != m.selected {
		t.Error("enter moved the selection")
	}
}

// No filtering key is bound: a key shipped early is a key that has to be
// un-shipped.
func TestNoFilteringKeysAreBound(t *testing.T) {
	m := listModel(t, []model.Ticket{ticket("#1", model.StatusTodo), ticket("#2", model.StatusDone)})

	for _, k := range []string{"/", "f", "h", "o"} {
		next, cmd := m.onKey(keyPress(k))
		if cmd != nil {
			t.Errorf("%q issued a command; filtering belongs to later work", k)
		}
		if len(next.(Model).rows) != len(m.rows) {
			t.Errorf("%q changed the list", k)
		}
	}
}
