package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
)

const doubleClickInterval = 500 * time.Millisecond

type listMouseClickMsg struct {
	id model.TicketID
}

type listMouseWheelMsg struct {
	delta int
}

type detailMouseWheelMsg struct {
	delta int
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
			if msg.Button != tea.MouseLeft || msg.Mod != 0 ||
				msg.X < 0 || msg.X >= width ||
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

// detailMouseHandler owns only vertical wheel translation. Clicks deliberately
// remain unclaimed until Detail navigation has its own interaction contract.
func (m Model) detailMouseHandler() func(tea.MouseMsg) tea.Cmd {
	width, height := m.width, m.height
	return func(msg tea.MouseMsg) tea.Cmd {
		wheel, ok := msg.(tea.MouseWheelMsg)
		if !ok || wheel.X < 0 || wheel.X >= width || wheel.Y < 0 || wheel.Y >= height {
			return nil
		}
		switch wheel.Button {
		case tea.MouseWheelUp:
			return mouseCmd(detailMouseWheelMsg{delta: -3})
		case tea.MouseWheelDown:
			return mouseCmd(detailMouseWheelMsg{delta: 3})
		default:
			return nil
		}
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

func (m Model) toggleMouse() Model {
	m.mouseEnabled = !m.mouseEnabled
	m = m.clearPendingClick()
	return m.syncMouseKeys()
}

func (m Model) syncMouseKeys() Model {
	if m.mouseEnabled {
		m.keys.ToggleMouse.SetHelp("m", "off · shift-drag to select text")
		m.detailKeys.ToggleMouse.SetHelp("m", "off · shift-drag to select text")
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
