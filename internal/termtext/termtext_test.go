package termtext_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/niekcandaele/sitrep/internal/termtext"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

func TestLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ordinary text is untouched", in: "Add the widget", want: "Add the widget"},
		{name: "an erase-display sequence", in: "hi\x1b[2Jthere", want: "hi[2Jthere"},
		{name: "a window-title sequence", in: "hi\x1b]0;pwned\athere", want: "hi]0;pwnedthere"},
		{
			name: "OSC 52, which writes the clipboard",
			in:   "\x1b]52;c;cHduZWQ=\x07ok",
			want: "]52;c;cHduZWQ=ok",
		},
		{name: "a bare carriage return", in: "real\rfake", want: "realfake"},
		{name: "CRLF", in: "one\r\ntwo", want: "one two"},
		{name: "a newline becomes a space", in: "one\ntwo", want: "one two"},
		{name: "a tab becomes a space", in: "one\ttwo", want: "one two"},
		{name: "DEL", in: "a\x7fb", want: "ab"},
		{name: "a C1 control", in: "a\u009bb", want: "ab"},
		{name: "a NUL", in: "a\x00b", want: "ab"},
		{name: "multi-byte UTF-8 survives", in: "héllo — 世界 ✓", want: "héllo — 世界 ✓"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := termtext.Line(tt.in); got != tt.want {
				t.Errorf("Line(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Line normalizes malformed UTF-8 (#66). The earlier policy left it alone,
// which is exactly what let a raw single-byte C1 reach a terminal as CSI: the
// rune scan classified it as utf8.RuneError and wrote the byte back out.
func TestLineNormalizesMalformedUTF8(t *testing.T) {
	tests := [][]byte{
		{'a', 0x80, 'b'},
		{'a', 0xc0, 0xaf, 'b'},
		{'a', 0xc2, 'b'},
		{'a', 0xe2, 0x82, 'b'},
		{'a', 0xf0, 0x9f, 0x92, 'b'},
		{'a', 0xf5, 0x80, 0x80, 0x80, 'b'},
		{'a', 0xff, 'b'},
	}
	for _, input := range tests {
		got := termtext.Line(string(input))
		if !utf8.ValidString(got) || got != "ab" {
			t.Errorf("Line(% x) = %q (valid=%v), want %q", input, got, utf8.ValidString(got), "ab")
		}
	}
}

// A raw single-byte C1 is not a rune, so nothing but UTF-8 normalization keeps
// it off the wire: 0x9b is CSI and 0x9d is OSC to every terminal that sees it.
func TestLineDropsRawSingleByteC1(t *testing.T) {
	for b := 0x80; b <= 0x9f; b++ {
		t.Run(fmt.Sprintf("raw_C1_%02x", b), func(t *testing.T) {
			got := termtext.Line("body" + string([]byte{byte(b)}) + "tail")
			if got != "bodytail" {
				t.Errorf("Line(body %02x tail) = %q, want %q", b, got, "bodytail")
			}
		})
	}
}

func TestBodyKeepsMarkdownLayoutAndPrintableUnicode(t *testing.T) {
	input := "# Café 東京 👩🏽‍💻\r\n\n\t- [x] **done** é"
	want := "# Café 東京 👩🏽‍💻\n\n\t- [x] **done** é"
	if got := termtext.Body(input); got != want {
		t.Errorf("Body(%q) = %q, want %q", input, got, want)
	}
}

func TestBodyRemovesAllControlsExceptLayout(t *testing.T) {
	for control := byte(0); control < 0x20; control++ {
		t.Run(fmt.Sprintf("C0_%02x", control), func(t *testing.T) {
			input := "body" + string([]byte{control})
			want := "body"
			switch control {
			case '\n':
				want += "\n"
			case '\t':
				want += "\t"
			}
			if got := termtext.Body(input); got != want {
				t.Errorf("Body(%q) = %q, want %q", input, got, want)
			}
		})
	}
	for control := rune(0x7f); control <= 0x9f; control++ {
		t.Run(fmt.Sprintf("control_%04x", control), func(t *testing.T) {
			input := "body" + string(control)
			if got := termtext.Body(input); got != "body" {
				t.Errorf("Body(%q) = %q, want %q", input, got, "body")
			}
		})
	}
	for control := byte(0x80); control <= 0x9f; control++ {
		t.Run(fmt.Sprintf("raw_C1_%02x", control), func(t *testing.T) {
			input := "body" + string([]byte{control})
			if got := termtext.Body(input); got != "body" {
				t.Errorf("Body(% x) = %q, want %q", []byte(input), got, "body")
			}
		})
	}
	if got := termtext.Body("one\rtwo\r\nthree"); got != "onetwo\nthree" {
		t.Errorf("bare CR and CRLF = %q, want %q", got, "onetwo\nthree")
	}
}

func TestBodyStripsCompleteTerminalSequencesAndPayloads(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ESC OSC 8 with BEL and ST",
			input: "before\x1b]8;;https://evil.example.test/a\aLABEL\x1b]8;;\x1b\\after",
			want:  "beforeLABELafter",
		},
		{
			name:  "ESC OSC 52 clipboard payload",
			input: "before\x1b]52;c;cHduZWQ=\aafter",
			want:  "beforeafter",
		},
		{
			name:  "raw C1 OSC 8 and ST",
			input: "before\x9d8;;https://evil.example.test/raw\x9cLABEL\x9d8;;\x9cafter",
			want:  "beforeLABELafter",
		},
		{
			name:  "UTF-8 C1 OSC 52 and ST",
			input: "before" + string(rune(0x9d)) + "52;c;cHduZWQ=" + string(rune(0x9c)) + "after",
			want:  "beforeafter",
		},
		{
			name:  "CSI styling keeps visible text",
			input: "before\x1b[31mred\x1b[0mafter",
			want:  "beforeredafter",
		},
		{
			name:  "DCS payload",
			input: "before\x1bP1;2|https://evil.example.test\x1b\\after",
			want:  "beforeafter",
		},
		{
			name:  "unterminated OSC payload",
			input: "before\x1b]52;c;cHduZWQ=https://evil.example.test",
			want:  "before",
		},
		{
			name:  "truncated CSI",
			input: "before\x1b[38;2",
			want:  "before",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := termtext.Body(tt.input)
			if got != tt.want {
				t.Errorf("Body(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Both policies are applied more than once to the same value — a Link target
// crosses the boundary again when it is seated as a Ticket — so neither may
// change its own output.
func TestLineAndBodyAreIdempotent(t *testing.T) {
	inputs := []string{
		termtexttest.Hostile,
		"before\x1b]8;;https://evil.example.test/a\aLABEL\x1b]8;;\x1b\\after",
		"before\x9d52;c;cHduZWQ=\x9cafter",
		"# Café 東京 👩🏽‍💻\r\n\n\t- [x] **done** é",
		string([]byte{'a', 0xff, 0xc0, 0xaf, 'b'}),
		"héllo — 世界 ✓ 東京 👩🏽‍💻",
		"a" + rlo + "b",
		"a" + pdi + "b",
		lri + "a " + rli + "ب" + pdi + " c" + pdi,
		lre + rli + "x",
	}
	for i, in := range inputs {
		t.Run(fmt.Sprintf("input-%d", i), func(t *testing.T) {
			for name, f := range map[string]func(string) string{
				"Line": termtext.Line,
				"Body": termtext.Body,
			} {
				once := f(in)
				if again := f(once); again != once {
					t.Errorf("%s is not idempotent: first %q, second %q", name, once, again)
				}
				if !utf8.ValidString(once) {
					t.Errorf("%s left malformed UTF-8: % x", name, []byte(once))
				}
			}
		})
	}
}

func TestPrintableUnicodeSurvivesBothPolicies(t *testing.T) {
	const text = "café — 世界 ✓ 東京 👩🏽‍💻"
	if got := termtext.Line(text); got != text {
		t.Errorf("Line(%q) = %q", text, got)
	}
	const body = "café\n\n\t東京 👩🏽‍💻"
	if got := termtext.Body(body); got != body {
		t.Errorf("Body(%q) = %q", body, got)
	}
}

var errSentinel = errors.New("sentinel")

func TestErrPreservesUnwrap(t *testing.T) {
	err := termtext.Err(fmt.Errorf("source failed: boom\x1b[2J\nnext: %w", errSentinel))

	if !errors.Is(err, errSentinel) {
		t.Errorf("errors.Is(%q, errSentinel) = false, want true", err)
	}
	if strings.ContainsRune(err.Error(), 0x1b) || strings.Contains(err.Error(), "\n") {
		t.Errorf("error = %q, want one clean line", err)
	}
	if !strings.Contains(err.Error(), "sentinel") {
		t.Errorf("error = %q, want it to still carry the wrapped message", err)
	}
}

func TestErrLeavesACleanErrorAlone(t *testing.T) {
	// Identity, not wrapping: a clean error must come back as the very error
	// that went in, so errors.Is would be the wrong question here.
	//nolint:errorlint // deliberate identity comparison
	if got := termtext.Err(errSentinel); got != errSentinel {
		t.Errorf("Err(errSentinel) = %v, want the same error back", got)
	}
	if got := termtext.Err(nil); got != nil {
		t.Errorf("Err(nil) = %v, want nil", got)
	}
}
