package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// allCaps declares everything, so a test that cares about a Capability has to
// say so.
var allCaps = model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true}

// fixtureDetailInput builds the rich fixture Ticket's DetailInput, which carries
// a multi-paragraph description, three comments and all three Link kinds.
func fixtureDetailInput(caps model.Capabilities) DetailInput {
	snap := fake.FixtureSnapshot()
	detail := fake.FixtureDetails()["acme/widgets#112"]
	return DetailFromTicket(snap.Tickets[0], detail, caps,
		Header{Key: snap.Epic.Key, Title: snap.Epic.Title, URL: snap.Epic.URL},
		time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC))
}

// plainLines strips the styling a golden strips, so a width assertion measures
// columns rather than escape sequences.
func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = ansi.Strip(line)
	}
	return out
}

// The body wraps rather than truncates: a description cut off at the right-hand
// edge is useless, and a line wider than the terminal would wrap in the terminal
// instead and silently break the window arithmetic above it.
func TestDetailLinesWrapToWidth(t *testing.T) {
	for _, width := range []int{40, 60, 120} {
		lines := plainLines(detailLines(fixtureDetailInput(allCaps), width, Styles{}))
		if len(lines) == 0 {
			t.Fatalf("width %d produced no lines", width)
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d: line %d is %d columns wide: %q", width, i, got, line)
			}
		}
	}
}

// A markdown body is shown as source, wrapped, with its paragraph breaks intact:
// blank lines are how the shape of a description survives without a renderer.
func TestDetailLinesKeepParagraphBreaks(t *testing.T) {
	lines := plainLines(detailLines(fixtureDetailInput(allCaps), 60, Styles{}))
	body := strings.Join(lines, "\n")

	if !strings.Contains(body, "## Shape") {
		t.Errorf("the markdown source was rendered away:\n%s", body)
	}
	if !strings.Contains(body, "\n\n1. The sender announces") {
		t.Errorf("the blank line before the list was lost:\n%s", body)
	}
	// The fixture's ampersand is data, not markup.
	if !strings.Contains(body, "&") {
		t.Errorf("the description lost its ampersand:\n%s", body)
	}
}

// Wrapping is over runes and cells, not bytes: a token longer than the line hard
// -wraps rather than overflowing, and an accented word is not cut mid-rune.
func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{
			name:  "blank lines survive",
			text:  "one\n\ntwo",
			width: 20,
			want:  []string{"one", "", "two"},
		},
		{
			name:  "words break at spaces",
			text:  "alpha beta gamma delta",
			width: 12,
			want:  []string{"alpha beta", "gamma delta"},
		},
		{
			name:  "a token longer than the line hard-wraps",
			text:  "https://tracker.example.test/acme/widgets/112#comment-1001",
			width: 20,
			want: []string{
				"https://tracker.exam",
				"ple.test/acme/widget",
				"s/112#comment-1001",
			},
		},
		{
			name:  "unicode wraps on runes",
			text:  "Renseigner la métrique « éclair » du tableau de bord",
			width: 20,
			want: []string{
				"Renseigner la",
				"métrique « éclair »",
				"du tableau de bord",
			},
		},
		{
			name:  "a width with no room for anything renders nothing",
			text:  "anything",
			width: 0,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapText(tt.text, tt.width)
			if len(got) != len(tt.want) {
				t.Fatalf("wrapText = %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// An undeclared Capability is silently absent: no heading, no placeholder, no
// explanation. Relates links survive without BlockingLinks, exactly as
// fake.applyDetailCapabilities and jsonout.RenderDetail already have it.
func TestDetailLinesRespectCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		caps    model.Capabilities
		absent  []string
		present []string
	}{
		{
			name:    "everything declared",
			caps:    allCaps,
			present: []string{"DESCRIPTION", "COMMENTS (3)", "LINKS (3)", "is blocked by", "blocks", "is duplicated by"},
		},
		{
			name:    "no comments capability",
			caps:    model.Capabilities{BlockingLinks: true},
			absent:  []string{"COMMENT", "mara-vos"},
			present: []string{"DESCRIPTION", "LINKS (3)"},
		},
		{
			name:    "no blocking links capability",
			caps:    model.Capabilities{Comments: true},
			absent:  []string{"is blocked by", "#115", "#116"},
			present: []string{"DESCRIPTION", "COMMENTS (3)", "LINKS (1)", "is duplicated by"},
		},
		{
			name:    "neither capability",
			caps:    model.Capabilities{},
			absent:  []string{"COMMENT", "is blocked by"},
			present: []string{"DESCRIPTION", "shard sync protocol", "LINKS (1)", "is duplicated by"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := fixtureDetailInput(tt.caps)
			// The fake's own capability filter runs at the Provider, so the
			// input a real session hands this renderer is already stripped. Both
			// layers have to agree, so the renderer is tested on unstripped data.
			body := strings.Join(plainLines(detailLines(in, 100, Styles{})), "\n")

			for _, want := range tt.present {
				if !strings.Contains(body, want) {
					t.Errorf("the body does not contain %q:\n%s", want, body)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(body, absent) {
					t.Errorf("the body mentions %q without the Capability behind it:\n%s", absent, body)
				}
			}
		})
	}
}

// An empty description is a fact about the Ticket, not a reason to draw nothing:
// a drill-in that answers with a blank screen reads as a bug.
func TestDetailLinesWithAnEmptyDescription(t *testing.T) {
	in := DetailInput{
		Ticket:       DetailHeader{Key: "#119", Title: "Emit sync telemetry"},
		Capabilities: allCaps,
		Detail:       fake.FixtureDetails()["acme/widgets#119"],
	}

	body := strings.Join(plainLines(detailLines(in, 80, Styles{})), "\n")

	if !strings.Contains(body, "No description.") {
		t.Errorf("an empty description drew nothing:\n%s", body)
	}
	if !strings.Contains(body, "Dashboards are live.") {
		t.Errorf("the comments went with the empty description:\n%s", body)
	}
}

// A Detail with the Comments Capability and no comments has something to say,
// and says it. The Capability's absence is what is silent, not an empty
// discussion.
func TestDetailLinesWithNoComments(t *testing.T) {
	in := DetailInput{
		Ticket:       DetailHeader{Key: "#115"},
		Capabilities: allCaps,
		Detail:       fake.FixtureDetails()["acme/widgets#115"],
	}

	body := strings.Join(plainLines(detailLines(in, 80, Styles{})), "\n")

	if !strings.Contains(body, "No comments yet.") {
		t.Errorf("a Tracker that does comments and a Ticket with none says nothing:\n%s", body)
	}
	if strings.Contains(body, "LINKS") {
		t.Errorf("a Ticket with no links drew a links heading:\n%s", body)
	}
}

// A comment's author and time are absolute facts, formatted in UTC with a fixed
// layout: time.Local in a golden is a test that passes in Amsterdam and fails in
// CI. An author who deleted their account is @unknown, not a dropped comment.
func TestCommentByline(t *testing.T) {
	at := time.Date(2026, time.January, 12, 9, 14, 0, 0, time.UTC)
	tests := []struct {
		name string
		c    model.Comment
		want string
	}{
		{
			name: "a login",
			c:    model.Comment{Author: model.User{Login: "mara-vos"}, CreatedAt: at},
			want: "@mara-vos · 2026-01-12 09:14 UTC",
		},
		{
			name: "a deleted account",
			c:    model.Comment{CreatedAt: at},
			want: "@unknown · 2026-01-12 09:14 UTC",
		},
		{
			name: "a timestamp in another zone reads in UTC",
			c: model.Comment{
				Author:    model.User{Login: "tobias"},
				CreatedAt: at.In(time.FixedZone("CET", 3600)),
			},
			want: "@tobias · 2026-01-12 09:14 UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commentByline(tt.c); got != tt.want {
				t.Errorf("commentByline = %q, want %q", got, tt.want)
			}
		})
	}
}

// The label column is the Tracker's own wording. A Tracker that supplies none
// falls back to the LinkKind's token, spelled with spaces: "blocked_by" is a
// wire token, and this is a sentence a human reads.
func TestLinkLabel(t *testing.T) {
	tests := []struct {
		link model.Link
		want string
	}{
		{model.Link{Kind: model.LinkBlockedBy, NativeLabel: "is blocked by"}, "is blocked by"},
		{model.Link{Kind: model.LinkRelates, NativeLabel: "is duplicated by"}, "is duplicated by"},
		{model.Link{Kind: model.LinkBlockedBy}, "blocked by"},
		{model.Link{Kind: model.LinkBlocks}, "blocks"},
		{model.Link{Kind: model.LinkRelates}, "relates"},
	}

	for _, tt := range tests {
		if got := linkLabel(tt.link); got != tt.want {
			t.Errorf("linkLabel(%+v) = %q, want %q", tt.link, got, tt.want)
		}
	}
}

// An offset that has fallen outside its document — a resize re-wrapped it, a
// re-fetch replaced it — comes back inside rather than scrolling into the void.
func TestClampDetailOffset(t *testing.T) {
	tests := []struct {
		name                      string
		offset, lineCount, height int
		want                      int
	}{
		{"inside the document", 5, 100, 20, 5},
		{"past the end", 500, 100, 20, 80},
		{"a document shorter than the window", 5, 10, 20, 0},
		{"negative", -3, 100, 20, 0},
		{"an empty document", 4, 0, 20, 0},
		{"exactly one screenful", 3, 20, 20, 0},
		// A terminal too small to have a body still has to answer.
		{"a zero height", 4, 10, 0, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampDetailOffset(tt.offset, tt.lineCount, tt.height); got != tt.want {
				t.Errorf("clampDetailOffset(%d, %d, %d) = %d, want %d",
					tt.offset, tt.lineCount, tt.height, got, tt.want)
			}
		})
	}
}

// The window is always exactly the height it was given: one line too many
// scrolls the alternate screen, one too few leaves the footer floating.
func TestRenderDetailBodyIsExactlyItsHeight(t *testing.T) {
	lines := plainLines(detailLines(fixtureDetailInput(allCaps), 80, Styles{}))

	for _, height := range []int{1, 5, 20, len(lines), len(lines) + 10} {
		for _, offset := range []int{-5, 0, 3, len(lines), len(lines) * 2} {
			got := strings.Split(renderDetailBody(lines, offset, height, 80), "\n")
			if len(got) != height {
				t.Errorf("height %d offset %d produced %d lines", height, offset, len(got))
			}
		}
	}
}

// A Detail that fits on screen has no position to report; one that does not
// reports it from top to bottom.
func TestScrollIndicator(t *testing.T) {
	tests := []struct {
		name                      string
		offset, lineCount, height int
		want                      string
	}{
		{"it all fits", 0, 10, 20, ""},
		{"at the top", 0, 120, 20, "0%"},
		{"at the bottom", 100, 120, 20, "100%"},
		{"halfway", 50, 120, 20, "50%"},
		{"past the bottom clamps", 900, 120, 20, "100%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scrollIndicator(tt.offset, tt.lineCount, tt.height); got != tt.want {
				t.Errorf("scrollIndicator(%d, %d, %d) = %q, want %q",
					tt.offset, tt.lineCount, tt.height, got, tt.want)
			}
		})
	}
}

// The breadcrumb is the seat a Ticket opened without a list behind it leaves
// empty. It is filled from DetailInput.Parent and from nowhere else.
func TestRenderBreadcrumb(t *testing.T) {
	tests := []struct {
		name   string
		parent Header
		want   string
	}{
		{"a collection", Header{Key: "#111", Title: "Widget sync v2"}, "#111 · Widget sync v2"},
		{"no parent at all", Header{}, ""},
		{"a title with no key", Header{Title: "Everything assigned to @tobias"}, "Everything assigned to @tobias"},
		{"a key with no title", Header{Key: "#111"}, "#111"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ansi.Strip(renderBreadcrumb(tt.parent, 80, Styles{})); got != tt.want {
				t.Errorf("renderBreadcrumb = %q, want %q", got, tt.want)
			}
		})
	}
}

// The Detail screen consumes a DetailInput, not a list. This is the executable
// form of that contract, and the shape the decoder fills for a Ticket Ref decoded
// straight into Detail: a hand-built DetailInput with no list behind it renders,
// breadcrumb seat and all.
func TestDetailFrameFromAHandBuiltInput(t *testing.T) {
	in := DetailInput{
		Ticket: DetailHeader{
			Key: "PROJ-7", Title: "Teach the gadget agent to speak sync v2",
			Status: model.StatusInProgress, NativeStatus: "In Progress",
		},
		Detail:       model.Detail{TicketID: "PROJ-7", Description: "A Ticket reached without a list."},
		Capabilities: allCaps,
	}

	header := ansi.Strip(renderDetailHeader(in, "read just now", 80, Styles{}))
	body := strings.Join(plainLines(detailLines(in, 80, Styles{})), "\n")

	if strings.Count(header, "\n") != detailHeaderHeight-1 {
		t.Errorf("the header is not %d lines:\n%s", detailHeaderHeight, header)
	}
	for _, want := range []string{"PROJ-7", "Teach the gadget agent", "[In Progress]", "read just now"} {
		if !strings.Contains(header, want) {
			t.Errorf("the header does not contain %q:\n%s", want, header)
		}
	}
	if !strings.Contains(body, "A Ticket reached without a list.") {
		t.Errorf("the body does not carry the description:\n%s", body)
	}
}

// The Detail reading is aged separately from the list's, so a cached Detail
// beside a freshly refreshed list cannot look as new as the list does.
func TestDetailStaleness(t *testing.T) {
	at := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		fetchedAt time.Time
		now       time.Time
		loading   bool
		want      string
	}{
		{"in flight", at, at, true, "reading…"},
		{"never read", time.Time{}, at, false, "never read"},
		{"just now", at, at, false, "read just now"},
		{"seconds", at, at.Add(4 * time.Second), false, "read 4s ago"},
		{"minutes", at, at.Add(3 * time.Minute), false, "read 3m ago"},
		{"hours", at, at.Add(64 * time.Minute), false, "read 1h 4m ago"},
		{"in flight beats an age", at, at.Add(time.Hour), true, "reading…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detailStaleness(tt.fetchedAt, tt.now, tt.loading); got != tt.want {
				t.Errorf("detailStaleness = %q, want %q", got, tt.want)
			}
		})
	}
}
