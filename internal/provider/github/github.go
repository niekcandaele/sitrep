// Package github is sitrep's GitHub Tracker driver: it turns an issue with
// sub-issues into a normalized Epic with its Tickets.
//
// The driver speaks GraphQL over plain net/http. A GraphQL client is a POST
// with a JSON body, and hand-rolling it keeps sitrep free of runtime
// dependencies and keeps the test seam at the HTTP boundary, where recorded
// payloads can be replayed by a local server.
//
// Everything here is read-only (ADR-0002): every document sent is a `query`.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// defaultHost is github.com; anything else is a GitHub Enterprise host.
const defaultHost = "github.com"

// maxPages bounds the sub-issue pagination loop at 100 Tickets a page. An epic
// with five thousand children is a bug somewhere, not a situation report, and
// an unbounded loop against a paging API is how a polling tool hangs.
const maxPages = 50

// requestTimeout is the default per-request budget when the caller supplies no
// HTTP client of its own.
const requestTimeout = 30 * time.Second

// Provider is the GitHub Tracker driver. Construct it with New; the zero value
// is not usable.
type Provider struct {
	host        string
	endpoint    string
	httpClient  *http.Client
	tokenSource TokenSource
	userAgent   string

	tokenOnce sync.Once
	token     string
	tokenErr  error
}

// Option configures a Provider.
type Option func(*Provider)

// New returns a GitHub Provider reading from host, which is "github.com" or a
// GitHub Enterprise host. Construction is cheap and free of side effects: no
// token is resolved and no request is made until the first fetch, so
// `sitrep --help` never shells out to gh.
func New(host string, opts ...Option) *Provider {
	if host == "" {
		host = defaultHost
	}
	p := &Provider{
		host:        host,
		endpoint:    endpointFor(host),
		httpClient:  &http.Client{Timeout: requestTimeout},
		tokenSource: DefaultTokenSource,
		userAgent:   buildinfo.Name + "/" + buildinfo.Version,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// WithHTTPClient replaces the HTTP client. The default has a 30s timeout.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) {
		if c != nil {
			p.httpClient = c
		}
	}
}

// WithEndpoint replaces the GraphQL endpoint derived from the host. Tests point
// it at a local server replaying recorded payloads; a host with a non-default
// API mount needs it too.
func WithEndpoint(rawurl string) Option {
	return func(p *Provider) {
		if rawurl != "" {
			p.endpoint = rawurl
		}
	}
}

// WithTokenSource replaces the token discovery chain. The default is
// DefaultTokenSource.
func WithTokenSource(ts TokenSource) Option {
	return func(p *Provider) {
		if ts != nil {
			p.tokenSource = ts
		}
	}
}

// WithUserAgent replaces the User-Agent header sent with every request.
func WithUserAgent(ua string) Option {
	return func(p *Provider) {
		if ua != "" {
			p.userAgent = ua
		}
	}
}

// endpointFor derives the GraphQL endpoint from a host: github.com has its own
// API domain, and every GitHub Enterprise host mounts the API under itself.
func endpointFor(host string) string {
	if host == defaultHost {
		return "https://api.github.com/graphql"
	}
	return "https://" + host + "/api/graphql"
}

// Name returns "github".
func (p *Provider) Name() string { return "github" }

// Capabilities declares what this driver actually returns today, not what
// GitHub is theoretically capable of. A capability declared ahead of its data
// renders an empty section, so flip a flag in the same change that ships the
// data behind it.
func (p *Provider) Capabilities() model.Capabilities {
	return model.Capabilities{
		Hierarchy:     true, // sub-issues are how an Epic is assembled
		BlockingLinks: true, // blockedBy / blocking on the detail query
		Comments:      true, // comments on the detail query
		PullRequests:  true, // closedByPullRequestsReferences on the epic query
	}
}

// FetchEpic returns the Epic named by r and every one of its sub-issues,
// following sub-issue pagination to the last page. Cross-repo children need no
// special handling: they arrive as ordinary nodes carrying their own
// repository, and are attributed to it.
//
// Only one level of the sub-issue graph is fetched — sub-issues of sub-issues
// are not expanded — so ParentID stays empty and every Ticket hangs directly
// off the Epic.
func (p *Provider) FetchEpic(ctx context.Context, r ref.Ref) (model.EpicSnapshot, error) {
	if err := checkRef(r); err != nil {
		return model.EpicSnapshot{}, err
	}

	var (
		snap     model.EpicSnapshot
		epicRepo string
		cursor   string
		haveEpic bool
	)
	snap.Tickets = []model.Ticket{}

	for page := 0; ; page++ {
		if page >= maxPages {
			return model.EpicSnapshot{}, fmt.Errorf(
				"github: %s has more than %d sub-issues; refusing to keep paging",
				refKey(r), maxPages*100)
		}

		issue, err := p.fetchPage(ctx, r, cursor)
		if err != nil {
			return model.EpicSnapshot{}, err
		}

		if !haveEpic {
			// The epic's own fields repeat on every page; the first page wins so
			// a title edited mid-fetch cannot produce a half-updated snapshot.
			snap.Epic = newEpic(issue)
			epicRepo = issue.Repository.NameWithOwner
			// The fetched issue's own parent, for an Epic Ref that turns out to
			// name a plain Ticket. Reporting it is all this driver does about it:
			// which screen that opens is internal/cli's decision.
			snap.Parent = newParent(issue.Parent, epicRepo)
			haveEpic = true
		}

		for _, node := range issue.SubIssues.Nodes {
			snap.Tickets = append(snap.Tickets, newTicket(node, epicRepo))
		}

		next := issue.SubIssues.PageInfo
		if !next.HasNextPage {
			break
		}
		if next.EndCursor == "" || next.EndCursor == cursor {
			return model.EpicSnapshot{}, fmt.Errorf(
				"github: %s reports more sub-issues but returned no new page cursor", refKey(r))
		}
		cursor = next.EndCursor
	}

	snap.Capabilities = p.Capabilities()
	return snap, nil
}

// FetchDetail returns one Ticket's description, comments and blocked-by/blocks
// links in a single request, sending detailQuery rather than widening the polled
// epic document (ADR-0003). id is GitHub's GraphQL node ID, which is what
// FetchEpic put on every Ticket.
//
// Nothing here paginates: one drill-in is one request, and the caps in
// detailQuery say what a very long discussion or dependency list gives up.
func (p *Provider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	if id == "" {
		return model.Detail{}, errors.New("github: a ticket id is required to read its detail")
	}

	var resp detailResponse
	if err := p.do(ctx, detailQuery, map[string]any{"id": string(id)}, &resp); err != nil {
		return model.Detail{}, err
	}

	if err := resp.err(id, p.endpoint); err != nil {
		return model.Detail{}, err
	}
	// A null node and a node that is not an Issue are the same thing to the
	// reader: nothing came back. The inline fragment means a non-Issue node
	// decodes to a struct with no id at all.
	if resp.Data.Node == nil || resp.Data.Node.ID == "" {
		return model.Detail{}, errors.New(detailNotFound(id))
	}
	return newDetail(*resp.Data.Node), nil
}

// checkRef rejects a Ref this driver cannot serve before any network call.
func checkRef(r ref.Ref) error {
	if r.Tracker != ref.TrackerGitHub {
		return fmt.Errorf("github: %q is not a GitHub Epic Ref", r.Raw)
	}
	if r.Owner == "" || r.Repo == "" || r.Number <= 0 {
		return fmt.Errorf("github: %q does not name a GitHub issue", r.Raw)
	}
	return nil
}

// refKey renders a Ref the way GitHub writes it, for error messages.
func refKey(r ref.Ref) string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// fetchPage issues one query and returns the epic issue it found, with the
// sub-issue page the cursor selected.
func (p *Provider) fetchPage(ctx context.Context, r ref.Ref, cursor string) (issueNode, error) {
	variables := map[string]any{
		"owner":  r.Owner,
		"repo":   r.Repo,
		"number": r.Number,
	}
	if cursor != "" {
		variables["cursor"] = cursor
	}

	var resp graphQLResponse
	if err := p.do(ctx, epicQuery, variables, &resp); err != nil {
		return issueNode{}, err
	}

	// A GraphQL response can carry both data and errors. A missing repository or
	// issue is authoritative, but an errors[] entry usually says more, so it is
	// preferred when there is one.
	if err := resp.err(r, p.endpoint); err != nil {
		return issueNode{}, err
	}
	if resp.Data.Repository == nil || resp.Data.Repository.Issue == nil {
		return issueNode{}, fmt.Errorf("github: %s not found (or you lack access)", refKey(r))
	}
	return *resp.Data.Repository.Issue, nil
}

// do performs one GraphQL POST of document and decodes the response into out.
//
// The document is a parameter so that every query this driver sends — the
// polled epic one and the Detail one — shares exactly one place that knows about
// auth, headers, status handling and decoding.
func (p *Provider) do(ctx context.Context, document string, variables map[string]any, out any) error {
	token, err := p.resolveToken(ctx)
	if err != nil {
		return err
	}

	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{Query: document, Variables: variables})
	if err != nil {
		return fmt.Errorf("github: encoding the query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: building the request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	// The sub-issue feature header. Without it some endpoints answer with an
	// error about an unknown field `subIssues`, which reads like a query bug and
	// is not one.
	req.Header.Set("GraphQL-Features", "sub_issues")

	res, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github: requesting %s: %w", p.endpoint, err)
	}
	defer res.Body.Close()

	if err := p.checkStatus(res); err != nil {
		return err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("github: decoding the response from %s: %w", p.endpoint, err)
	}
	return nil
}

// checkStatus turns a non-200 response into the clearest one-line explanation
// the status and headers support. No retries and no backoff: the TUI polls
// anyway.
func (p *Provider) checkStatus(res *http.Response) error {
	switch {
	case res.StatusCode == http.StatusOK:
		return nil
	case res.StatusCode == http.StatusUnauthorized:
		return errors.New(`github: authentication failed (401) — check "gh auth status" or GITHUB_TOKEN`)
	case isRateLimited(res):
		return fmt.Errorf("github: API rate limit exceeded; resets at %s", rateLimitReset(res))
	default:
		return fmt.Errorf("github: unexpected response %d from %s", res.StatusCode, p.endpoint)
	}
}

// isRateLimited reports whether a rejection is GitHub saying "not now" rather
// than "not ever": both 403 and 429 carry an exhausted rate limit.
func isRateLimited(res *http.Response) bool {
	if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return res.Header.Get("x-ratelimit-remaining") == "0"
}

// rateLimitReset renders the reset moment in the user's own timezone, because
// "resets at 14:32" is actionable and a unix timestamp is not.
func rateLimitReset(res *http.Response) string {
	secs, err := strconv.ParseInt(res.Header.Get("x-ratelimit-reset"), 10, 64)
	if err != nil || secs <= 0 {
		return "an unknown time"
	}
	return time.Unix(secs, 0).Local().Format(time.RFC1123)
}

// resolveToken fetches the token once per Provider. A 60s poll must not fork a
// gh subprocess forever, and a token that could not be found once will not be
// found on the next tick either.
func (p *Provider) resolveToken(ctx context.Context) (string, error) {
	p.tokenOnce.Do(func() {
		token, err := p.tokenSource(ctx, p.host)
		if err != nil {
			p.tokenErr = fmt.Errorf("github: %w", err)
			return
		}
		if strings.TrimSpace(token) == "" {
			p.tokenErr = fmt.Errorf("github: %w", errNoToken)
			return
		}
		p.token = strings.TrimSpace(token)
	})
	return p.token, p.tokenErr
}

// The GitHub driver must always satisfy the interface it implements.
var _ provider.Provider = (*Provider)(nil)
