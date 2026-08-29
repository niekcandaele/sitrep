package tui

import (
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// A Markdown body is rendered into terminal presentation while keeping its
// structure and ordinary data.
func TestDetailLinesRenderMarkdownStructure(t *testing.T) {
	rawLines := detailLines(fixtureDetailInput(allCaps), 60, Styles{})
	lines := plainLines(rawLines)
	body := strings.Join(lines, "\n")

	headingStyled := false
	for i, line := range lines {
		if strings.Contains(line, "## Shape") && strings.Contains(rawLines[i], "\x1b[") {
			headingStyled = true
			break
		}
	}
	if !headingStyled {
		t.Errorf("the built-in Markdown heading was not styled:\n%s", body)
	}
	for _, want := range []string{"Shape", "1. The sender announces", "2. The receiver either accepts"} {
		if !strings.Contains(body, want) {
			t.Errorf("the rendered Markdown lost %q:\n%s", want, body)
		}
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

	header := ansi.Strip(renderDetailHeader(in, "read just now", 80, Styles{}, nil))
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

func TestDetailDocumentRecordsVisibleLinkRows(t *testing.T) {
	wantLines := map[int][]int{
		40:  {43, 44, 45},
		60:  {37, 38, 39},
		120: {31, 32, 33},
	}
	for _, width := range []int{40, 60, 120} {
		doc := composeDetailDocument(fixtureDetailInput(allCaps), width, Styles{}, detailLinkIdentity{}, false)
		if len(doc.LinkRows) != 3 {
			t.Fatalf("width %d: got %d Link rows, want 3", width, len(doc.LinkRows))
		}
		for i, row := range doc.LinkRows {
			if row.Line != wantLines[width][i] {
				t.Errorf("width %d Link %d line = %d, want exact document line %d",
					width, i, row.Line, wantLines[width][i])
			}
			if row.Line < 0 || row.Line >= len(doc.Lines) {
				t.Fatalf("width %d Link %d points outside the document: %+v", width, i, row)
			}
			line := ansi.Strip(doc.Lines[row.Line])
			if !strings.HasPrefix(line, unselectedMarker) {
				t.Errorf("width %d Link %d has no fixed gutter: %q", width, i, line)
			}
			if !strings.Contains(line, row.Link.Target.Key) {
				t.Errorf("width %d Link %d metadata points at %q", width, i, line)
			}
		}
	}

	hidden := composeDetailDocument(fixtureDetailInput(model.Capabilities{Comments: true}), 80, Styles{}, detailLinkIdentity{}, false)
	if len(hidden.LinkRows) != 1 || hidden.LinkRows[0].Link.Kind != model.LinkRelates {
		t.Fatalf("capability-filtered Link rows = %+v, want only Relates", hidden.LinkRows)
	}
}

func TestDetailDocumentPinsSectionOrderAndLinkGeometry(t *testing.T) {
	input := DetailInput{
		Capabilities: model.Capabilities{Comments: true, BlockingLinks: true},
		Detail: model.Detail{
			Description: "alpha beta gamma delta",
			Comments: []model.Comment{{
				Author:    model.User{Login: "ann"},
				CreatedAt: time.Date(2026, time.January, 2, 3, 4, 0, 0, time.UTC),
				Body:      "comment words wrap here",
			}},
			Links: []model.Link{{
				Kind:        model.LinkBlocks,
				NativeLabel: "blocks",
				Target:      model.LinkTarget{ID: "K-2", Key: "K-2", Title: "Linked target"},
			}},
		},
	}

	tests := []struct {
		name     string
		width    int
		caps     model.Capabilities
		wantRows []string
		linkLine int
	}{
		{
			name: "wrapped at twelve", width: 12, caps: input.Capabilities, linkLine: 20,
			wantRows: []string{
				"DESCRIPTION", "", "  alpha   ", "  beta    ", "  gamma   ", "  delta   ", "", "",
				"COMMENTS (1)", "", "@ann · 2026-", "    commen", "    t     ", "    words ",
				"    wrap  ", "    here  ", "  ", "", "LINKS (1)", "", "<LINK>",
			},
		},
		{
			name: "wrapped at twenty", width: 20, caps: input.Capabilities, linkLine: 15,
			wantRows: []string{
				"DESCRIPTION", "", "  alpha beta gamma", "  delta           ", "", "",
				"COMMENTS (1)", "", "@ann · 2026-01-02 03", "    comment words ", "    wrap here     ",
				"  ", "", "LINKS (1)", "", "<LINK>",
			},
		},
		{
			name: "single-line content at thirty", width: 30, caps: input.Capabilities, linkLine: 13,
			wantRows: []string{
				"DESCRIPTION", "", "  alpha beta gamma delta    ", "", "",
				"COMMENTS (1)", "", "@ann · 2026-01-02 03:04 UTC", "    comment words wrap here ",
				"  ", "", "LINKS (1)", "", "<LINK>",
			},
		},
		{
			name: "comments capability hidden", width: 20,
			caps: model.Capabilities{BlockingLinks: true}, linkLine: 8,
			wantRows: []string{
				"DESCRIPTION", "", "  alpha beta gamma", "  delta           ", "", "",
				"LINKS (1)", "", "<LINK>",
			},
		},
		{
			name: "blocking links capability hidden", width: 20,
			caps: model.Capabilities{Comments: true}, linkLine: -1,
			wantRows: []string{
				"DESCRIPTION", "", "  alpha beta gamma", "  delta           ", "", "",
				"COMMENTS (1)", "", "@ann · 2026-01-02 03", "    comment words ", "    wrap here     ", "  ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input
			in.Capabilities = tt.caps
			doc := composeDetailDocument(in, tt.width, Styles{}, detailLinkIdentity{}, false)
			got := plainLines(doc.Lines)
			if len(doc.LinkRows) == 1 {
				if doc.LinkRows[0].Line != tt.linkLine {
					t.Fatalf("Link line = %d, want %d", doc.LinkRows[0].Line, tt.linkLine)
				}
				got[tt.linkLine] = "<LINK>"
			} else if tt.linkLine >= 0 {
				t.Fatalf("Link rows = %d, want one at %d", len(doc.LinkRows), tt.linkLine)
			} else if len(doc.LinkRows) != 0 {
				t.Fatalf("hidden Links produced metadata: %+v", doc.LinkRows)
			}
			if !reflect.DeepEqual(got, tt.wantRows) {
				t.Errorf("document rows = %#v\nwant          = %#v", got, tt.wantRows)
			}
		})
	}
}

func TestDetailLinkTitleUsesCellWidthAndPreservesStatus(t *testing.T) {
	in := DetailInput{
		Capabilities: model.Capabilities{BlockingLinks: true},
		Detail: model.Detail{Links: []model.Link{{
			Kind:        model.LinkBlocks,
			NativeLabel: "blocks",
			Target: model.LinkTarget{
				ID: "K-2", Key: "K-2", Title: strings.Repeat("界", 20), NativeStatus: "Open",
			},
		}}},
	}
	const width = 40
	doc := composeDetailDocument(in, width, Styles{}, detailLinkIdentity{}, false)
	if len(doc.LinkRows) != 1 {
		t.Fatalf("Link rows = %d, want one", len(doc.LinkRows))
	}
	line := ansi.Strip(doc.Lines[doc.LinkRows[0].Line])
	if !strings.Contains(line, "[Open]") {
		t.Errorf("cell-truncated Unicode title displaced Native Status: %q", line)
	}
	if got := ansi.StringWidth(line); got > width {
		t.Errorf("Link line width = %d, budget %d: %q", got, width, line)
	}
}

func TestDetailLongUnicodeLinkLabelPreservesTargetIdentity(t *testing.T) {
	longLabel := strings.Repeat("界", 20)
	in := DetailInput{
		Capabilities: model.Capabilities{BlockingLinks: true},
		Detail: model.Detail{Links: []model.Link{
			{
				Kind:        model.LinkBlocks,
				NativeLabel: longLabel,
				Target: model.LinkTarget{
					ID: "K-2", Key: "K-2", Title: "Target identity remains visible", NativeStatus: "Open",
				},
			},
			{
				Kind:        model.LinkBlocks,
				NativeLabel: "blocks",
				Target: model.LinkTarget{
					ID: "K-3", Key: "K-3", Title: "Target identity stays aligned", NativeStatus: "Open",
				},
			},
		}},
	}

	for _, width := range []int{40, 42} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			doc := composeDetailDocument(in, width, Styles{}, detailLinkIdentity{}, false)
			if len(doc.LinkRows) != len(in.Detail.Links) {
				t.Fatalf("Link rows = %d, want %d", len(doc.LinkRows), len(in.Detail.Links))
			}
			for i, row := range doc.LinkRows {
				line := ansi.Strip(doc.Lines[row.Line])
				if got := ansi.StringWidth(line); got > width {
					t.Errorf("Link %d width = %d, budget %d: %q", i, got, width, line)
				}
				if !strings.HasPrefix(line, unselectedMarker) {
					t.Errorf("Link %d lost fixed focus gutter: %q", i, line)
				}
				if !strings.Contains(line, row.Link.Target.Key) || !strings.Contains(line, "Target") {
					t.Errorf("Link %d lost target identity behind relationship label: %q", i, line)
				}
			}
			longLine := ansi.Strip(doc.Lines[doc.LinkRows[0].Line])
			if strings.Contains(longLine, longLabel) {
				t.Errorf("long relationship label was not cell-truncated at width %d: %q", width, longLine)
			}
		})
	}
}

func TestDetailDocumentsStayWidthBoundedAtExtremeWidths(t *testing.T) {
	loaded := detailModel(t)
	loaded.detail.input.Detail.Description = "界面界面 a-long-token-without-breaks"
	loaded.detail.input.Detail.Comments = []model.Comment{{Body: "éclair 界面"}}
	loaded.detail.input.Detail.Links[0].Target.Title = "界面 relationship target"

	empty := loaded
	empty.detail.input.Detail = model.Detail{}
	empty.detail.input.Capabilities = model.Capabilities{Comments: true}

	loading := loaded
	loading.detail.loaded = false
	loading.detail.loading = true
	loading.detail.lastErr = nil

	failed := loading
	failed.detail.loading = false
	failed.detail.lastErr = errors.New("échec 界面 with a long diagnostic")

	states := []struct {
		name  string
		model Model
	}{
		{name: "loaded Unicode", model: loaded},
		{name: "loaded empty sections", model: empty},
		{name: "loading", model: loading},
		{name: "initial error", model: failed},
	}
	for _, width := range []int{0, 1, 2, 3, 5, 9} {
		for _, state := range states {
			t.Run(fmt.Sprintf("%s/width-%d", state.name, width), func(t *testing.T) {
				m := state.model
				m.width = width
				doc := m.detailDocument()
				for i, line := range doc.Lines {
					if got := ansi.StringWidth(line); got > max(width, 0) {
						t.Errorf("document line %d width = %d, budget %d: %q", i, got, width, line)
					}
				}
				for i, row := range doc.LinkRows {
					if row.Line < 0 || row.Line >= len(doc.Lines) {
						t.Errorf("Link row %d points outside %d document lines: %+v", i, len(doc.Lines), row)
					}
				}

				const bodyHeight = 4
				rendered := strings.Split(renderDetailBody(doc.Lines, 0, bodyHeight, width), "\n")
				if len(rendered) != bodyHeight {
					t.Fatalf("rendered body height = %d, want %d", len(rendered), bodyHeight)
				}
				for i, line := range rendered {
					if got := ansi.StringWidth(line); got > max(width, 0) {
						t.Errorf("rendered line %d width = %d, budget %d", i, got, width)
					}
				}
			})
		}
	}
}

func TestDetailMarkdownCacheSurvivesHeartbeatsAndScrolling(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	m := detailModel(t)
	m.width, m.height, m.ready = 48, 14, true
	m.detail.input.Detail.Description = "## CACHE DESCRIPTION\n\n" + strings.Repeat("long description words ", 20)
	m.detail.input.Detail.Comments = []model.Comment{
		{Author: model.User{Login: "ann"}, Body: "CACHE COMMENT " + strings.Repeat("comment words ", 12)},
		{Author: model.User{Login: "bob"}, Body: "SECOND CACHE COMMENT"},
	}
	m = m.invalidateDetailMarkdownSections().reconcileDetail(true)
	if !m.detail.markdownSections.valid {
		t.Fatal("loaded Detail reconciliation did not populate the Markdown cache")
	}

	rerendered := errors.New("Markdown was rendered again")
	m.markdown.description = markdownRenderer{err: rerendered}
	m.markdown.comment = markdownRenderer{err: rerendered}
	messages := []tea.Msg{
		heartbeatMsg(time.Now()),
		heartbeatMsg(time.Now().Add(time.Second)),
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyPgDown},
		tea.KeyPressMsg{Code: tea.KeyUp},
	}
	for i, message := range messages {
		updated, _ := m.Update(message)
		m = updated.(Model)
		document := ansi.Strip(strings.Join(m.detailDocument().Lines, "\n"))
		frame := ansi.Strip(m.View().Content)
		for _, rendered := range []string{document, frame} {
			if strings.Contains(rendered, "Could not render Markdown:") || strings.Contains(rendered, rerendered.Error()) {
				t.Fatalf("step %d rerendered cached Markdown:\n%s", i, rendered)
			}
		}
		for _, want := range []string{"CACHE DESCRIPTION", "CACHE COMMENT", "SECOND CACHE COMMENT"} {
			if !strings.Contains(document, want) {
				t.Errorf("step %d document lost cached %q:\n%s", i, want, document)
			}
		}
	}
}

func TestDetailMarkdownCacheInvalidatesOnResizeThemeAndReread(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "")
	m := detailModel(t)
	m.width, m.height, m.ready = 42, 14, true
	m.detail.input.Detail.Description = "OLD CACHE PROSE " + strings.Repeat("wrap these words ", 12)
	m.detail.input.Detail.Comments = []model.Comment{{Author: model.User{Login: "ann"}, Body: "OLD CACHE COMMENT"}}
	m = m.invalidateDetailMarkdownSections().reconcileDetail(true)
	m = m.moveDetailLinkFocus(1)
	focus := m.detail.linkFocus
	narrowDescriptionLines := len(m.detail.markdownSections.description)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 88, Height: 14})
	m = updated.(Model)
	if !m.detail.markdownSections.valid || m.detail.markdownSections.width != 88 {
		t.Fatalf("resize cache = %+v, want valid width 88", m.detail.markdownSections)
	}
	if got := len(m.detail.markdownSections.description); got >= narrowDescriptionLines {
		t.Errorf("resize left description at %d lines, narrow rendering had %d", got, narrowDescriptionLines)
	}
	if !m.detail.hasLinkFocus || m.detail.linkFocus != focus {
		t.Errorf("resize changed Link focus from %+v to %+v", focus, m.detail.linkFocus)
	}
	if row, ok := detailLinkRowByIdentity(m.detailDocument(), focus); !ok ||
		row.Line < m.detail.offset || row.Line >= m.detail.offset+m.detailBodyHeight() {
		t.Errorf("resize did not reconcile retained Link focus into view: row=%+v offset=%d", row, m.detail.offset)
	}

	updated, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	m = updated.(Model)
	if !m.detail.markdownSections.valid || m.detail.markdownSections.theme != markdownLight {
		t.Fatalf("light-theme cache = %+v, want valid light rendering", m.detail.markdownSections)
	}
	lightDocument := strings.Join(m.detailDocument().Lines, "\n")
	if !strings.Contains(lightDocument, "\x1b[38;5;234m") || strings.Contains(lightDocument, "\x1b[38;5;252mOLD CACHE PROSE") {
		t.Errorf("theme invalidation did not recolor ordinary prose for light output: %q", lightDocument)
	}

	m.detailGeneration = 9
	m = m.onDetailFetched(detailFetchedMsg{
		generation: 9,
		id:         m.detail.ticket.ID,
		detail: model.Detail{
			Description: "NEW CACHE PROSE",
			Links: []model.Link{{
				Kind:   model.LinkRelates,
				Target: model.LinkTarget{ID: "NEW-2", Key: "NEW-2", Title: "New link geometry"},
			}},
		},
		caps: model.Capabilities{},
	})
	doc := m.detailDocument()
	body := ansi.Strip(strings.Join(doc.Lines, "\n"))
	for _, old := range []string{"OLD CACHE PROSE", "OLD CACHE COMMENT"} {
		if strings.Contains(body, old) {
			t.Errorf("successful reread retained stale %q:\n%s", old, body)
		}
	}
	if !strings.Contains(body, "NEW CACHE PROSE") || strings.Contains(body, "COMMENTS") {
		t.Errorf("successful reread did not rebuild description/comment capability:\n%s", body)
	}
	if len(doc.LinkRows) != 1 || doc.LinkRows[0].Link.Target.ID != "NEW-2" ||
		doc.LinkRows[0].Line < 0 || doc.LinkRows[0].Line >= len(doc.Lines) {
		t.Errorf("successful reread Link geometry = %+v, want one current row", doc.LinkRows)
	}
	if m.detail.hasLinkFocus {
		t.Errorf("successful reread retained focus for a removed Link: %+v", m.detail.linkFocus)
	}
}

func TestDetailDocumentDistinguishesDuplicateRelationships(t *testing.T) {
	base := model.Link{
		Kind:        model.LinkRelates,
		NativeLabel: "duplicates",
		Target:      model.LinkTarget{ID: "T-2", Key: "T-2", Title: "Second"},
	}
	links := []model.Link{
		base,
		base,
		base,
		{Kind: model.LinkBlocks, NativeLabel: base.NativeLabel, Target: base.Target},
		{Kind: base.Kind, NativeLabel: "is related to", Target: base.Target},
	}
	in := DetailInput{Capabilities: model.Capabilities{BlockingLinks: true}, Detail: model.Detail{Description: "body", Links: links}}
	doc := composeDetailDocument(in, 80, Styles{}, detailLinkIdentity{}, false)
	if len(doc.LinkRows) != len(links) {
		t.Fatalf("got %d Link rows, want %d", len(doc.LinkRows), len(links))
	}
	for i, want := range []int{0, 1, 2} {
		if got := doc.LinkRows[i].Identity.Occurrence; got != want {
			t.Errorf("duplicate %d occurrence = %d, want %d", i, got, want)
		}
	}
	for i := 1; i < len(doc.LinkRows); i++ {
		if doc.LinkRows[i].Identity == doc.LinkRows[0].Identity {
			t.Errorf("Link %d collapsed onto the first relationship identity", i)
		}
	}
	focused := composeDetailDocument(in, 80, Styles{}, doc.LinkRows[2].Identity, true)
	selected := 0
	for _, row := range focused.LinkRows {
		if strings.HasPrefix(ansi.Strip(focused.Lines[row.Line]), selectedMarker) {
			selected++
			if row.Identity != doc.LinkRows[2].Identity {
				t.Errorf("focus marker landed on %+v, want third duplicate %+v", row.Identity, doc.LinkRows[2].Identity)
			}
		}
	}
	if selected != 1 {
		t.Errorf("third exact duplicate rendered %d focus markers, want 1", selected)
	}

	bodyText := "https://tracker.example/T-2 [T-2](https://example.test) #115 \x1b]8;;https://example.test\x1b\\text\x1b]8;;\x1b\\"
	in.Detail.Description = bodyText
	in.Detail.Comments = []model.Comment{{Author: model.User{Login: "ann"}, Body: bodyText}}
	in.Detail.Links = nil
	in.Capabilities.Comments = true
	if got := composeDetailDocument(in, 80, Styles{}, detailLinkIdentity{}, false).LinkRows; len(got) != 0 {
		t.Errorf("description/comment text synthesized Link metadata: %+v", got)
	}
}

func TestDetailLinkFocusCyclesAcrossThreeExactDuplicates(t *testing.T) {
	m, _ := navigableDetailModel(t)
	duplicate := model.Link{
		Kind:        model.LinkRelates,
		NativeLabel: "duplicates",
		Target:      model.LinkTarget{ID: "DUP-1", Key: "DUP-1", Title: "same"},
	}
	m.detail.input.Detail.Links = []model.Link{duplicate, duplicate, duplicate}
	m.detail.input.Capabilities.BlockingLinks = true
	m = m.reconcileDetail(true)

	for want := range 3 {
		next, _ := m.onDetailKey(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(Model)
		if got := m.detail.linkFocus.Occurrence; got != want {
			t.Fatalf("Tab %d focused duplicate occurrence %d, want %d", want+1, got, want)
		}
	}
	next, _ := m.onDetailKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(Model)
	if got := m.detail.linkFocus.Occurrence; got != 0 {
		t.Errorf("forward wrap focused occurrence %d, want 0", got)
	}
	next, _ = m.onDetailKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if got := next.(Model).detail.linkFocus.Occurrence; got != 2 {
		t.Errorf("backward wrap focused occurrence %d, want 2", got)
	}
}

func TestDetailLinkFocusCyclesAcrossOneRelationship(t *testing.T) {
	m, _ := navigableDetailModel(t)
	m.detail.input.Detail.Links = m.detail.input.Detail.Links[:1]
	m = m.reconcileDetail(true)
	identity := m.detailDocument().LinkRows[0].Identity

	for _, keyMsg := range []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: tea.KeyTab},
		{Code: tea.KeyTab, Mod: tea.ModShift},
	} {
		next, _ := m.onDetailKey(keyMsg)
		m = next.(Model)
		if !m.detail.hasLinkFocus || m.detail.linkFocus != identity {
			t.Fatalf("one-Link cycle focused %+v, want %+v", m.detail.linkFocus, identity)
		}
	}
}

func TestDetailDocumentFocusGutterDoesNotMoveLayout(t *testing.T) {
	in := fixtureDetailInput(allCaps)
	unfocused := composeDetailDocument(in, 60, Styles{}, detailLinkIdentity{}, false)
	focus := unfocused.LinkRows[1].Identity
	focused := composeDetailDocument(in, 60, Styles{}, focus, true)

	if len(focused.Lines) != len(unfocused.Lines) {
		t.Fatalf("focus changed line count from %d to %d", len(unfocused.Lines), len(focused.Lines))
	}
	for i := range focused.Lines {
		got, want := ansi.Strip(focused.Lines[i]), ansi.Strip(unfocused.Lines[i])
		if i == focused.LinkRows[1].Line {
			if !strings.HasPrefix(got, selectedMarker) ||
				strings.TrimPrefix(got, selectedMarker) != strings.TrimPrefix(want, unselectedMarker) {
				t.Errorf("focused row = %q, unfocused = %q", got, want)
			}
		} else if got != want {
			t.Errorf("focus changed unrelated line %d: %q != %q", i, got, want)
		}
		if ansi.StringWidth(got) > 60 {
			t.Errorf("focused line %d is wider than the frame: %q", i, got)
		}
	}
}

func TestDetailLinkFocusCyclesWithoutOwningBodyScroll(t *testing.T) {
	m := detailModel(t)
	m.width, m.height = 60, 12
	m = m.reconcileDetail(true)
	if m.detail.hasLinkFocus {
		t.Fatal("a newly opened Detail started with a Link focused")
	}

	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	next, _ := m.onDetailKey(tab)
	m = next.(Model)
	first := m.detail.linkFocus
	if !m.detail.hasLinkFocus || first != m.detailDocument().LinkRows[0].Identity {
		t.Fatalf("Tab focus = %+v, want first Link", first)
	}

	for range len(m.detailDocument().LinkRows) {
		next, _ = m.onDetailKey(tab)
		m = next.(Model)
	}
	if m.detail.linkFocus != first {
		t.Errorf("Tab did not wrap to the first Link: %+v", m.detail.linkFocus)
	}

	m.detail.hasLinkFocus = false
	m.detail.linkFocus = detailLinkIdentity{}
	next, _ = m.onDetailKey(shiftTab)
	m = next.(Model)
	rows := m.detailDocument().LinkRows
	if m.detail.linkFocus != rows[len(rows)-1].Identity {
		t.Errorf("initial Shift-Tab focus = %+v, want last Link", m.detail.linkFocus)
	}

	focus := m.detail.linkFocus
	offset := m.detail.offset
	next, _ = m.onDetailKey(upKey)
	m = next.(Model)
	if m.detail.linkFocus != focus {
		t.Errorf("body scrolling moved Link focus from %+v to %+v", focus, m.detail.linkFocus)
	}
	if m.detail.offset > offset {
		t.Errorf("up moved body offset from %d to %d", offset, m.detail.offset)
	}
}

func TestEnsureDocumentLineVisibleMovesMinimally(t *testing.T) {
	tests := []struct {
		target, offset, count, height, want int
	}{
		{5, 3, 20, 5, 3},
		{2, 3, 20, 5, 2},
		{8, 3, 20, 5, 4},
		{19, 3, 20, 5, 15},
		{0, 99, 3, 10, 0},
	}
	for _, tt := range tests {
		if got := ensureDocumentLineVisible(tt.target, tt.offset, tt.count, tt.height); got != tt.want {
			t.Errorf("ensureDocumentLineVisible(%d, %d, %d, %d) = %d, want %d",
				tt.target, tt.offset, tt.count, tt.height, got, tt.want)
		}
	}
}
