package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/render/plain"
)

func hyperlinkOpen(url string) string { return ansi.SetHyperlink(url) }

func assertBalancedHyperlink(t *testing.T, raw, url string, want int) {
	t.Helper()
	if got := strings.Count(raw, hyperlinkOpen(url)); got != want {
		t.Errorf("hyperlink open count = %d, want %d in %q", got, want, raw)
	}
	if got := strings.Count(raw, ansi.ResetHyperlink()); got != want {
		t.Errorf("hyperlink reset count = %d, want %d in %q", got, want, raw)
	}
}

func TestRenderHyperlinkUsesLipGlossScopeWithoutMutatingStyle(t *testing.T) {
	style := lipgloss.NewStyle().Bold(true)
	url := "https://example.test/T-1"
	linked := renderHyperlink(style, "T-1", url)
	plainText := style.Render("ordinary")

	assertBalancedHyperlink(t, linked, url, 1)
	if !strings.Contains(linked, hyperlinkOpen(url)+style.Render("T-1")+ansi.ResetHyperlink()) {
		t.Errorf("linked fragment has wrong ordering: %q", linked)
	}
	if strings.Contains(plainText, "\x1b]8;") {
		t.Errorf("style was mutated by hyperlink helper: %q", plainText)
	}
	if got := renderHyperlink(style, "T-1", ""); strings.Contains(got, "\x1b]8;") {
		t.Errorf("empty URL emitted OSC 8: %q", got)
	}
}

func TestListTicketKeyHyperlinkExcludesMarkerPaddingAndTitle(t *testing.T) {
	url := "https://example.test/T-1"
	rows := []Row{{Kind: RowTicket, Ticket: model.Ticket{
		ID: "T-1", Key: "T-1", Title: "A title", URL: url,
	}}}
	raw := rowLines(rows, 0, 7, 40, true, model.Capabilities{}, Styles{})[0]
	assertBalancedHyperlink(t, raw, url, 1)
	scope := hyperlinkOpen(url) + "T-1" + ansi.ResetHyperlink()
	if !strings.Contains(raw, selectedMarker+scope+"    A title") {
		t.Errorf("Ticket key scope includes marker, padding, or title: %q", raw)
	}
	if got := ansi.Strip(raw); got != "▸ T-1    A title" {
		t.Errorf("visible row = %q", got)
	}
}

func TestDetailIdentityAndDisplayedURLHaveSeparateHyperlinks(t *testing.T) {
	url := "https://example.test/T-1"
	raw := headerIdentity(Header{Key: "T-1", Title: "A title", URL: url}, 80, Styles{}, true)
	assertBalancedHyperlink(t, raw, url, 2)
	keyScope := hyperlinkOpen(url) + "T-1" + ansi.ResetHyperlink()
	urlScope := hyperlinkOpen(url) + url + ansi.ResetHyperlink()
	if !strings.Contains(raw, keyScope+"  A title") || !strings.HasSuffix(raw, urlScope) {
		t.Errorf("Detail identity scopes are not isolated: %q", raw)
	}

	listHeader := headerIdentity(Header{Key: "ROOT", Title: "Watchlist", URL: url}, 80, Styles{})
	if strings.Count(listHeader, hyperlinkOpen(url)) != 1 || !strings.HasSuffix(listHeader, urlScope) {
		t.Errorf("list header should link only its displayed URL: %q", listHeader)
	}
}

func TestLinkTargetKeyAndTitleUseDistinctScopes(t *testing.T) {
	url := "https://example.test/T-2"
	in := DetailInput{
		Capabilities: model.Capabilities{BlockingLinks: true},
		Detail: model.Detail{Links: []model.Link{{
			Kind: model.LinkBlockedBy, NativeLabel: "is blocked by",
			Target: model.LinkTarget{
				ID: "T-2", Key: "T-2", Title: "Target title", URL: url, NativeStatus: "open",
			},
		}}},
	}
	lines, rows := linkDocument(in, 60, Styles{}, detailLinkIdentity{}, false)
	raw := lines[rows[0].Line]
	assertBalancedHyperlink(t, raw, url, 2)
	key := hyperlinkOpen(url) + "T-2" + ansi.ResetHyperlink()
	title := hyperlinkOpen(url) + "Target title" + ansi.ResetHyperlink()
	if !strings.Contains(raw, key) || !strings.Contains(raw, title) {
		t.Errorf("Link row lacks distinct key/title scopes: %q", raw)
	}
	if strings.Index(raw, ansi.ResetHyperlink()) > strings.Index(raw, title) ||
		!strings.HasSuffix(raw, "[open]") {
		t.Errorf("Link padding/label/status leaked into a scope: %q", raw)
	}
}

func TestBreadcrumbHyperlinksKeepDividersAndCollapsePlain(t *testing.T) {
	rootURL := "https://example.test/root"
	oldURL := "https://example.test/old"
	newURL := "https://example.test/new"
	crumbs := []detailBreadcrumbCrumb{
		{text: "ROOT", url: rootURL},
		{text: "OLD", url: oldURL},
		{text: "NEW", url: newURL},
	}
	full := renderBreadcrumbCrumbs(crumbs, 80, Styles{})
	for _, url := range []string{rootURL, oldURL, newURL} {
		if got := strings.Count(full, hyperlinkOpen(url)); got != 1 {
			t.Errorf("breadcrumb open count for %s = %d", url, got)
		}
	}
	if got := strings.Count(full, ansi.ResetHyperlink()); got != 3 {
		t.Errorf("breadcrumb reset count = %d, want 3", got)
	}
	rootReset := strings.Index(full, ansi.ResetHyperlink())
	firstDivider := strings.Index(full, breadcrumbDivider)
	if rootReset < 0 || firstDivider < rootReset || ansi.Strip(full) != "ROOT › OLD › NEW" {
		t.Errorf("full breadcrumb divider inherited a scope: %q", full)
	}

	collapsed := renderBreadcrumbCrumbs(crumbs, 7, Styles{})
	assertBalancedHyperlink(t, collapsed, newURL, 1)
	if strings.Contains(collapsed[:strings.Index(collapsed, hyperlinkOpen(newURL))], "\x1b]8;") ||
		ansi.Strip(collapsed) != "… › NEW" {
		t.Errorf("collapse marker inherited a hyperlink: %q", collapsed)
	}
}

func TestPullRequestHyperlinkEndsBeforeAdditionalCount(t *testing.T) {
	lead := model.PullRequest{
		Number: 8, Title: "Lead", URL: "https://example.test/pr/8",
		Repository: "acme/widgets", State: model.PROpen,
	}
	ticket := model.Ticket{Repository: "acme/widgets", PullRequests: []model.PullRequest{lead}}
	caps := model.Capabilities{PullRequests: true}

	one := pullRequest(ticket, caps, Styles{})
	assertBalancedHyperlink(t, one, lead.URL, 1)
	wantLead := plain.PullRequestSummary(lead, ticket.Repository)
	if ansi.Strip(one) != wantLead {
		t.Errorf("one PR visible text = %q, want %q", ansi.Strip(one), wantLead)
	}

	ticket.PullRequests = append(ticket.PullRequests, model.PullRequest{Number: 9})
	many := pullRequest(ticket, caps, Styles{})
	assertBalancedHyperlink(t, many, lead.URL, 1)
	reset := strings.Index(many, ansi.ResetHyperlink())
	suffix := strings.Index(many, " +1 more")
	if reset < 0 || suffix < 0 || reset > suffix {
		t.Errorf("PR count suffix is inside lead scope: %q", many)
	}
}

func TestOSC8TruncationRemainsBalancedWidthBoundedAndDoesNotBleed(t *testing.T) {
	url := "https://example.test/wide"
	visible := "界面-abcdefghij"
	raw := renderHyperlink(lipgloss.NewStyle(), visible, url)
	for width := 1; width <= lipgloss.Width(visible); width++ {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			got := truncateLine(raw, width)
			assertBalancedHyperlink(t, got, url, 1)
			if ansi.StringWidth(got) > width {
				t.Errorf("truncated scope width = %d, budget %d: %q", ansi.StringWidth(got), width, got)
			}
			if !strings.HasSuffix(got, ansi.ResetHyperlink()) {
				t.Errorf("truncated scope is not closed: %q", got)
			}
		})
	}

	block := renderDetailBody([]string{raw, "ordinary next line"}, 0, 2, 8)
	newline := strings.Index(block, "\n")
	reset := strings.LastIndex(block[:newline], ansi.ResetHyperlink())
	if reset < 0 || strings.Contains(block[newline+1:], "\x1b]8;") {
		t.Errorf("hyperlink bled across a body newline: %q", block)
	}
}

func TestHyperlinksDoNotChangeVisibleCellGeometry(t *testing.T) {
	url := "https://example.test/geometry"
	style := DefaultStyles(true).TicketKey
	linked := renderHyperlink(style, "界面-1", url)
	unlinked := renderHyperlink(style, "界面-1", "")
	if ansi.Strip(linked) != ansi.Strip(unlinked) || ansi.StringWidth(linked) != ansi.StringWidth(unlinked) {
		t.Errorf("OSC 8 changed visible output: linked=%q unlinked=%q", linked, unlinked)
	}
}
