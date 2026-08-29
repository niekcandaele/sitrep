package plain

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The bar's arithmetic has edge cases no fixture epic can reach — an epic with
// nothing in it, one where every Ticket is cancelled, a Provider reporting more
// Done than Denominator — so it gets a direct test. Everything else about the
// renderer is proven through the goldens at the terminal seam.
func TestProgressBar(t *testing.T) {
	const width = 10

	tests := []struct {
		name     string
		progress model.Progress
		want     string
	}{
		{
			name:     "nothing to do yet does not divide by zero",
			progress: model.Progress{Done: 0, Denominator: 0},
			want:     "░░░░░░░░░░",
		},
		{
			name:     "no work finished",
			progress: model.Progress{Done: 0, Denominator: 9},
			want:     "░░░░░░░░░░",
		},
		{
			name:     "everything finished fills the bar",
			progress: model.Progress{Done: 9, Denominator: 9},
			want:     "██████████",
		},
		{
			name:     "a third rounds to the nearest cell",
			progress: model.Progress{Done: 1, Denominator: 3},
			want:     "███░░░░░░░",
		},
		{
			name:     "a broken Provider reporting more done than possible is clamped",
			progress: model.Progress{Done: 12, Denominator: 9},
			want:     "██████████",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProgressBar(tt.progress, width)

			if got != tt.want {
				t.Errorf("ProgressBar(%+v, %d) = %q, want %q", tt.progress, width, got, tt.want)
			}
			if n := len([]rune(got)); n != width {
				t.Errorf("ProgressBar(%+v, %d) is %d runes wide, want exactly %d",
					tt.progress, width, n, width)
			}
		})
	}
}

func TestRenderTicketKeepsMarkdownSourceLiteral(t *testing.T) {
	markdown := "# Heading\n\n- [x] task\n\n```go\nfmt.Println(\"raw\")\n```\n\nSee #12, @alice, and [docs](https://example.test/docs)."
	var output bytes.Buffer
	if err := RenderTicket(&output, TicketSnapshot{
		Ticket: model.Ticket{Key: "#40", Title: "Markdown"},
		Detail: model.Detail{
			Description: markdown,
			Comments:    []model.Comment{{Body: markdown}},
		},
		Capabilities: model.Capabilities{Comments: true},
	}); err != nil {
		t.Fatalf("RenderTicket: %v", err)
	}

	raw := output.String()
	if !strings.Contains(raw, "DESCRIPTION\n\n"+markdown+"\n\n") {
		t.Errorf("plain description did not preserve literal Markdown:\n%s", raw)
	}
	for _, line := range strings.Split(markdown, "\n") {
		if !strings.Contains(raw, "\n  "+line+"\n") {
			t.Errorf("plain comment did not preserve Markdown line %q:\n%s", line, raw)
		}
	}
	for _, rewritten := range []string{"https://github.com/", "\x1b[", "\x1b]8;"} {
		if strings.Contains(raw, rewritten) {
			t.Errorf("plain Ticket unexpectedly rendered or rewrote Markdown with %q: %q", rewritten, raw)
		}
	}
}

func TestLimitNotice(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{count: 1, want: "Limit reached — showing 1 ticket."},
		{count: 2, want: "Limit reached — showing 2 tickets."},
	}
	for _, tt := range tests {
		if got := LimitNotice(tt.count); got != tt.want {
			t.Errorf("LimitNotice(%d) = %q, want %q", tt.count, got, tt.want)
		}
	}
}

// Titles are truncated by runes, not bytes: the fixture epic carries « éclair »
// and a byte slice would cut a code point in half.
func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		title string
		width int
		want  string
	}{
		{
			name:  "a short title is untouched",
			title: "Cache shard lookups",
			width: 20,
			want:  "Cache shard lookups",
		},
		{
			name:  "a title exactly at the limit is untouched",
			title: "abcde",
			width: 5,
			want:  "abcde",
		},
		{
			name:  "a long title ends in an ellipsis",
			title: "abcdefgh",
			width: 5,
			want:  "abcd…",
		},
		{
			name:  "a multi-byte title is cut by runes",
			title: "Renseigner la métrique « éclair »",
			width: 20,
			want:  "Renseigner la métri…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.title, tt.width)

			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.title, tt.width, got, tt.want)
			}
			if n := len([]rune(got)); n > tt.width {
				t.Errorf("Truncate(%q, %d) is %d runes, want at most %d", tt.title, tt.width, n, tt.width)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Truncate(%q, %d) = %q, which corrupted a code point", tt.title, tt.width, got)
			}
		})
	}
}
