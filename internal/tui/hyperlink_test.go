package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
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

func TestHyperlinkSeamsStripTerminalControlInjection(t *testing.T) {
	hostileURL := "https://safe.example/path\a\x1b]8;;https://evil.example/\a" +
		"\x1b]52;c;YXR0YWNr\x1b\\" + string(rune(0x9d)) + "52;c;YXR0YWNr" +
		string(rune(0x9c)) + "\r\nend"
	cleanURL := provider.SanitizeLine(hostileURL)
	if cleanURL == hostileURL {
		t.Fatal("hostile URL fixture did not require sanitizing")
	}

	linkInput := DetailInput{
		Capabilities: model.Capabilities{BlockingLinks: true},
		Detail: model.Detail{Links: []model.Link{{
			Kind: model.LinkBlockedBy, NativeLabel: "is blocked by",
			Target: model.LinkTarget{ID: "T-2", Key: "T-2", Title: "Target title", URL: hostileURL},
		}}},
	}
	linkLines, linkRows := linkDocument(linkInput, 500, Styles{}, detailLinkIdentity{}, false)
	lead := model.PullRequest{
		Number: 8, Title: "Lead", URL: hostileURL,
		Repository: "acme/widgets", State: model.PROpen,
	}

	tests := []struct {
		name   string
		raw    string
		scopes int
	}{
		{
			name: "list Ticket key",
			raw: rowLines([]Row{{Kind: RowTicket, Ticket: model.Ticket{
				ID: "T-1", Key: "T-1", Title: "A title", URL: hostileURL,
			}}}, 0, 7, 500, true, model.Capabilities{}, Styles{})[0],
			scopes: 1,
		},
		{
			name:   "Detail key and displayed header URL",
			raw:    headerIdentity(Header{Key: "T-1", Title: "A title", URL: hostileURL}, 500, Styles{}, true),
			scopes: 2,
		},
		{
			name:   "Link target key and title",
			raw:    linkLines[linkRows[0].Line],
			scopes: 2,
		},
		{
			name: "Trail breadcrumb",
			raw: renderBreadcrumbCrumbs([]detailBreadcrumbCrumb{{
				text: "T-1", url: hostileURL,
			}}, 500, Styles{}),
			scopes: 1,
		},
		{
			name: "lead pull request summary",
			raw: pullRequest(model.Ticket{
				Repository: "acme/widgets", PullRequests: []model.PullRequest{lead},
			}, model.Capabilities{PullRequests: true}, Styles{}),
			scopes: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBalancedHyperlink(t, tt.raw, cleanURL, tt.scopes)
			if strings.Contains(tt.raw, hyperlinkOpen("https://evil.example/")) ||
				strings.Contains(tt.raw, "\x1b]52;") {
				t.Errorf("terminal control payload escaped the intended URI: %q", tt.raw)
			}
			for _, forbidden := range []string{"\r", "\n", string(rune(0x9c)), string(rune(0x9d))} {
				if strings.Contains(tt.raw, forbidden) {
					t.Errorf("rendered hyperlink contains control %q: %q", forbidden, tt.raw)
				}
			}
			if got := strings.Count(tt.raw, "\x1b]"); got != tt.scopes*2 {
				t.Errorf("OSC sequence count = %d, want exactly %d OSC 8 open/reset sequences in %q",
					got, tt.scopes*2, tt.raw)
			}
			if got := strings.Count(tt.raw, "\a"); got != tt.scopes*2 {
				t.Errorf("OSC terminator count = %d, want exactly %d in %q", got, tt.scopes*2, tt.raw)
			}
		})
	}
}

func TestRenderHyperlinkSanitizesVisibleTextAndDropsControlOnlyURL(t *testing.T) {
	text := "visible\a\x1b]52;c;YXR0YWNr\x1b\\" + string(rune(0x9c)) + "\r\ntext"
	raw := renderHyperlink(lipgloss.NewStyle(), text, "\a\x1b\r"+string(rune(0x9c))+"\x7f")
	if strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\a") ||
		strings.Contains(raw, "\r") || strings.Contains(raw, string(rune(0x9c))) {
		t.Errorf("control-only URL or hostile visible text emitted terminal controls: %q", raw)
	}
	if got, want := raw, provider.SanitizeLine(text); got != want {
		t.Errorf("sanitized plain fragment = %q, want %q", got, want)
	}
}

func TestRenderHyperlinkNormalizesMalformedUTF8BeforeControlSanitizing(t *testing.T) {
	rawC1 := make([]byte, 0, 0x20)
	for b := 0x80; b <= 0x9f; b++ {
		rawC1 = append(rawC1, byte(b))
	}

	tests := []struct {
		name  string
		bytes []byte
	}{
		{name: "lone continuation", bytes: []byte{0x80}},
		{name: "raw C1 range", bytes: rawC1},
		{name: "truncated two-byte", bytes: []byte{0xc2}},
		{name: "truncated three-byte", bytes: []byte{0xe2, 0x82}},
		{name: "truncated four-byte", bytes: []byte{0xf0, 0x9f, 0x92}},
		{name: "overlong escape", bytes: []byte{0xc0, 0x9b}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := "界text-" + string(tt.bytes) + "-safe"
			url := "https://example.test/界/" + string(tt.bytes) + "/safe"
			wantText := provider.SanitizeLine(strings.ToValidUTF8(text, ""))
			wantURL := provider.SanitizeLine(strings.ToValidUTF8(url, ""))

			raw := renderHyperlink(lipgloss.NewStyle(), text, url)
			if !utf8.ValidString(raw) {
				t.Errorf("rendered hyperlink contains malformed UTF-8: % x", []byte(raw))
			}
			assertBalancedHyperlink(t, raw, wantURL, 1)
			if got := ansi.Strip(raw); got != wantText {
				t.Errorf("visible text = %q, want %q", got, wantText)
			}
			if !strings.Contains(raw, "界") {
				t.Errorf("valid Unicode was removed: %q", raw)
			}
		})
	}
}

func TestDirectOptionsAndDetailSourceCannotInjectRawC1OSC(t *testing.T) {
	rawOSC52 := string([]byte{0x9d}) + "52;c;YXR0YWNr\a"
	rawST := string([]byte{0x9c})
	hostileURL := "https://safe.example/界/" + rawOSC52 + rawST +
		"\x1b]8;;https://evil.example/\aend"
	cleanURL := provider.SanitizeLine(strings.ToValidUTF8(hostileURL, ""))

	tests := []struct {
		name          string
		open          OpenTicket
		detail        model.Detail
		caps          model.Capabilities
		visibleMarker string
		wantText      string
		wantScopes    int
	}{
		{
			name: "direct Open Ticket header",
			open: OpenTicket{Ticket: model.Ticket{
				ID: "ROOT", Key: "ROOT-" + rawOSC52 + "界", Title: "Root", URL: hostileURL,
			}},
			detail:        model.Detail{Description: "DIRECT OPEN READY"},
			visibleMarker: "DIRECT OPEN READY",
			wantText:      "ROOT-52;c;YXR0YWNr界",
			wantScopes:    2,
		},
		{
			name: "injectable DetailSource Link",
			open: OpenTicket{Ticket: model.Ticket{
				ID: "ROOT", Key: "ROOT", Title: "Root",
			}},
			detail: model.Detail{
				Description: "DETAIL SOURCE READY",
				Links: []model.Link{{
					Kind: model.LinkRelates, NativeLabel: "relates to",
					Target: model.LinkTarget{
						ID: "TARGET", Key: "TARGET-" + rawOSC52 + "界",
						Title: "Target-" + rawST + rawOSC52 + "界", URL: hostileURL,
					},
				}},
			},
			caps:          model.Capabilities{BlockingLinks: true},
			visibleMarker: "DETAIL SOURCE READY",
			wantText:      "TARGET-52;c;YXR0YWNr界",
			wantScopes:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			s := startWith(t, c, Options{
				Open: &tt.open,
				DetailSource: func(context.Context, model.TicketID) (model.Detail, model.Capabilities, error) {
					return tt.detail, tt.caps, nil
				},
				Interval: time.Minute,
				Now:      c.now,
			})
			s.waitFor(t, tt.visibleMarker)
			m, _ := s.finish(t)
			raw := m.View().Content

			if !utf8.ValidString(raw) {
				t.Errorf("frame contains malformed UTF-8: % x", []byte(raw))
			}
			assertBalancedHyperlink(t, raw, cleanURL, tt.wantScopes)
			if strings.Contains(raw, "\x1b]52;") || strings.Contains(raw, string([]byte{0x9d})) ||
				strings.Contains(raw, string([]byte{0x9c})) {
				t.Errorf("raw C1/OSC 52 payload reached frame: %q", raw)
			}
			if got := strings.Count(raw, "\x1b]"); got != tt.wantScopes*2 {
				t.Errorf("OSC sequence count = %d, want %d intended OSC 8 open/resets", got, tt.wantScopes*2)
			}
			if got := strings.Count(raw, "\a"); got != tt.wantScopes*2 {
				t.Errorf("OSC terminator count = %d, want %d", got, tt.wantScopes*2)
			}
			if !strings.Contains(ansi.Strip(raw), tt.wantText) {
				t.Errorf("sanitized visible text missing from frame: %q", ansi.Strip(raw))
			}
		})
	}
}

func TestLinkPlainTextFieldsCannotInjectTerminalControls(t *testing.T) {
	hostile := func(marker string) string {
		return "visible-" + marker + "\x00\t\n\r\x7f " +
			"\x1b]52;c;YXR0YWNr\a " +
			"\x1b]8;;https://evil.example/" + marker + "\x1b\\scope\x1b]8;;\x1b\\ " +
			string([]byte{0x9d}) + "52;c;c1" + string([]byte{0x9c, 0xff}) + " café 東京"
	}
	label := hostile("label")
	status := hostile("status")
	url := "https://tracker.example/TARGET"
	lines, _ := linkDocument(DetailInput{
		Detail: model.Detail{Links: []model.Link{{
			Kind:        model.LinkRelates,
			NativeLabel: label,
			Target: model.LinkTarget{
				ID: "TARGET", Key: "TARGET", Title: "Target",
				URL: url, NativeStatus: status,
			},
		}}},
		Capabilities: model.Capabilities{BlockingLinks: true},
	}, 600, Styles{}, detailLinkIdentity{}, false)
	raw := strings.Join(lines, "\n")

	if !utf8.ValidString(raw) {
		t.Errorf("Link row contains malformed UTF-8: % x", []byte(raw))
	}
	assertBalancedHyperlink(t, raw, url, 2)
	if got := strings.Count(raw, "\x1b]8;"); got != 4 {
		t.Errorf("OSC 8 sequence count = %d, want four intended opens/resets", got)
	}
	if got := strings.Count(raw, "\x1b"); got != 4 {
		t.Errorf("ESC count = %d, want four intended OSC 8 sequences", got)
	}
	if got := strings.Count(raw, "\a"); got != 4 {
		t.Errorf("BEL count = %d, want four intended OSC 8 terminators", got)
	}
	for _, injected := range []string{
		"\x1b]52;", hyperlinkOpen("https://evil.example/"),
	} {
		if strings.Contains(raw, injected) {
			t.Errorf("Link row retained injected terminal bytes % x: %q", []byte(injected), raw)
		}
	}
	for _, control := range []rune{0x9c, 0x9d} {
		if strings.ContainsRune(raw, control) {
			t.Errorf("Link row retained C1 control U+%04X: %q", control, raw)
		}
	}
	visible := ansi.Strip(raw)
	safeLabel := sanitizeTerminalText(label)
	if !strings.Contains(visible, safeLabel) {
		t.Errorf("sanitized NativeLabel missing from %q", visible)
	}
	safeStatus := sanitizeTerminalText(status)
	if !strings.Contains(visible, "["+safeStatus+"]") {
		t.Errorf("sanitized Native Status missing from %q", visible)
	}
}

func TestFollowingHostileLinkSanitizesThinDetailSeat(t *testing.T) {
	hostile := func(marker string) string {
		return "visible-" + marker + "\x00\t\n\r\x7f " +
			"\x1b]52;c;YXR0YWNr\a " +
			"\x1b]8;;https://evil.example/" + marker + "\x1b\\scope\x1b]8;;\x1b\\ " +
			string([]byte{0x9d}) + "52;c;c1" + string([]byte{0x9c, 0xff}) + " café 東京"
	}
	title := hostile("title")
	status := hostile("status")
	url := "https://tracker.example/CHILD"

	m, _ := navigableDetailModel(t)
	m.width = 600
	m.help.SetWidth(m.width)
	m.detail.input.Detail.Links = []model.Link{{
		Kind: model.LinkRelates, NativeLabel: "relates to",
		Target: model.LinkTarget{
			ID: "CHILD", Key: "CHILD", Title: title, URL: url,
			Status: model.StatusTodo, NativeStatus: status,
		},
	}}
	m = focusLinkAt(m.reconcileDetail(true), 0)
	child, cmd := followFocused(t, m)
	if cmd == nil {
		t.Fatal("hostile Link follow issued no Detail fetch")
	}

	safeTitle := sanitizeTerminalText(title)
	safeStatus := sanitizeTerminalText(status)
	if child.detail.ticket.Title != safeTitle || child.detail.ticket.NativeStatus != safeStatus {
		t.Errorf("thin seat retained unsafe display fields: title=%q status=%q", child.detail.ticket.Title, child.detail.ticket.NativeStatus)
	}
	raw := child.View().Content
	if !utf8.ValidString(raw) {
		t.Errorf("followed Detail frame contains malformed UTF-8: % x", []byte(raw))
	}
	for _, injected := range []string{"\x1b]52;", hyperlinkOpen("https://evil.example/title"), hyperlinkOpen("https://evil.example/status")} {
		if strings.Contains(raw, injected) {
			t.Errorf("followed Detail retained injected terminal bytes % x: %q", []byte(injected), raw)
		}
	}
	for control := rune(0x80); control <= 0x9f; control++ {
		if strings.ContainsRune(raw, control) {
			t.Errorf("followed Detail retained C1 control U+%04X: %q", control, raw)
		}
	}
	visible := ansi.Strip(raw)
	if !strings.Contains(visible, safeTitle) || !strings.Contains(visible, "["+safeStatus+"]") {
		t.Errorf("followed Detail omitted sanitized title/status: %q", visible)
	}
	if !strings.Contains(visible, "café 東京") {
		t.Errorf("followed Detail removed valid Unicode: %q", visible)
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
