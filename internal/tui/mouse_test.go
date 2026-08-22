package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

func mouseListModel(t *testing.T, opts Options, tickets []model.Ticket, width, height int) Model {
	t.Helper()
	if opts.Interval == 0 {
		opts.Interval = time.Minute
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Time{} }
	}
	m := New(t.Context(), opts)
	m.width, m.height, m.ready = width, height, true
	m.input = ListInput{Tickets: tickets, Capabilities: model.Capabilities{PullRequests: true}}
	m.hasData = true
	m.refreshing = false
	return m.rebuildRows()
}

func ticketLineY(t *testing.T, m Model, id model.TicketID, line int) int {
	t.Helper()
	row, ok := rowOf(m.rows, id)
	if !ok {
		t.Fatalf("Ticket %q has no row", id)
	}
	heights := rowHeights(m.rows, m.input.Capabilities)
	if line < 0 || line >= heights[row] {
		t.Fatalf("line %d is outside Ticket %q height %d", line, id, heights[row])
	}
	y := headerHeight + line
	for i := m.offset; i < row; i++ {
		y += heights[i]
	}
	return y
}

func dispatchMouse(t *testing.T, m Model, msg tea.MouseMsg) (Model, tea.Cmd, bool) {
	t.Helper()
	handler := m.View().OnMouse
	if handler == nil {
		return m, nil, false
	}
	translate := handler(msg)
	if translate == nil {
		return m, nil, false
	}
	domain := translate()
	next, cmd := m.Update(domain)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return got, cmd, true
}

func TestMouseViewLifecycleAndToggle(t *testing.T) {
	t.Run("default capture starts before size and handler waits for size", func(t *testing.T) {
		m := New(t.Context(), Options{})
		view := m.View()
		if view.MouseMode != tea.MouseModeCellMotion {
			t.Errorf("MouseMode = %v, want CellMotion", view.MouseMode)
		}
		if view.OnMouse != nil {
			t.Error("OnMouse is non-nil before a size-backed frame exists")
		}

		m = mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 20)
		if m.View().OnMouse == nil {
			t.Error("OnMouse is nil after the list frame is ready")
		}
		if help := m.keys.ToggleMouse.Help(); help.Key != "m" || !strings.Contains(help.Desc, "shift-drag") {
			t.Errorf("enabled mouse help = %+v", help)
		}
		for _, want := range []string{"enter", "open", "m", "shift-drag", "q", "quit"} {
			if content := m.View().Content; !strings.Contains(content, want) {
				t.Errorf("80-column list help omitted %q:\n%s", want, content)
			}
		}

		m.mode = modeDetail
		m.detail = detailState{loaded: true, input: fixtureDetailInput(model.Capabilities{})}
		for _, want := range []string{"esc", "back", "m", "shift-drag", "q", "quit"} {
			if content := m.View().Content; !strings.Contains(content, want) {
				t.Errorf("80-column Detail help omitted %q:\n%s", want, content)
			}
		}
	})

	t.Run("no mouse starts disabled and m toggles both screens", func(t *testing.T) {
		m := mouseListModel(t, Options{NoMouse: true}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 20)
		if view := m.View(); view.MouseMode != tea.MouseModeNone || view.OnMouse != nil {
			t.Errorf("disabled view = mode %v handler nil=%t", view.MouseMode, view.OnMouse == nil)
		}
		if got := m.keys.ToggleMouse.Help().Desc; got != "on" {
			t.Errorf("disabled help = %q, want on", got)
		}

		m.lastClickID = "#1"
		next, cmd := m.Update(keyPress("m"))
		m = next.(Model)
		if cmd != nil || !m.mouseEnabled || m.lastClickID != "" || m.View().OnMouse == nil {
			t.Errorf("list toggle: enabled=%t pending=%q handler nil=%t cmd=%v",
				m.mouseEnabled, m.lastClickID, m.View().OnMouse == nil, cmd)
		}

		m.mode = modeDetail
		m.detail = detailState{loaded: true, input: fixtureDetailInput(model.Capabilities{})}
		next, _ = m.Update(keyPress("m"))
		m = next.(Model)
		if m.mouseEnabled || m.View().MouseMode != tea.MouseModeNone || m.View().OnMouse != nil {
			t.Errorf("Detail toggle did not disable capture: enabled=%t mode=%v", m.mouseEnabled, m.View().MouseMode)
		}
	})
}

func TestMouseToggleDoesNotStealSearchText(t *testing.T) {
	m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 20)
	next, _ := m.Update(keyPress("/"))
	m = next.(Model)
	next, _ = m.Update(keyPress("m"))
	m = next.(Model)

	if !m.mouseEnabled {
		t.Error("m toggled mouse while the find box owned the keyboard")
	}
	if got := m.search.Value(); got != "m" {
		t.Errorf("search value = %q, want m", got)
	}
	if content := m.View().Content; !strings.Contains(content, "shift-drag") || strings.Contains(content, "m off") {
		t.Errorf("search help does not own the keyboard honestly:\n%s", content)
	}
}

func TestListMouseClickSelectionAndDoubleClick(t *testing.T) {
	start := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	now := start
	detailCalls := 0
	opts := Options{
		Now: func() time.Time { return now },
		DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			detailCalls++
			return model.Detail{Description: "loaded"}, model.Capabilities{}, nil
		},
	}
	tickets := []model.Ticket{
		ticket("#1", model.StatusTodo),
		ticket("#2", model.StatusTodo),
	}
	m := mouseListModel(t, opts, tickets, 80, 20)
	click := func(id model.TicketID) tea.MouseClickMsg {
		return tea.MouseClickMsg{X: 70, Y: ticketLineY(t, m, id, 0), Button: tea.MouseLeft}
	}

	var cmd tea.Cmd
	m, cmd, _ = dispatchMouse(t, m, click("#1"))
	if cmd != nil || m.mode != modeList || m.selectedID != "#1" || detailCalls != 0 {
		t.Fatalf("first click on selected Ticket opened it: mode=%v selected=%q calls=%d", m.mode, m.selectedID, detailCalls)
	}

	now = start.Add(doubleClickInterval)
	m, cmd, _ = dispatchMouse(t, m, click("#1"))
	if m.mode != modeDetail || cmd == nil || m.lastClickID != "" {
		t.Fatalf("second click at threshold: mode=%v cmd nil=%t pending=%q", m.mode, cmd == nil, m.lastClickID)
	}
	if msg := cmd(); msg == nil {
		t.Fatal("Detail fetch command returned no message")
	}
	if detailCalls != 1 {
		t.Errorf("Detail calls = %d, want exactly one lazy read", detailCalls)
	}

	m.mode = modeList
	m.detail = detailState{}
	now = start.Add(time.Second)
	m, _, _ = dispatchMouse(t, m, click("#1"))
	now = now.Add(doubleClickInterval + time.Nanosecond)
	m, cmd, _ = dispatchMouse(t, m, click("#1"))
	if m.mode != modeList || cmd != nil || detailCalls != 1 {
		t.Errorf("late second click opened Detail: mode=%v calls=%d", m.mode, detailCalls)
	}

	now = now.Add(time.Millisecond)
	m, _, _ = dispatchMouse(t, m, click("#2"))
	if m.selectedID != "#2" || m.mode != modeList || m.lastClickID != "#2" {
		t.Errorf("different Ticket click: selected=%q mode=%v pending=%q", m.selectedID, m.mode, m.lastClickID)
	}
}

func TestDoubleClickWhileSearchingCommitsTheLiveFilter(t *testing.T) {
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	m := mouseListModel(t, Options{
		Now: func() time.Time { return now },
		DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
			return model.Detail{}, model.Capabilities{}, nil
		},
	}, []model.Ticket{
		{ID: "#1", Key: "#1", Title: "alpha", Status: model.StatusTodo},
		{ID: "#2", Key: "#2", Title: "beta", Status: model.StatusTodo},
	}, 80, 20)
	next, _ := m.Update(keyPress("/"))
	m = next.(Model)
	next, _ = m.Update(keyPress("b"))
	m = next.(Model)
	if m.selectedID != "#2" {
		t.Fatalf("live query selected %q, want #2", m.selectedID)
	}

	click := tea.MouseClickMsg{X: 20, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft}
	m, _, _ = dispatchMouse(t, m, click)
	now = now.Add(100 * time.Millisecond)
	m, _, _ = dispatchMouse(t, m, click)
	if m.mode != modeDetail || m.searching || m.search.Focused() || m.filter.Query != "b" {
		t.Errorf("double click from search: mode=%v searching=%t focused=%t query=%q",
			m.mode, m.searching, m.search.Focused(), m.filter.Query)
	}
}

func TestListMouseHitMapAndNoOps(t *testing.T) {
	first := ticket("#1", model.StatusInProgress)
	first.NativeStatus = "In Progress"
	second := ticket("#2", model.StatusTodo)
	m := mouseListModel(t, Options{}, []model.Ticket{first, second}, 40, 18)

	for _, line := range []int{0, 1} {
		got, _, applied := dispatchMouse(t, m, tea.MouseClickMsg{
			X: 39, Y: ticketLineY(t, m, "#1", line), Button: tea.MouseLeft,
		})
		if !applied || got.selectedID != "#1" {
			t.Errorf("Ticket #1 line %d did not map to #1", line)
		}
	}

	heights := rowHeights(m.rows, m.input.Capabilities)
	secondGroup, ok := rowOf(m.rows, "#2")
	if !ok {
		t.Fatal("Ticket #2 missing")
	}
	secondGroup--
	groupY := headerHeight
	for i := m.offset; i < secondGroup; i++ {
		groupY += heights[i]
	}

	paddingY := headerHeight
	for _, h := range heights[m.offset:] {
		paddingY += h
	}
	footerY := headerHeight + m.bodyHeight()
	tests := []struct {
		name string
		msg  tea.MouseMsg
	}{
		{name: "header", msg: tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft}},
		{name: "first heading", msg: tea.MouseClickMsg{X: 0, Y: headerHeight, Button: tea.MouseLeft}},
		{name: "later group spacer", msg: tea.MouseClickMsg{X: 0, Y: groupY, Button: tea.MouseLeft}},
		{name: "later group heading", msg: tea.MouseClickMsg{X: 0, Y: groupY + 1, Button: tea.MouseLeft}},
		{name: "body padding", msg: tea.MouseClickMsg{X: 0, Y: paddingY, Button: tea.MouseLeft}},
		{name: "footer", msg: tea.MouseClickMsg{X: 0, Y: footerY, Button: tea.MouseLeft}},
		{name: "negative x", msg: tea.MouseClickMsg{X: -1, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft}},
		{name: "x at width", msg: tea.MouseClickMsg{X: m.width, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft}},
		{name: "right click", msg: tea.MouseClickMsg{X: 0, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseRight}},
		{name: "middle click", msg: tea.MouseClickMsg{X: 0, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseMiddle}},
		{name: "shift left", msg: tea.MouseClickMsg{X: 0, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft, Mod: tea.ModShift}},
		{name: "release", msg: tea.MouseReleaseMsg{X: 0, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft}},
		{name: "motion", msg: tea.MouseMotionMsg{X: 0, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft}},
		{name: "horizontal wheel", msg: tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelLeft}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := m
			before.lastClickID = "#1"
			got, _, _ := dispatchMouse(t, before, tt.msg)
			if got.selectedID != before.selectedID || got.mode != before.mode || got.offset != before.offset {
				t.Errorf("event changed visible state: selected=%q mode=%v offset=%d", got.selectedID, got.mode, got.offset)
			}
		})
	}
}

func TestStaleListMouseHandlerUsesTicketIdentity(t *testing.T) {
	m := mouseListModel(t, Options{}, []model.Ticket{
		ticket("#1", model.StatusTodo),
		ticket("#2", model.StatusTodo),
	}, 80, 20)
	handler := m.View().OnMouse
	targetY := ticketLineY(t, m, "#2", 0)

	m.input.Tickets = []model.Ticket{
		ticket("#2", model.StatusTodo),
		ticket("#1", model.StatusTodo),
	}
	m = m.rebuildRows()
	translate := handler(tea.MouseClickMsg{X: 10, Y: targetY, Button: tea.MouseLeft})
	next, _ := m.Update(translate())
	m = next.(Model)
	if m.selectedID != "#2" {
		t.Errorf("stale callback selected %q, want captured Ticket #2", m.selectedID)
	}

	m.input.Tickets = []model.Ticket{ticket("#1", model.StatusTodo)}
	m = m.rebuildRows()
	before := m.selectedID
	next, _ = m.Update(translate())
	m = next.(Model)
	if m.selectedID != before {
		t.Errorf("disappeared stale target selected replacement %q, want %q", m.selectedID, before)
	}
}

func TestListMouseWheelMovesTicketsAndDerivesOffset(t *testing.T) {
	tickets := []model.Ticket{
		ticket("#1", model.StatusInProgress),
		ticket("#2", model.StatusTodo),
		ticket("#3", model.StatusTodo),
		ticket("#4", model.StatusDone),
	}
	m := mouseListModel(t, Options{}, tickets, 80, 8)
	down := tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown}
	up := tea.MouseWheelMsg{X: 79, Y: 7, Button: tea.MouseWheelUp}

	m.lastClickID = "#1"
	m, _, _ = dispatchMouse(t, m, down)
	if m.selectedID != "#2" || m.lastClickID != "" {
		t.Errorf("first wheel selected %q pending=%q, want #2 and cleared", m.selectedID, m.lastClickID)
	}
	m, _, _ = dispatchMouse(t, m, down)
	if m.selectedID != "#3" || m.offset == 0 {
		t.Errorf("second wheel selected %q offset=%d, want #3 and derived scrolling", m.selectedID, m.offset)
	}
	m, _, _ = dispatchMouse(t, m, down)
	if m.selectedID != "#4" {
		t.Errorf("wheel across group selected %q, want #4", m.selectedID)
	}

	atEnd := m
	m, _, _ = dispatchMouse(t, m, down)
	if m.selectedID != atEnd.selectedID || m.offset != atEnd.offset {
		t.Errorf("wheel at end moved selection/offset from %q/%d to %q/%d",
			atEnd.selectedID, atEnd.offset, m.selectedID, m.offset)
	}
	m, _, _ = dispatchMouse(t, m, up)
	if m.selectedID != "#3" {
		t.Errorf("wheel up selected %q, want #3", m.selectedID)
	}

	before := m
	m, _, applied := dispatchMouse(t, m, tea.MouseWheelMsg{X: 80, Y: 0, Button: tea.MouseWheelDown})
	if applied || m.selectedID != before.selectedID || m.offset != before.offset {
		t.Error("out-of-frame wheel changed the list")
	}
}

func TestListWheelWorksWhileSearching(t *testing.T) {
	m := mouseListModel(t, Options{}, []model.Ticket{
		{ID: "#1", Key: "#1", Title: "match one", Status: model.StatusTodo},
		{ID: "#2", Key: "#2", Title: "match two", Status: model.StatusTodo},
	}, 80, 20)
	next, _ := m.Update(keyPress("/"))
	m = next.(Model)
	for _, r := range "match" {
		next, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(Model)
	}
	m, _, _ = dispatchMouse(t, m, tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if !m.searching || m.filter.Query != "match" || m.selectedID != "#2" {
		t.Errorf("search wheel: searching=%t query=%q selected=%q", m.searching, m.filter.Query, m.selectedID)
	}
}

func TestDetailMouseWheelAndClicks(t *testing.T) {
	m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 32, 12)
	m.mode = modeDetail
	m.detail = detailState{
		ticket: m.rows[1].Ticket,
		input:  fixtureDetailInput(model.Capabilities{Comments: true, BlockingLinks: true}),
		loaded: true,
	}
	if len(m.detailBodyLines()) <= m.detailBodyHeight()+3 {
		t.Fatal("Detail fixture is not long enough to test wheel scrolling")
	}

	m.lastClickID = "#1"
	m, _, _ = dispatchMouse(t, m, tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	if m.detail.offset != 3 || m.lastClickID != "" {
		t.Errorf("wheel down offset=%d pending=%q, want 3 and cleared", m.detail.offset, m.lastClickID)
	}
	m, _, _ = dispatchMouse(t, m, tea.MouseWheelMsg{X: 31, Y: 11, Button: tea.MouseWheelUp})
	if m.detail.offset != 0 {
		t.Errorf("wheel up offset=%d, want clamped top", m.detail.offset)
	}
	for i := 0; i < 100; i++ {
		m, _, _ = dispatchMouse(t, m, tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	}
	if m.detail.offset != m.clampDetail(1<<20) {
		t.Errorf("wheel bottom offset=%d, want %d", m.detail.offset, m.clampDetail(1<<20))
	}

	m.width = 80
	m.detail.offset = m.clampDetail(m.detail.offset)
	before := m.detail.offset
	m, _, _ = dispatchMouse(t, m, tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelUp})
	if m.detail.offset != max(before-3, 0) {
		t.Errorf("wheel after rewrap offset=%d, want %d", m.detail.offset, max(before-3, 0))
	}

	linkLine := -1
	for i, line := range m.detailBodyLines() {
		if strings.Contains(line, "is blocked by") {
			linkLine = i
			break
		}
	}
	if linkLine < 0 {
		t.Fatal("Detail fixture has no rendered Link line")
	}
	m.detail.offset = m.clampDetail(linkLine)
	linkY := detailHeaderHeight + linkLine - m.detail.offset
	if linkY < detailHeaderHeight || linkY >= detailHeaderHeight+m.detailBodyHeight() {
		t.Fatalf("Link line y=%d is outside the rendered Detail body", linkY)
	}

	before = m.detail.offset
	for _, click := range []tea.MouseMsg{
		tea.MouseClickMsg{X: 0, Y: linkY, Button: tea.MouseLeft},
		tea.MouseClickMsg{X: 0, Y: detailHeaderHeight + 1, Button: tea.MouseRight},
		tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelRight},
	} {
		got, _, applied := dispatchMouse(t, m, click)
		if applied || got.detail.offset != before || got.mode != modeDetail {
			t.Errorf("Detail click/horizontal wheel changed state: applied=%t offset=%d", applied, got.detail.offset)
		}
	}

	next, cmd := m.Update(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if cmd != nil || next.(Model).detail.offset != m.detail.offset {
		t.Error("raw mouse message was applied in Update as well as through OnMouse")
	}
}

func TestTeatestMouseDispatchAppliesEachEventOnce(t *testing.T) {
	p := fake.New()
	c := newClock()
	s := startWith(t, c, Options{
		Source:       selectorSource(p, c),
		DetailSource: TicketDetailSource(p),
		Interval:     time.Minute,
		Now:          c.now,
	})
	s.waitFor(t, "Widget sync v2")

	// The first Ticket starts on rows 4-5 and the second on rows 6-7 in the
	// 120-column fixture frame. A trailing blank column is intentionally valid.
	s.tm.Send(tea.MouseClickMsg{X: 119, Y: 6, Button: tea.MouseLeft})
	s.waitFor(t, "▸ #113")
	s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	s.waitFor(t, "▸ #7")

	m, _ := s.finish(t)
	if m.selectedID != "acme/gadgets#7" {
		t.Errorf("selection = %q, want one wheel step to acme/gadgets#7", m.selectedID)
	}
	if p.DetailCalls() != 0 {
		t.Errorf("single click/wheel made %d Detail calls", p.DetailCalls())
	}
}

func TestTeatestDoubleClickAndRuntimeEnable(t *testing.T) {
	t.Run("double click opens once", func(t *testing.T) {
		p := fake.New()
		c := newClock()
		s := startWith(t, c, Options{
			Source:       selectorSource(p, c),
			DetailSource: TicketDetailSource(p),
			Interval:     time.Minute,
			Now:          c.now,
		})
		s.waitFor(t, "Widget sync v2")
		click := tea.MouseClickMsg{X: 10, Y: 4, Button: tea.MouseLeft}
		s.tm.Send(click)
		s.tm.Send(click)
		s.waitFor(t, "DESCRIPTION")
		m, _ := s.finish(t)
		if m.mode != modeDetail || p.DetailCalls() != 1 {
			t.Errorf("mode=%v Detail calls=%d, want Detail and one", m.mode, p.DetailCalls())
		}
	})

	t.Run("disabled then enabled", func(t *testing.T) {
		p := fake.New()
		c := newClock()
		s := startWith(t, c, Options{
			Source:   selectorSource(p, c),
			Interval: time.Minute,
			Now:      c.now,
			NoMouse:  true,
		})
		s.waitFor(t, "Widget sync v2")
		s.tm.Send(tea.MouseClickMsg{X: 10, Y: 6, Button: tea.MouseLeft})
		s.tm.Send(keyPress("m"))
		s.waitFor(t, "shift-drag")
		s.tm.Send(tea.MouseClickMsg{X: 10, Y: 6, Button: tea.MouseLeft})
		s.waitFor(t, "▸ #113")
		m, _ := s.finish(t)
		if !m.mouseEnabled || m.selectedID != "acme/widgets#113" {
			t.Errorf("enabled=%t selected=%q", m.mouseEnabled, m.selectedID)
		}
	})
}

func TestTeatestMouseAfterFilterAndResize(t *testing.T) {
	t.Run("filter click Detail round trip", func(t *testing.T) {
		p := fake.New()
		c := newClock()
		s := startWith(t, c, Options{
			Source:       selectorSource(p, c),
			DetailSource: TicketDetailSource(p),
			Interval:     time.Minute,
			Now:          c.now,
		})
		s.waitFor(t, "Widget sync v2")
		s.tm.Send(keyPress("/"))
		s.typeText("shard")
		s.tm.Send(enterKey)
		s.waitFor(t, `filter: "shard"`)

		click := tea.MouseClickMsg{X: 10, Y: 8, Button: tea.MouseLeft}
		s.tm.Send(click)
		s.tm.Send(click)
		s.waitFor(t, "DESCRIPTION")
		s.tm.Send(keyPress("esc"))
		s.waitFor(t, `filter: "shard"`)

		m, got := s.finish(t)
		keyboardProvider := fake.New()
		keyboardClock := newClock()
		keyboard := startWith(t, keyboardClock, Options{
			Source:       selectorSource(keyboardProvider, keyboardClock),
			DetailSource: TicketDetailSource(keyboardProvider),
			Interval:     time.Minute,
			Now:          keyboardClock.now,
		})
		keyboard.waitFor(t, "Widget sync v2")
		keyboard.tm.Send(keyPress("/"))
		keyboard.typeText("shard")
		keyboard.tm.Send(enterKey)
		keyboard.waitFor(t, `filter: "shard"`)
		keyboard.tm.Send(downKey)
		keyboard.tm.Send(enterKey)
		keyboard.waitFor(t, "DESCRIPTION")
		keyboard.tm.Send(keyPress("esc"))
		keyboard.waitFor(t, `filter: "shard"`)
		_, want := keyboard.finish(t)
		if string(got) != string(want) {
			t.Errorf("mouse round trip changed the filtered list frame\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
		if m.mode != modeList || m.searching || m.filter.Query != "shard" || m.selectedID != "acme/widgets#115" {
			t.Errorf("round trip: mode=%v searching=%t query=%q selected=%q",
				m.mode, m.searching, m.filter.Query, m.selectedID)
		}
		if p.DetailCalls() != 1 {
			t.Errorf("Detail calls = %d, want one", p.DetailCalls())
		}
	})

	t.Run("resized scrolled frame uses new hit map", func(t *testing.T) {
		p := fake.New()
		c := newClock()
		s := startWith(t, c, Options{
			Source:   selectorSource(p, c),
			Interval: time.Minute,
			Now:      c.now,
		})
		s.waitFor(t, "Widget sync v2")
		s.tm.Send(tea.WindowSizeMsg{Width: termWidth, Height: 10})
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.waitFor(t, "▸ #7")

		// Wrapping the help in the short frame shifts #113 from y=6 to
		// y=3. The callback installed for the resized frame must use that map.
		s.tm.Send(tea.MouseClickMsg{X: 10, Y: 3, Button: tea.MouseLeft})
		s.waitFor(t, "▸ #113")
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.waitFor(t, "▸ #115")
		s.tm.Send(tea.WindowSizeMsg{Width: termWidth, Height: termHeight})
		s.waitFor(t, "CANCELLED (1)")

		m, _ := s.finish(t)
		if m.selectedID != "acme/widgets#115" || m.offset == 0 {
			t.Errorf("selection/offset = %q/%d, want #115 in a scrolled window", m.selectedID, m.offset)
		}
	})

	t.Run("Detail wheel matches three owned scroll steps", func(t *testing.T) {
		p := fake.New(fake.WithDetails(longDetails()))
		c := newClock()
		s := startWith(t, c, Options{
			Source:       selectorSource(p, c),
			DetailSource: TicketDetailSource(p),
			Interval:     time.Minute,
			Now:          c.now,
		})
		s.waitFor(t, "Widget sync v2")
		doubleClick := tea.MouseClickMsg{X: 10, Y: 4, Button: tea.MouseLeft}
		s.tm.Send(doubleClick)
		s.tm.Send(doubleClick)
		s.waitFor(t, "DESCRIPTION")
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})

		m, got := s.finish(t)
		if m.detail.offset != 3 {
			t.Errorf("Detail wheel offset = %d, want 3", m.detail.offset)
		}
		expected := m
		expected.detail.offset = 0
		expected = expected.scrollDetail(3)
		if want := frame(expected.View().Content); string(got) != string(want) {
			t.Errorf("mouse-wheel frame differs from three-line owned scroll\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})
}
