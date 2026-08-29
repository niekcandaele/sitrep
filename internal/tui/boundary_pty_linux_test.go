//go:build linux

package tui

import (
	"context"
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
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

const boundaryPTYHelperMode = "SITREP_BOUNDARY_PTY_HELPER"

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
)

// boundaryPTYBodyPayload is carried only by body fields, where the Body policy
// removes a complete sequence together with its payload. Line deliberately
// keeps a sequence's printable payload, so a single payload shared by both
// policies could not be asserted absent.
const boundaryPTYBodyPayload = "cHR5LWJvZHktcGF5bG9hZA=="

// Every hostile field of every funnel, through a real terminal. The frame-level
// tests assert what the Model drew; this asserts what the terminal actually
// received, across both screens and a Trail step, including the teardown.
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
	assertTerminalModePair(t, output, ansi.HideCursor, ansi.ShowCursor)
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

// boundaryPTYOptions fills all four funnels: a seated reading, a decoded Ticket,
// a Source and a DetailSource, none of them a Provider.
func boundaryPTYOptions() Options {
	now := time.Unix(1_700_000_000, 0)
	list := ListInput{
		Header: Header{
			Key:   hostilePTYLine("PTY"),
			Title: hostilePTYLine(boundaryPTYListMarker),
			URL:   hostilePTYLine("https://tracker.example/watchlist"),
		},
		Tickets:      []model.Ticket{hostilePTYTicket("T-3", "PTY-ROW")},
		Capabilities: model.Capabilities{Comments: true, BlockingLinks: true, PullRequests: true},
		FetchedAt:    now,
	}
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
		Source:  func(context.Context) (ListInput, error) { return list, nil },
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
	// tab focuses the Link and enter follows it into a second Detail seat; u
	// walks up into the list, and enter drills back in from a row. The last
	// step walks up again and opens the Frontier, whose canvas draws the same
	// hostile fields through a third render path.
	script := []struct {
		waitFor string
		send    string
	}{
		{waitFor: boundaryPTYDetailMarker, send: "\t\r"},
		{waitFor: boundaryPTYChildMarker, send: "u"},
		{waitFor: boundaryPTYListMarker, send: "\r"},
		{waitFor: boundaryPTYDrillMarker, send: "uv"},
		{waitFor: boundaryPTYFrontierMarker, send: ""},
		{waitFor: boundaryPTYFrontierCardMarker, send: "q"},
	}
	for _, step := range script {
		output = waitForPTYMarker(t, chunks, output, step.waitFor)
		if _, err := io.WriteString(master, step.send); err != nil {
			t.Fatalf("write %q to the PTY: %v", step.send, err)
		}
	}

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
