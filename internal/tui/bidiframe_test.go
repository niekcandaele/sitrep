package tui

import (
	"context"
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

// bidiTicket is a Ticket whose every displayed field opens a bidirectional
// scope early and then runs long, so any cut lands inside the scope rather than
// after it. That is the shape that drops the terminator the boundary appended.
func bidiTicket(marker string) model.Ticket {
	return model.Ticket{
		ID: "T-1", Key: "#1", Status: model.StatusTodo,
		Title:        marker + " \u202e" + strings.Repeat("blocked ", 20),
		NativeStatus: "in review \u202e" + strings.Repeat("native ", 20),
		Assignees:    []model.User{{Login: "\u202e" + strings.Repeat("who ", 20)}},
	}
}

// The list is not the only screen that cuts Tracker-controlled text. The Detail
// header's meta line and a comment's byline are cut by the same helper, and the
// Frontier cuts card fields and its own footer. One invariant, every screen: a
// rendered line never carries a bidirectional scope the reader's terminal will
// apply to the rest of the screen.
func TestEveryScreenStaysBalancedWhenTruncated(t *testing.T) {
	const marker = "NORMAL"
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	ticket := bidiTicket(marker)
	// The description and the comment body are short on purpose: they are
	// wrapped into a block rather than clipped, and a wrapped block's scope
	// legitimately spans its own lines. What is asserted here is every line the
	// screen *cuts* — the header meta line, the comment byline, the LINKS rows.
	detail := model.Detail{
		TicketID:    ticket.ID,
		Description: marker + " body",
		Comments: []model.Comment{{
			ID:        "c1",
			Author:    model.User{Login: "\u202e" + strings.Repeat("author ", 20)},
			Body:      marker + " said",
			CreatedAt: now,
		}},
		Links: []model.Link{{
			Kind:        model.LinkBlockedBy,
			NativeLabel: "\u202e" + strings.Repeat("label ", 20),
			Target: model.LinkTarget{
				ID: "T-2", Key: "#2", Status: model.StatusTodo,
				Title:        marker + " \u202e" + strings.Repeat("target ", 20),
				NativeStatus: "\u202e" + strings.Repeat("state ", 20),
			},
		}},
	}
	in := ListInput{
		Header:       Header{Key: "EPIC-1", Title: marker + " watchlist"},
		Tickets:      []model.Ticket{ticket},
		Capabilities: model.Capabilities{Comments: true, BlockingLinks: true},
		FetchedAt:    now,
	}

	// 40 columns: narrow enough that every one of these fields is cut.
	seat := func(t *testing.T) Model {
		t.Helper()
		m := New(t.Context(), Options{
			Initial: &in,
			DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
				return detail, in.Capabilities, nil
			},
			Now: func() time.Time { return now },
		})
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
		return updated.(Model)
	}
	assertBalanced := func(t *testing.T, what, content string) {
		t.Helper()
		visible := ansi.Strip(content)
		if !strings.Contains(visible, marker) {
			t.Fatalf("%s does not show %q, so it proves nothing:\n%s", what, marker, visible)
		}
		if strings.Contains(visible, ticket.Title) {
			t.Fatalf("%s was not truncated at this width, so it proves nothing:\n%s", what, visible)
		}
		if unterminated, stray := termtexttest.Unbalanced(visible); unterminated != 0 || stray != 0 {
			t.Errorf("%s is not bidi-balanced (unterminated %U, stray %U):\n%+q",
				what, unterminated, stray, visible)
		}
	}

	t.Run("Detail", func(t *testing.T) {
		m := seat(t)
		updated, cmd := m.Update(keyPress("enter"))
		m = updated.(Model)
		if cmd == nil {
			t.Fatal("opening the Ticket issued no fetch")
		}
		updated, _ = m.Update(cmd())
		m = updated.(Model)
		if m.mode != modeDetail || !m.detail.loaded {
			t.Fatalf("the Detail screen is not seated: mode %v loaded %v", m.mode, m.detail.loaded)
		}
		assertBalanced(t, "the Detail frame", m.View().Content)
	})

	t.Run("Frontier", func(t *testing.T) {
		m := seat(t)
		updated, _ := m.Update(keyPress("v"))
		m = updated.(Model)
		updated, _ = m.Update(frontierResultMsg(
			m.frontierGeneration, 0, ticket.ID,
			// A hostile answer arriving through the fan-out, which never
			// crossed the seat funnels.
			detail, in.Capabilities, nil))
		m = updated.(Model)
		if m.mode != modeFrontier {
			t.Fatalf("mode = %v, want the Frontier", m.mode)
		}
		assertBalanced(t, "the Frontier frame", m.View().Content)
	})
}
