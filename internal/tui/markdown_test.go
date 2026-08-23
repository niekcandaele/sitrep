package tui

import (
	"image/color"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/niekcandaele/sitrep/internal/model"
)

func renderMarkdownTestBody(t *testing.T, source, ticketURL string, width int) (string, string) {
	t.Helper()
	t.Setenv("GLAMOUR_STYLE", "dark")
	lines, err := newMarkdownRenderer(width, markdownDark).render(source, ticketURL)
	if err != nil {
		t.Fatalf("render Markdown: %v", err)
	}
	raw := strings.Join(lines, "\n")
	return raw, ansi.Strip(raw)
}

func TestMarkdownRendererPresentsGFMAndFlattensDetails(t *testing.T) {
	source := "# Heading\n\n" +
		"- unordered\n1. ordered\n- [x] checked\n- [ ] unchecked\n\n" +
		"| Name | Value |\n| --- | --- |\n| alpha | beta |\n\n" +
		"```go\nfmt.Println(\"fenced\")\n```\n\n" +
		"Use `inline`, **bold**, *italic*, and ~~struck~~.\n\n" +
		"> quoted\n\n" +
		"[Docs](https://docs.example.test/path) and https://bare.example.test/path.\n\n" +
		"![diagram](https://images.example.test/diagram.png)\n\n" +
		"<details><summary>More</summary>Hidden body</details>"

	raw, visible := renderMarkdownTestBody(t, source, "", 100)
	for _, want := range []string{
		"Heading", "• unordered", "1. ordered", "[✓] checked", "[ ] unchecked",
		"Name", "Value", "alpha", "beta", "fmt.Println", "fenced", "inline",
		"bold", "italic", "struck", "quoted", "Docs", "https://docs.example.test/path",
		"https://bare.example.test/path", "Image: diagram →", "https://images.example.test/diagram.png",
		"More", "Hidden body",
	} {
		if !strings.Contains(visible, want) {
			t.Errorf("rendered Markdown does not contain %q:\n%s", want, visible)
		}
	}
	for _, sourceMarkup := range []string{"# Heading", "```go", "**bold**", "~~struck~~", "<details>", "</details>"} {
		if strings.Contains(visible, sourceMarkup) {
			t.Errorf("rendered presentation leaked source markup %q:\n%s", sourceMarkup, visible)
		}
	}
	if !strings.Contains(raw, "\x1b[") {
		t.Errorf("built-in dark style produced no ANSI styling: %q", raw)
	}
	assertAllHyperlinksBalanced(t, raw)
}

func TestDescriptionsAndCommentsShareMarkdownSemantics(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	in := DetailInput{
		Ticket:       DetailHeader{URL: "https://github.com/acme/widgets/issues/40"},
		Capabilities: model.Capabilities{Comments: true},
		Detail: model.Detail{
			Description: "## Description heading\n\n**description bold**",
			Comments:    []model.Comment{{Body: "### Comment heading\n\n_comment italic_"}},
		},
	}
	doc := composeDetailDocument(in, 80, Styles{}, detailLinkIdentity{}, false)
	visible := strings.Join(plainLines(doc.Lines), "\n")
	for _, want := range []string{"Description heading", "description bold", "Comment heading", "comment italic"} {
		if !strings.Contains(visible, want) {
			t.Errorf("Detail does not contain rendered %q:\n%s", want, visible)
		}
	}
	for _, rawSource := range []string{"**description bold**", "_comment italic_"} {
		if strings.Contains(visible, rawSource) {
			t.Errorf("Detail leaked Markdown source %q:\n%s", rawSource, visible)
		}
	}
}

func TestMarkdownDetailLinesStayCellBoundedAtExtremeWidths(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	in := DetailInput{
		Ticket:       DetailHeader{URL: "https://github.com/acme/widgets/issues/40"},
		Capabilities: model.Capabilities{Comments: true},
		Detail: model.Detail{
			Description: "# A heading\n\nA very long paragraph with 東京 and a-long-token-without-breaks-0123456789.",
			Comments:    []model.Comment{{Body: "- [x] a long comment with https://example.test/very/long/path"}},
		},
	}

	for _, width := range []int{0, 1, 2, 8, 20, 40, 80} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			lines := detailLines(in, width, Styles{})
			for index, line := range lines {
				if got := ansi.StringWidth(line); got > max(width, 0) {
					t.Errorf("width %d line %d occupies %d cells: %q", width, index, got, line)
				}
			}
			raw := strings.Join(lines, "\n")
			if strings.Contains(raw, "\x1b]52;") {
				t.Errorf("width %d emitted OSC 52: %q", width, raw)
			}
			assertAllHyperlinksBalanced(t, raw)
		})
	}
}

func TestMarkdownThemeLifecycleAndEnvironmentOverride(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "")
	model := New(t.Context(), Options{})
	if model.markdownTheme != markdownDark {
		t.Fatalf("initial Markdown theme = %q, want dark", model.markdownTheme)
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
	model = updated.(Model)
	if model.markdown.width != 72 || model.markdown.description.renderer == nil {
		t.Fatalf("resize did not build width-bound renderer: %+v", model.markdown)
	}
	updated, _ = model.Update(tea.BackgroundColorMsg{Color: color.White})
	model = updated.(Model)
	if model.markdownTheme != markdownLight || model.markdown.theme != markdownLight {
		t.Fatalf("light background left Markdown theme at %q / %q", model.markdownTheme, model.markdown.theme)
	}

	t.Setenv("GLAMOUR_STYLE", "notty")
	overridden := newMarkdownRenderer(72, markdownLight)
	lines, err := overridden.render("**plain override**", "")
	if err != nil {
		t.Fatalf("render environment override: %v", err)
	}
	if raw := strings.Join(lines, "\n"); strings.Contains(raw, "\x1b[") {
		t.Errorf("GLAMOUR_STYLE=notty did not override detected light style: %q", raw)
	}
}

func TestMarkdownRendererErrorsRemainVisibleWithSafeSourceFallback(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "this-style-does-not-exist")
	in := DetailInput{Detail: model.Detail{Description: "**still readable**\x1b]52;c;cHduZWQ=\a"}}
	doc := composeDetailDocument(in, 80, Styles{}, detailLinkIdentity{}, false)
	raw := strings.Join(doc.Lines, "\n")
	visible := ansi.Strip(raw)
	if !strings.Contains(visible, "Could not render Markdown:") || !strings.Contains(visible, "**still readable**") {
		t.Errorf("renderer error did not include an error and safe source fallback:\n%s", visible)
	}
	if strings.Contains(raw, "\x1b]52;") || strings.Contains(raw, "cHduZWQ=") {
		t.Errorf("unsafe source survived renderer fallback: %q", raw)
	}
}

func TestRewriteGitHubReferencesOnlyTouchesOrdinaryProse(t *testing.T) {
	ticketURL := "https://git.acme.test/space/widgets/issues/40?view=full#discussion"
	source := "See #12 with @alice.\n\n" +
		"`#13 @inline`\n\n" +
		"```text\n#14 @fenced\n```\n\n" +
		"    #15 @indented\n\n" +
		"[#16 @linked](https://example.test/#16)\n\n" +
		"![#17 @image](https://example.test/image.png)\n\n" +
		"<span data-ref=\"#18 @attribute\">#18 @html</span>\n\n" +
		"\\#19 \\@escaped dev@example.com https://example.test/#20/@url " +
		"word@joined path/@path colon:@colon dot.@dot plus+@plus dash-@dash " +
		"café@intl café@combining 東京@tokyo 東京#21 @alice東京"

	got := rewriteGitHubReferences(source, ticketURL)
	for _, want := range []string{
		"[#12](https://git.acme.test/space/widgets/issues/12)",
		"[@alice](https://git.acme.test/alice)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reference rewrite does not contain %q:\n%s", want, got)
		}
	}
	for _, unchanged := range []string{
		"`#13 @inline`", "#14 @fenced", "#15 @indented",
		"[#16 @linked](https://example.test/#16)",
		"![#17 @image](https://example.test/image.png)",
		"#18 @attribute", "#18 @html", "\\#19 \\@escaped",
		"dev@example.com", "https://example.test/#20/@url",
		"word@joined", "path/@path", "colon:@colon", "dot.@dot", "plus+@plus", "dash-@dash",
		"café@intl", "café@combining", "東京@tokyo", "東京#21", "@alice東京",
	} {
		if !strings.Contains(got, unchanged) {
			t.Errorf("reference rewrite changed protected Markdown %q:\n%s", unchanged, got)
		}
	}
}

func TestGitHubReferenceContextDoesNotGuessTrackerSemantics(t *testing.T) {
	source := "See #12 and @alice"
	for _, ticketURL := range []string{
		"", "https://tracker.example.test/acme/widgets/40", "https://github.com/acme/widgets/pull/40",
		"ftp://github.com/acme/widgets/issues/40", "https://github.com/acme/widgets/issues/not-a-number",
	} {
		if got := rewriteGitHubReferences(source, ticketURL); got != source {
			t.Errorf("URL %q rewrote unknown context to %q", ticketURL, got)
		}
	}
}

func TestGitHubReferenceBoundaryHelpers(t *testing.T) {
	t.Run("Ticket URL context", func(t *testing.T) {
		ctx, ok := githubContext("https://github.com/acme/%77idgets/issues/40")
		if !ok || ctx.owner != "acme" || ctx.repo != "widgets" {
			t.Fatalf("safe escaped path context = %+v, %t; want acme/widgets", ctx, ok)
		}
		if got := ctx.issueURL("12"); got != "https://github.com/acme/widgets/issues/12" {
			t.Errorf("issue URL = %q", got)
		}
		for _, rawURL := range []string{
			"https://github.com/acme%2Fcorp/widgets/issues/40",
			"https://github.com/%2E/widgets/issues/40",
			"https://github.com/%2E%2E/widgets/issues/40",
			"https://reader@github.com/acme/widgets/issues/40",
		} {
			if _, ok := githubContext(rawURL); ok {
				t.Errorf("githubContext(%q) accepted an unsafe context", rawURL)
			}
		}
	})

	t.Run("decimal issue references", func(t *testing.T) {
		for _, tt := range []struct {
			value string
			want  bool
		}{
			{value: "0"},
			{value: "01"},
			{value: "18446744073709551615", want: true},
			{value: "18446744073709551616"},
		} {
			if got := decimalReference(tt.value); got != tt.want {
				t.Errorf("decimalReference(%q) = %t, want %t", tt.value, got, tt.want)
			}
		}
	})

	t.Run("backslash parity", func(t *testing.T) {
		for count := 1; count <= 4; count++ {
			source := strings.Repeat("\\", count) + "#12"
			if got, want := escapedAt(source, count), count%2 == 1; got != want {
				t.Errorf("escapedAt(%q, %d) = %t, want %t", source, count, got, want)
			}
		}
	})

	t.Run("username grammar", func(t *testing.T) {
		for _, tt := range []struct {
			username string
			want     bool
		}{
			{username: strings.Repeat("a", 39), want: true},
			{username: strings.Repeat("a", 40)},
			{username: "-alice"},
			{username: "alice-"},
			{username: "ali--ce"},
		} {
			if got := validGitHubUsername(tt.username); got != tt.want {
				t.Errorf("validGitHubUsername(%q) = %t, want %t", tt.username, got, tt.want)
			}
		}
	})
}

func TestRewriteGitHubReferenceBoundaries(t *testing.T) {
	const ticketURL = "https://github.com/acme/widgets/issues/40"
	issueTarget := func(number string) string {
		return "[#" + number + "](https://github.com/acme/widgets/issues/" + number + ")"
	}
	userTarget := func(username string) string {
		return "[@" + username + "](https://github.com/" + username + ")"
	}
	validUser := strings.Repeat("a", 39)
	tests := []struct {
		name      string
		source    string
		context   string
		want      string
		unchanged bool
	}{
		{name: "safe escaped path", source: "#12", context: "https://github.com/acme/%77idgets/issues/40", want: issueTarget("12")},
		{name: "escaped slash context", source: "#12", context: "https://github.com/acme%2Fcorp/widgets/issues/40", unchanged: true},
		{name: "escaped dot context", source: "#12", context: "https://github.com/%2E/widgets/issues/40", unchanged: true},
		{name: "escaped dot-dot context", source: "#12", context: "https://github.com/%2E%2E/widgets/issues/40", unchanged: true},
		{name: "userinfo context", source: "#12", context: "https://reader@github.com/acme/widgets/issues/40", unchanged: true},
		{name: "issue zero", source: "#0", unchanged: true},
		{name: "issue leading zero", source: "#01", unchanged: true},
		{name: "issue uint64 max", source: "#18446744073709551615", want: issueTarget("18446744073709551615")},
		{name: "issue uint64 overflow", source: "#18446744073709551616", unchanged: true},
		{name: "one backslash", source: strings.Repeat("\\", 1) + "#12", unchanged: true},
		{name: "two backslashes", source: strings.Repeat("\\", 2) + "#12", want: strings.Repeat("\\", 2) + issueTarget("12")},
		{name: "three backslashes", source: strings.Repeat("\\", 3) + "#12", unchanged: true},
		{name: "four backslashes", source: strings.Repeat("\\", 4) + "#12", want: strings.Repeat("\\", 4) + issueTarget("12")},
		{name: "username 39 characters", source: "@" + validUser, want: userTarget(validUser)},
		{name: "username 40 characters", source: "@" + strings.Repeat("a", 40), unchanged: true},
		{name: "username leading hyphen", source: "@-alice", unchanged: true},
		{name: "username trailing hyphen", source: "@alice-", unchanged: true},
		{name: "username double hyphen", source: "@ali--ce", unchanged: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := tt.context
			if context == "" {
				context = ticketURL
			}
			got := rewriteGitHubReferences(tt.source, context)
			want := tt.want
			if tt.unchanged {
				want = tt.source
			}
			if got != want {
				t.Errorf("rewriteGitHubReferences(%q, %q) = %q, want %q", tt.source, context, got, want)
			}
			if tt.unchanged && strings.Contains(got, "](https://") {
				t.Errorf("rejected source synthesized a hyperlink target: %q", got)
			}
		})
	}
}

func TestMarkdownReferencesAreTerminalLinksNotTrailLinks(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	in := DetailInput{
		Ticket: DetailHeader{URL: "https://github.com/acme/widgets/issues/40"},
		Detail: model.Detail{Description: "See #12, ask @alice, or read [docs](https://docs.example.test)."},
	}
	doc := composeDetailDocument(in, 100, Styles{}, detailLinkIdentity{}, false)
	if len(doc.LinkRows) != 0 {
		t.Fatalf("body hyperlinks synthesized %d explicit Link rows: %+v", len(doc.LinkRows), doc.LinkRows)
	}
	raw := strings.Join(doc.Lines, "\n")
	for _, target := range []string{
		"https://github.com/acme/widgets/issues/12", "https://github.com/alice", "https://docs.example.test",
	} {
		if !strings.Contains(raw, target) {
			t.Errorf("rendered body does not carry expected OSC 8 target %q: %q", target, raw)
		}
	}
	assertAllHyperlinksBalanced(t, raw)
}

func assertAllHyperlinksBalanced(t *testing.T, raw string) {
	t.Helper()
	resets := strings.Count(raw, ansi.ResetHyperlink())
	sequences := strings.Count(raw, "\x1b]8;")
	opens := sequences - resets
	if opens != resets {
		t.Errorf("OSC 8 scopes are not balanced: %d opens, %d resets in %q", opens, resets, raw)
	}
}
