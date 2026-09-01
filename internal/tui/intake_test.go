package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

// hostileTicket is a Ticket whose every string field carries the hostile
// fixture, with an identity the Model can still look itself up by: identity is
// outside the boundary precisely because it is never drawn.
func hostileTicket(id model.TicketID) model.Ticket {
	var t model.Ticket
	termtexttest.Fill(&t)
	t.ID = id
	t.ParentID = ""
	t.Status = model.StatusTodo
	return t
}

func hostileListInput(fetchedAt time.Time) ListInput {
	return ListInput{
		Header:       Header{Key: termtexttest.Hostile, Title: termtexttest.Hostile, URL: termtexttest.Hostile},
		Tickets:      []model.Ticket{hostileTicket("T-1")},
		Capabilities: model.Capabilities{PullRequests: true, Comments: true, BlockingLinks: true},
		FetchedAt:    fetchedAt,
	}
}

func hostileDetail(id model.TicketID) model.Detail {
	var d model.Detail
	termtexttest.Fill(&d)
	d.TicketID = id
	d.Links[0].Target.ID = "T-2"
	return d
}

// Identity is exempt everywhere below: a screen shows a Key, never an ID, so
// cleaning one would only corrupt the Detail cache key and the Provider's
// re-reads.
var (
	listStateOptions = []termtexttest.Option{
		termtexttest.Exempt("Tickets[].ID", "Tickets[].ParentID"),
	}
	detailStateOptions = []termtexttest.Option{
		termtexttest.Multiline("Detail.Description", "Detail.Comments[].Body"),
		termtexttest.Exempt("Detail.TicketID", "Detail.Links[].Target.ID"),
	}
	detailValueOptions = []termtexttest.Option{
		termtexttest.Multiline("Description", "Comments[].Body"),
		termtexttest.Exempt("TicketID", "Links[].Target.ID"),
	}
)

// The four direct non-plural funnels below are safe without depending on a
// Provider: every Source and DetailSource is a plain closure. The fifth funnel,
// DetailFanout, is exercised through Frontier because its boundary is the plural
// result fold rather than Model construction. Assertions target Model state,
// where the boundary owns the data, instead of rendered bytes.
func TestEveryDirectInputFunnelIsSanitized(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	t.Run("Options.Initial", func(t *testing.T) {
		in := hostileListInput(now)
		if termtexttest.IsClean(in, listStateOptions...) {
			t.Fatal("the hostile ListInput fixture carries nothing hostile")
		}
		m := New(t.Context(), Options{Initial: &in, Now: func() time.Time { return now }})
		termtexttest.AssertClean(t, "m.input", m.input, listStateOptions...)
	})

	t.Run("Options.Open", func(t *testing.T) {
		open := OpenTicket{
			Ticket: hostileTicket("T-1"),
			Parent: Header{Key: termtexttest.Hostile, Title: termtexttest.Hostile, URL: termtexttest.Hostile},
		}
		m := New(t.Context(), Options{Open: &open, Now: func() time.Time { return now }})
		termtexttest.AssertClean(t, "m.detail.ticket", m.detail.ticket,
			termtexttest.Exempt("ID", "ParentID"))
		termtexttest.AssertClean(t, "m.detail.input", m.detail.input, detailStateOptions...)
	})

	t.Run("Options.Source", func(t *testing.T) {
		src := func(context.Context) (ListInput, error) { return hostileListInput(now), nil }
		m := New(t.Context(), Options{Source: src, Now: func() time.Time { return now }})
		updated, _ := m.Update(m.fetchCmd(m.generation)())
		m = updated.(Model)

		if !m.hasData {
			t.Fatal("the refresh did not land, so this test asserts on an empty model")
		}
		termtexttest.AssertClean(t, "m.input", m.input, listStateOptions...)
	})

	t.Run("Options.DetailSource", func(t *testing.T) {
		open := OpenTicket{Ticket: model.Ticket{ID: "T-1", Key: "#1", Title: "Root"}}
		m := New(t.Context(), Options{
			Open: &open,
			DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
				return hostileDetail("T-1"), model.Capabilities{Comments: true, BlockingLinks: true}, nil
			},
			Now: func() time.Time { return now },
		})
		updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
		m = updated.(Model)
		updated, _ = m.Update(m.detailFetchCmd(m.detailGeneration, m.detail.ticket.ID)())
		m = updated.(Model)

		cached, hit := m.details["T-1"]
		if !hit {
			t.Fatal("the Detail read did not land in the session cache")
		}
		termtexttest.AssertClean(t, "m.details[T-1].detail", cached.detail, detailValueOptions...)
		termtexttest.AssertClean(t, "m.detail.input.Detail", m.detail.input.Detail, detailValueOptions...)
	})
}

// A Source is a plain function, so provider.Errorf never sees its failure: the
// footer would draw whatever bytes the error carried.
func TestSourceErrorTextIsSanitized(t *testing.T) {
	sentinel := errors.New("sentinel")
	hostileErr := func() error {
		return errors.Join(errors.New(termtexttest.Hostile), sentinel)
	}

	tests := []struct {
		name  string
		build func(t *testing.T) Model
		read  func(Model) error
	}{
		{
			name: "Source",
			build: func(t *testing.T) Model {
				m := New(t.Context(), Options{
					Source: func(context.Context) (ListInput, error) { return ListInput{}, hostileErr() },
				})
				updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
				m = updated.(Model)
				updated, _ = m.Update(m.fetchCmd(m.generation)())
				return updated.(Model)
			},
			read: func(m Model) error { return m.lastErr },
		},
		{
			name: "DetailSource",
			build: func(t *testing.T) Model {
				open := OpenTicket{Ticket: model.Ticket{ID: "T-1", Key: "#1", Title: "Root"}}
				m := New(t.Context(), Options{
					Open: &open,
					DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
						return model.Detail{}, model.Capabilities{}, hostileErr()
					},
				})
				updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
				m = updated.(Model)
				updated, _ = m.Update(m.detailFetchCmd(m.detailGeneration, m.detail.ticket.ID)())
				return updated.(Model)
			},
			read: func(m Model) error { return m.detail.lastErr },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.build(t)
			err := tt.read(m)
			if err == nil {
				t.Fatal("the failing read did not reach the model")
			}
			if !errors.Is(err, sentinel) {
				t.Errorf("errors.Is lost the wrapped sentinel through the boundary: %v", err)
			}
			termtexttest.AssertClean(t, "lastErr.Error()", err.Error())
			assertNoInjectedControls(t, "frame", m.View().Content)
		})
	}
}

// The frame is the last place to look, and it looks at the whole screen rather
// than at the one field a render site remembered to clean.
func TestHostileListFrameCarriesNoInjectedControls(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	in := hostileListInput(now)
	m := New(t.Context(), Options{Initial: &in, Now: func() time.Time { return now }})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
	m = updated.(Model)

	raw := m.View().Content
	assertNoInjectedControls(t, "list frame", raw)
	assertAllHyperlinksBalanced(t, raw)

	visible := ansi.Strip(raw)
	// A frame that rendered nothing would pass every assertion above.
	for _, want := range []string{"café", "東京", "visible"} {
		if !strings.Contains(visible, want) {
			t.Errorf("the frame lost surviving printable text %q:\n%s", want, visible)
		}
	}
	// Line keeps a sequence's printable payload — that policy is unchanged —
	// so what must not survive is the sequence itself: no OSC 52 reaches the
	// terminal, and no hyperlink scope opens on an attacker's URI.
	for _, forbidden := range []string{"\x1b]52;", hyperlinkOpen("https://evil.example.test/")} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the frame carries the hostile sequence % x", []byte(forbidden))
		}
	}
}

// assertNoInjectedControls fails when anything but sitrep's own escape
// sequences reaches the terminal: the frame must be valid UTF-8, and with its
// own ANSI stripped it must carry no control character at all.
func assertNoInjectedControls(t *testing.T, label, raw string) {
	t.Helper()
	if !utf8.ValidString(raw) {
		t.Errorf("%s contains malformed UTF-8: % x", label, []byte(raw))
	}
	for _, r := range ansi.Strip(raw) {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("%s carries the control character %U outside its own escape sequences", label, r)
			return
		}
	}
}
