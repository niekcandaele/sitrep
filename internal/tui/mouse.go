package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
)

const (
	doubleClickInterval = 500 * time.Millisecond
	// Every footer item names what its key does, and so do these: "m off" with
	// capture on parses as "the mouse is off", which is the opposite of both
	// the state and the effect of pressing m.
	mouseEnabledHelp           = "release · shift-drag to select text"
	mouseEnabledCompactHelp    = "release, shift-drag"
	mouseEnabledTerseHelp      = "release/⇧drag"
	mouseDisabledHelp          = "capture"
	searchMouseHintHelp        = "to select text"
	searchMouseHintCompactHelp = "select text"
)

type listMouseClickMsg struct {
	epoch int
	id    model.TicketID
}

type listMouseWheelMsg struct {
	epoch int
	delta int
}

type frontierMouseClickMsg struct {
	epoch int
	id    model.TicketID
}

type frontierMouseWheelMsg struct {
	epoch int
	delta int
}

type detailMouseWheelMsg struct {
	sourceID model.TicketID
	epoch    int
	delta    int
}

type detailMouseLinkMsg struct {
	sourceID model.TicketID
	epoch    int
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
	epoch := m.mouseEpoch
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
				return mouseCmd(listMouseClickMsg{epoch: epoch})
			}
			// Modified primary clicks are reserved for terminal gestures such as
			// Shift-drag text selection. They are transparent to both selection and
			// an in-progress click pair, just like release and motion messages.
			if msg.Mod != 0 {
				return nil
			}
			if msg.X < 0 || msg.X >= width ||
				msg.Y < headerHeight || msg.Y >= headerHeight+bodyHeight {
				return mouseCmd(listMouseClickMsg{epoch: epoch})
			}
			row, ok := rowAt(heights, offset, msg.Y-headerHeight)
			if !ok || ids[row] == "" {
				return mouseCmd(listMouseClickMsg{epoch: epoch})
			}
			return mouseCmd(listMouseClickMsg{epoch: epoch, id: ids[row]})

		case tea.MouseWheelMsg:
			if msg.X < 0 || msg.X >= width || msg.Y < 0 || msg.Y >= height {
				return nil
			}
			switch msg.Button {
			case tea.MouseWheelUp:
				return mouseCmd(listMouseWheelMsg{epoch: epoch, delta: -1})
			case tea.MouseWheelDown:
				return mouseCmd(listMouseWheelMsg{epoch: epoch, delta: 1})
			}
		}
		return nil
	}
}

// frontierMouseHandler translates input against the exact canvas geometry of
// the last rendered Frontier frame. Like the list's, it never mutates Model:
// its domain message is re-resolved against the current layout in Update, and
// ignored when its epoch is stale.
func (m Model) frontierMouseHandler() func(tea.MouseMsg) tea.Cmd {
	width, height := m.width, m.height
	bodyHeight := m.frontierBodyHeight()
	inner := frontierInnerRect(width, bodyHeight)
	offsetX, offsetY := m.frontier.offsetX, m.frontier.offsetY
	epoch := m.mouseEpoch
	layout := m.frontier.layout
	keys := m.effectiveFrontierKeys()

	return func(msg tea.MouseMsg) tea.Cmd {
		if inner.W <= 0 || inner.H <= 0 {
			return nil
		}
		switch msg := msg.(type) {
		case tea.MouseClickMsg:
			// Modified primary clicks are reserved for terminal gestures such as
			// shift-drag text selection, and are transparent here.
			if !keys.MouseSelect.Enabled() || msg.Button != tea.MouseLeft || msg.Mod != 0 {
				return nil
			}
			bodyY := msg.Y - headerHeight
			if msg.X < inner.X || msg.X >= inner.X+inner.W ||
				bodyY < inner.Y || bodyY >= inner.Y+inner.H {
				// Reserved chrome is inert: it does not even clear a pending click.
				return nil
			}
			id, ok := layout.nodeAtPoint(
				msg.X-inner.X+offsetX, bodyY-inner.Y+offsetY)
			if !ok {
				return nil
			}
			return mouseCmd(frontierMouseClickMsg{epoch: epoch, id: id})

		case tea.MouseWheelMsg:
			if !keys.MouseWheel.Enabled() ||
				msg.X < 0 || msg.X >= width || msg.Y < 0 || msg.Y >= height {
				return nil
			}
			switch msg.Button {
			case tea.MouseWheelUp:
				return mouseCmd(frontierMouseWheelMsg{epoch: epoch, delta: -3})
			case tea.MouseWheelDown:
				return mouseCmd(frontierMouseWheelMsg{epoch: epoch, delta: 3})
			}
		}
		return nil
	}
}

func (m Model) onFrontierMouseClick(msg frontierMouseClickMsg) (tea.Model, tea.Cmd) {
	keys := m.effectiveFrontierKeys()
	if !keys.MouseSelect.Enabled() {
		return m, nil
	}
	if _, drawn := m.frontier.layout.nodeAt[msg.id]; !drawn {
		return m.clearPendingClick(), nil
	}

	now := m.now()
	delta := now.Sub(m.lastClickAt)
	double := m.lastClickID == msg.id && delta >= 0 && delta <= doubleClickInterval
	m.frontier.focusID = msg.id
	m.frontier.hasFocus = true
	m = m.reconcileFrontier(true)
	if !double {
		m.lastClickID = msg.id
		m.lastClickAt = now
		return m, nil
	}
	m = m.clearPendingClick()
	if !keys.MouseOpen.Enabled() {
		return m, nil
	}
	return m.openFrontierNode()
}

// onFrontierMouseWheel scrolls the canvas rather than moving focus: a graph's
// rows are six lines tall, and moving focus by wheel would feel broken.
func (m Model) onFrontierMouseWheel(msg frontierMouseWheelMsg) Model {
	if !m.effectiveFrontierKeys().MouseWheel.Enabled() {
		return m
	}
	m = m.clearPendingClick()
	m.frontier.offsetY += msg.delta
	return m.reconcileFrontier(false)
}

// detailMouseHandler translates clicks and wheel movement against the exact
// document and geometry of the last rendered Detail frame. Every domain message
// captures the source seat; Link data is still re-resolved in Update.
func (m Model) detailMouseHandler(doc detailDocument) func(tea.MouseMsg) tea.Cmd {
	width, height := m.width, m.height
	bodyHeight, offset := m.detailBodyHeight(), m.detail.offset
	sourceID, epoch := m.detail.ticket.ID, m.mouseEpoch
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
			return mouseCmd(detailMouseLinkMsg{
				sourceID: sourceID,
				epoch:    epoch,
				identity: identity,
			})

		case tea.MouseWheelMsg:
			if msg.X < 0 || msg.X >= width || msg.Y < 0 || msg.Y >= height {
				return nil
			}
			switch msg.Button {
			case tea.MouseWheelUp:
				return mouseCmd(detailMouseWheelMsg{sourceID: sourceID, epoch: epoch, delta: -3})
			case tea.MouseWheelDown:
				return mouseCmd(detailMouseWheelMsg{sourceID: sourceID, epoch: epoch, delta: 3})
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
	if !m.mouseEnabled || msg.sourceID != m.detail.ticket.ID || msg.epoch != m.mouseEpoch {
		return m
	}
	m = m.clearPendingClick()
	return m.scrollDetail(msg.delta)
}

func (m Model) onDetailMouseLink(msg detailMouseLinkMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled || msg.sourceID != m.detail.ticket.ID || msg.epoch != m.mouseEpoch {
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
		switch m.mode {
		case modeDetail:
			previousBodyHeight = m.detailBodyHeight()
		case modeFrontier:
			previousBodyHeight = m.frontierBodyHeight()
		default:
			previousBodyHeight = m.bodyHeight()
		}
	}

	m.mouseEpoch++
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
	if m.mode == modeFrontier {
		return m.reconcileFrontier(m.frontierBodyHeight() != previousBodyHeight)
	}
	if m.bodyHeight() != previousBodyHeight {
		m.offset = ensureVisible(rowHeights(m.rows, m.input.Capabilities), m.selected, m.offset, m.bodyHeight())
	}
	return m
}

func (m Model) syncMouseKeys() Model {
	if m.mouseEnabled {
		m.keys.ToggleMouse.SetHelp("m", mouseEnabledHelp)
		m.keys.MouseSelect.SetEnabled(true)
		m.keys.MouseOpen.SetEnabled(true)
		m.keys.MouseWheel.SetEnabled(true)
		m.detailKeys.ToggleMouse.SetHelp("m", mouseEnabledHelp)
		m.detailKeys.MouseWheel.SetEnabled(true)
		m.detailKeys.MouseFollow.SetEnabled(m.detailKeys.NextLink.Enabled())
		m.frontierKeys.ToggleMouse.SetHelp("m", mouseEnabledHelp)
		m.frontierKeys.MouseSelect.SetEnabled(true)
		m.frontierKeys.MouseOpen.SetEnabled(true)
		m.frontierKeys.MouseWheel.SetEnabled(true)
		m.searchKeys.MouseHint.SetEnabled(true)
		return m
	}
	m.keys.ToggleMouse.SetHelp("m", mouseDisabledHelp)
	m.keys.MouseSelect.SetEnabled(false)
	m.keys.MouseOpen.SetEnabled(false)
	m.keys.MouseWheel.SetEnabled(false)
	m.detailKeys.ToggleMouse.SetHelp("m", mouseDisabledHelp)
	m.detailKeys.MouseWheel.SetEnabled(false)
	m.detailKeys.MouseFollow.SetEnabled(false)
	m.frontierKeys.ToggleMouse.SetHelp("m", mouseDisabledHelp)
	m.frontierKeys.MouseSelect.SetEnabled(false)
	m.frontierKeys.MouseOpen.SetEnabled(false)
	m.frontierKeys.MouseWheel.SetEnabled(false)
	m.searchKeys.MouseHint.SetEnabled(false)
	return m
}

func (m Model) clearPendingClick() Model {
	m.lastClickID = ""
	m.lastClickAt = time.Time{}
	return m
}
