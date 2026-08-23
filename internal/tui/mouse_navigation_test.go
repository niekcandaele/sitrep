package tui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
)

func detailLinkY(t *testing.T, m Model, doc detailDocument, index int) int {
	t.Helper()
	if index < 0 || index >= len(doc.LinkRows) {
		t.Fatalf("Link index %d outside %d rows", index, len(doc.LinkRows))
	}
	y := detailHeaderHeight + doc.LinkRows[index].Line - m.detail.offset
	if y < detailHeaderHeight || y >= detailHeaderHeight+m.detailBodyHeight() || y >= m.height {
		t.Fatalf("Link row %d at y=%d is not visible (offset=%d body=%d height=%d)",
			index, y, m.detail.offset, m.detailBodyHeight(), m.height)
	}
	return y
}

func TestDetailMouseHitMapUsesRenderedDocumentLines(t *testing.T) {
	m, _ := navigableDetailModel(t)
	doc := m.detailDocument()
	m.detail.offset = clampDetailOffset(doc.LinkRows[0].Line-1, len(doc.Lines), m.detailBodyHeight())
	doc = m.detailDocument()
	y := detailLinkY(t, m, doc, 0)
	handler := m.detailMouseHandler(doc)

	for _, x := range []int{0, m.width - 1} {
		cmd := handler(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		if cmd == nil {
			t.Fatalf("x=%d on rendered Link returned no message", x)
		}
		msg, ok := cmd().(detailMouseLinkMsg)
		if !ok {
			t.Fatalf("x=%d translated to %T", x, cmd())
		}
		if msg.sourceID != m.detail.ticket.ID || msg.epoch != m.detailMouseEpoch ||
			msg.identity != doc.LinkRows[0].Identity {
			t.Errorf("x=%d message = %+v, want source %q epoch %d identity %+v",
				x, msg, m.detail.ticket.ID, m.detailMouseEpoch, doc.LinkRows[0].Identity)
		}
	}

	misses := []struct {
		name string
		msg  tea.MouseMsg
	}{
		{name: "header", msg: tea.MouseClickMsg{X: 0, Y: detailHeaderHeight - 1, Button: tea.MouseLeft}},
		{name: "section heading", msg: tea.MouseClickMsg{X: 0, Y: detailHeaderHeight, Button: tea.MouseLeft}},
		{name: "body non-Link", msg: tea.MouseClickMsg{X: 0, Y: y - 1, Button: tea.MouseLeft}},
		{name: "footer", msg: tea.MouseClickMsg{X: 0, Y: detailHeaderHeight + m.detailBodyHeight(), Button: tea.MouseLeft}},
		{name: "negative x", msg: tea.MouseClickMsg{X: -1, Y: y, Button: tea.MouseLeft}},
		{name: "x at width", msg: tea.MouseClickMsg{X: m.width, Y: y, Button: tea.MouseLeft}},
		{name: "negative y", msg: tea.MouseClickMsg{X: 0, Y: -1, Button: tea.MouseLeft}},
		{name: "y at height", msg: tea.MouseClickMsg{X: 0, Y: m.height, Button: tea.MouseLeft}},
		{name: "right", msg: tea.MouseClickMsg{X: 0, Y: y, Button: tea.MouseRight}},
		{name: "modified", msg: tea.MouseClickMsg{X: 0, Y: y, Button: tea.MouseLeft, Mod: tea.ModShift}},
		{name: "release", msg: tea.MouseReleaseMsg{X: 0, Y: y, Button: tea.MouseLeft}},
		{name: "motion", msg: tea.MouseMotionMsg{X: 0, Y: y, Button: tea.MouseLeft}},
		{name: "horizontal wheel", msg: tea.MouseWheelMsg{X: 0, Y: y, Button: tea.MouseWheelRight}},
	}
	for _, tt := range misses {
		t.Run(tt.name, func(t *testing.T) {
			if cmd := handler(tt.msg); cmd != nil {
				domain := cmd()
				t.Errorf("miss translated to %T: %+v", domain, domain)
			}
		})
	}
}

func TestDetailMouseClickAndKeyboardFollowShareTransition(t *testing.T) {
	keyboard, _ := navigableDetailModel(t)
	keyboard = focusLinkAt(keyboard, 0)
	keyboardChild, keyboardCmd := followFocused(t, keyboard)

	mouse, _ := navigableDetailModel(t)
	doc := mouse.detailDocument()
	mouse.detail.offset = ensureDocumentLineVisible(doc.LinkRows[0].Line, mouse.detail.offset, len(doc.Lines), mouse.detailBodyHeight())
	mouseChild, mouseCmd, applied := dispatchMouse(t, mouse, tea.MouseClickMsg{
		X: mouse.width - 1, Y: detailLinkY(t, mouse, doc, 0), Button: tea.MouseLeft,
	})
	if !applied || mouseCmd == nil || keyboardCmd == nil {
		t.Fatalf("follow command applied=%t mouse nil=%t keyboard nil=%t", applied, mouseCmd == nil, keyboardCmd == nil)
	}
	if !reflect.DeepEqual(mouseChild.detail.ticket, keyboardChild.detail.ticket) ||
		mouseChild.detail.linkFocus != keyboardChild.detail.linkFocus ||
		mouseChild.detailGeneration != keyboardChild.detailGeneration ||
		mouseChild.detailMouseEpoch != keyboardChild.detailMouseEpoch ||
		len(mouseChild.trail) != len(keyboardChild.trail) {
		t.Errorf("mouse/keyboard seats differ:\nmouse: %+v\nkeyboard: %+v", mouseChild.detail, keyboardChild.detail)
	}
	if !mouseChild.trail[0].hasLinkFocus || mouseChild.trail[0].linkFocus != doc.LinkRows[0].Identity {
		t.Error("mouse follow did not archive clicked parent focus")
	}
}

func TestDetailMouseExpandedHelpReflowsClickedFocusBeforeTrailSnapshot(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m.width, m.height = 42, 40
	m.help.SetWidth(m.width)
	m.help.ShowAll = true
	m = m.reconcileDetail(false)
	doc := m.detailDocument()
	clicked := doc.LinkRows[len(doc.LinkRows)-1]
	beforeBodyHeight := m.detailBodyHeight()
	m.detail.offset = clampDetailOffset(clicked.Line-beforeBodyHeight+1, len(doc.Lines), beforeBodyHeight)
	if y := detailLinkY(t, m, doc, len(doc.LinkRows)-1); y != detailHeaderHeight+beforeBodyHeight-1 {
		t.Fatalf("clicked Link y=%d, want former last body row %d", y, detailHeaderHeight+beforeBodyHeight-1)
	}

	child, cmd, applied := dispatchMouse(t, m, tea.MouseClickMsg{
		X:      m.width - 1,
		Y:      detailLinkY(t, m, doc, len(doc.LinkRows)-1),
		Button: tea.MouseLeft,
	})
	if !applied || cmd == nil || len(child.trail) != 1 {
		t.Fatalf("mouse follow = applied:%t cmd nil:%t Trail:%d", applied, cmd == nil, len(child.trail))
	}
	entry := child.trail[0]
	if entry.bodyHeight >= beforeBodyHeight {
		t.Fatalf("focused help did not shrink body: before=%d snapshot=%d", beforeBodyHeight, entry.bodyHeight)
	}
	if entry.offset <= m.detail.offset {
		t.Errorf("snapshot kept stale offset %d after body shrank from %d to %d",
			entry.offset, beforeBodyHeight, entry.bodyHeight)
	}

	parent := child.popDetailTrail()
	parentDoc := parent.detailDocument()
	row, ok := detailLinkRowByIdentity(parentDoc, clicked.Identity)
	if !ok || row.Line < parent.detail.offset || row.Line >= parent.detail.offset+parent.detailBodyHeight() {
		t.Errorf("Esc restored clicked focus offscreen: row=%d offset=%d body=%d",
			row.Line, parent.detail.offset, parent.detailBodyHeight())
	}
}

func TestDetailMouseDomainMessageRevalidatesLastFrameFacts(t *testing.T) {
	base, _ := navigableDetailModel(t)
	doc := base.detailDocument()
	base.detail.offset = ensureDocumentLineVisible(doc.LinkRows[0].Line, base.detail.offset, len(doc.Lines), base.detailBodyHeight())
	y := detailLinkY(t, base, doc, 0)
	translate := base.View().OnMouse(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
	if translate == nil {
		t.Fatal("captured Link produced no domain message")
	}
	msg := translate()
	captured := doc.LinkRows[0]

	t.Run("same source reorder resolves current Link data", func(t *testing.T) {
		m := base
		links := append([]model.Link(nil), m.detail.input.Detail.Links...)
		links[0].Target.Title = "Current renamed target"
		links[0], links[1] = links[1], links[0]
		m.detail.input.Detail.Links = links
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd == nil || got.detail.ticket.ID != captured.Link.Target.ID || got.detail.ticket.Title != "Current renamed target" {
			t.Errorf("reordered stale callback = target %q title %q cmd nil=%t",
				got.detail.ticket.ID, got.detail.ticket.Title, cmd == nil)
		}
	})

	t.Run("same source after seat round trip", func(t *testing.T) {
		childModel, _ := base.followDetailLink(captured.Identity)
		restored := childModel.(Model).popDetailTrail()
		if restored.detail.ticket.ID != base.detail.ticket.ID || restored.detailMouseEpoch == base.detailMouseEpoch {
			t.Fatalf("round trip source=%q epoch=%d, want source %q and epoch after %d",
				restored.detail.ticket.ID, restored.detailMouseEpoch,
				base.detail.ticket.ID, base.detailMouseEpoch)
		}

		next, cmd := restored.Update(msg)
		got := next.(Model)
		if cmd != nil || got.detail.ticket.ID != base.detail.ticket.ID || len(got.trail) != 0 {
			t.Error("old-seat callback followed after returning to the same Ticket")
		}
	})

	t.Run("removed relationship", func(t *testing.T) {
		m := base
		m.detail.input.Detail.Links = m.detail.input.Detail.Links[1:]
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || got.detail.ticket.ID != base.detail.ticket.ID || len(got.trail) != 0 {
			t.Error("removed relationship followed from a stale callback")
		}
	})

	t.Run("capability hidden relationship", func(t *testing.T) {
		m := base
		m.detail.input.Capabilities.BlockingLinks = false
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || got.detail.ticket.ID != base.detail.ticket.ID || len(got.trail) != 0 {
			t.Error("capability-hidden relationship followed from a stale callback")
		}
	})

	t.Run("another source Ticket", func(t *testing.T) {
		m := base
		m.detail.ticket.ID = "another-source"
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || len(got.trail) != 0 {
			t.Error("another Ticket accepted a stale callback")
		}
	})

	t.Run("mouse disabled after queue", func(t *testing.T) {
		m := base
		m.mouseEnabled = false
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || len(got.trail) != 0 {
			t.Error("queued click followed after mouse capture was disabled")
		}
	})

	t.Run("mouse toggled off and on after queue", func(t *testing.T) {
		m := base.toggleMouse().toggleMouse()
		if !m.mouseEnabled || m.detailMouseEpoch == base.detailMouseEpoch {
			t.Fatalf("mouse toggle state enabled=%t epoch=%d, want enabled and epoch after %d",
				m.mouseEnabled, m.detailMouseEpoch, base.detailMouseEpoch)
		}
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || len(got.trail) != 0 {
			t.Error("queued click followed after mouse was toggled off and on")
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		m := base
		m.mode = modeList
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || got.mode != modeList || len(got.trail) != 0 {
			t.Error("list mode accepted a Detail callback")
		}
	})
}

func TestDetailMouseDuplicateOccurrenceIsIndependent(t *testing.T) {
	m, _ := navigableDetailModel(t)
	duplicate := model.Link{
		Kind: model.LinkRelates, NativeLabel: "duplicates",
		Target: model.LinkTarget{ID: "DUP-1", Key: "DUP-1", Title: "same"},
	}
	m.detail.input.Detail.Links = []model.Link{duplicate, duplicate, duplicate}
	m.detail.input.Capabilities.BlockingLinks = true
	m = m.reconcileDetail(true)
	doc := m.detailDocument()
	if len(doc.LinkRows) != 3 || doc.LinkRows[0].Identity.Occurrence != 0 ||
		doc.LinkRows[1].Identity.Occurrence != 1 || doc.LinkRows[2].Identity.Occurrence != 2 {
		t.Fatalf("duplicate identities = %+v", doc.LinkRows)
	}
	m.detail.offset = ensureDocumentLineVisible(doc.LinkRows[2].Line, 0, len(doc.Lines), m.detailBodyHeight())
	next, _, applied := dispatchMouse(t, m, tea.MouseClickMsg{
		X: 0, Y: detailLinkY(t, m, doc, 2), Button: tea.MouseLeft,
	})
	if !applied || len(next.trail) != 1 || !next.trail[0].hasLinkFocus ||
		next.trail[0].linkFocus != doc.LinkRows[2].Identity {
		t.Errorf("third duplicate was not archived independently: %+v", next.trail)
	}
}

func TestRawDetailMouseMessagesNeverMutate(t *testing.T) {
	m, _ := navigableDetailModel(t)
	beforeDetail := m.detail
	beforeGeneration := m.detailGeneration
	beforeMouseEpoch := m.detailMouseEpoch
	for _, msg := range []tea.Msg{
		tea.MouseClickMsg{X: 0, Y: detailHeaderHeight, Button: tea.MouseLeft},
		tea.MouseWheelMsg{X: 0, Y: detailHeaderHeight, Button: tea.MouseWheelDown},
	} {
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || !reflect.DeepEqual(got.detail, beforeDetail) ||
			got.detailGeneration != beforeGeneration || got.detailMouseEpoch != beforeMouseEpoch ||
			len(got.trail) != 0 || got.mode != modeDetail {
			t.Errorf("raw %T mutated Model or issued command", msg)
		}
	}
}
