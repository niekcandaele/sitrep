package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

// terminalSizes are the shapes a repaint has to be correct at. They are the
// sizes a person actually runs: a default xterm, a half-screen split, a
// full-screen one.
var terminalSizes = []struct {
	name          string
	width, height int
}{
	{"80x24", 80, 24},
	{"100x34", 100, 34},
	{"120x50", 120, 50},
}

// sizedFixtureModel returns a Model holding the fixture reading, sized as a
// terminal of w by h, without a program behind it: a repaint is a pure
// function of the model, and asserting it directly needs no pty.
func sizedFixtureModel(t *testing.T, w, h int) Model {
	t.Helper()

	m := New(t.Context(), Options{Interval: time.Minute, Now: func() time.Time { return time.Time{} }})
	snap := provider.StampSnapshot(fake.New(), fake.FixtureSnapshot(), time.Time{})
	m.input = ListFromEpicSnapshot(snap)
	m.hasData = true

	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	sized, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return sized.rebuildRows()
}

// checkFrameShape asserts the two invariants a well-formed frame has, and that
// a repaint therefore cannot corrupt: it is exactly the terminal's height, and
// no line is wider than the terminal.
//
// A frame one line too tall scrolls the alternate screen and walks the header
// off the top; a line one cell too wide wraps and shifts every line below it,
// which is what a Ticket drawn with another Ticket's status looks like.
func checkFrameShape(t *testing.T, where string, frame string, width, height int) {
	t.Helper()

	if got := strings.Count(frame, "\n") + 1; got != height {
		t.Errorf("%s: the frame is %d lines, want exactly the terminal's %d", where, got, height)
	}
	for i, line := range strings.Split(frame, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("%s: line %d is %d cells wide, want at most %d: %q", where, i, w, width, line)
		}
	}
}

// The frame is well formed at every step of a filtering session, at every size.
// The find box adds a footer row and taking it away removes one, so these are
// the transitions where the frame changes shape — and where a renderer that
// diffs against the previous frame has the most to get wrong.
func TestFrameShapeSurvivesFilterTransitions(t *testing.T) {
	sessions := []struct {
		name string
		keys []tea.KeyPressMsg
	}{
		{
			name: "find, query, esc",
			keys: []tea.KeyPressMsg{
				keyPress("/"), keyPress("w"), keyPress("i"), keyPress("d"), {Code: tea.KeyEsc},
			},
		},
		{
			name: "find, query, enter",
			keys: []tea.KeyPressMsg{
				keyPress("/"), keyPress("w"), keyPress("i"), keyPress("d"), {Code: tea.KeyEnter},
			},
		},
		{
			name: "find, query, backspace to empty",
			keys: []tea.KeyPressMsg{
				keyPress("/"), keyPress("w"), keyPress("i"), keyPress("d"),
				{Code: tea.KeyBackspace}, {Code: tea.KeyBackspace}, {Code: tea.KeyBackspace},
			},
		},
	}

	for _, size := range terminalSizes {
		for _, sess := range sessions {
			t.Run(size.name+"/"+sess.name, func(t *testing.T) {
				m := sizedFixtureModel(t, size.width, size.height)
				checkFrameShape(t, "before any keystroke", m.View().Content, size.width, size.height)

				for i, k := range sess.keys {
					next, _ := m.Update(k)
					updated, ok := next.(Model)
					if !ok {
						t.Fatalf("Update returned %T, want tui.Model", next)
					}
					m = updated
					checkFrameShape(t, "after keystroke "+string(rune('1'+i)),
						m.View().Content, size.width, size.height)
				}
			})
		}
	}
}

// The keystrokes that open or close the find box change how many rows the
// footer occupies, so the frame that follows one has a different shape from
// the frame before it. The incremental renderer must not diff across that, and
// tea.ClearScreen is how this model says so.
func TestFilterTransitionsForceAFullRepaint(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.KeyPressMsg
		want bool
	}{
		{name: "opening the find box", keys: []tea.KeyPressMsg{keyPress("/")}, want: true},
		{
			name: "closing it with esc",
			keys: []tea.KeyPressMsg{keyPress("/"), keyPress("w"), {Code: tea.KeyEsc}},
			want: true,
		},
		{
			name: "committing it with enter",
			keys: []tea.KeyPressMsg{keyPress("/"), keyPress("w"), {Code: tea.KeyEnter}},
			want: true,
		},
		{
			name: "clearing a committed filter",
			keys: []tea.KeyPressMsg{
				keyPress("/"), keyPress("w"), {Code: tea.KeyEnter}, {Code: tea.KeyEsc},
			},
			want: true,
		},
		{
			// The hide toggle turns the filter line on and off too, when
			// there is no query keeping it on screen.
			name: "toggling hide-finished",
			keys: []tea.KeyPressMsg{keyPress("d")},
			want: true,
		},
		{
			name: "typing inside the box",
			keys: []tea.KeyPressMsg{keyPress("/"), keyPress("w"), keyPress("i")},
			want: false,
		},
		{name: "moving the cursor", keys: []tea.KeyPressMsg{{Code: tea.KeyDown}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := sizedFixtureModel(t, 100, 34)

			var cmd tea.Cmd
			for _, k := range tt.keys {
				next, c := m.Update(k)
				updated, ok := next.(Model)
				if !ok {
					t.Fatalf("Update returned %T, want tui.Model", next)
				}
				m, cmd = updated, c
			}

			if got := carriesClearScreen(cmd); got != tt.want {
				t.Errorf("the last keystroke returned clear-screen=%v, want %v", got, tt.want)
			}
		})
	}
}

// carriesClearScreen reports whether cmd produces a tea.ClearScreen, walking a
// tea.Batch's children. Running a Cmd is how its message is read; nothing in
// these branches blocks or touches the network.
func carriesClearScreen(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if reflect.TypeOf(msg) == reflect.TypeOf(tea.ClearScreen()) {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if carriesClearScreen(c) {
				return true
			}
		}
	}
	return false
}

// A group heading is never the last line of a body: the model has to leave the
// screen in a state a repaint can reproduce whatever the filter did to it.
func TestFilteringNeverLeavesAStrayHeading(t *testing.T) {
	m := sizedFixtureModel(t, 80, 24)
	for _, k := range []tea.KeyPressMsg{keyPress("/"), keyPress("z"), keyPress("z"), keyPress("z")} {
		next, _ := m.Update(k)
		updated, ok := next.(Model)
		if !ok {
			t.Fatalf("Update returned %T, want tui.Model", next)
		}
		m = updated
	}

	// Nothing matches "zzz", so no group heading may survive into the frame.
	content := m.View().Content
	for _, category := range []model.StatusCategory{
		model.StatusTodo, model.StatusInProgress, model.StatusDone, model.StatusCancelled,
	} {
		if heading := strings.ToUpper(plain.CategoryLabel(category)) + " ("; strings.Contains(content, heading) {
			t.Errorf("the frame still shows the %q heading with nothing under it:\n%s", heading, content)
		}
	}
	checkFrameShape(t, "with nothing matching", content, 80, 24)
}
