package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
		if got := m.keys.ToggleMouse.Help().Desc; got != mouseDisabledHelp {
			t.Errorf("disabled help = %q, want %q", got, mouseDisabledHelp)
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

func requireHelpText(t *testing.T, view string, wants ...string) {
	t.Helper()
	got := strings.Join(strings.Fields(string(frame(view))), " ")
	for _, want := range wants {
		want = strings.Join(strings.Fields(want), " ")
		if !strings.Contains(got, want) {
			t.Errorf("help omitted %q:\n%s", want, string(frame(view)))
		}
	}
}

func TestMouseHelpLayoutContracts(t *testing.T) {
	const (
		enabledMouseHelp    = "m release · shift-drag to select text"
		searchMouseHelp     = "shift-drag select text"
		fullSearchMouseHelp = "shift-drag to select text"
	)
	listHelp := []string{
		enabledMouseHelp, "click select Ticket", "double-click open Ticket", "wheel move selection",
		"enter open", "r refresh", "? help", "q quit",
		"↑/k up", "↓/j down", "pgup page up", "pgdn page down",
		"g first", "G last", "d hide finished", "/ find", "esc clear filter",
	}

	t.Run("full list reference is complete at 80 in both mouse states", func(t *testing.T) {
		m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 40)
		m.help.SetWidth(80)
		m.help.ShowAll = true
		m.keys.ClearFilter.SetEnabled(true)
		requireHelpText(t, m.help.View(m.helpKeys()), listHelp...)

		next, _ := m.Update(keyPress("m"))
		m = next.(Model)
		disabledHelp := append([]string(nil), listHelp[4:]...)
		disabledHelp = append([]string{"m capture"}, disabledHelp...)
		requireHelpText(t, m.help.View(m.helpKeys()), disabledHelp...)
	})

	t.Run("narrow list short help remains actionable and full help is complete", func(t *testing.T) {
		m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 40, 40)
		m.help.SetWidth(40)
		m.keys.ClearFilter.SetEnabled(true)
		requireHelpText(t, m.help.View(m.helpKeys()), "m release/⇧drag", "enter open", "q quit")

		m.help.ShowAll = true
		narrowListHelp := append([]string(nil), listHelp...)
		narrowListHelp[0] = "m release, shift-drag"
		requireHelpText(t, m.help.View(m.helpKeys()), narrowListHelp...)

		next, _ := m.Update(keyPress("m"))
		m = next.(Model)
		m.width = 12
		m.help.SetWidth(12)
		m.help.ShowAll = false
		disabled := m.help.View(m.helpKeys())
		requireHelpText(t, disabled, "m capture")
		if strings.Contains(disabled, "shift-drag") {
			t.Errorf("disabled narrow help advertises capture mitigation:\n%s", disabled)
		}
	})

	t.Run("Detail reference and narrow layouts remain actionable and complete", func(t *testing.T) {
		m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 40)
		m.mode = modeDetail
		m.detailKeys.Parent.SetEnabled(true)
		m.help.SetWidth(80)
		m.help.ShowAll = true
		requireHelpText(t, m.detailHelpView(),
			enabledMouseHelp, "wheel scroll body", "esc back", "u watchlist", "r refresh", "? help", "q quit",
			"↑/k up", "↓/j down", "pgup page up", "pgdn page down", "g first", "G last")

		m.width = 40
		m.help.SetWidth(40)
		m.help.ShowAll = false
		requireHelpText(t, m.detailHelpView(), "m release, shift-drag", "esc back", "q quit")
		m.help.ShowAll = true
		requireHelpText(t, m.detailHelpView(),
			enabledMouseHelp, "wheel scroll body", "esc back", "u watchlist", "r refresh", "? help", "q quit",
			"↑/k up", "↓/j down", "pgup page up", "pgdn page down", "g first", "G last")
	})

	t.Run("narrow search exposes text selection and cancel", func(t *testing.T) {
		m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 40, 40)
		m.searching = true
		m.help.SetWidth(40)
		requireHelpText(t, m.help.View(m.helpKeys()), searchMouseHelp, "esc cancel")
		m.help.ShowAll = true
		requireHelpText(t, m.help.View(m.helpKeys()),
			fullSearchMouseHelp, "esc cancel", "enter apply", "↑/↓ move", "ctrl+c quit")
	})
}

func listDiscoveryModel(t *testing.T, noMouse bool, width int) Model {
	t.Helper()
	m := mouseListModel(t, Options{NoMouse: noMouse}, []model.Ticket{
		ticket("#1", model.StatusTodo),
		ticket("#2", model.StatusDone),
	}, width, 20)
	m.input.Header = Header{Key: "#103", Title: "Responsive list footer"}
	m.help.SetWidth(width)
	return m
}

func listShortHelpLine(content string) string {
	lines := strings.Split(ansi.Strip(content), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func requireListDiscoveryFooter(t *testing.T, m Model) {
	t.Helper()
	content := m.View().Content
	lines := strings.Split(content, "\n")
	if got := len(lines); got != m.height {
		t.Errorf("frame height = %d, want %d", got, m.height)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > m.width {
			t.Errorf("line %d width = %d, want at most %d: %q", i, got, m.width, ansi.Strip(line))
		}
	}

	helpLine := listShortHelpLine(content)
	clippedAfterHelp := strings.HasSuffix(helpLine, " …")
	if clippedAfterHelp {
		helpLine = strings.TrimSuffix(helpLine, " …")
	}
	if strings.Contains(helpLine, "…") {
		t.Errorf("short help contains a partial segment: %q", helpLine)
	}
	segments := strings.Split(helpLine, " • ")
	counts := make(map[string]int, len(segments))
	for _, segment := range segments {
		counts[segment]++
	}
	for _, required := range []string{"enter open", "q quit", "? help"} {
		if counts[required] != 1 {
			t.Errorf("short help has %d complete %q segments, want 1: %q", counts[required], required, helpLine)
		}
	}

	mouseSegments := []string{"m release · shift-drag to select text", "m release, shift-drag", "m release/⇧drag"}
	if !m.mouseEnabled {
		mouseSegments = []string{"m capture"}
	}
	mouseCount := 0
	for _, segment := range mouseSegments {
		mouseCount += counts[segment]
	}
	if mouseCount != 1 {
		t.Errorf("short help has %d complete mouse segments, want 1: %q", mouseCount, helpLine)
	}

	sourceOrder := map[string]int{
		"m release · shift-drag to select text": 0,
		"m release, shift-drag":                 0,
		"m release/⇧drag":                       0,
		"m capture":                             0,
		"enter open":                            1,
		"q quit":                                2,
		"↑/k up":                                3,
		"↓/j down":                              4,
		"d hide finished":                       5,
		"/ find":                                6,
		"esc clear filter":                      7,
		"? help":                                8,
		"v frontier":                            9,
	}
	previous := -1
	for _, segment := range segments {
		index, ok := sourceOrder[segment]
		if !ok {
			t.Errorf("short help contains unknown or partial segment %q: %q", segment, helpLine)
			continue
		}
		if index <= previous {
			t.Errorf("short help changed source order at %q: %q", segment, helpLine)
		}
		previous = index
	}
	if clippedAfterHelp && segments[len(segments)-1] != "? help" {
		t.Errorf("short help clips before its complete Help segment: %q …", helpLine)
	}
}

func TestListShortHelpSourceOrder(t *testing.T) {
	keys := DefaultKeyMap()
	keys.ClearFilter.SetEnabled(true)
	keys.Frontier.SetEnabled(true)
	var got []string
	for _, binding := range keys.ShortHelp() {
		got = append(got, binding.Help().Key)
	}
	want := []string{"m", "enter", "q", "↑/k", "↓/j", "d", "/", "esc", "?", "v"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ShortHelp source order = %v, want %v", got, want)
	}
	if got[len(got)-1] != keys.Frontier.Help().Key || got[len(got)-2] != keys.Help.Help().Key {
		t.Fatalf("ShortHelp tail = %v, want Help before final Frontier", got[len(got)-2:])
	}
}

func TestListResponsiveShortHelpKeepsDiscoveryWithAndWithoutFilter(t *testing.T) {
	for _, width := range []int{60, 80, 100, 115, 120} {
		for _, noMouse := range []bool{false, true} {
			mouseState := "captured"
			if noMouse {
				mouseState = "released"
			}
			t.Run(fmt.Sprintf("%d/%s", width, mouseState), func(t *testing.T) {
				m := listDiscoveryModel(t, noMouse, width)
				unfilteredBodyHeight := m.bodyHeight()
				unfilteredFooterLines := len(m.footerLines())
				requireListDiscoveryFooter(t, m)

				next, cmd := m.Update(keyPress("d"))
				m = next.(Model)
				if cmd == nil {
					t.Fatal("d did not request the filter-shape repaint")
				}
				if !m.filter.Active() || !m.keys.ClearFilter.Enabled() {
					t.Fatal("real d transition did not activate Filter and ClearFilter")
				}
				if filterLine := ansi.Strip(m.renderFilterLine()); !strings.Contains(filterLine, "esc clear") {
					t.Errorf("active filter line omitted esc clear: %q", filterLine)
				}
				requireListDiscoveryFooter(t, m)

				next, cmd = m.Update(keyPress("?"))
				m = next.(Model)
				if cmd != nil || !m.help.ShowAll {
					t.Fatalf("? did not open expanded help: ShowAll=%t cmd=%v", m.help.ShowAll, cmd)
				}
				requireHelpText(t, m.help.View(m.helpKeys()), "esc clear filter", "v frontier",
					"pgup page up", "pgdn page down", "g first", "G last", "r refresh")

				next, _ = m.Update(keyPress("?"))
				m = next.(Model)
				next, cmd = m.Update(keyPress("esc"))
				m = next.(Model)
				if cmd == nil || m.filter.Active() || m.keys.ClearFilter.Enabled() {
					t.Fatalf("esc did not clear Filter and repaint: active=%t ClearFilter=%t cmd nil=%t",
						m.filter.Active(), m.keys.ClearFilter.Enabled(), cmd == nil)
				}
				requireListDiscoveryFooter(t, m)
				if m.bodyHeight() != unfilteredBodyHeight || len(m.footerLines()) != unfilteredFooterLines {
					t.Errorf("clear restored body/footer geometry %d/%d, want %d/%d",
						m.bodyHeight(), len(m.footerLines()), unfilteredBodyHeight, unfilteredFooterLines)
				}
				wantMouseDesc := mouseEnabledHelp
				if noMouse {
					wantMouseDesc = mouseDisabledHelp
				}
				if got := m.keys.ToggleMouse.Help().Desc; got != wantMouseDesc {
					t.Errorf("responsive rendering mutated source mouse help to %q, want %q", got, wantMouseDesc)
				}
			})
		}
	}
}

func TestListFooterFramesAtDiscoveryWidths(t *testing.T) {
	for _, width := range []int{60, 80, 100, 115} {
		t.Run(fmt.Sprintf("unfiltered-%d", width), func(t *testing.T) {
			m := listDiscoveryModel(t, false, width)
			requireListDiscoveryFooter(t, m)
			checkGolden(t, fmt.Sprintf("list_footer_%dx20.golden.txt", width), frame(m.View().Content))
		})
		t.Run(fmt.Sprintf("filtered-%d", width), func(t *testing.T) {
			m := listDiscoveryModel(t, false, width)
			next, _ := m.Update(keyPress("d"))
			m = next.(Model)
			if !m.filter.Active() || !m.keys.ClearFilter.Enabled() {
				t.Fatal("real d transition did not enable ClearFilter")
			}
			if filterLine := ansi.Strip(m.renderFilterLine()); !strings.Contains(filterLine, "esc clear") {
				t.Errorf("filtered frame omitted esc clear: %q", filterLine)
			}
			requireListDiscoveryFooter(t, m)
			checkGolden(t, fmt.Sprintf("list_footer_filtered_%dx20.golden.txt", width), frame(m.View().Content))
		})
	}
}

func TestListResponsiveHelpResizePreservesBehaviorAndGeometry(t *testing.T) {
	m := mouseListModel(t, Options{}, []model.Ticket{
		ticket("#1", model.StatusTodo),
		ticket("#2", model.StatusTodo),
		ticket("#3", model.StatusTodo),
	}, 60, 20)
	m.help.SetWidth(60)
	requireListDiscoveryFooter(t, m)
	if helpLine := listShortHelpLine(m.View().Content); strings.Contains(helpLine, "↓/j down") {
		t.Fatalf("60-column fixture unexpectedly advertises optional Down binding: %q", helpLine)
	}
	next, cmd := m.Update(keyPress("j"))
	m = next.(Model)
	if cmd != nil || m.selectedID != "#2" {
		t.Fatalf("omitted j binding is not dispatch-live: selected=%q cmd=%v", m.selectedID, cmd)
	}

	wantBodyHeight := m.bodyHeight()
	wantFooterLines := len(m.footerLines())
	wantTicketY := ticketLineY(t, m, "#1", 0)
	for _, width := range []int{60, 80, 100, 115, 120, 42, 60} {
		beforeID, beforeOffset := m.selectedID, m.offset
		next, cmd = m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		m = next.(Model)
		if cmd != nil {
			t.Fatalf("resize to %d issued command %v", width, cmd)
		}
		if m.selectedID != beforeID || m.offset != beforeOffset {
			t.Errorf("resize to %d moved selection/offset from %q/%d to %q/%d",
				width, beforeID, beforeOffset, m.selectedID, m.offset)
		}
		if m.bodyHeight() != wantBodyHeight || len(m.footerLines()) != wantFooterLines {
			t.Errorf("resize to %d changed list geometry: body=%d footer=%d, want %d/%d",
				width, m.bodyHeight(), len(m.footerLines()), wantBodyHeight, wantFooterLines)
		}
		if gotY := ticketLineY(t, m, "#1", 0); gotY != wantTicketY {
			t.Errorf("resize to %d moved #1 hit row from %d to %d", width, wantTicketY, gotY)
		}
		if width >= listHelpDiscoveryWidth {
			requireListDiscoveryFooter(t, m)
		}

		m.lastClickID = ""
		m.lastClickAt = time.Time{}
		clicked, clickCmd, applied := dispatchMouse(t, m, tea.MouseClickMsg{
			X: 1, Y: ticketLineY(t, m, "#1", 0), Button: tea.MouseLeft,
		})
		if !applied || clickCmd != nil || clicked.selectedID != "#1" {
			t.Errorf("mouse hit after resize to %d: applied=%t selected=%q cmd=%v",
				width, applied, clicked.selectedID, clickCmd)
		}
		row, ok := rowOf(clicked.rows, "#2")
		if !ok {
			t.Fatal("#2 disappeared from resize fixture")
		}
		m = clicked.selectRow(row)
		m.lastClickID = ""
		m.lastClickAt = time.Time{}
	}
}

func TestExpandedMouseHelpIsCompleteAtRequiredTerminalSizes(t *testing.T) {
	for _, noMouse := range []bool{false, true} {
		state := "enabled"
		if noMouse {
			state = "disabled"
		}
		for _, size := range []struct{ width, height int }{{80, 24}, {42, 16}} {
			name := fmt.Sprintf("%s/%dx%d", state, size.width, size.height)
			t.Run("list/"+name, func(t *testing.T) {
				m := mouseListModel(t, Options{NoMouse: noMouse}, fixtureTickets(), size.width, size.height)
				m.help.SetWidth(size.width)
				m.help.ShowAll = true
				content := m.View().Content
				if got := len(strings.Split(content, "\n")); got != size.height {
					t.Errorf("list frame height = %d, want %d", got, size.height)
				}
				if size.width == 80 {
					requireHelpText(t, content,
						"enter open", "r refresh", "? help", "q quit", "↑/k up", "↓/j down",
						"pgup page up", "pgdn page down", "g first", "G last",
						"d hide finished", "/ find")
				} else {
					requireHelpText(t, content,
						"↑/↓/k/j move", "pgup/pgdn page", "g/G first/last",
						"enter/r open/refresh", "d / hide finished/find", "L/?/q legend/help/quit")
				}
				if noMouse {
					requireHelpText(t, content, "m capture")
					for _, forbidden := range []string{"shift-drag", "click select Ticket", "double-click open Ticket", "wheel move selection"} {
						if strings.Contains(content, forbidden) {
							t.Errorf("disabled list help advertises %q:\n%s", forbidden, content)
						}
					}
				} else {
					mouseRecovery := "m release · shift-drag to select text"
					if size.width == 42 {
						mouseRecovery = "m release, shift-drag"
					}
					requireHelpText(t, content,
						mouseRecovery, "click select Ticket",
						"double-click open Ticket", "wheel move selection")
				}
			})

			t.Run("Detail/"+name, func(t *testing.T) {
				m, _ := navigableDetailModel(t)
				m.hasSource = true
				m.width, m.height = size.width, size.height
				m.help.SetWidth(size.width)
				if noMouse {
					m = m.toggleMouse()
				}
				m.help.ShowAll = true
				m = focusLinkAt(m, 0)
				m = m.reconcileDetail(true)
				content := m.View().Content
				if got := len(strings.Split(content, "\n")); got != size.height {
					t.Errorf("Detail frame height = %d, want %d", got, size.height)
				}
				if size.width == 80 {
					requireHelpText(t, content,
						"esc back", "enter follow", "tab next link", "⇧tab previous link",
						"u watchlist", "r refresh", "? help", "q quit", "↑/k up", "↓/j down",
						"pgup page up", "pgdn page down", "g first", "G last")
				} else {
					requireHelpText(t, content,
						"esc/u back/watchlist", "tab/⇧tab links", "enter follow",
						"r/L/?/q refresh/legend/help/quit", "↑/↓/k/j scroll", "pgup/pgdn page", "g/G first/last")
				}
				if noMouse {
					requireHelpText(t, content, "m capture")
					for _, forbidden := range []string{"shift-drag", "wheel scroll body", "click Link follow"} {
						if strings.Contains(content, forbidden) {
							t.Errorf("disabled Detail help advertises %q:\n%s", forbidden, content)
						}
					}
				} else {
					mouseRecovery := "m release · shift-drag to select text"
					if size.width == 42 {
						mouseRecovery = "m release, shift-drag"
					}
					requireHelpText(t, content,
						mouseRecovery, "wheel scroll body", "click Link follow")
				}
			})
		}
	}
}

func TestDetailResponsiveShortHelpKeepsPriorityActions(t *testing.T) {
	for _, width := range []int{40, 42, 45, 50, 60, 80, 120} {
		for _, focused := range []bool{false, true} {
			name := fmt.Sprintf("width-%d/unfocused", width)
			if focused {
				name = fmt.Sprintf("width-%d/focused", width)
			}
			t.Run(name, func(t *testing.T) {
				m, _ := navigableDetailModel(t)
				m.width, m.height = width, 16
				m.help.SetWidth(width)
				m = m.reconcileDetail(true)
				if focused {
					m = focusLinkAt(m, 0)
				}
				m.detailKeys.Parent.SetEnabled(true)
				helpText := strings.TrimSpace(string(frame(m.detailHelpView())))
				normalized := strings.Join(strings.Fields(helpText), " ")
				for _, want := range []string{"m", "esc", "q", "tab", "⇧", "u"} {
					if !strings.Contains(normalized, want) {
						t.Errorf("priority help at width %d omitted %q: %q", width, want, normalized)
					}
				}
				if focused && !strings.Contains(normalized, "follow") {
					t.Errorf("focused help at width %d omitted follow meaning: %q", width, normalized)
				}
				if strings.HasSuffix(normalized, "·") || strings.HasSuffix(normalized, "•") ||
					strings.Contains(normalized, "· ·") || strings.Contains(normalized, "• •") {
					t.Errorf("compact help has a dangling/doubled separator: %q", normalized)
				}
			})
		}
	}
}

func TestDetailResponsiveShortHelpKeepsAtomicActionsAcrossSeats(t *testing.T) {
	input := ListInput{
		Header:       Header{Key: "#111", Title: "Widget sync v2"},
		Tickets:      fixtureTickets(),
		Capabilities: allCaps,
	}
	type detailHelpSeat int
	const (
		noLinks detailHelpSeat = iota
		unfocusedLinks
		focusedLink
	)
	newSeat := func(noMouse bool, kind detailHelpSeat) Model {
		t.Helper()
		m := New(t.Context(), Options{
			Source: func(context.Context) (ListInput, error) {
				return input, nil
			},
			DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
				detail, ok := fake.FixtureDetails()[id]
				if !ok {
					return model.Detail{}, model.Capabilities{}, errors.New("fixture detail not found")
				}
				return detail, allCaps, nil
			},
			Initial:  &input,
			Interval: time.Minute,
			Now:      func() time.Time { return time.Time{} },
			NoMouse:  noMouse,
		})
		m.width, m.height, m.ready = 80, 18, true
		m.details["acme/widgets#112"] = detailEntry{
			detail: fake.FixtureDetails()["acme/widgets#112"],
			caps:   allCaps,
		}
		rootModel, _ := m.openDetail()
		seat := rootModel.(Model)
		switch kind {
		case noLinks:
			seat = focusLinkAt(seat, 0)
			childModel, _ := seat.followFocusedDetailLink()
			seat = childModel.(Model)
		case focusedLink:
			seat = focusLinkAt(seat, 0)
		}
		if !seat.hasSource || seat.detail.input.Parent == (Header{}) {
			t.Fatal("Detail help fixture has no root Watchlist context")
		}
		return seat
	}

	states := []struct {
		name          string
		mouseSegments []string
		noMouse       bool
		kind          detailHelpSeat
		hasLinks      bool
		focused       bool
	}{
		{name: "no-links/mouse-enabled", mouseSegments: []string{"m release", "m release, shift-drag", "m/⇧drag", "m/⇧", "m"}, kind: noLinks},
		{name: "no-links/no-mouse", mouseSegments: []string{"m capture", "m"}, noMouse: true, kind: noLinks},
		{name: "unfocused/mouse-enabled", mouseSegments: []string{"m release", "m release, shift-drag", "m/⇧drag", "m/⇧", "m"}, kind: unfocusedLinks, hasLinks: true},
		{name: "unfocused/no-mouse", mouseSegments: []string{"m capture", "m"}, noMouse: true, kind: unfocusedLinks, hasLinks: true},
		{name: "focused/mouse-enabled", mouseSegments: []string{"m release", "m release, shift-drag", "m/⇧drag", "m/⇧", "m"}, kind: focusedLink, hasLinks: true, focused: true},
		{name: "focused/no-mouse", mouseSegments: []string{"m capture", "m"}, noMouse: true, kind: focusedLink, hasLinks: true, focused: true},
	}
	widths := make([]int, 0, 48)
	for width := 21; width <= 66; width++ {
		widths = append(widths, width)
	}
	widths = append(widths, 80, 120)
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			base := newSeat(state.noMouse, state.kind)
			for _, width := range widths {
				t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
					m := base
					m.width, m.height = width, 16
					m.help.SetWidth(width)
					m = m.reconcileDetail(true)

					helpText := strings.Join(strings.Fields(string(frame(m.detailHelpView()))), " ")
					if helpText == "" {
						t.Fatal("Detail help is empty")
					}
					segments := strings.FieldsFunc(helpText, func(r rune) bool {
						return r == '•' || r == '·'
					})
					for i := range segments {
						segments[i] = strings.TrimSpace(segments[i])
					}
					countExact := func(approved ...string) int {
						count := 0
						for _, segment := range segments {
							for _, want := range approved {
								if segment == want {
									count++
									break
								}
							}
						}
						return count
					}
					type segmentRequirement struct {
						name     string
						approved []string
					}
					requiredSegments := []segmentRequirement{
						{name: "mouse toggle", approved: state.mouseSegments},
						{name: "Back", approved: []string{"esc back", "esc"}},
						{name: "Quit", approved: []string{"q quit", "q"}},
						{name: "Parent", approved: []string{"u watchlist", "u↑"}},
					}
					followSegments := []string{"enter follow", "follow↵", "↵"}
					linkSegments := []string{"tab/⇧tab links", "tab/⇧ links", "tab/⇧"}
					if state.hasLinks {
						requiredSegments = append(requiredSegments,
							segmentRequirement{name: "Links", approved: linkSegments})
					}
					if state.focused {
						requiredSegments = append(requiredSegments,
							segmentRequirement{name: "Follow", approved: followSegments})
					}
					if width >= detailHelpDiscoveryWidth {
						requiredSegments = append(requiredSegments,
							segmentRequirement{name: "Help", approved: []string{"? help"}})
					}
					for _, required := range requiredSegments {
						if count := countExact(required.approved...); count != 1 {
							t.Errorf("Detail help has %d exact %s segments, want 1: %q",
								count, required.name, helpText)
						}
					}
					if !m.detailKeys.Parent.Enabled() {
						t.Error("Detail Parent binding is disabled despite root Watchlist context")
					}
					if state.hasLinks {
						if !m.detailKeys.NextLink.Enabled() || !m.detailKeys.PreviousLink.Enabled() {
							t.Error("Detail with Links has a disabled Link-cycle binding")
						}
					} else {
						if m.detailKeys.NextLink.Enabled() || m.detailKeys.PreviousLink.Enabled() {
							t.Error("no-Link Detail has an enabled Link-cycle binding")
						}
						if count := countExact(linkSegments...); count != 0 {
							t.Errorf("no-Link Detail help has %d Links segments, want 0: %q", count, helpText)
						}
					}
					if state.focused {
						if !m.detailKeys.Follow.Enabled() {
							t.Error("focused Detail has a disabled Follow binding")
						}
					} else {
						if m.detailKeys.Follow.Enabled() {
							t.Error("unfocused Detail has an enabled Follow binding")
						}
						if count := countExact(followSegments...); count != 0 {
							t.Errorf("unfocused Detail help has %d Follow segments, want 0: %q", count, helpText)
						}
					}
					if state.noMouse && strings.Contains(helpText, "shift-drag") {
						t.Errorf("no-mouse help advertises capture recovery: %q", helpText)
					}
					compact := strings.ReplaceAll(helpText, " ", "")
					if strings.HasPrefix(helpText, "•") || strings.HasPrefix(helpText, "·") ||
						strings.HasSuffix(helpText, "•") || strings.HasSuffix(helpText, "·") ||
						strings.Contains(compact, "••") || strings.Contains(compact, "··") ||
						strings.Contains(compact, "•·") || strings.Contains(compact, "·•") ||
						strings.Contains(helpText, "…") {
						t.Errorf("Detail help has a partial binding or dangling/doubled separator: %q", helpText)
					}
					if got := ansi.StringWidth(helpText); got > width {
						t.Errorf("Detail help width = %d, budget %d: %q", got, width, helpText)
					}
				})
			}
		})
	}
}

func TestDetailResponsiveShortHelpAtDecoderRoot(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m.width, m.height = 42, 16
	m.help.SetWidth(m.width)
	m.listArmed = false
	m.hasSource = false
	m.detail.input.Parent = Header{}
	m = m.reconcileDetail(true)

	helpText := func(m Model) string {
		return strings.Join(strings.Fields(string(frame(m.detailHelpView()))), " ")
	}
	hasSegment := func(text, want string) bool {
		for _, segment := range strings.FieldsFunc(text, func(r rune) bool { return r == '•' || r == '·' }) {
			if strings.TrimSpace(segment) == want {
				return true
			}
		}
		return false
	}
	assertBackSegment := func(t *testing.T, m Model, want string) {
		t.Helper()
		text := helpText(m)
		if !hasSegment(text, want) {
			t.Errorf("Detail help omitted %q: %q", want, text)
		}
		other := "esc back"
		if want == other {
			other = "esc quit"
		}
		if hasSegment(text, other) {
			t.Errorf("Detail help advertised %q instead of %q: %q", other, want, text)
		}
	}

	t.Run("unfocused root", func(t *testing.T) {
		text := helpText(m)
		for _, want := range []string{"m/⇧drag", "q quit", "tab/⇧", "? help"} {
			if !strings.Contains(text, want) {
				t.Errorf("decoder-root help omitted %q: %q", want, text)
			}
		}
		if !hasSegment(text, "esc quit") && !hasSegment(text, "esc") {
			t.Errorf("decoder-root help omitted a complete Esc action: %q", text)
		}
		if hasSegment(text, "esc back") {
			t.Errorf("decoder-root help falsely advertised esc back: %q", text)
		}
		if strings.Contains(text, "watchlist") || strings.Contains(text, "u↑") {
			t.Errorf("decoder-root help advertises unavailable Parent: %q", text)
		}
	})

	t.Run("focused root quits", func(t *testing.T) {
		focused := focusLinkAt(m, 0)
		assertBackSegment(t, focused, "esc quit")
		next, cmd := focused.onDetailKey(keyPress("esc"))
		if cmd == nil {
			t.Fatal("focused decoder-root Esc issued no quit command")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("focused decoder-root Esc command produced %T, want tea.QuitMsg", cmd())
		}
		if got := next.(Model); got.mode != modeDetail || len(got.trail) != 0 {
			t.Errorf("focused decoder-root Esc left mode %v Trail %d before quit", got.mode, len(got.trail))
		}
	})

	t.Run("focused list root goes back", func(t *testing.T) {
		root := focusLinkAt(m, 0)
		root.listArmed = true
		root = root.reconcileDetail(false)
		assertBackSegment(t, root, "esc back")
		next, cmd := root.onDetailKey(keyPress("esc"))
		if got := next.(Model); cmd != nil || got.mode != modeList {
			t.Errorf("focused list-root Esc = cmd nil:%t mode:%v, want no command and list", cmd == nil, got.mode)
		}
	})

	t.Run("focused Trail seat goes back", func(t *testing.T) {
		child := focusLinkAt(m, 0)
		child.trail = []detailTrailEntry{child.detailTrailSnapshot()}
		child = child.reconcileDetail(false)
		assertBackSegment(t, child, "esc back")
		next, cmd := child.onDetailKey(keyPress("esc"))
		got := next.(Model)
		if cmd != nil || got.mode != modeDetail || len(got.trail) != 0 {
			t.Errorf("focused Trail Esc = cmd nil:%t mode:%v Trail:%d, want no command, Detail, empty Trail",
				cmd == nil, got.mode, len(got.trail))
		}
	})
}

func TestInitialDetailErrorGuidanceMatchesEscapeDestination(t *testing.T) {
	base, _ := navigableDetailModel(t)
	base.width, base.height = 42, 16
	base.help.SetWidth(base.width)
	base.listArmed = false
	base.hasSource = false
	base.detail.input.Parent = Header{}
	base.detail.loaded = false
	base.detail.loading = false
	base.detail.lastErr = errors.New("permission denied while reading Ticket")
	base = base.reconcileDetail(false)

	frameText := func(m Model) string {
		return strings.Join(strings.Fields(string(frame(m.View().Content))), " ")
	}

	t.Run("decoded root quits", func(t *testing.T) {
		text := frameText(base)
		if !strings.Contains(text, "Press r to try again, esc to quit.") || strings.Contains(text, "esc to go back") {
			t.Errorf("decoded-root error guidance does not name quit: %q", text)
		}
		next, cmd := base.onDetailKey(keyPress("esc"))
		if cmd == nil {
			t.Fatal("decoded-root error Esc issued no quit command")
		}
		quitMsg := cmd()
		if _, ok := quitMsg.(tea.QuitMsg); !ok {
			t.Fatalf("decoded-root error Esc command produced %T, want tea.QuitMsg", quitMsg)
		}
		if got := next.(Model); got.mode != modeDetail || len(got.trail) != 0 {
			t.Errorf("decoded-root error Esc left mode %v Trail %d before quit", got.mode, len(got.trail))
		}
	})

	t.Run("list root goes back", func(t *testing.T) {
		root := base
		root.listArmed = true
		root = root.reconcileDetail(false)
		text := frameText(root)
		if !strings.Contains(text, "Press r to try again, esc to go back.") || strings.Contains(text, "esc to quit") {
			t.Errorf("list-root error guidance does not name back: %q", text)
		}
		next, cmd := root.onDetailKey(keyPress("esc"))
		if got := next.(Model); cmd != nil || got.mode != modeList {
			t.Errorf("list-root error Esc = cmd nil:%t mode:%v, want no command and list", cmd == nil, got.mode)
		}
	})

	t.Run("Trail seat goes back", func(t *testing.T) {
		child := base
		child.trail = []detailTrailEntry{child.detailTrailSnapshot()}
		child = child.reconcileDetail(false)
		text := frameText(child)
		if !strings.Contains(text, "Press r to try again, esc to go back.") || strings.Contains(text, "esc to quit") {
			t.Errorf("Trail error guidance does not name back: %q", text)
		}
		next, cmd := child.onDetailKey(keyPress("esc"))
		got := next.(Model)
		if cmd != nil || got.mode != modeDetail || len(got.trail) != 0 {
			t.Errorf("Trail error Esc = cmd nil:%t mode:%v Trail:%d, want no command, Detail, empty Trail",
				cmd == nil, got.mode, len(got.trail))
		}
	})
}

func TestDetailFramesFitConstrainedTerminalHeight(t *testing.T) {
	t.Run("focused short help at 60x16", func(t *testing.T) {
		m, _ := navigableDetailModel(t)
		m.width, m.height = 60, 16
		m.help.SetWidth(m.width)
		m = focusLinkAt(m, len(m.detailDocument().LinkRows)-1)
		m.detailKeys.Parent.SetEnabled(true)
		m.detail.offset = m.clampDetail(1 << 20)
		content := m.View().Content
		checkGolden(t, "detail_short_60x16.golden.txt", frame(content))
		if got := len(strings.Split(content, "\n")); got != m.height {
			t.Errorf("frame height = %d, want terminal height %d", got, m.height)
		}
		requireHelpText(t, content, "follow", "tab/⇧", "u watchlist", "100%")
	})

	t.Run("focused full help at 42x16", func(t *testing.T) {
		m, _ := navigableDetailModel(t)
		m.width, m.height = 42, 16
		m.help.SetWidth(m.width)
		m.help.ShowAll = true
		m = focusLinkAt(m, 0)
		m.detailKeys.Parent.SetEnabled(true)
		content := m.View().Content
		checkGolden(t, "detail_full_42x16.golden.txt", frame(content))
		if got := len(strings.Split(content, "\n")); got != m.height {
			t.Errorf("frame height = %d, want terminal height %d", got, m.height)
		}
		requireHelpText(t, content,
			"m release, shift-drag", "wheel scroll body", "click Link follow",
			"esc/u back/watchlist", "tab/⇧tab links", "enter follow",
			"r/L/?/q refresh/legend/help/quit", "↑/↓/k/j scroll", "pgup/pgdn page", "g/G first/last")
	})

	t.Run("readable initial error at 42x16", func(t *testing.T) {
		m, _ := navigableDetailModel(t)
		m.width, m.height = 42, 16
		m.help.SetWidth(m.width)
		m.detail.loaded = false
		m.detail.loading = false
		m.detail.lastErr = errors.New("permission denied while reading a private project that no longer exists")
		m = m.reconcileDetail(false)
		content := m.View().Content
		checkGolden(t, "detail_error_42x16.golden.txt", frame(content))
		if got := len(strings.Split(content, "\n")); got != m.height {
			t.Errorf("frame height = %d, want terminal height %d", got, m.height)
		}
		normalized := strings.Join(strings.Fields(string(frame(content))), " ")
		if !strings.Contains(normalized, "permission denied while reading a private project that no longer exists") {
			t.Errorf("wrapped error lost its reason: %q", normalized)
		}
		if !strings.Contains(normalized, "current · never read") {
			t.Errorf("narrow error does not identify current Detail staleness: %q", normalized)
		}
		requireHelpText(t, content, "m/⇧drag", "esc back", "q quit")
	})
}

func TestMouseToggleReclampsChangedHelpGeometry(t *testing.T) {
	t.Run("list keeps the selected Ticket visible in both directions", func(t *testing.T) {
		tickets := make([]model.Ticket, 40)
		for i := range tickets {
			key := fmt.Sprintf("#%d", i+1)
			tickets[i] = ticket(key, model.StatusTodo)
		}
		m := mouseListModel(t, Options{NoMouse: true}, tickets, 42, 20)
		m.help.SetWidth(42)
		m.help.ShowAll = true
		m = m.jump(len(m.rows)-1, -1)

		disabledHeight := m.bodyHeight()
		disabledOffset := m.offset
		next, cmd := m.Update(keyPress("m"))
		m = next.(Model)
		if cmd != nil {
			t.Fatalf("toggle command = %v, want nil", cmd)
		}
		if m.bodyHeight() >= disabledHeight {
			t.Fatalf("enabled body height = %d, want less than disabled height %d", m.bodyHeight(), disabledHeight)
		}
		if m.offset <= disabledOffset {
			t.Errorf("offset = %d after body shrank, want greater than %d", m.offset, disabledOffset)
		}
		selectedY := ticketLineY(t, m, m.selectedID, 0)
		if selectedY >= headerHeight+m.bodyHeight() {
			t.Errorf("selected Ticket y=%d is below body ending at %d", selectedY, headerHeight+m.bodyHeight()-1)
		}
		if got := string(frame(m.View().Content)); !strings.Contains(got, "▸ #40") {
			t.Errorf("selected Ticket disappeared after toggle:\n%s", got)
		}

		shrunkenOffset := m.offset
		next, _ = m.Update(keyPress("m"))
		m = next.(Model)
		if m.bodyHeight() != disabledHeight {
			t.Errorf("disabled body height = %d, want %d", m.bodyHeight(), disabledHeight)
		}
		if m.offset != shrunkenOffset {
			t.Errorf("growing body moved offset from %d to %d", shrunkenOffset, m.offset)
		}
		selectedY = ticketLineY(t, m, m.selectedID, 0)
		if selectedY >= headerHeight+m.bodyHeight() {
			t.Errorf("selected Ticket y=%d is below grown body ending at %d", selectedY, headerHeight+m.bodyHeight()-1)
		}
	})

	t.Run("unchanged Detail help keeps an offscreen focused Link offscreen", func(t *testing.T) {
		m, _ := navigableDetailModel(t)
		m.help.SetWidth(m.width)
		m = focusLinkAt(m, len(m.detailDocument().LinkRows)-1)
		m = m.scrollDetailTo(0)
		row, _ := detailLinkRowByIdentity(m.detailDocument(), m.detail.linkFocus)
		if row.Line < m.detail.offset+m.detailBodyHeight() {
			t.Fatal("test focus is still visible")
		}
		beforeHeight, beforeOffset := m.detailBodyHeight(), m.detail.offset

		next, _ := m.Update(keyPress("m"))
		m = next.(Model)
		if m.detailBodyHeight() != beforeHeight {
			t.Fatalf("toggle changed Detail body height from %d to %d", beforeHeight, m.detailBodyHeight())
		}
		if m.detail.offset != beforeOffset || !m.detail.hasLinkFocus || m.detail.linkFocus != row.Identity {
			t.Errorf("unchanged toggle moved offscreen focus: offset=%d focus=%+v", m.detail.offset, m.detail.linkFocus)
		}
	})

	t.Run("changed Detail help brings an offscreen focused Link into view", func(t *testing.T) {
		m, _ := navigableDetailModel(t)
		m.width, m.height = 42, 20
		m.help.SetWidth(m.width)
		m.help.ShowAll = true
		m = m.reconcileDetail(true)
		m = focusLinkAt(m, len(m.detailDocument().LinkRows)-1)
		m = m.scrollDetailTo(0)
		row, _ := detailLinkRowByIdentity(m.detailDocument(), m.detail.linkFocus)
		if row.Line < m.detail.offset+m.detailBodyHeight() {
			t.Fatal("test focus is still visible")
		}
		beforeHeight := m.detailBodyHeight()

		next, _ := m.Update(keyPress("m"))
		m = next.(Model)
		if m.detailBodyHeight() <= beforeHeight {
			t.Fatalf("disabled body height = %d, want greater than enabled height %d", m.detailBodyHeight(), beforeHeight)
		}
		if row.Line < m.detail.offset || row.Line >= m.detail.offset+m.detailBodyHeight() ||
			!m.detail.hasLinkFocus || m.detail.linkFocus != row.Identity {
			t.Errorf("geometry-changing toggle did not reveal focus: row=%d offset=%d body=%d focus=%+v",
				row.Line, m.detail.offset, m.detailBodyHeight(), m.detail.linkFocus)
		}
	})

	t.Run("decoded Detail stays inside the document in both directions", func(t *testing.T) {
		m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 42, 20)
		m.help.SetWidth(42)
		m.help.ShowAll = true
		m.mode = modeDetail
		m.listArmed = false
		m.hasSource = false
		m.detail = detailState{
			ticket: m.rows[1].Ticket,
			input:  fixtureDetailInput(model.Capabilities{Comments: true, BlockingLinks: true}),
			loaded: true,
		}
		m = m.syncDetailKeys()
		if len(m.detailBodyLines()) <= m.detailBodyHeight()+1 {
			t.Fatal("Detail fixture is not long enough to expose a changed bottom clamp")
		}

		enabledHeight := m.detailBodyHeight()
		m.detail.offset = m.clampDetail(1 << 20)
		enabledBottom := m.detail.offset
		next, _ := m.Update(keyPress("m"))
		m = next.(Model)
		if m.detailBodyHeight() <= enabledHeight {
			t.Fatalf("disabled Detail body height = %d, want greater than enabled height %d", m.detailBodyHeight(), enabledHeight)
		}
		want := m.clampDetail(1 << 20)
		if enabledBottom <= want {
			t.Fatalf("fixture did not expose stale bottom offset: enabled=%d disabled=%d", enabledBottom, want)
		}
		if m.detail.offset != want {
			t.Errorf("disabled Detail offset = %d, want clamped bottom %d", m.detail.offset, want)
		}

		grownOffset := m.detail.offset
		next, _ = m.Update(keyPress("m"))
		m = next.(Model)
		if m.detailBodyHeight() != enabledHeight {
			t.Errorf("re-enabled Detail body height = %d, want %d", m.detailBodyHeight(), enabledHeight)
		}
		if m.detail.offset != grownOffset {
			t.Errorf("shrinking Detail body moved offset from %d to %d", grownOffset, m.detail.offset)
		}
		if got, clamped := m.detail.offset, m.clampDetail(m.detail.offset); got != clamped {
			t.Errorf("re-enabled Detail offset = %d, clamped = %d", got, clamped)
		}
	})
}

func TestTeatestMouseHelpFramesAtConstrainedWidths(t *testing.T) {
	listFull := []string{
		"enter open", "r refresh", "? help", "q quit",
		"↑/k up", "↓/j down", "pgup page up", "pgdn page down",
		"g first", "G last", "d hide finished", "/ find",
	}
	detailFull := []string{
		"esc back", "tab next link", "⇧tab previous link", "u watchlist",
		"r refresh", "? help", "q quit", "↑/k up", "↓/j down",
		"pgup page up", "pgdn page down", "g first", "G last",
	}
	tests := []struct {
		name     string
		screen   mode
		search   bool
		width    int
		expanded bool
		noMouse  bool
		golden   string
		wants    []string
		forbids  []string
	}{
		{
			name: "complete expanded list help at 80", width: 80, expanded: true,
			golden: "mouse_help_full_80.golden.txt",
			wants: append([]string{
				"m release · shift-drag to select text", "click select Ticket",
				"double-click open Ticket", "wheel move selection",
			}, listFull...),
		},
		{
			name: "actionable enabled list short help at 42", width: 42,
			golden: "mouse_help_short_42.golden.txt",
			wants:  []string{"m release/⇧drag", "enter open", "q quit"},
		},
		{
			name: "actionable disabled list short help at 42", width: 42, noMouse: true,
			golden: "mouse_help_short_disabled_42.golden.txt",
			wants:  []string{"m capture", "enter open", "q quit"}, forbids: []string{"shift-drag"},
		},
		{
			name: "complete enabled list full help at 42", width: 42, expanded: true,
			golden: "mouse_help_full_42.golden.txt",
			wants: append([]string{
				"m release, shift-drag", "click select Ticket",
				"double-click open Ticket", "wheel move selection",
			}, listFull...),
		},
		{
			name: "complete disabled list full help at 42", width: 42, expanded: true, noMouse: true,
			golden: "mouse_help_full_disabled_42.golden.txt",
			wants:  append([]string{"m capture"}, listFull...), forbids: []string{"shift-drag"},
		},
		{
			name: "actionable enabled Detail short help at 42", screen: modeDetail, width: 42,
			golden: "mouse_detail_short_42.golden.txt",
			wants:  []string{"m/⇧drag", "q quit", "tab/⇧", "? help", "u↑"},
		},
		{
			name: "actionable disabled Detail short help at 42", screen: modeDetail, width: 42, noMouse: true,
			golden: "mouse_detail_short_disabled_42.golden.txt",
			wants:  []string{"m capture", "esc back", "q quit", "tab/⇧", "? help", "u↑"}, forbids: []string{"shift-drag"},
		},
		{
			name: "complete enabled Detail full help at 42", screen: modeDetail, width: 42, expanded: true,
			golden: "mouse_detail_full_42.golden.txt",
			wants: append([]string{
				"m release · shift-drag to select text", "wheel scroll body", "click Link follow",
			}, detailFull...),
		},
		{
			name: "complete disabled Detail full help at 42", screen: modeDetail, width: 42, expanded: true, noMouse: true,
			golden: "mouse_detail_full_disabled_42.golden.txt",
			wants:  append([]string{"m capture"}, detailFull...), forbids: []string{"shift-drag"},
		},
		{
			name: "enabled find short help exposes cancel at 42", search: true, width: 42,
			golden: "mouse_find_short_42.golden.txt",
			wants:  []string{"shift-drag select text", "esc cancel"}, forbids: []string{"m release"},
		},
		{
			name: "disabled find short help exposes cancel at 42", search: true, width: 42, noMouse: true,
			golden: "mouse_find_short_disabled_42.golden.txt",
			wants:  []string{"esc cancel", "enter apply"}, forbids: []string{"shift-drag", "m capture"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fake.New()
			c := newClock()
			s := startWith(t, c, Options{
				Source:       selectorSource(p, c),
				DetailSource: TicketDetailSource(p),
				Interval:     time.Minute,
				Now:          c.now,
				NoMouse:      tt.noMouse,
			})
			s.waitFor(t, "Widget sync v2")
			s.tm.Send(tea.WindowSizeMsg{Width: tt.width, Height: termHeight})
			if tt.screen == modeDetail {
				s.tm.Send(enterKey)
				s.waitFor(t, "DESCRIPTION")
			}
			if tt.search {
				s.tm.Send(keyPress("/"))
			}
			if tt.expanded {
				s.tm.Send(keyPress("?"))
			}

			quit := keyPress("q")
			if tt.search {
				quit = ctrlCKey
			}
			m, got := s.finishWith(t, quit)
			checkGolden(t, tt.golden, got)
			help := string(got)
			requireHelpText(t, help, tt.wants...)
			for _, forbidden := range tt.forbids {
				if strings.Contains(help, forbidden) {
					t.Errorf("help unexpectedly contains %q:\n%s", forbidden, help)
				}
			}
			if m.help.ShowAll != tt.expanded {
				t.Errorf("ShowAll = %t, want %t", m.help.ShowAll, tt.expanded)
			}
		})
	}
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
	if content := m.View().Content; !strings.Contains(content, "shift-drag") || strings.Contains(content, "m release") {
		t.Errorf("search help does not own the keyboard honestly:\n%s", content)
	}
}

func TestQueuedListMouseMessagesAreRejectedAfterCaptureChanges(t *testing.T) {
	transitions := []struct {
		name  string
		apply func(Model) Model
	}{
		{name: "disabled", apply: func(m Model) Model { return m.toggleMouse() }},
		{name: "off and on", apply: func(m Model) Model { return m.toggleMouse().toggleMouse() }},
	}
	newModel := func(t *testing.T, detailCalls *int) Model {
		t.Helper()
		return mouseListModel(t, Options{
			DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
				*detailCalls = *detailCalls + 1
				return model.Detail{Description: "loaded"}, model.Capabilities{}, nil
			},
		}, []model.Ticket{ticket("#1", model.StatusTodo), ticket("#2", model.StatusTodo)}, 80, 20)
	}
	queue := func(t *testing.T, m Model, msg tea.MouseMsg) tea.Msg {
		t.Helper()
		handler := m.View().OnMouse
		if handler == nil {
			t.Fatal("list view has no mouse handler")
		}
		cmd := handler(msg)
		if cmd == nil {
			t.Fatal("mouse event produced no domain message")
		}
		return cmd()
	}
	applyStale := func(t *testing.T, m Model, msg tea.Msg) Model {
		t.Helper()
		next, cmd := m.Update(msg)
		if cmd != nil {
			t.Fatalf("stale %T issued command %v", msg, cmd)
		}
		return next.(Model)
	}

	for _, transition := range transitions {
		t.Run(transition.name, func(t *testing.T) {
			t.Run("click cannot select or mutate pending click", func(t *testing.T) {
				detailCalls := 0
				m := newModel(t, &detailCalls)
				domain := queue(t, m, tea.MouseClickMsg{
					X: 1, Y: ticketLineY(t, m, "#2", 0), Button: tea.MouseLeft,
				})
				changed := transition.apply(m)
				changed.searching = true
				changed.lastClickID = "#1"
				changed.lastClickAt = time.Unix(1, 0)
				got := applyStale(t, changed, domain)
				if got.selectedID != changed.selectedID || got.lastClickID != changed.lastClickID ||
					!got.lastClickAt.Equal(changed.lastClickAt) || !got.searching ||
					got.mode != modeList || detailCalls != 0 {
					t.Errorf("stale click mutated list: selected=%q pending=%q mode=%v calls=%d",
						got.selectedID, got.lastClickID, got.mode, detailCalls)
				}
			})

			t.Run("second click cannot open", func(t *testing.T) {
				detailCalls := 0
				m := newModel(t, &detailCalls)
				click := tea.MouseClickMsg{X: 1, Y: ticketLineY(t, m, "#1", 0), Button: tea.MouseLeft}
				m, _, _ = dispatchMouse(t, m, click)
				if m.lastClickID != "#1" {
					t.Fatalf("first click did not arm #1: %q", m.lastClickID)
				}
				domain := queue(t, m, click)
				changed := transition.apply(m)
				got := applyStale(t, changed, domain)
				if got.mode != modeList || got.lastClickID != "" || !got.lastClickAt.IsZero() || detailCalls != 0 {
					t.Errorf("stale second click opened or re-armed: mode=%v pending=%q calls=%d",
						got.mode, got.lastClickID, detailCalls)
				}
			})

			t.Run("wheel cannot move selection", func(t *testing.T) {
				detailCalls := 0
				m := newModel(t, &detailCalls)
				domain := queue(t, m, tea.MouseWheelMsg{X: 1, Y: headerHeight, Button: tea.MouseWheelDown})
				changed := transition.apply(m)
				got := applyStale(t, changed, domain)
				if got.selectedID != changed.selectedID || got.lastClickID != changed.lastClickID || detailCalls != 0 {
					t.Errorf("stale wheel moved selection or pending click: selected=%q pending=%q calls=%d",
						got.selectedID, got.lastClickID, detailCalls)
				}
			})
		})
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
	if m.mode != modeList || cmd != nil || detailCalls != 1 || m.lastClickID != "#1" || !m.lastClickAt.Equal(now) {
		t.Errorf("late second click did not re-arm: mode=%v calls=%d pending=%q at=%s",
			m.mode, detailCalls, m.lastClickID, m.lastClickAt)
	}

	now = now.Add(doubleClickInterval)
	m, cmd, _ = dispatchMouse(t, m, click("#1"))
	if m.mode != modeDetail || cmd == nil || detailCalls != 1 {
		t.Fatalf("click after re-armed timeout did not open: mode=%v cmd nil=%t calls=%d",
			m.mode, cmd == nil, detailCalls)
	}
	_ = cmd()
	if detailCalls != 2 {
		t.Errorf("re-armed Detail calls = %d, want 2 total", detailCalls)
	}

	m.mode = modeList
	m.detail = detailState{}
	now = now.Add(time.Millisecond)
	m, _, _ = dispatchMouse(t, m, click("#2"))
	if m.selectedID != "#2" || m.mode != modeList || m.lastClickID != "#2" {
		t.Errorf("different Ticket click: selected=%q mode=%v pending=%q", m.selectedID, m.mode, m.lastClickID)
	}
}

func TestDoubleClickInterruptionState(t *testing.T) {
	start := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name  string
		event tea.MouseMsg
	}{
		{name: "release", event: tea.MouseReleaseMsg{X: 10, Button: tea.MouseLeft}},
		{name: "motion", event: tea.MouseMotionMsg{X: 10, Button: tea.MouseLeft}},
		{name: "shift-left", event: tea.MouseClickMsg{X: 10, Button: tea.MouseLeft, Mod: tea.ModShift}},
		{name: "horizontal wheel", event: tea.MouseWheelMsg{X: 10, Button: tea.MouseWheelRight}},
	} {
		t.Run(tt.name+" preserves a qualifying pair", func(t *testing.T) {
			now := start
			m := mouseListModel(t, Options{
				Now: func() time.Time { return now },
				DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
					return model.Detail{}, model.Capabilities{}, nil
				},
			}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 20)
			click := tea.MouseClickMsg{X: 10, Y: ticketLineY(t, m, "#1", 0), Button: tea.MouseLeft}
			event := tt.event
			switch msg := event.(type) {
			case tea.MouseReleaseMsg:
				msg.Y = click.Y
				event = msg
			case tea.MouseMotionMsg:
				msg.Y = click.Y
				event = msg
			case tea.MouseClickMsg:
				msg.Y = click.Y
				event = msg
			case tea.MouseWheelMsg:
				msg.Y = click.Y
				event = msg
			}

			m, _, _ = dispatchMouse(t, m, click)
			pendingAt := m.lastClickAt
			got, _, applied := dispatchMouse(t, m, event)
			if applied || got.lastClickID != "#1" || !got.lastClickAt.Equal(pendingAt) {
				t.Fatalf("intermediate event applied=%t pending=%q at=%s, want preserved",
					applied, got.lastClickID, got.lastClickAt)
			}

			now = now.Add(100 * time.Millisecond)
			got, cmd, _ := dispatchMouse(t, got, click)
			if got.mode != modeDetail || cmd == nil {
				t.Errorf("press-event-press sequence did not open: mode=%v cmd nil=%t", got.mode, cmd == nil)
			}
		})
	}

	for _, tt := range []struct {
		name      string
		interrupt func(t *testing.T, m Model, click tea.MouseClickMsg) Model
	}{
		{
			name: "click miss",
			interrupt: func(t *testing.T, m Model, click tea.MouseClickMsg) Model {
				got, _, _ := dispatchMouse(t, m, tea.MouseClickMsg{X: click.X, Y: 0, Button: tea.MouseLeft})
				return got
			},
		},
		{
			name: "non-left click",
			interrupt: func(t *testing.T, m Model, click tea.MouseClickMsg) Model {
				click.Button = tea.MouseRight
				got, _, _ := dispatchMouse(t, m, click)
				return got
			},
		},
		{
			name: "vertical wheel",
			interrupt: func(t *testing.T, m Model, click tea.MouseClickMsg) Model {
				got, _, _ := dispatchMouse(t, m, tea.MouseWheelMsg{X: click.X, Y: click.Y, Button: tea.MouseWheelDown})
				return got
			},
		},
		{
			name: "mouse toggle",
			interrupt: func(t *testing.T, m Model, _ tea.MouseClickMsg) Model {
				next, _ := m.Update(keyPress("m"))
				return next.(Model)
			},
		},
		{
			name: "command key",
			interrupt: func(t *testing.T, m Model, _ tea.MouseClickMsg) Model {
				next, _ := m.Update(keyPress("?"))
				return next.(Model)
			},
		},
		{
			name: "mode transition",
			interrupt: func(t *testing.T, m Model, _ tea.MouseClickMsg) Model {
				next, _ := m.Update(enterKey)
				return next.(Model)
			},
		},
	} {
		t.Run(tt.name+" clears a qualifying pair", func(t *testing.T) {
			m := mouseListModel(t, Options{}, []model.Ticket{ticket("#1", model.StatusTodo)}, 80, 20)
			click := tea.MouseClickMsg{X: 10, Y: ticketLineY(t, m, "#1", 0), Button: tea.MouseLeft}
			m, _, _ = dispatchMouse(t, m, click)
			m = tt.interrupt(t, m, click)
			if m.lastClickID != "" || !m.lastClickAt.IsZero() {
				t.Errorf("pending click survived: id=%q at=%s", m.lastClickID, m.lastClickAt)
			}
		})
	}
}

func TestSearchInputClearsPendingDoubleClick(t *testing.T) {
	start := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name  string
		input tea.Msg
	}{
		{name: "typed text", input: keyPress("a")},
		{name: "pasted text", input: tea.PasteMsg{Content: "a"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := start
			m := mouseListModel(t, Options{
				Now: func() time.Time { return now },
				DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
					return model.Detail{}, model.Capabilities{}, nil
				},
			}, []model.Ticket{{ID: "#1", Key: "#1", Title: "alpha", Status: model.StatusTodo}}, 80, 20)
			next, _ := m.Update(keyPress("/"))
			m = next.(Model)

			click := tea.MouseClickMsg{X: 10, Y: ticketLineY(t, m, "#1", 0), Button: tea.MouseLeft}
			m, _, _ = dispatchMouse(t, m, click)
			next, _ = m.Update(tt.input)
			m = next.(Model)
			if m.lastClickID != "" || !m.lastClickAt.IsZero() {
				t.Fatalf("search input left pending click id=%q at=%s", m.lastClickID, m.lastClickAt)
			}

			now = now.Add(100 * time.Millisecond)
			m, cmd, _ := dispatchMouse(t, m, click)
			if m.mode != modeList || cmd != nil || m.lastClickID != "#1" {
				t.Errorf("click after search input: mode=%v cmd nil=%t pending=%q, want a re-armed first click",
					m.mode, cmd == nil, m.lastClickID)
			}
		})
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
	// "In Review" rather than "In Progress": a Native Status that only
	// restates its Status Category is suppressed, and this test needs a row
	// two lines tall to have a second line to click.
	first.NativeStatus = "In Review"
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

func TestListResizeRejectsStaleMouseCallbackAndFreshFrameActs(t *testing.T) {
	m := mouseListModel(t, Options{}, []model.Ticket{
		ticket("#1", model.StatusInProgress),
		ticket("#2", model.StatusTodo),
		ticket("#3", model.StatusTodo),
	}, 80, 8)
	wheel := tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown}
	handler := m.View().OnMouse
	if handler == nil {
		t.Fatal("list view has no mouse handler")
	}
	cmd := handler(wheel)
	if cmd == nil {
		t.Fatal("pre-resize wheel produced no domain message")
	}
	stale, ok := cmd().(listMouseWheelMsg)
	if !ok {
		t.Fatalf("pre-resize wheel translated to %T", cmd())
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 79, Height: 8})
	resized := updated.(Model)
	if resized.mouseEpoch != m.mouseEpoch+1 {
		t.Fatalf("resize epoch = %d, want %d", resized.mouseEpoch, m.mouseEpoch+1)
	}
	beforeID, beforeOffset, beforeClick := resized.selectedID, resized.offset, resized.lastClickID
	updated, cmd = resized.Update(stale)
	got := updated.(Model)
	if cmd != nil || got.selectedID != beforeID || got.offset != beforeOffset || got.lastClickID != beforeClick {
		t.Errorf("stale resize wheel mutated list: selected=%q offset=%d pending=%q cmd=%v", got.selectedID, got.offset, got.lastClickID, cmd)
	}

	freshHandler := resized.View().OnMouse
	if freshHandler == nil {
		t.Fatal("resized list view has no mouse handler")
	}
	cmd = freshHandler(wheel)
	if cmd == nil {
		t.Fatal("fresh resize wheel produced no domain message")
	}
	fresh, ok := cmd().(listMouseWheelMsg)
	if !ok || fresh.epoch != resized.mouseEpoch {
		t.Fatalf("fresh resize wheel = %#v, want current-epoch list wheel", fresh)
	}
	updated, cmd = resized.Update(fresh)
	got = updated.(Model)
	if cmd != nil || got.selectedID == resized.selectedID {
		t.Errorf("fresh resize wheel did not act: selected=%q want after %q", got.selectedID, resized.selectedID)
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
	clicked, clickCmd, applied := dispatchMouse(t, m, tea.MouseClickMsg{X: 0, Y: linkY, Button: tea.MouseLeft})
	if !applied || clicked.mode != modeDetail || len(clicked.trail) != 1 || clickCmd == nil {
		t.Errorf("Detail Link click applied=%t mode=%v depth=%d cmd nil=%t",
			applied, clicked.mode, len(clicked.trail), clickCmd == nil)
	}
	for _, click := range []tea.MouseMsg{
		tea.MouseClickMsg{X: 0, Y: detailHeaderHeight + 1, Button: tea.MouseRight},
		tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelRight},
	} {
		got, _, applied := dispatchMouse(t, m, click)
		if applied || got.detail.offset != before || got.mode != modeDetail {
			t.Errorf("Detail non-Link event changed state: applied=%t offset=%d", applied, got.detail.offset)
		}
	}

	next, cmd := m.Update(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if cmd != nil || next.(Model).detail.offset != m.detail.offset {
		t.Error("raw mouse message was applied in Update as well as through OnMouse")
	}
}

func TestDetailMouseWheelAcrossDocumentStates(t *testing.T) {
	loaded := detailModel(t)
	loaded.width, loaded.height = 26, 10
	loaded.help.SetWidth(loaded.width)
	loaded.trail = []detailTrailEntry{{ticket: model.Ticket{ID: "TRAIL", Key: "TRAIL"}}}
	loaded = focusLinkAt(loaded, 0)
	loaded.detail.offset = clampDetailOffset(1, len(loaded.detailDocument().Lines), loaded.detailBodyHeight())

	loading := loaded
	loading.detail = detailState{ticket: loaded.detail.ticket, input: loaded.detail.input, loading: true}
	loading = loading.reconcileDetail(false)

	failed := loading
	failed.detail.loading = false
	failed.detail.lastErr = errors.New("permission denied while reading a deliberately long private project diagnostic")
	failed = failed.reconcileDetail(false)
	failed.detail.offset = clampDetailOffset(1, len(failed.detailDocument().Lines), failed.detailBodyHeight())

	cached := loaded
	cached.details[cached.detail.ticket.ID] = detailEntry{
		detail: cached.detail.input.Detail, caps: cached.detail.input.Capabilities, fetchedAt: cached.detail.input.FetchedAt,
	}
	cached.detail.offset = cached.clampDetail(1 << 20)

	resized := loaded
	resized.width = 60
	resized.help.SetWidth(resized.width)
	resized.detail.offset = resized.clampDetail(4)
	resized.width = 26
	resized.help.SetWidth(resized.width)
	resized = resized.reconcileDetail(false)

	states := []struct {
		name  string
		model Model
	}{
		{name: "loaded", model: loaded},
		{name: "loading", model: loading},
		{name: "initial error", model: failed},
		{name: "cached", model: cached},
		{name: "resized", model: resized},
	}
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			before := state.model
			doc := before.detailDocument()
			wantDown := clampDetailOffset(before.detail.offset+3, len(doc.Lines), before.detailBodyHeight())
			down, cmd, applied := dispatchMouse(t, before, tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
			if !applied || cmd != nil || down.detail.offset != wantDown {
				t.Fatalf("wheel down = applied:%t cmd:%v offset:%d, want true/nil/%d",
					applied, cmd, down.detail.offset, wantDown)
			}
			wantUp := clampDetailOffset(wantDown-3, len(doc.Lines), down.detailBodyHeight())
			up, cmd, applied := dispatchMouse(t, down, tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
			if !applied || cmd != nil || up.detail.offset != wantUp {
				t.Errorf("wheel up = applied:%t cmd:%v offset:%d, want true/nil/%d",
					applied, cmd, up.detail.offset, wantUp)
			}

			for _, got := range []Model{down, up} {
				if got.detail.ticket.ID != before.detail.ticket.ID ||
					got.detail.linkFocus != before.detail.linkFocus ||
					got.detail.hasLinkFocus != before.detail.hasLinkFocus ||
					got.detailKeys.Follow.Enabled() != before.detailKeys.Follow.Enabled() ||
					got.detailGeneration != before.detailGeneration ||
					!reflect.DeepEqual(got.trail, before.trail) || !reflect.DeepEqual(got.details, before.details) {
					t.Errorf("wheel mutated focus, follow, Trail, generation, or cache")
				}
			}
		})
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
		// Input handlers are installed with rendered frames. Wait for the short
		// resized footer before sending mouse input, rather than letting queued
		// wheel events be translated by the old viewport's handler.
		s.waitFor(t, "? help …")
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.waitFor(t, "▸ #7")

		// Wrapping the help in the short frame shifts #113 from y=6 to
		// y=3. The callback installed for the resized frame must use that map.
		s.tm.Send(tea.MouseClickMsg{X: 10, Y: 3, Button: tea.MouseLeft})
		s.waitFor(t, "▸ #113")
		s.tm.Send(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		s.waitFor(t, "▸ #7")
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
