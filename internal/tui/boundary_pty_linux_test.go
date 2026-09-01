//go:build linux

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

const (
	boundaryPTYHelperMode      = "SITREP_BOUNDARY_PTY_HELPER"
	frontierLimitPTYHelperMode = "SITREP_FRONTIER_LIMIT_PTY_HELPER"
	// Race instrumentation and the full repository suite can contend for the PTY
	// helper's CPU. This deadline bounds a deadlock without timing ordinary work.
	frontierLimitPTYTimeout = 30 * time.Second
)

// The markers each screen must still show. A hostile matrix that passes on a
// blank screen proves nothing, so every wait below is also an assertion that
// the screen drew. No two of them share a character in any position on
// purpose: Bubble Tea repaints only the cells that changed, so a marker that
// overlapped its predecessor would never appear whole in the byte stream.
const (
	boundaryPTYDetailMarker = "ROOTDETAIL"
	boundaryPTYChildMarker  = "LINKTARGET"
	boundaryPTYDrillMarker  = "DRILLDETAIL"
	boundaryPTYListMarker   = "WATCHLISTSCREEN"
	// The Frontier's own header word, which no other screen draws. It is drawn
	// at seat time, before any card, so it says the screen opened and nothing
	// more.
	boundaryPTYFrontierMarker = "frontier"
	// The dashed top border of a Ghost Ticket's card, which only the canvas's
	// card renderer draws. Waiting on it is what makes this leg an assertion
	// about that renderer: quitting on the header word alone, which is drawn at
	// seat time, leaves whether a card ever drew a race, and the leg passes with
	// node drawing entirely broken. The card's own fields are the hostile Link
	// target's, so the assertions above cover the bytes it wrote.
	boundaryPTYFrontierCardMarker = "╭╌╌╌"
	frontierLimitSafeCardMarker   = "┌──────────────────────────┐"
)

// boundaryPTYBodyPayload is carried only by body fields, where the Body policy
// removes a complete sequence together with its payload. Line deliberately
// keeps a sequence's printable payload, so a single payload shared by both
// policies could not be asserted absent.
const boundaryPTYBodyPayload = "cHR5LWJvZHktcGF5bG9hZA=="

// Every hostile field carried by the four PTY-fed funnels, through a real
// terminal. The frame-level tests assert what the Model drew; this asserts what
// the terminal actually received across both screens and a Trail step, including
// teardown. DetailFanout's fifth intake boundary is exercised separately through
// its plural Frontier result path.
func TestBoundaryHostileFieldsThroughRawPTY(t *testing.T) {
	raw := runBoundaryPTYSession(t)
	if !utf8.Valid(raw) {
		t.Fatalf("PTY output contains malformed UTF-8: % x", raw)
	}
	output := string(raw)

	for _, forbidden := range []string{
		"\x1b]52;", boundaryPTYBodyPayload,
		hyperlinkOpen("https://evil.example.test/body"),
		hyperlinkOpen("https://evil.example.test/"),
	} {
		if strings.Contains(output, forbidden) {
			t.Errorf("PTY output contains the hostile sequence % x", []byte(forbidden))
		}
	}
	// A raw single-byte C1 cannot be here at all: it would have made the stream
	// invalid UTF-8 above. Its valid spelling is what is left to rule out.
	for _, control := range []rune{0x9b, 0x9c, 0x9d} {
		if strings.ContainsRune(output, control) {
			t.Errorf("PTY output contains the C1 control U+%04X", control)
		}
	}

	visible := ansi.Strip(output)
	for _, want := range []string{
		boundaryPTYDetailMarker, boundaryPTYChildMarker, boundaryPTYDrillMarker,
		boundaryPTYListMarker, boundaryPTYFrontierMarker, boundaryPTYFrontierCardMarker,
		"monitor held", "MANUAL-REFRESH", "p resume refresh",
		"café", "東京",
	} {
		if !strings.Contains(visible, want) {
			t.Errorf("PTY output lost %q:\n%s", want, visible)
		}
	}

	// Every fragment sitrep writes is balanced on its own, and a concatenation
	// of balanced sequences is balanced, so a single unmatched opener anywhere
	// in the session fails here. This is the truncation-proof form of "an
	// unterminated override cannot affect a subsequent line": cursor motion and
	// repaints add no bidi code points, so a partial repaint cannot fake it.
	if unterminated, stray := termtexttest.Unbalanced(visible); unterminated != 0 || stray != 0 {
		t.Errorf("the PTY stream is not bidi-balanced (unterminated %U, stray %U):\n%+q",
			unterminated, stray, visible)
	}

	assertPTYHyperlinksClosed(t, output)
	assertTerminalModePair(t, output, ansi.SetModeAltScreenSaveCursor, ansi.ResetModeAltScreenSaveCursor)
	assertTerminalModePair(t, output, ansi.SetModeMouseButtonEvent, ansi.ResetModeMouseButtonEvent)
	assertTerminalModePair(t, output, ansi.SetModeMouseExtSgr, ansi.ResetModeMouseExtSgr)
	assertTerminalModePair(t, output, ansi.SetModeFocusEvent, ansi.ResetModeFocusEvent)
	assertTerminalModePair(t, output, ansi.HideCursor, ansi.ShowCursor)
}

func TestFrontierCanvasLimitThroughRawPTY(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{name: "ordinary", width: 120, height: 40},
		{name: "narrow", width: 60, height: 20},
		{name: "vertical-fit", width: 300, height: 45},
	} {
		t.Run(size.name, func(t *testing.T) {
			hostileRaw := runFrontierLimitPTYSession(t, size.width, size.height, "hostile")
			hostileOutput := string(hostileRaw)
			hostileVisible := ansi.Strip(hostileOutput)
			hostileNormalized := strings.Join(strings.Fields(hostileVisible), " ")
			for _, want := range []string{
				"canvas refused", "permitted 500,000 cells", "raw frontierCell grid payload",
				"total process memory", "HOSTILE-RETURN", "frontier",
			} {
				if !strings.Contains(hostileNormalized, want) {
					t.Errorf("%s hostile PTY output lost %q:\n%s", size.name, want, hostileVisible)
				}
			}
			if strings.Count(hostileVisible, "TODO (50)") < 2 {
				t.Errorf("%s hostile PTY did not visibly return to the Watchlist", size.name)
			}
			assertFrontierPTYTerminalModes(t, hostileOutput)

			safeRaw := runFrontierLimitPTYSession(t, size.width, size.height, "safe")
			safeOutput := string(safeRaw)
			safeVisible := ansi.Strip(safeOutput)
			safeNormalized := strings.Join(strings.Fields(safeVisible), " ")
			for _, want := range []string{
				"SAFE-CARD", "SAFE-GHOST", frontierLimitSafeCardMarker,
				"SAFE-RETURN", "frontier",
			} {
				if !strings.Contains(safeNormalized, want) {
					t.Errorf("%s safe PTY output lost %q:\n%s", size.name, want, safeVisible)
				}
			}
			if strings.Contains(safeVisible, "canvas refused") {
				t.Errorf("%s safe PTY unexpectedly refused its canvas:\n%s", size.name, safeVisible)
			}
			if strings.Count(safeVisible, "TODO (2)") < 2 {
				t.Errorf("%s safe PTY did not visibly return to the Watchlist", size.name)
			}
			assertFrontierPTYTerminalModes(t, safeOutput)
		})
	}
}

func TestDenseFrontierThroughRawPTY(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{name: "ordinary", width: 120, height: 40},
		{name: "narrow", width: 60, height: 20},
		{name: "tiny", width: 42, height: 28},
	} {
		t.Run(size.name, func(t *testing.T) {
			raw := runFrontierLimitPTYSession(t, size.width, size.height, "dense")
			output := string(raw)
			visible := ansi.Strip(output)
			normalized := strings.Join(strings.Fields(visible), " ")
			for _, want := range []string{
				"━ S-1", "┈ SAFE-GHOST", frontierLimitSafeCardMarker,
				"SAFE-RETURN", "frontier",
			} {
				if !strings.Contains(normalized, want) {
					t.Errorf("%s dense PTY output lost %q:\n%s", size.name, want, visible)
				}
			}
			if strings.Contains(visible, "canvas refused") {
				t.Errorf("%s dense PTY unexpectedly refused its canvas:\n%s", size.name, visible)
			}
			assertFrontierPTYTerminalModes(t, output)
		})
	}
}

func assertFrontierPTYTerminalModes(t *testing.T, output string) {
	t.Helper()
	assertTerminalModePair(t, output, ansi.SetModeAltScreenSaveCursor, ansi.ResetModeAltScreenSaveCursor)
	assertTerminalModePair(t, output, ansi.SetModeMouseButtonEvent, ansi.ResetModeMouseButtonEvent)
	assertTerminalModePair(t, output, ansi.SetModeMouseExtSgr, ansi.ResetModeMouseExtSgr)
	assertTerminalModePair(t, output, ansi.SetModeFocusEvent, ansi.ResetModeFocusEvent)
}

func TestFrontierCanvasLimitPTYHelper(t *testing.T) {
	fixture := os.Getenv(frontierLimitPTYHelperMode)
	if fixture == "" {
		return
	}
	if err := Run(context.Background(), frontierLimitPTYOptions(fixture)); err != nil {
		t.Fatalf("tui.Run: %v", err)
	}
}

func frontierLimitPTYOptions(fixture string) Options {
	now := time.Unix(1_700_000_000, 0)
	capabilities := model.Capabilities{BlockingLinks: true}

	var initial ListInput
	var details map[model.TicketID]model.Detail
	switch fixture {
	case "hostile":
		tickets, links := benchmarkFrontierFixture("hostile", 50)
		tickets[0].Title = "LIMIT-ROW"
		initial = ListInput{
			Header:       Header{Key: "#105", Title: "LIMIT-PTY"},
			Tickets:      tickets,
			Capabilities: capabilities,
			FetchedAt:    now,
		}
		details = make(map[model.TicketID]model.Detail, len(links))
		for id, ticketLinks := range links {
			detail := model.Detail{TicketID: id, Links: ticketLinks}
			if id == tickets[0].ID {
				detail.Description = "HOSTILE-RETURN"
			}
			details[id] = detail
		}
	case "safe", "dense":
		tickets := []model.Ticket{
			{ID: "S-0", Key: "S-0", Title: "safe root", Status: model.StatusDone},
			{ID: "S-1", Key: "S-1", Title: "safe dependent", Status: model.StatusTodo},
			{ID: "S-2", Key: "S-2", Title: "SAFE-CARD", Status: model.StatusTodo},
		}
		links := map[model.TicketID][]model.Link{
			"S-0": nil,
			"S-1": blockedBy("S-0"),
			"S-2": blockedBy("S-1", "SAFE-GHOST"),
		}
		initial = ListInput{
			Header:       Header{Key: "#105", Title: "SAFE-PTY"},
			Tickets:      tickets,
			Capabilities: capabilities,
			FetchedAt:    now,
		}
		details = make(map[model.TicketID]model.Detail, len(links))
		for id, ticketLinks := range links {
			detail := model.Detail{TicketID: id, Links: ticketLinks}
			if id == tickets[1].ID {
				detail.Description = "SAFE-RETURN"
			}
			details[id] = detail
		}
	default:
		panic("unknown Frontier PTY fixture: " + fixture)
	}

	return Options{
		Initial: &initial,
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			return details[id], capabilities, nil
		},
		Interval: time.Hour,
		Now:      func() time.Time { return now },
		Input:    os.Stdin,
		Output:   os.Stdout,
	}
}

func writeFrontierPTYInput(t *testing.T, writer io.Writer, inputs ...string) {
	t.Helper()
	for _, input := range inputs {
		if _, err := io.WriteString(writer, input); err != nil {
			t.Fatalf("write %q to Frontier PTY: %v", input, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type frontierPTYScreen struct {
	width, height         int
	row, column           int
	savedRow, savedColumn int
	cells                 [][]rune
	parser                *ansi.Parser
}

func newFrontierPTYScreen(width, height int) *frontierPTYScreen {
	screen := &frontierPTYScreen{width: width, height: height}
	screen.cells = make([][]rune, height)
	for row := range screen.cells {
		screen.cells[row] = make([]rune, width)
		for column := range screen.cells[row] {
			screen.cells[row][column] = ' '
		}
	}
	screen.parser = ansi.NewParser()
	screen.parser.SetHandler(ansi.Handler{
		Print:     screen.print,
		Execute:   screen.execute,
		HandleCsi: screen.handleCSI,
		HandleEsc: screen.handleESC,
	})
	return screen
}

func (s *frontierPTYScreen) write(data []byte) {
	for _, b := range data {
		s.parser.Advance(b)
	}
}

func (s *frontierPTYScreen) print(r rune) {
	width := ansi.StringWidth(string(r))
	if width <= 0 || s.row < 0 || s.row >= s.height || s.column < 0 || s.column >= s.width {
		return
	}
	s.cells[s.row][s.column] = r
	for offset := 1; offset < width && s.column+offset < s.width; offset++ {
		s.cells[s.row][s.column+offset] = 0
	}
	s.column = min(s.column+width, s.width)
}

func (s *frontierPTYScreen) execute(control byte) {
	switch control {
	case '\b':
		s.column = max(s.column-1, 0)
	case '\t':
		s.column = min((s.column/8+1)*8, s.width)
	case '\n', '\v', '\f':
		s.index()
	case '\r':
		s.column = 0
	}
}

func (s *frontierPTYScreen) handleCSI(command ansi.Cmd, params ansi.Params) {
	count := frontierPTYParam(params, 0, 1)
	switch command.Final() {
	case 'A':
		s.row = max(s.row-count, 0)
	case 'B':
		s.row = min(s.row+count, s.height-1)
	case 'C':
		s.column = min(s.column+count, s.width)
	case 'D':
		s.column = max(s.column-count, 0)
	case 'E':
		s.row = min(s.row+count, s.height-1)
		s.column = 0
	case 'F':
		s.row = max(s.row-count, 0)
		s.column = 0
	case 'G':
		s.column = min(max(count-1, 0), s.width)
	case 'H', 'f':
		s.row = min(max(frontierPTYParam(params, 0, 1)-1, 0), s.height-1)
		s.column = min(max(frontierPTYParam(params, 1, 1)-1, 0), s.width)
	case 'J':
		s.eraseDisplay(frontierPTYParam(params, 0, 0))
	case 'K':
		s.eraseLine(frontierPTYParam(params, 0, 0))
	case 'P':
		s.deleteCharacters(count)
	case 'S':
		for range count {
			s.scrollUp()
		}
	case 'T':
		for range count {
			s.scrollDown()
		}
	case 'X':
		end := min(s.column+count, s.width)
		s.clearCells(s.row, s.column, end)
	case 'd':
		s.row = min(max(count-1, 0), s.height-1)
	case 's':
		s.savedRow, s.savedColumn = s.row, s.column
	case 'u':
		s.row, s.column = s.savedRow, s.savedColumn
	}
}

func (s *frontierPTYScreen) handleESC(command ansi.Cmd) {
	switch command.Final() {
	case '7':
		s.savedRow, s.savedColumn = s.row, s.column
	case '8':
		s.row, s.column = s.savedRow, s.savedColumn
	case 'D':
		s.index()
	case 'E':
		s.index()
		s.column = 0
	case 'M':
		if s.row == 0 {
			s.scrollDown()
		} else {
			s.row--
		}
	case 'c':
		s.row, s.column = 0, 0
		s.eraseDisplay(2)
	}
}

func frontierPTYParam(params ansi.Params, index, fallback int) int {
	value, _, ok := params.Param(index, fallback)
	if !ok || value == 0 && fallback == 1 {
		return fallback
	}
	return value
}

func (s *frontierPTYScreen) index() {
	if s.row == s.height-1 {
		s.scrollUp()
		return
	}
	s.row++
}

func (s *frontierPTYScreen) scrollUp() {
	copy(s.cells, s.cells[1:])
	s.cells[s.height-1] = make([]rune, s.width)
	for column := range s.cells[s.height-1] {
		s.cells[s.height-1][column] = ' '
	}
}

func (s *frontierPTYScreen) scrollDown() {
	copy(s.cells[1:], s.cells[:s.height-1])
	s.cells[0] = make([]rune, s.width)
	for column := range s.cells[0] {
		s.cells[0][column] = ' '
	}
}

func (s *frontierPTYScreen) eraseDisplay(mode int) {
	switch mode {
	case 0:
		s.clearCells(s.row, s.column, s.width)
		for row := s.row + 1; row < s.height; row++ {
			s.clearCells(row, 0, s.width)
		}
	case 1:
		for row := 0; row < s.row; row++ {
			s.clearCells(row, 0, s.width)
		}
		s.clearCells(s.row, 0, min(s.column+1, s.width))
	case 2, 3:
		for row := 0; row < s.height; row++ {
			s.clearCells(row, 0, s.width)
		}
	}
}

func (s *frontierPTYScreen) eraseLine(mode int) {
	switch mode {
	case 0:
		s.clearCells(s.row, s.column, s.width)
	case 1:
		s.clearCells(s.row, 0, min(s.column+1, s.width))
	case 2:
		s.clearCells(s.row, 0, s.width)
	}
}

func (s *frontierPTYScreen) deleteCharacters(count int) {
	if s.column >= s.width {
		return
	}
	count = min(count, s.width-s.column)
	copy(s.cells[s.row][s.column:], s.cells[s.row][s.column+count:])
	s.clearCells(s.row, s.width-count, s.width)
}

func (s *frontierPTYScreen) clearCells(row, start, end int) {
	for column := max(start, 0); column < min(end, s.width); column++ {
		s.cells[row][column] = ' '
	}
}

func (s *frontierPTYScreen) normalized() string {
	var text strings.Builder
	for _, row := range s.cells {
		for _, cell := range row {
			if cell != 0 {
				text.WriteRune(cell)
			}
		}
		text.WriteByte('\n')
	}
	return strings.Join(strings.Fields(text.String()), " ")
}

func waitForFrontierPTYMarker(t *testing.T, chunks <-chan []byte, output []byte, marker string) []byte {
	t.Helper()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	for !strings.Contains(string(output), marker) {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("PTY closed before marker %q\n%s", marker, output)
			}
			output = append(output, chunk...)
		case <-deadline.C:
			t.Fatalf("PTY did not render marker %q\n%s", marker, output)
		}
	}
	return output
}

func waitForPTYScreenMarker(t *testing.T, chunks <-chan []byte, output []byte,
	width, height int, marker string) []byte {
	t.Helper()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	screen := newFrontierPTYScreen(width, height)
	screen.write(output)
	for !strings.Contains(screen.normalized(), marker) {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("PTY closed before screen marker %q appeared\n%s", marker, output)
			}
			output = append(output, chunk...)
			screen.write(chunk)
		case <-deadline.C:
			t.Fatalf("PTY did not render screen marker %q\nscreen: %s\n%s", marker, screen.normalized(), output)
		}
	}
	return output
}

func waitForPTYRawMarkerCount(t *testing.T, chunks <-chan []byte, output []byte, marker string, count int) []byte {
	t.Helper()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	for strings.Count(string(output), marker) < count {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("PTY closed before raw marker %q appeared %d times\n%s", marker, count, output)
			}
			output = append(output, chunk...)
		case <-deadline.C:
			t.Fatalf("PTY did not render raw marker %q %d times\n%s", marker, count, output)
		}
	}
	return output
}

func waitForPTYMarkerCount(t *testing.T, chunks <-chan []byte, output []byte, marker string, count int) []byte {
	t.Helper()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	for strings.Count(ansi.Strip(string(output)), marker) < count {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("PTY closed before marker %q appeared %d times\n%s", marker, count, output)
			}
			output = append(output, chunk...)
		case <-deadline.C:
			t.Fatalf("PTY did not render marker %q %d times\n%s", marker, count, output)
		}
	}
	return output
}

func runFrontierLimitPTYSession(t *testing.T, width, height int, fixture string) []byte {
	t.Helper()
	master, slave := openMarkdownPTY(t, uint16(width), uint16(height))
	command := exec.Command(os.Args[0], "-test.run=^TestFrontierCanvasLimitPTYHelper$", "-test.v=false")
	command.Env = environmentWithout(os.Environ(), "TERM")
	command.Env = environmentWithout(command.Env, "COLORTERM")
	command.Env = append(command.Env, frontierLimitPTYHelperMode+"="+fixture, "TERM=xterm-256color", "GLAMOUR_STYLE=dark")
	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatalf("start PTY helper: %v", err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	_ = slave.Close()

	chunks := make(chan []byte, 256)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				chunks <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()

	output := make([]byte, 0, 32*1024)
	switch fixture {
	case "hostile":
		output = waitForFrontierPTYMarker(t, chunks, output, "LIMIT-ROW")
		writeFrontierPTYInput(t, master, "v")
		output = waitForFrontierPTYMarker(t, chunks, output, "canvas refused")

		// These are all graph-only actions, including terminal key sequences and
		// translated mouse messages. If any opens Detail or leaves the refused
		// Frontier, the following expanded-Help marker cannot appear.
		writeFrontierPTYInput(t, master,
			"r", "j", "k", "h", "l", "\x1b[5~", "\x1b[6~", "\x1b[H", "\x1b[F", "g", "G", "\r",
			"\x1b[<0;10;10M", "\x1b[<65;10;10M", "?")
		output = waitForPTYScreenMarker(t, chunks, output, width, height,
			"m release · shift-drag to select text v/esc list V dense/full cards ? help L legend q quit")
		writeFrontierPTYInput(t, master, "?")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "v/esc list •")
		writeFrontierPTYInput(t, master, "m")
		output = waitForPTYRawMarkerCount(t, chunks, output, ansi.ResetModeMouseButtonEvent, 1)
		writeFrontierPTYInput(t, master, "m")
		output = waitForPTYRawMarkerCount(t, chunks, output, ansi.SetModeMouseButtonEvent, 2)
		writeFrontierPTYInput(t, master, "v")
		output = waitForPTYMarkerCount(t, chunks, output, "TODO (50)", 2)
		writeFrontierPTYInput(t, master, "\r")
		output = waitForFrontierPTYMarker(t, chunks, output, "HOSTILE-RETURN")
	case "safe":
		output = waitForFrontierPTYMarker(t, chunks, output, "SAFE-CARD")
		writeFrontierPTYInput(t, master, "v")
		output = waitForFrontierPTYMarker(t, chunks, output, frontierLimitSafeCardMarker)
		output = waitForFrontierPTYMarker(t, chunks, output, "SAFE-GHOST")
		writeFrontierPTYInput(t, master, "?")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "double-click open Ticket")
		writeFrontierPTYInput(t, master, "?")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "v/esc list •")
		writeFrontierPTYInput(t, master, "m")
		output = waitForPTYRawMarkerCount(t, chunks, output, ansi.ResetModeMouseButtonEvent, 1)
		writeFrontierPTYInput(t, master, "m")
		output = waitForPTYRawMarkerCount(t, chunks, output, ansi.SetModeMouseButtonEvent, 2)
		writeFrontierPTYInput(t, master, "v")
		output = waitForPTYMarkerCount(t, chunks, output, "TODO (2)", 2)
		writeFrontierPTYInput(t, master, "\r")
		output = waitForFrontierPTYMarker(t, chunks, output, "SAFE-RETURN")
	case "dense":
		output = waitForFrontierPTYMarker(t, chunks, output, "SAFE-CARD")
		writeFrontierPTYInput(t, master, "?")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "v compute actionability")
		writeFrontierPTYInput(t, master, "?")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "•")
		writeFrontierPTYInput(t, master, "V")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "━ S-1")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "┈ SAFE-GHOST")
		writeFrontierPTYInput(t, master, "V")
		output = waitForFrontierPTYMarker(t, chunks, output, frontierLimitSafeCardMarker)
		writeFrontierPTYInput(t, master, "V")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "━ S-1")
		writeFrontierPTYInput(t, master, "v")
		output = waitForPTYScreenMarker(t, chunks, output, width, height, "SAFE-CARD")
		writeFrontierPTYInput(t, master, "\r")
		output = waitForFrontierPTYMarker(t, chunks, output, "SAFE-RETURN")
	default:
		t.Fatalf("unknown Frontier PTY fixture %q", fixture)
	}
	writeFrontierPTYInput(t, master, "q")

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	readerOpen := true
	processDone := false
	for readerOpen || !processDone {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				readerOpen = false
				chunks = nil
				continue
			}
			output = append(output, chunk...)
		case err := <-waited:
			processDone = true
			waited = nil
			if err != nil {
				t.Fatalf("PTY helper exited unsuccessfully: %v\n%s", err, output)
			}
		case <-deadline.C:
			_ = command.Process.Kill()
			t.Fatalf("PTY helper did not cleanly exit\n%s", output)
		}
	}
	_ = master.Close()
	return output
}

func TestBoundaryPTYHelper(t *testing.T) {
	if os.Getenv(boundaryPTYHelperMode) == "" {
		return
	}
	if err := Run(context.Background(), boundaryPTYOptions()); err != nil {
		t.Fatalf("tui.Run: %v", err)
	}
}

// hostilePTYLine is a single-line field: a marker so the screen can be waited
// on, and every terminal control the fixture knows. termtexttest.Hostile also
// carries the bidi payload — an unterminated U+202E, an unterminated U+2067
// and a stray U+2069 — so the balance assertion above needs no field of its
// own.
func hostilePTYLine(marker string) string {
	return marker + " " + termtexttest.Hostile
}

// hostilePTYBody is a body field, carrying a complete OSC 8 and OSC 52 whose
// payloads the Body policy removes outright.
func hostilePTYBody(marker string) string {
	return "## " + marker + "\n\ncafé 東京 " + string([]byte{0xff, 0xc0, 0xaf}) +
		"\x1b]8;;https://evil.example.test/body\aVISIBLE\x1b]8;;\x1b\\" +
		"\x1b]52;c;" + boundaryPTYBodyPayload + "\a\x1b[2J\r\n" +
		string([]byte{0x9d}) + "52;c;" + boundaryPTYBodyPayload + string([]byte{0x9c})
}

func hostilePTYTicket(id model.TicketID, marker string) model.Ticket {
	ticket := hostileTicket(id)
	ticket.Title = hostilePTYLine(marker)
	ticket.PullRequests = []model.PullRequest{{
		Number: 8, Title: hostilePTYLine("PR"), URL: hostilePTYLine("https://tracker.example/pr/8"),
		Repository: hostilePTYLine("acme/widgets"), State: model.PROpen,
	}}
	return ticket
}

// boundaryPTYOptions fills the four PTY-fed funnels: a seated reading, a decoded
// Ticket, a Source, and a DetailSource, none of them a Provider. The fifth
// DetailFanout funnel is not synthesized here; Frontier reaches its fallback
// through the already-sanitized DetailSource.
func boundaryPTYOptions() Options {
	now := time.Unix(1_700_000_000, 0)
	capabilities := model.Capabilities{Comments: true, BlockingLinks: true, PullRequests: true}
	freshList := func(titleMarker string) ListInput {
		return ListInput{
			Header: Header{
				Key:   hostilePTYLine("PTY"),
				Title: hostilePTYLine(titleMarker),
				URL:   hostilePTYLine("https://tracker.example/watchlist"),
			},
			Tickets:      []model.Ticket{hostilePTYTicket("T-3", "PTY-ROW")},
			Capabilities: capabilities,
			FetchedAt:    now,
		}
	}
	list := freshList(boundaryPTYListMarker)
	// One Detail per seat, so every wait in the script below is for something
	// the screen has not drawn yet rather than for a marker already in the
	// buffer: the decoded root, the Link target, and the list drill-in.
	detailFor := func(id model.TicketID) model.Detail {
		marker := boundaryPTYChildMarker
		switch id {
		case "T-1":
			marker = boundaryPTYDetailMarker
		case "T-3":
			marker = boundaryPTYDrillMarker
		}
		return model.Detail{
			TicketID:    id,
			Description: hostilePTYBody(marker),
			Comments: []model.Comment{{
				ID:     hostilePTYLine("c1"),
				Author: model.User{Login: hostilePTYLine("alice"), DisplayName: hostilePTYLine("Alice")},
				Body:   hostilePTYBody("PTY-COMMENT-READY"),
				URL:    hostilePTYLine("https://tracker.example/c/1"),
			}},
			Links: []model.Link{{
				Kind:        model.LinkBlockedBy,
				NativeLabel: hostilePTYLine("is blocked by"),
				Target: model.LinkTarget{
					ID: "T-2", Key: hostilePTYLine("#2"), Title: hostilePTYLine("PTY-TARGET"),
					URL: hostilePTYLine("https://tracker.example/T-2"), Status: model.StatusTodo,
					NativeStatus: hostilePTYLine("in review"),
				},
			}},
		}
	}

	return Options{
		Open: &OpenTicket{
			Ticket:       hostilePTYTicket("T-1", "PTY-ROOT"),
			Parent:       list.Header,
			Capabilities: list.Capabilities,
		},
		Initial: &list,
		Source: func(context.Context) (ListInput, error) {
			return freshList("MANUAL-REFRESH"), nil
		},
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			return detailFor(id), list.Capabilities, nil
		},
		Interval: time.Hour,
		Now:      func() time.Time { return now },
		Input:    os.Stdin,
		Output:   os.Stdout,
	}
}

// runBoundaryPTYSession drives the helper through both screens: the decoded
// Ticket's Detail, a Trail step into a Link target, the walk-up into the list,
// and a drill-in back into Detail.
func runBoundaryPTYSession(t *testing.T) []byte {
	t.Helper()
	master, slave := openMarkdownPTY(t, 240, 40)
	command := exec.Command(os.Args[0], "-test.run=^TestBoundaryPTYHelper$", "-test.v=false")
	command.Env = environmentWithout(os.Environ(), "TERM")
	command.Env = environmentWithout(command.Env, "COLORTERM")
	command.Env = append(command.Env, boundaryPTYHelperMode+"=1", "TERM=xterm-256color", "GLAMOUR_STYLE=dark")
	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatalf("start PTY helper: %v", err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	_ = slave.Close()

	chunks := make(chan []byte, 256)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				chunks <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()

	output := make([]byte, 0, 16*1024)
	send := func(input string) {
		t.Helper()
		if _, err := io.WriteString(master, input); err != nil {
			t.Fatalf("write %q to the PTY: %v", input, err)
		}
	}

	// Follow the decoded root's Link, walk up to the List, hold refresh, and
	// cross Detail and Frontier boundaries. The Frontier card proves explicit
	// fan-out still runs while held. List r then lands the marked fake Source
	// reading without releasing hold; the final p exposes the resumed Help action.
	output = waitForPTYMarker(t, chunks, output, boundaryPTYDetailMarker)
	send("\x1b[O\x1b[I\t\r")
	output = waitForPTYMarker(t, chunks, output, boundaryPTYChildMarker)
	send("u")
	output = waitForPTYMarker(t, chunks, output, boundaryPTYListMarker)
	send("p")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "monitor held")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "p resume refresh")
	send("\r")
	output = waitForPTYMarker(t, chunks, output, boundaryPTYDrillMarker)
	send("u")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "monitor held")
	send("v")
	output = waitForPTYMarker(t, chunks, output, boundaryPTYFrontierMarker)
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, boundaryPTYFrontierCardMarker)
	send("v")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "TODO (1)")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "monitor held")
	send("r")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "MANUAL-REFRESH")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "p resume refresh")
	screen := newFrontierPTYScreen(240, 40)
	screen.write(output)
	if visible := screen.normalized(); !strings.Contains(visible, "monitor held") || !strings.Contains(visible, "p resume refresh") {
		t.Fatalf("manual List r released the PTY hold:\n%s", visible)
	}
	send("p")
	output = waitForPTYScreenMarker(t, chunks, output, 240, 40, "p hold refresh")
	send("q")

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	readerOpen := true
	processDone := false
	for readerOpen || !processDone {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				readerOpen = false
				chunks = nil
				continue
			}
			output = append(output, chunk...)
		case err := <-waited:
			processDone = true
			waited = nil
			if err != nil {
				t.Fatalf("PTY helper exited unsuccessfully: %v\n%s", err, output)
			}
		case <-deadline.C:
			_ = command.Process.Kill()
			t.Fatalf("PTY helper did not cleanly exit\n%s", output)
		}
	}
	_ = master.Close()
	return output
}

const (
	ratePTYHelperMode     = "SITREP_RATE_ADMISSION_PTY_HELPER"
	ratePTYLogPathEnv     = "SITREP_RATE_ADMISSION_PTY_LOG"
	ratePTYReleasePathEnv = "SITREP_RATE_ADMISSION_PTY_RELEASE"
)

// TestRateAdmissionPTYHelper is deliberately a separate process. Its fake
// Tracker callbacks run behind Bubble Tea's real command scheduler, while the
// parent observes only the terminal conversation and a local, deterministic
// call ledger.
func TestRateAdmissionPTYHelper(t *testing.T) {
	fixture := os.Getenv(ratePTYHelperMode)
	if fixture == "" {
		return
	}
	if err := Run(context.Background(), ratePTYOptions(fixture)); err != nil {
		t.Fatalf("tui.Run: %v", err)
	}
}

func ratePTYEvent(event string) {
	log, err := os.OpenFile(os.Getenv(ratePTYLogPathEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	if _, err := io.WriteString(log, event+"\n"); err != nil {
		panic(err)
	}
}

func ratePTYOptions(fixture string) Options {
	now := time.Unix(1_700_000_000, 0)
	capabilities := model.Capabilities{BlockingLinks: true}
	input := ListInput{
		Header: Header{Key: "#rate", Title: "RATE-PTY-" + fixture},
		Tickets: []model.Ticket{
			{ID: "P-1", Key: "P-1", Title: "PARTIAL-ONE", Status: model.StatusTodo},
			{ID: "P-2", Key: "P-2", Title: "PARTIAL-TWO", Status: model.StatusTodo},
			{ID: "P-3", Key: "P-3", Title: "PARTIAL-THREE", Status: model.StatusTodo},
		},
		Capabilities: capabilities,
		FetchedAt:    now,
	}

	cloneInput := func(title string) ListInput {
		result := input
		result.Header.Title = title
		result.Tickets = append([]model.Ticket(nil), input.Tickets...)
		return result
	}
	sourceCalls := 0
	source := func(context.Context) (ListInput, error) {
		sourceCalls++
		ratePTYEvent("source:" + string(rune('0'+sourceCalls)))
		switch fixture {
		case "known":
			return ListInput{}, provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: now.Add(time.Hour)}, "KNOWN-TRACKER-REFUSAL")
		case "unknown":
			if sourceCalls == 1 {
				return ListInput{}, provider.Errorf(provider.KindRateLimit, "UNKNOWN-TRACKER-REFUSAL")
			}
			ratePTYEvent("unknown-probe:started")
			release := os.Getenv(ratePTYReleasePathEnv)
			for {
				if _, err := os.Stat(release); err == nil {
					break
				} else if !errors.Is(err, os.ErrNotExist) {
					panic(err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			result := cloneInput("UNKNOWN-PROBE-SUCCESS")
			ratePTYEvent("unknown-probe:settled")
			return result, nil
		case "composition":
			return cloneInput("MANUAL-SOURCE"), nil
		default:
			return cloneInput(input.Header.Title), nil
		}
	}

	return Options{
		Initial: &input,
		Source:  source,
		DetailSource: func(_ context.Context, id model.TicketID) (model.Detail, model.Capabilities, error) {
			ratePTYEvent("detail:" + string(id))
			return model.Detail{TicketID: id, Description: "DETAIL-ALLOWED"}, capabilities, nil
		},
		DetailFanout: func(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, model.Capabilities, error) {
			joined := strings.Join(ticketIDStrings(ids), ",")
			ratePTYEvent("fanout:" + joined)
			switch fixture {
			case "partial":
				details := map[model.TicketID]model.Detail{
					"P-1": {TicketID: "P-1"},
					"P-3": {TicketID: "P-3"},
				}
				failures := &provider.DetailFailures{Failures: map[model.TicketID]error{
					"P-2": errors.New("PARTIAL-MEMBER-FAILURE"),
				}}
				return details, capabilities, failures
			case "cancel":
				<-ctx.Done()
				ratePTYEvent("fanout:cancelled")
				return nil, capabilities, ctx.Err()
			default:
				details := make(map[model.TicketID]model.Detail, len(ids))
				for _, id := range ids {
					details[id] = model.Detail{TicketID: id}
				}
				return details, capabilities, nil
			}
		},
		Interval: time.Hour,
		Now:      func() time.Time { return now },
		Input:    os.Stdin,
		Output:   os.Stdout,
	}
}

func ticketIDStrings(ids []model.TicketID) []string {
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = string(id)
	}
	return values
}

type ratePTYSession struct {
	t       *testing.T
	master  *os.File
	command *exec.Cmd
	chunks  <-chan []byte
	output  []byte
	log     string
	release string
}

func startRatePTYSession(t *testing.T, fixture string, width, height int) *ratePTYSession {
	t.Helper()
	master, slave := openMarkdownPTY(t, uint16(width), uint16(height))
	dir := t.TempDir()
	session := &ratePTYSession{t: t, master: master, log: dir + "/calls", release: dir + "/release"}
	command := exec.Command(os.Args[0], "-test.run=^TestRateAdmissionPTYHelper$", "-test.v=false")
	command.Env = environmentWithout(os.Environ(), "TERM")
	command.Env = environmentWithout(command.Env, "COLORTERM")
	command.Env = append(command.Env,
		ratePTYHelperMode+"="+fixture,
		ratePTYLogPathEnv+"="+session.log,
		ratePTYReleasePathEnv+"="+session.release,
		"TERM=xterm-256color", "GLAMOUR_STYLE=dark")
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatalf("start rate admission PTY helper: %v", err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	_ = slave.Close()
	session.command = command

	chunks := make(chan []byte, 256)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				chunks <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	session.chunks = chunks
	return session
}

func (s *ratePTYSession) send(inputs ...string) {
	s.t.Helper()
	writeFrontierPTYInput(s.t, s.master, inputs...)
}

func (s *ratePTYSession) waitScreen(width, height int, marker string) {
	s.t.Helper()
	s.output = waitForPTYScreenMarker(s.t, s.chunks, s.output, width, height, marker)
}

func (s *ratePTYSession) screen(width, height int) string {
	s.t.Helper()
	screen := newFrontierPTYScreen(width, height)
	screen.write(s.output)
	return screen.normalized()
}

func (s *ratePTYSession) waitEvent(event string) string {
	s.t.Helper()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	for {
		contents, err := os.ReadFile(s.log)
		if err == nil && strings.Contains(string(contents), event) {
			return string(contents)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.t.Fatalf("read rate admission call ledger: %v", err)
		}
		select {
		case <-deadline.C:
			s.t.Fatalf("rate admission helper did not record %q", event)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func (s *ratePTYSession) events() string {
	s.t.Helper()
	contents, err := os.ReadFile(s.log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.t.Fatalf("read rate admission call ledger: %v", err)
	}
	return string(contents)
}

func (s *ratePTYSession) finish() []byte {
	s.t.Helper()
	s.send("q")
	waited := make(chan error, 1)
	go func() { waited <- s.command.Wait() }()
	deadline := time.NewTimer(frontierLimitPTYTimeout)
	defer deadline.Stop()
	readerOpen, processDone := true, false
	for readerOpen || !processDone {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				readerOpen = false
				s.chunks = nil
				continue
			}
			s.output = append(s.output, chunk...)
		case err := <-waited:
			processDone, waited = true, nil
			if err != nil {
				s.t.Fatalf("rate admission PTY helper exited unsuccessfully: %v\n%s", err, s.output)
			}
		case <-deadline.C:
			_ = s.command.Process.Kill()
			s.t.Fatalf("rate admission PTY helper did not cleanly exit\n%s", s.output)
		}
	}
	_ = s.master.Close()
	return s.output
}

func assertRatePTYTeardown(t *testing.T, output []byte) {
	t.Helper()
	raw := string(output)
	assertTerminalModePair(t, raw, ansi.SetModeAltScreenSaveCursor, ansi.ResetModeAltScreenSaveCursor)
	assertTerminalModePair(t, raw, ansi.SetModeMouseButtonEvent, ansi.ResetModeMouseButtonEvent)
	assertTerminalModePair(t, raw, ansi.SetModeMouseExtSgr, ansi.ResetModeMouseExtSgr)
	assertTerminalModePair(t, raw, ansi.SetModeFocusEvent, ansi.ResetModeFocusEvent)
	assertTerminalModePair(t, raw, ansi.HideCursor, ansi.ShowCursor)
}

func TestFrontierPartialMemberFailureThroughRawPTY(t *testing.T) {
	session := startRatePTYSession(t, "partial", 180, 40)
	session.waitScreen(180, 40, "PARTIAL-ONE")
	session.send("v")
	session.waitScreen(180, 40, "1 Ticket's Links could not be read")
	session.waitScreen(180, 40, "PARTIAL-MEMBER-FAILURE")
	events := session.waitEvent("fanout:P-1,P-2,P-3")
	if strings.Count(events, "fanout:") != 1 {
		t.Fatalf("plural Frontier issued %d fan-out commands, want one:\n%s", strings.Count(events, "fanout:"), events)
	}
	assertRatePTYTeardown(t, session.finish())
}

func TestTrackerHardRefusalAcrossRawPTYScreens(t *testing.T) {
	session := startRatePTYSession(t, "known", 180, 40)
	session.waitScreen(180, 40, "RATE-PTY-known")
	session.send("r")
	session.waitScreen(180, 40, "Tracker requests are held.")
	session.send("\r") // uncached Detail is denied in place.
	session.send("v")
	session.waitScreen(180, 40, "frontier")
	visible := session.screen(180, 40)
	if !strings.Contains(visible, "Tracker requests are held.") {
		t.Fatalf("Frontier hid the Tracker-wide refusal:\n%s", visible)
	}
	events := session.waitEvent("source:1")
	if strings.Contains(events, "detail:") || strings.Contains(events, "fanout:") {
		t.Fatalf("hard refusal issued denied work:\n%s", events)
	}
	assertRatePTYTeardown(t, session.finish())
}

func TestUnknownProbeReservationThroughRawPTY(t *testing.T) {
	session := startRatePTYSession(t, "unknown", 180, 40)
	session.waitScreen(180, 40, "RATE-PTY-unknown")
	session.send("r")
	session.waitScreen(180, 40, "reset time is unknown")
	session.send("r")
	session.waitScreen(180, 40, "explicit retry is in progress")
	session.waitEvent("unknown-probe:started")
	session.send("\r")
	session.waitScreen(180, 40, "Ticket detail is held")
	session.send("\x1b")
	session.waitScreen(180, 40, "TODO (3)")
	session.send("v")
	session.waitScreen(180, 40, "frontier ·")
	if events := session.events(); strings.Contains(events, "detail:") || strings.Contains(events, "fanout:") {
		t.Fatalf("unknown-reset reservation admitted overlapping work:\n%s", events)
	}
	if err := os.WriteFile(session.release, []byte("release"), 0o600); err != nil {
		t.Fatalf("release unknown probe: %v", err)
	}
	session.waitEvent("unknown-probe:settled")
	session.waitScreen(180, 40, "r re-read Details")
	session.send("\r")
	session.waitScreen(180, 40, "DETAIL-ALLOWED")
	events := session.waitEvent("detail:P-1")
	if strings.Count(events, "source:") != 2 || strings.Count(events, "detail:") != 1 || strings.Contains(events, "fanout:") {
		t.Fatalf("unknown probe settlement calls = %q, want two Source, one Detail, no Frontier", events)
	}
	assertRatePTYTeardown(t, session.finish())
}

func TestFrontierCancellationThroughRawPTY(t *testing.T) {
	session := startRatePTYSession(t, "cancel", 180, 40)
	session.waitScreen(180, 40, "RATE-PTY-cancel")
	session.send("v")
	session.waitEvent("fanout:P-1,P-2,P-3")
	session.send("v")
	session.waitEvent("fanout:cancelled")
	events := session.events()
	if strings.Count(events, "fanout:") != 2 { // start and cancellation acknowledgement
		t.Fatalf("Frontier cancellation ledger = %q, want one start and one cancellation", events)
	}
	assertRatePTYTeardown(t, session.finish())
}

func TestRefreshHoldFocusManualAndNarrowHelpThroughRawPTY(t *testing.T) {
	const width, height = 60, 20
	session := startRatePTYSession(t, "composition", width, height)
	session.waitScreen(width, height, "RATE-PTY-composition")
	session.send("p")
	session.waitScreen(width, height, "monitor held")
	session.send("\x1b[O")
	session.waitScreen(width, height, "terminal unfocused")
	session.send("\x1b[I")
	session.waitScreen(width, height, "monitor held")
	if events := session.events(); events != "" {
		t.Fatalf("hold/focus composition started automatic work:\n%s", events)
	}
	session.send("r")
	session.waitScreen(width, height, "MANUAL-SOURCE")
	session.waitEvent("source:1")
	session.send("?")
	session.waitScreen(width, height, "p resume refresh")
	visible := session.screen(width, height)
	for _, marker := range []string{"? help", "q quit", "p resume refresh"} {
		if !strings.Contains(visible, marker) {
			t.Fatalf("narrow Help/footer lost %q:\n%s", marker, visible)
		}
	}
	assertRatePTYTeardown(t, session.finish())
}
