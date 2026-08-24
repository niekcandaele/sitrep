//go:build linux

package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"

	"github.com/niekcandaele/sitrep/internal/model"
)

const (
	markdownPTYHelperMode = "SITREP_MARKDOWN_PTY_HELPER"
	markdownPTYLightMode  = "SITREP_MARKDOWN_PTY_LIGHT"
)

func TestMarkdownBodySafetyThroughRawPTY(t *testing.T) {
	for _, mode := range []string{"list", "decoder"} {
		t.Run(mode, func(t *testing.T) {
			raw := runMarkdownPTYSession(t, mode)
			if !utf8.Valid(raw) {
				t.Fatalf("PTY output contains malformed UTF-8: % x", raw)
			}
			output := string(raw)
			for _, forbidden := range []string{
				"https://evil.example.test/pty", "cHR5LW9zYy01Mg==", "\x1b]52;",
			} {
				if strings.Contains(output, forbidden) {
					t.Errorf("PTY output contains hostile body payload %q", forbidden)
				}
			}
			visible := ansi.Strip(output)
			for _, want := range []string{"PTY MARKDOWN READY", "café", "東京", "safe PTY link"} {
				if !strings.Contains(visible, want) {
					t.Errorf("PTY output lost %q:\n%s", want, visible)
				}
			}
			for _, target := range []string{
				"https://safe.example.test/pty", "https://github.com/acme/widgets/issues/12",
			} {
				if !strings.Contains(output, target) {
					t.Errorf("PTY output lacks expected safe hyperlink %q", target)
				}
			}
			assertPTYHyperlinksClosed(t, output)
			assertTerminalModePair(t, output, ansi.SetModeAltScreenSaveCursor, ansi.ResetModeAltScreenSaveCursor)
			assertTerminalModePair(t, output, ansi.SetModeMouseButtonEvent, ansi.ResetModeMouseButtonEvent)
			assertTerminalModePair(t, output, ansi.SetModeMouseExtSgr, ansi.ResetModeMouseExtSgr)
			assertTerminalModePair(t, output, ansi.HideCursor, ansi.ShowCursor)
		})
	}
}

func TestMarkdownLightThemeThroughRawPTY(t *testing.T) {
	raw := runMarkdownPTYSessionWithTheme(t, "decoder", true)
	if !utf8.Valid(raw) {
		t.Fatalf("PTY output contains malformed UTF-8: % x", raw)
	}
	output := string(raw)
	visible := ansi.Strip(output)
	markers := []string{"DESCRIPTION PROSE MARKER", "LIST PROSE MARKER", "COMMENT PROSE MARKER"}
	for _, marker := range markers {
		if !strings.Contains(visible, marker) {
			t.Errorf("light PTY output lost %q:\n%s", marker, visible)
		}
	}
	assertMarkdownProseForeground(t, output, markers, "\x1b[38;5;234m", "\x1b[38;5;252m")
	assertPTYHyperlinksClosed(t, output)
	assertTerminalModePair(t, output, ansi.SetModeAltScreenSaveCursor, ansi.ResetModeAltScreenSaveCursor)
	assertTerminalModePair(t, output, ansi.SetModeMouseButtonEvent, ansi.ResetModeMouseButtonEvent)
	assertTerminalModePair(t, output, ansi.SetModeMouseExtSgr, ansi.ResetModeMouseExtSgr)
	assertTerminalModePair(t, output, ansi.HideCursor, ansi.ShowCursor)
}

func TestMarkdownPTYHelper(t *testing.T) {
	mode := os.Getenv(markdownPTYHelperMode)
	if mode == "" {
		return
	}
	if os.Getenv(markdownPTYLightMode) != "" {
		if err := os.Unsetenv("GLAMOUR_STYLE"); err != nil {
			t.Fatalf("remove GLAMOUR_STYLE for light PTY helper: %v", err)
		}
	}
	if err := Run(context.Background(), markdownPTYOptions(mode)); err != nil {
		t.Fatalf("tui.Run: %v", err)
	}
}

func markdownPTYOptions(mode string) Options {
	ticket := model.Ticket{
		ID: "acme/widgets#40", Key: "#40", Title: "PTY body safety",
		URL: "https://github.com/acme/widgets/issues/40", Status: model.StatusInProgress,
	}
	hostile := "## PTY MARKDOWN READY\n\nDESCRIPTION PROSE MARKER café 東京 " + string([]byte{0xff, 0xc0, 0xaf}) +
		"\x1b]8;;https://evil.example.test/pty\aVISIBLE\x1b]8;;\x1b\\" +
		"\x1b]52;c;cHR5LW9zYy01Mg==\a\x1b[2J\r\n" +
		"- LIST PROSE MARKER\n\n[safe PTY link](https://safe.example.test/pty) and #12"
	detailSource := func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
		return model.Detail{
			Description: hostile,
			Comments:    []model.Comment{{Body: "COMMENT PROSE MARKER **Markdown** is safe."}},
		}, model.Capabilities{Comments: true}, nil
	}
	opts := Options{
		DetailSource: detailSource,
		Interval:     time.Hour,
		Input:        os.Stdin,
		Output:       os.Stdout,
	}
	if mode == "decoder" {
		opts.Open = &OpenTicket{Ticket: ticket, Capabilities: model.Capabilities{Comments: true}}
		return opts
	}
	now := time.Unix(1_700_000_000, 0)
	opts.Now = func() time.Time { return now }
	opts.Initial = &ListInput{
		Header:       Header{Key: "PTY", Title: "PTY LIST READY"},
		Tickets:      []model.Ticket{ticket},
		Capabilities: model.Capabilities{Comments: true},
		FetchedAt:    now,
	}
	return opts
}

func runMarkdownPTYSession(t *testing.T, mode string) []byte {
	t.Helper()
	return runMarkdownPTYSessionWithTheme(t, mode, false)
}

func runMarkdownPTYSessionWithTheme(t *testing.T, mode string, light bool) []byte {
	t.Helper()
	master, slave := openMarkdownPTY(t, 100, 40)
	command := exec.Command(os.Args[0], "-test.run=^TestMarkdownPTYHelper$", "-test.v=false")
	command.Env = environmentWithout(os.Environ(), "GLAMOUR_STYLE")
	command.Env = environmentWithout(command.Env, markdownPTYLightMode)
	command.Env = append(command.Env, markdownPTYHelperMode+"="+mode)
	if light {
		command.Env = append(command.Env, markdownPTYLightMode+"=1")
	} else {
		command.Env = append(command.Env, "GLAMOUR_STYLE=dark")
	}
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
				chunk := append([]byte(nil), buffer[:n]...)
				chunks <- chunk
			}
			if err != nil {
				return
			}
		}
	}()

	output := make([]byte, 0, 16*1024)
	if light {
		output = waitForPTYMarker(t, chunks, output, "\x1b]11;?\a")
		output = waitForPTYMarker(t, chunks, output, ansi.SetModeAltScreenSaveCursor)
		if _, err := io.WriteString(master, "\x1b]11;rgb:ffff/ffff/ffff\a"); err != nil {
			t.Fatalf("answer PTY background query: %v", err)
		}
		output = waitForPTYMarkdownForeground(t, chunks, output,
			[]string{"DESCRIPTION PROSE MARKER", "LIST PROSE MARKER", "COMMENT PROSE MARKER"},
			"\x1b[38;5;234m")
	}
	if mode == "list" {
		output = waitForPTYMarker(t, chunks, output, "PTY LIST READY")
		if _, err := io.WriteString(master, "\r"); err != nil {
			t.Fatalf("open list Ticket through PTY: %v", err)
		}
	}
	output = waitForPTYMarker(t, chunks, output, "PTY MARKDOWN READY")
	if _, err := io.WriteString(master, "q"); err != nil {
		t.Fatalf("quit PTY session: %v", err)
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

func waitForPTYMarker(t *testing.T, chunks <-chan []byte, output []byte, marker string) []byte {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
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

func waitForPTYMarkdownForeground(t *testing.T, chunks <-chan []byte, output []byte,
	markers []string, foreground string) []byte {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		matched := make(map[string]bool, len(markers))
		for _, line := range strings.Split(string(output), "\n") {
			visible := ansi.Strip(line)
			if !strings.Contains(line, foreground) {
				continue
			}
			for _, marker := range markers {
				if strings.Contains(visible, marker) {
					matched[marker] = true
				}
			}
		}
		if len(matched) == len(markers) {
			return output
		}
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("PTY closed before light Markdown prose rendered\n%s", output)
			}
			output = append(output, chunk...)
		case <-deadline.C:
			t.Fatalf("PTY did not render light Markdown prose\n%s", output)
		}
	}
}

func environmentWithout(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func openMarkdownPTY(t *testing.T, width, height uint16) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("read PTY number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: width, Row: height}); err != nil {
		t.Fatalf("size PTY: %v", err)
	}
	return master, slave
}

func assertPTYHyperlinksClosed(t *testing.T, raw string) {
	t.Helper()
	resets := strings.Count(raw, ansi.ResetHyperlink())
	opens := strings.Count(raw, "\x1b]8;") - resets
	if opens > resets || strings.LastIndex(raw, ansi.ResetHyperlink()) < strings.LastIndex(raw, "\x1b]8;") {
		t.Errorf("PTY OSC 8 scopes are not closed: %d opens, %d resets", opens, resets)
	}
}

func assertTerminalModePair(t *testing.T, raw, enable, disable string) {
	t.Helper()
	enables := strings.Count(raw, enable)
	disables := strings.Count(raw, disable)
	if enables == 0 || enables != disables || strings.LastIndex(raw, disable) < strings.LastIndex(raw, enable) {
		t.Errorf("terminal mode pair %q/%q = %d/%d with invalid teardown order", enable, disable, enables, disables)
	}
}
