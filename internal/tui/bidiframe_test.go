package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

// The boundary balances a Ticket title, and then the list cuts it to the width
// left beside the key column — which can drop the terminator the boundary
// appended and put the defect back on the screen. Every line the monitor draws
// stays balanced, at a width that truncates.
func TestFrameLinesStayBalancedWhenTruncated(t *testing.T) {
	const marker = "NORMAL"
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	in := ListInput{
		Header: Header{Key: "EPIC-1", Title: marker + " watchlist"},
		Tickets: []model.Ticket{{
			ID: "T-1", Key: "#1", Status: model.StatusTodo,
			// The override sits early and the run after it is long, so the cut
			// lands inside the scope rather than after it.
			Title:        marker + " \u202e" + strings.Repeat("blocked ", 20),
			NativeStatus: "in review \u202e" + strings.Repeat("native ", 20),
		}},
		FetchedAt: now,
	}

	m := New(t.Context(), Options{Initial: &in, Now: func() time.Time { return now }})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	m = updated.(Model)

	content := m.View().Content
	visible := ansi.Strip(content)
	if !strings.Contains(visible, marker) {
		t.Fatalf("the frame does not show %q, so it proves nothing:\n%s", marker, visible)
	}
	if !strings.Contains(visible, "…") {
		t.Fatalf("the frame was not truncated at this width, so it proves nothing:\n%s", visible)
	}
	if unterminated, stray := termtexttest.Unbalanced(visible); unterminated != 0 || stray != 0 {
		t.Errorf("the rendered frame is not bidi-balanced (unterminated %U, stray %U):\n%+q",
			unterminated, stray, visible)
	}
}
