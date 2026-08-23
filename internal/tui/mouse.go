package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
)

const (
	doubleClickInterval        = 500 * time.Millisecond
	mouseEnabledHelp           = "off · shift-drag to select text"
	mouseEnabledCompactHelp    = "off, shift-drag"
	searchMouseHintHelp        = "to select text"
	searchMouseHintCompactHelp = "select text"
)

type listMouseClickMsg struct {
	id model.TicketID
}

type listMouseWheelMsg struct {
	delta int
}

type detailMouseWheelMsg struct {
	delta int
}

type detailMouseLinkMsg struct {
	sourceID model.TicketID
	identity detailLinkIdentity
}

func mouseCmd(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

// listMouseHandler translates input against the exact list geometry and Ticket
// identities from the last rendered frame. The callback does not mutate Model;
// its domain message is resolved against current rows in Update.
func (m Model) listMouseHandler() func(tea.MouseMsg) tea.Cmd {
	width, height := m.width, m.height
	bodyHeight, offset := m.bodyHeight(), m.offset
	heights := rowHeights(m.rows, m.input.Capabilities)
	ids := make([]model.TicketID, len(m.rows))
	for i, row := range m.rows {
		if row.Kind == RowTicket {
			ids[i] = row.Ticket.ID
		}
	}

	return func(msg tea.MouseMsg) tea.Cmd {
		switch msg := msg.(type) {
		case tea.MouseClickMsg:
			if msg.Button != tea.MouseLeft {
				return mouseCmd(listMouseClickMsg{})
			}
			// Modified primary clicks are reserved for terminal gestures such as
			// Shift-drag text selection. They are transparent to both selection and
			// an in-progress click pair, just like release and motion messages.
			if msg.Mod != 0 {
				return nil
			}
			if msg.X < 0 || msg.X >= width ||
				msg.Y < headerHeight || msg.Y >= headerHeight+bodyHeight {
				return mouseCmd(listMouseClickMsg{})
			}
			row, ok := rowAt(heights, offset, msg.Y-headerHeight)
			if !ok || ids[row] == "" {
				return mouseCmd(listMouseClickMsg{})
			}
			return mouseCmd(listMouseClickMsg{id: ids[row]})

		case tea.MouseWheelMsg:
			if msg.X < 0 || msg.X >= width || msg.Y < 0 || msg.Y >= height {
				return nil
			}
			switch msg.Button {
			case tea.MouseWheelUp:
				return mouseCmd(listMouseWheelMsg{delta: -1})
			case tea.MouseWheelDown:
				return mouseCmd(listMouseWheelMsg{delta: 1})
			}
		}
		return nil
	}
}

// detailMouseHandler translates clicks and wheel movement against the exact
// document and geometry of the last rendered Detail frame. It captures only
// relationship identity; current Link data is re-resolved in Update.
func (m Model) detailMouseHandler(doc detailDocument) func(tea.MouseMsg) tea.Cmd {
	width, height := m.width, m.height
	bodyHeight, offset := m.detailBodyHeight(), m.detail.offset
	sourceID := m.detail.ticket.ID
	rows := make(map[int]detailLinkIdentity, len(doc.LinkRows))
	for _, row := range doc.LinkRows {
		rows[row.Line] = row.Identity
	}

	return func(msg tea.MouseMsg) tea.Cmd {
		switch msg := msg.(type) {
		case tea.MouseClickMsg:
			if msg.Button != tea.MouseLeft || msg.Mod != 0 ||
				msg.X < 0 || msg.X >= width ||
				msg.Y < detailHeaderHeight || msg.Y >= height ||
				msg.Y >= detailHeaderHeight+bodyHeight {
				return nil
			}
			identity, ok := rows[offset+msg.Y-detailHeaderHeight]
			if !ok {
				return nil
			}
			return mouseCmd(detailMouseLinkMsg{sourceID: sourceID, identity: identity})

		case tea.MouseWheelMsg:
			if msg.X < 0 || msg.X >= width || msg.Y < 0 || msg.Y >= height {
				return nil
			}
			switch msg.Button {
			case tea.MouseWheelUp:
				return mouseCmd(detailMouseWheelMsg{delta: -3})
			case tea.MouseWheelDown:
				return mouseCmd(detailMouseWheelMsg{delta: 3})
			}
		}
		return nil
	}
}

func (m Model) onListMouseClick(msg listMouseClickMsg) (tea.Model, tea.Cmd) {
	row, found := rowOf(m.rows, msg.id)
	if !found {
		return m.clearPendingClick(), nil
	}

	now := m.now()
	delta := now.Sub(m.lastClickAt)
	double := m.lastClickID == msg.id && delta >= 0 && delta <= doubleClickInterval
	m = m.selectRow(row)
	if !double {
		m.lastClickID = msg.id
		m.lastClickAt = now
		return m, nil
	}

	m = m.clearPendingClick()
	wasSearching := m.searching
	if wasSearching {
		m.searching = false
		m.search.Blur()
		m = m.setFilter(m.filter)
	}
	next, cmd := m.openDetail()
	if wasSearching {
		cmd = tea.Batch(repaint, cmd)
	}
	return next, cmd
}

func (m Model) onListMouseWheel(msg listMouseWheelMsg) Model {
	m = m.clearPendingClick()
	return m.move(msg.delta)
}

func (m Model) onDetailMouseWheel(msg detailMouseWheelMsg) Model {
	m = m.clearPendingClick()
	return m.scrollDetail(msg.delta)
}

func (m Model) onDetailMouseLink(msg detailMouseLinkMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled || msg.sourceID != m.detail.ticket.ID {
		return m, nil
	}

	doc := m.detailDocument()
	row, ok := detailLinkRowByIdentity(doc, msg.identity)
	if !ok {
		return m.syncDetailKeysFor(doc), nil
	}
	previousBodyHeight := m.detailBodyHeight()
	m.detail.linkFocus = msg.identity
	m.detail.hasLinkFocus = true
	m = m.syncDetailKeysFor(doc)
	if m.detailBodyHeight() != previousBodyHeight {
		m.detail.offset = ensureDocumentLineVisible(
			row.Line, m.detail.offset, len(doc.Lines), m.detailBodyHeight())
	}
	return m.followDetailLink(msg.identity)
}

func (m Model) toggleMouse() Model {
	previousBodyHeight := 0
	if m.ready {
		if m.mode == modeDetail {
			previousBodyHeight = m.detailBodyHeight()
		} else {
			previousBodyHeight = m.bodyHeight()
		}
	}

	m.mouseEnabled = !m.mouseEnabled
	m = m.clearPendingClick().syncMouseKeys()
	if !m.ready {
		return m
	}

	// At constrained widths the shorter disabled-state help can admit another
	// expanded-help column. Reconcile the active screen only when that changes
	// the body geometry; an unchanged footer must not move either scroll owner.
	if m.mode == modeDetail {
		return m.reconcileDetail(m.detailBodyHeight() != previousBodyHeight)
	}
	if m.bodyHeight() != previousBodyHeight {
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	}
	return m
}

func (m Model) syncMouseKeys() Model {
	if m.mouseEnabled {
		m.keys.ToggleMouse.SetHelp("m", mouseEnabledHelp)
		m.detailKeys.ToggleMouse.SetHelp("m", mouseEnabledHelp)
		m.searchKeys.MouseHint.SetEnabled(true)
		return m
	}
	m.keys.ToggleMouse.SetHelp("m", "on")
	m.detailKeys.ToggleMouse.SetHelp("m", "on")
	m.searchKeys.MouseHint.SetEnabled(false)
	return m
}

func (m Model) clearPendingClick() Model {
	m.lastClickID = ""
	m.lastClickAt = time.Time{}
	return m
}
