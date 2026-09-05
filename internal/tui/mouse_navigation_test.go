package tui

import (
	"reflect"
	"strings"
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
		if msg.sourceID != m.detail.ticket.ID || msg.epoch != m.mouseEpoch ||
			msg.identity != doc.LinkRows[0].Identity {
			t.Errorf("x=%d message = %+v, want source %q epoch %d identity %+v",
				x, msg, m.detail.ticket.ID, m.mouseEpoch, doc.LinkRows[0].Identity)
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

func scrollableDetailMouseModel(t *testing.T) Model {
	t.Helper()
	m, _ := navigableDetailModel(t)
	m.width, m.height = 80, 12
	m.help.SetWidth(m.width)
	m.detail.input.Detail.Description = strings.Repeat("scrollable Detail body line\n", 50)
	m = m.reconcileDetail(true)
	if doc := m.detailDocument(); len(doc.Lines) <= m.detailBodyHeight() {
		t.Fatalf("wheel fixture has %d lines for body height %d", len(doc.Lines), m.detailBodyHeight())
	}
	return m
}

func queuedDetailWheel(t *testing.T, m Model) detailMouseWheelMsg {
	t.Helper()
	handler := m.View().OnMouse
	if handler == nil {
		t.Fatal("Detail view has no mouse handler")
	}
	cmd := handler(tea.MouseWheelMsg{X: 0, Y: detailHeaderHeight, Button: tea.MouseWheelDown})
	if cmd == nil {
		t.Fatal("in-frame Detail wheel produced no domain message")
	}
	msg, ok := cmd().(detailMouseWheelMsg)
	if !ok {
		t.Fatalf("Detail wheel translated to %T", cmd())
	}
	if msg.sourceID != m.detail.ticket.ID || msg.epoch != m.mouseEpoch || msg.delta != 3 {
		t.Fatalf("Detail wheel message = %+v, want source %q epoch %d delta 3",
			msg, m.detail.ticket.ID, m.mouseEpoch)
	}
	return msg
}

func applyDetailWheel(t *testing.T, m Model, msg detailMouseWheelMsg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("Detail wheel issued command %v", cmd)
	}
	return next.(Model)
}

func TestDetailMouseWheelDomainMessageRevalidatesSeat(t *testing.T) {
	base := scrollableDetailMouseModel(t)
	msg := queuedDetailWheel(t, base)

	t.Run("resize rejects the stale callback and fresh frame scrolls", func(t *testing.T) {
		next, _ := base.Update(tea.WindowSizeMsg{Width: 72, Height: 10})
		resized := next.(Model)
		if resized.mouseEpoch != base.mouseEpoch+1 {
			t.Fatalf("resize epoch = %d, want %d", resized.mouseEpoch, base.mouseEpoch+1)
		}
		beforeDetail := resized.detail
		got := applyDetailWheel(t, resized, msg)
		if !reflect.DeepEqual(got.detail, beforeDetail) || got.mouseEpoch != resized.mouseEpoch {
			t.Errorf("stale resize wheel changed Detail: detail=%+v epoch=%d", got.detail, got.mouseEpoch)
		}

		fresh := queuedDetailWheel(t, resized)
		got = applyDetailWheel(t, resized, fresh)
		if got.detail.offset != resized.detail.offset+3 {
			t.Errorf("fresh resize wheel offset = %d, want %d", got.detail.offset, resized.detail.offset+3)
		}
	})

	t.Run("same-seat reorder remains valid", func(t *testing.T) {
		m := base
		links := append([]model.Link(nil), m.detail.input.Detail.Links...)
		links[0], links[1] = links[1], links[0]
		m.detail.input.Detail.Links = links
		m = m.reconcileDetail(true)
		got := applyDetailWheel(t, m, msg)
		if got.detail.offset != m.detail.offset+3 {
			t.Errorf("same-seat reorder wheel offset = %d, want %d", got.detail.offset, m.detail.offset+3)
		}
	})

	t.Run("same-seat reread remains valid", func(t *testing.T) {
		rereading, _ := base.startDetailFetch()
		reread := rereading.onDetailFetched(detailFetchedMsg{
			generation: rereading.detailGeneration,
			id:         rereading.detail.ticket.ID,
			detail:     base.detail.input.Detail,
			caps:       base.detail.input.Capabilities,
		})
		if reread.mouseEpoch != base.mouseEpoch {
			t.Fatalf("reread advanced mouse epoch from %d to %d", base.mouseEpoch, reread.mouseEpoch)
		}
		got := applyDetailWheel(t, reread, msg)
		if got.detail.offset != reread.detail.offset+3 {
			t.Errorf("same-seat reread wheel offset = %d, want %d", got.detail.offset, reread.detail.offset+3)
		}
	})

	t.Run("followed child rejects parent wheel", func(t *testing.T) {
		doc := base.detailDocument()
		childModel, cmd := base.followDetailLink(doc.LinkRows[0].Identity)
		child := childModel.(Model)
		if cmd != nil {
			child = acceptDetailCommand(t, child, cmd)
		}
		child.detail.input.Detail.Description = strings.Repeat("child Detail body line\n", 50)
		child = child.reconcileDetail(true)
		got := applyDetailWheel(t, child, msg)
		if got.detail.offset != child.detail.offset {
			t.Errorf("stale parent wheel moved child from %d to %d", child.detail.offset, got.detail.offset)
		}
	})

	t.Run("popped parent rejects prior-seat wheel", func(t *testing.T) {
		doc := base.detailDocument()
		childModel, _ := base.followDetailLink(doc.LinkRows[0].Identity)
		restored := childModel.(Model).popDetailTrail()
		if restored.detail.ticket.ID != base.detail.ticket.ID || restored.mouseEpoch == base.mouseEpoch {
			t.Fatalf("pop restored source %q epoch %d, want source %q with epoch after %d",
				restored.detail.ticket.ID, restored.mouseEpoch, base.detail.ticket.ID, base.mouseEpoch)
		}
		got := applyDetailWheel(t, restored, msg)
		if got.detail.offset != restored.detail.offset {
			t.Errorf("pre-round-trip wheel moved restored parent from %d to %d", restored.detail.offset, got.detail.offset)
		}
	})

	t.Run("another seated root rejects wheel", func(t *testing.T) {
		next, _ := base.seatDetail(model.Ticket{ID: "other-root", Key: "OTHER-1", Title: "Other root"},
			base.detail.input.Parent, allCaps, false)
		other := next.(Model)
		other.detail.loaded = true
		other.detail.loading = false
		other.detail.input.Detail.Description = strings.Repeat("other Detail body line\n", 50)
		other = other.reconcileDetail(true)
		got := applyDetailWheel(t, other, msg)
		if got.detail.offset != other.detail.offset {
			t.Errorf("stale wheel moved another root from %d to %d", other.detail.offset, got.detail.offset)
		}
	})

	t.Run("mouse disabled after queue", func(t *testing.T) {
		disabled := base.toggleMouse()
		got := applyDetailWheel(t, disabled, msg)
		if got.detail.offset != disabled.detail.offset {
			t.Errorf("queued wheel moved disabled Detail from %d to %d", disabled.detail.offset, got.detail.offset)
		}
	})

	t.Run("mouse off and on rejects prior-epoch wheel", func(t *testing.T) {
		retoggled := base.toggleMouse().toggleMouse()
		if !retoggled.mouseEnabled || retoggled.mouseEpoch == base.mouseEpoch {
			t.Fatalf("retoggle enabled=%t epoch=%d, want enabled with epoch after %d",
				retoggled.mouseEnabled, retoggled.mouseEpoch, base.mouseEpoch)
		}
		got := applyDetailWheel(t, retoggled, msg)
		if got.detail.offset != retoggled.detail.offset {
			t.Errorf("prior-epoch wheel moved retoggled Detail from %d to %d", retoggled.detail.offset, got.detail.offset)
		}
	})
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
		mouseChild.mouseEpoch != keyboardChild.mouseEpoch ||
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
		if restored.detail.ticket.ID != base.detail.ticket.ID || restored.mouseEpoch == base.mouseEpoch {
			t.Fatalf("round trip source=%q epoch=%d, want source %q and epoch after %d",
				restored.detail.ticket.ID, restored.mouseEpoch,
				base.detail.ticket.ID, base.mouseEpoch)
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
		if !m.mouseEnabled || m.mouseEpoch == base.mouseEpoch {
			t.Fatalf("mouse toggle state enabled=%t epoch=%d, want enabled and epoch after %d",
				m.mouseEnabled, m.mouseEpoch, base.mouseEpoch)
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

func TestDetailCompositeIdentitySurvivesRereadReorderAndMutableTargetFields(t *testing.T) {
	const label = "is related to"
	linkA0 := model.Link{
		Kind:        model.LinkRelates,
		NativeLabel: label,
		Target: model.LinkTarget{
			ID: "A", Key: "A-old-0", Title: "A old zero", URL: "https://old.test/a0",
			Status: model.StatusTodo, NativeStatus: "Open",
		},
	}
	linkB := model.Link{
		Kind:        model.LinkRelates,
		NativeLabel: label,
		Target: model.LinkTarget{
			ID: "B", Key: "B-old", Title: "B old", URL: "https://old.test/b",
			Status: model.StatusTodo, NativeStatus: "Open",
		},
	}
	linkA1 := linkA0
	linkA1.Target.Key = "A-old-1"
	linkA1.Target.Title = "A old one"
	linkA1.Target.URL = "https://old.test/a1"

	m, _ := navigableDetailModel(t)
	m.width, m.height = 100, 30
	m.help.SetWidth(m.width)
	m.detail.input.Detail.Description = ""
	m.detail.input.Detail.Comments = nil
	m.detail.input.Detail.Links = []model.Link{linkA0, linkB, linkA1}
	m = m.reconcileDetail(true)
	original := m.detailDocument()
	if len(original.LinkRows) != 3 {
		t.Fatalf("Link rows = %d, want 3", len(original.LinkRows))
	}
	wantA0 := detailLinkIdentity{TargetID: "A", Kind: model.LinkRelates, NativeLabel: label}
	wantB := detailLinkIdentity{TargetID: "B", Kind: model.LinkRelates, NativeLabel: label}
	wantA1 := wantA0
	wantA1.Occurrence = 1
	for i, want := range []detailLinkIdentity{wantA0, wantB, wantA1} {
		if got := original.LinkRows[i].Identity; got != want {
			t.Fatalf("original Link %d identity = %+v, want %+v", i, got, want)
		}
	}

	queueClick := func(index int) detailMouseLinkMsg {
		t.Helper()
		cmd := m.View().OnMouse(tea.MouseClickMsg{
			X: 1, Y: detailLinkY(t, m, original, index), Button: tea.MouseLeft,
		})
		if cmd == nil {
			t.Fatalf("Link %d produced no mouse domain message", index)
		}
		msg, ok := cmd().(detailMouseLinkMsg)
		if !ok {
			t.Fatalf("Link %d translated to %T", index, cmd())
		}
		return msg
	}
	queuedB := queueClick(1)
	queuedA1 := queueClick(2)
	m = focusLinkAt(m, 1)

	updatedA0 := linkA1
	updatedA0.Target.Key = "A-new-first"
	updatedA0.Target.Title = "A new first"
	updatedA0.Target.URL = "https://new.test/a-first"
	updatedA0.Target.Status = model.StatusInProgress
	updatedA0.Target.NativeStatus = "Doing"
	updatedA1 := linkA0
	updatedA1.Target.Key = "A-new-second"
	updatedA1.Target.Title = "A new second"
	updatedA1.Target.URL = "https://new.test/a-second"
	updatedA1.Target.Status = model.StatusDone
	updatedA1.Target.NativeStatus = "Closed"
	updatedB := linkB
	updatedB.Target.Key = "B-new"
	updatedB.Target.Title = "B new"
	updatedB.Target.URL = "https://new.test/b"
	updatedB.Target.Status = model.StatusDone
	updatedB.Target.NativeStatus = "Resolved"
	updated := m.detail.input.Detail
	updated.Links = []model.Link{updatedA0, updatedA1, updatedB}

	rereading, _ := m.startDetailFetch()
	reread := rereading.onDetailFetched(detailFetchedMsg{
		generation: rereading.detailGeneration,
		id:         rereading.detail.ticket.ID,
		detail:     updated,
		caps:       rereading.detail.input.Capabilities,
	})
	if reread.mouseEpoch != m.mouseEpoch {
		t.Fatalf("same-seat reread advanced mouse epoch from %d to %d", m.mouseEpoch, reread.mouseEpoch)
	}
	if !reread.detail.hasLinkFocus || reread.detail.linkFocus != wantB || !reread.detailKeys.Follow.Enabled() {
		t.Fatalf("B focus after reread = focused:%t identity:%+v follow:%t, want %+v",
			reread.detail.hasLinkFocus, reread.detail.linkFocus, reread.detailKeys.Follow.Enabled(), wantB)
	}
	rereadDoc := reread.detailDocument()
	for i, want := range []detailLinkIdentity{wantA0, wantA1, wantB} {
		if got := rereadDoc.LinkRows[i].Identity; got != want {
			t.Fatalf("reread Link %d identity = %+v, want %+v", i, got, want)
		}
	}

	keyboardChild, keyboardCmd := followFocused(t, reread)
	if keyboardCmd == nil || !reflect.DeepEqual(keyboardChild.detail.ticket, ticketFromLinkTarget(updatedB.Target)) {
		t.Errorf("retained keyboard focus followed Ticket %+v with nil command %t, want updated B %+v",
			keyboardChild.detail.ticket, keyboardCmd == nil, ticketFromLinkTarget(updatedB.Target))
	}

	mouseBModel, mouseBCmd := reread.Update(queuedB)
	mouseB := mouseBModel.(Model)
	if mouseBCmd == nil || !reflect.DeepEqual(mouseB.detail.ticket, ticketFromLinkTarget(updatedB.Target)) {
		t.Errorf("pre-reread B callback followed Ticket %+v with nil command %t, want updated B %+v",
			mouseB.detail.ticket, mouseBCmd == nil, ticketFromLinkTarget(updatedB.Target))
	}

	mouseAModel, mouseACmd := reread.Update(queuedA1)
	mouseA := mouseAModel.(Model)
	if mouseACmd == nil || !reflect.DeepEqual(mouseA.detail.ticket, ticketFromLinkTarget(updatedA1.Target)) {
		t.Errorf("pre-reread A occurrence 1 callback followed Ticket %+v with nil command %t, want current second A %+v",
			mouseA.detail.ticket, mouseACmd == nil, ticketFromLinkTarget(updatedA1.Target))
	}
	if reflect.DeepEqual(mouseA.detail.ticket, ticketFromLinkTarget(updatedA0.Target)) {
		t.Error("A occurrence 1 callback collided with occurrence 0")
	}
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
	beforeMouseEpoch := m.mouseEpoch
	for _, msg := range []tea.Msg{
		tea.MouseClickMsg{X: 0, Y: detailHeaderHeight, Button: tea.MouseLeft},
		tea.MouseWheelMsg{X: 0, Y: detailHeaderHeight, Button: tea.MouseWheelDown},
	} {
		next, cmd := m.Update(msg)
		got := next.(Model)
		if cmd != nil || !reflect.DeepEqual(got.detail, beforeDetail) ||
			got.detailGeneration != beforeGeneration || got.mouseEpoch != beforeMouseEpoch ||
			len(got.trail) != 0 || got.mode != modeDetail {
			t.Errorf("raw %T mutated Model or issued command", msg)
		}
	}
}
