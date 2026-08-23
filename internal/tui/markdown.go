package tui

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	goldmarktext "github.com/yuin/goldmark/text"
)

type markdownTheme string

const (
	markdownDark  markdownTheme = "dark"
	markdownLight markdownTheme = "light"
)

type markdownRenderer struct {
	renderer *glamour.TermRenderer
	err      error
}

type detailMarkdownRenderers struct {
	width       int
	theme       markdownTheme
	description markdownRenderer
	comment     markdownRenderer
}

func newDetailMarkdownRenderers(width int, theme markdownTheme) detailMarkdownRenderers {
	return detailMarkdownRenderers{
		width:       width,
		theme:       theme,
		description: newMarkdownRenderer(width, theme),
		comment:     newMarkdownRenderer(width-lipgloss.Width(commentIndent), theme),
	}
}

func (m Model) rebuildMarkdownRenderers() Model {
	m.markdown = newDetailMarkdownRenderers(m.width, m.markdownTheme)
	return m
}

func (m Model) effectiveMarkdownRenderers() detailMarkdownRenderers {
	if m.markdown.width == m.width && m.markdown.theme == m.markdownTheme {
		return m.markdown
	}
	// Focused model tests and embedding callers may assign geometry directly
	// instead of sending WindowSizeMsg. Keep that compatibility path correct;
	// the live Bubble Tea path rebuilds once per resize and reuses its renderers.
	return newDetailMarkdownRenderers(m.width, m.markdownTheme)
}

func newMarkdownRenderer(width int, theme markdownTheme) markdownRenderer {
	if width <= 0 {
		return markdownRenderer{err: fmt.Errorf("markdown rendering requires a positive width (got %d)", width)}
	}

	options := []glamour.TermRendererOption{
		glamour.WithWordWrap(width),
		glamour.WithStandardStyle(string(theme)),
	}
	if style := os.Getenv("GLAMOUR_STYLE"); style != "" {
		// The environment option must come last so an explicit user override wins
		// over the light/dark style selected from Bubble Tea's existing query.
		options = append(options, glamour.WithEnvironmentConfig())
	}
	renderer, err := glamour.NewTermRenderer(options...)
	return markdownRenderer{renderer: renderer, err: err}
}

func (r markdownRenderer) render(source, ticketURL string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.renderer == nil {
		return nil, fmt.Errorf("markdown renderer is not configured")
	}
	rendered, err := r.renderer.Render(rewriteGitHubReferences(source, ticketURL))
	if err != nil {
		return nil, err
	}
	return splitMarkdownLines(rendered), nil
}

func splitMarkdownLines(rendered string) []string {
	// Built-in Glamour document styles own one outer newline on each side. Remove
	// only those delimiters; broad whitespace trimming would corrupt code blocks
	// and style padding.
	rendered = strings.TrimPrefix(rendered, "\n")
	rendered = strings.TrimSuffix(rendered, "\n")
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

type githubReferenceContext struct {
	scheme string
	host   string
	owner  string
	repo   string
}

func githubContext(ticketURL string) (githubReferenceContext, bool) {
	u, err := url.Parse(ticketURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return githubReferenceContext{}, false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "issues" || !validGitHubPathPart(parts[0]) ||
		!validGitHubPathPart(parts[1]) || !decimalReference(parts[3]) {
		return githubReferenceContext{}, false
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return githubReferenceContext{}, false
	}
	repo, err := url.PathUnescape(parts[1])
	if err != nil {
		return githubReferenceContext{}, false
	}
	return githubReferenceContext{scheme: u.Scheme, host: u.Host, owner: owner, repo: repo}, true
}

func validGitHubPathPart(part string) bool {
	if part == "" || strings.Contains(strings.ToLower(part), "%2f") {
		return false
	}
	decoded, err := url.PathUnescape(part)
	if err != nil || decoded == "." || decoded == ".." {
		return false
	}
	for _, r := range decoded {
		if !isASCIIAlphaNumeric(r) && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func (c githubReferenceContext) issueURL(number string) string {
	return (&url.URL{Scheme: c.scheme, Host: c.host,
		Path: path.Join("/", c.owner, c.repo, "issues", number)}).String()
}

func (c githubReferenceContext) userURL(username string) string {
	return (&url.URL{Scheme: c.scheme, Host: c.host, Path: "/" + username}).String()
}

type sourceReplacement struct {
	start int
	stop  int
	text  string
}

func rewriteGitHubReferences(source, ticketURL string) string {
	ctx, ok := githubContext(ticketURL)
	if !ok || source == "" {
		return source
	}

	markdown := goldmark.New(goldmark.WithExtensions(extension.GFM))
	document := markdown.Parser().Parse(goldmarktext.NewReader([]byte(source)))
	replacements := make([]sourceReplacement, 0)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindLink, ast.KindImage, ast.KindAutoLink, ast.KindCodeSpan,
			ast.KindRawHTML, ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindHTMLBlock:
			return ast.WalkSkipChildren, nil
		}
		textNode, ok := node.(*ast.Text)
		if !ok || textNode.IsRaw() || containsRawHTML(textNode.Parent()) {
			return ast.WalkContinue, nil
		}
		segment := textNode.Segment
		replacements = append(replacements,
			referenceReplacements(source, segment.Start, segment.Stop, ctx)...)
		return ast.WalkContinue, nil
	})
	if len(replacements) == 0 {
		return source
	}

	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start > replacements[j].start })
	result := source
	for _, replacement := range replacements {
		result = result[:replacement.start] + replacement.text + result[replacement.stop:]
	}
	return result
}

func containsRawHTML(parent ast.Node) bool {
	if parent == nil {
		return false
	}
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Kind() == ast.KindRawHTML {
			return true
		}
	}
	return false
}

func referenceReplacements(source string, start, stop int, ctx githubReferenceContext) []sourceReplacement {
	var replacements []sourceReplacement
	for i := start; i < stop; {
		switch source[i] {
		case '#':
			end := i + 1
			for end < stop && source[end] >= '0' && source[end] <= '9' {
				end++
			}
			if end > i+1 && decimalReference(source[i+1:end]) && referenceStart(source, start, i) &&
				referenceEnd(source, end, stop) {
				label := source[i:end]
				replacements = append(replacements, sourceReplacement{
					start: i, stop: end, text: "[" + label + "](" + ctx.issueURL(source[i+1:end]) + ")",
				})
				i = end
				continue
			}
		case '@':
			end := i + 1
			for end < stop && isGitHubUsernameByte(source[end]) {
				end++
			}
			username := source[i+1 : end]
			if validGitHubUsername(username) && mentionStart(source, start, i) && referenceEnd(source, end, stop) {
				label := source[i:end]
				replacements = append(replacements, sourceReplacement{
					start: i, stop: end, text: "[" + label + "](" + ctx.userURL(username) + ")",
				})
				i = end
				continue
			}
		}
		i++
	}
	return replacements
}

func decimalReference(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func referenceStart(source string, nodeStart, at int) bool {
	if escapedAt(source, at) {
		return false
	}
	if at == nodeStart {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(source[nodeStart:at])
	return !isWordRune(previous) && previous != '/' && previous != ':'
}

func mentionStart(source string, nodeStart, at int) bool {
	if !referenceStart(source, nodeStart, at) {
		return false
	}
	if at == nodeStart {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(source[nodeStart:at])
	return previous != '.' && previous != '+' && previous != '-'
}

func referenceEnd(source string, at, nodeStop int) bool {
	if at == nodeStop {
		return true
	}
	next, _ := utf8.DecodeRuneInString(source[at:nodeStop])
	return !isWordRune(next)
}

func escapedAt(source string, at int) bool {
	backslashes := 0
	for i := at - 1; i >= 0 && source[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func validGitHubUsername(username string) bool {
	if len(username) == 0 || len(username) > 39 || username[0] == '-' || username[len(username)-1] == '-' ||
		strings.Contains(username, "--") {
		return false
	}
	for i := range len(username) {
		if !isGitHubUsernameByte(username[i]) {
			return false
		}
	}
	return true
}

func isGitHubUsernameByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-'
}

func isWordRune(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value)
}

func isASCIIAlphaNumeric(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}
