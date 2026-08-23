// Package github is sitrep's GitHub Tracker driver: it turns an issue with
// sub-issues or an explicit list of issues into a normalized Watchlist.
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

// maxRefListAliases bounds one dynamically aliased GraphQL document. Larger
// explicit lists are finite, known membership and are read in consecutive
// chunks without changing their Selector order.
const maxRefListAliases = 100

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
	maxTickets  int

	tokenMu sync.Mutex
	token   string
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
		maxTickets:  provider.DefaultMaxTickets,
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

// WithMaxTickets sets the Query membership budget. Non-Query Selectors are
// unaffected; non-positive values leave the default in place.
func WithMaxTickets(maxTickets int) Option {
	return func(p *Provider) {
		if maxTickets > 0 {
			p.maxTickets = maxTickets
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
		Selectors: model.SelectorCapabilities{
			Epic: true, RefList: true, Query: true,
		},
	}
}

// Resolve performs the authoritative read selected by selector. An Epic
// Selector retains the paged sub-issue path; a Ref-list Selector directly reads
// exactly its named roots without expanding hierarchy.
func (p *Provider) Resolve(ctx context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	if err := provider.CheckSelectorSupport(p.Name(), p.Capabilities(), selector); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	switch selected := selector.(type) {
	case provider.EpicSelector:
		return p.resolveEpic(ctx, selected.Ref)
	case provider.RefListSelector:
		return p.resolveRefList(ctx, selected.Refs)
	case provider.QuerySelector:
		return p.resolveQuery(ctx, selected.Query)
	default:
		panic("provider.CheckSelectorSupport accepted an unknown Selector")
	}
}

// resolveEpic returns the named Epic and every one of its sub-issues, following
// sub-issue pagination to the last page. Cross-repo children need no special
// handling: they arrive as ordinary nodes carrying their own repository.
//
// Only one level of the sub-issue graph is fetched — sub-issues of sub-issues
// are not expanded — so ParentID stays empty and every Ticket hangs directly
// off the Epic.
func (p *Provider) resolveEpic(ctx context.Context, r ref.Ref) (model.WatchlistSnapshot, error) {
	if err := checkRef(r); err != nil {
		return model.WatchlistSnapshot{}, err
	}

	var (
		snap     model.WatchlistSnapshot
		epicRepo string
		cursor   string
		haveEpic bool
	)
	snap.Tickets = []model.Ticket{}

	for page := 0; ; page++ {
		if page >= maxPages {
			// A collection larger than sitrep's cap is a stable property of the
			// ref: it will be exactly as large on the next tick, so retrying
			// buys nothing.
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindBadRef,
				"github: %s has more than %d sub-issues; refusing to keep paging",
				refKey(r), maxPages*100)
		}

		issue, err := p.fetchPage(ctx, r, cursor)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}

		if !haveEpic {
			// The epic's own fields repeat on every page; the first page wins so
			// a title edited mid-fetch cannot produce a half-updated snapshot.
			snap.Epic = newEpic(issue)
			epicRepo = issue.Repository.NameWithOwner
			// The fetched issue's own parent, for a Ref that turns out to
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
			// A server that reports a next page and hands over no usable cursor
			// is misbehaving, not a ref the user got wrong — so it stays
			// retryable, on the same terms as a 500.
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
				"github: %s reports more sub-issues but returned no new page cursor", refKey(r))
		}
		cursor = next.EndCursor
	}

	snap.Header = provider.EpicHeader(snap.Epic)
	snap.Capabilities = p.Capabilities()
	return snap, nil
}

// resolveQuery searches for membership identities page by page up to the
// Provider's configured budget, then re-reads every accepted member through the
// authoritative exact-root path. Search payload fields can therefore never
// become rendered Ticket state.
func (p *Provider) resolveQuery(ctx context.Context, query string) (model.WatchlistSnapshot, error) {
	membershipLimit := min(p.maxTickets, querySearchResultLimit)
	initialCapacity := min(membershipLimit, queryPageSize)
	refs := make([]ref.Ref, 0, initialCapacity)
	seen := make(map[string]struct{}, initialCapacity)
	seenCursors := make(map[string]struct{})
	cursor := ""
	consumed := 0
	strongestIssueCount := 0
	limitReached := false

	for {
		remaining := membershipLimit - consumed
		variables := map[string]any{
			"query": query,
			"first": min(queryPageSize, remaining),
			"after": nil,
		}
		if cursor != "" {
			variables["after"] = cursor
		}

		var response queryResponse
		header, status, err := p.doGraphQL(ctx, queryMembershipDocument, variables, &response, true)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}
		if err := response.err(p.endpoint, header, query); err != nil {
			return model.WatchlistSnapshot{}, err
		}
		if status != http.StatusOK {
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
				"github: unexpected response %d from %s", status, p.endpoint)
		}

		strongestIssueCount = max(strongestIssueCount, response.Data.Search.IssueCount)
		nodes := response.Data.Search.Nodes
		overflow := len(nodes) > remaining
		if overflow {
			nodes = nodes[:remaining]
		}
		for _, node := range nodes {
			if node.TypeName != "Issue" {
				continue
			}
			owner, repo, ok := strings.Cut(node.Repository.NameWithOwner, "/")
			if !ok || owner == "" || repo == "" || node.Number <= 0 {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"github: query search returned an Issue with an invalid identity")
			}
			identity := strings.ToLower(node.Repository.NameWithOwner) + "#" + strconv.Itoa(node.Number)
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			refs = append(refs, ref.Ref{
				Tracker: ref.TrackerGitHub,
				Host:    p.host,
				Owner:   owner,
				Repo:    repo,
				Number:  node.Number,
				Raw:     node.Repository.NameWithOwner + "#" + strconv.Itoa(node.Number),
			})
		}
		consumed += len(nodes)
		if cursor != "" && len(nodes) == 0 {
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
				"github: query pagination returned an empty continuation page")
		}

		pageInfo := response.Data.Search.PageInfo
		switch {
		case overflow:
			limitReached = true
		case consumed == membershipLimit:
			limitReached = pageInfo.HasNextPage || strongestIssueCount > consumed
		case !pageInfo.HasNextPage:
			limitReached = strongestIssueCount > consumed
		default:
			if len(nodes) == 0 {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"github: query reports more results but returned an empty page")
			}
			if pageInfo.EndCursor == "" {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"github: query reports more results but returned no new page cursor")
			}
			if _, repeated := seenCursors[pageInfo.EndCursor]; repeated {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"github: query reports more results but repeated a page cursor")
			}
			seenCursors[pageInfo.EndCursor] = struct{}{}
			cursor = pageInfo.EndCursor
			continue
		}
		break
	}

	tickets := []model.Ticket{}
	if len(refs) > 0 {
		var err error
		tickets, err = p.readExactRefs(ctx, refs)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}
	}
	return model.WatchlistSnapshot{
		Header:       provider.QueryHeader(query),
		Tickets:      tickets,
		LimitReached: limitReached,
		Capabilities: p.Capabilities(),
	}, nil
}

// resolveRefList wraps the direct authoritative read in the Ref-list snapshot
// shape. No outer Epic or Parent is synthesized from the named members.
func (p *Provider) resolveRefList(ctx context.Context, refs []ref.Ref) (model.WatchlistSnapshot, error) {
	if len(refs) == 0 {
		return model.WatchlistSnapshot{}, provider.Errorf(provider.KindBadRef,
			"github: a Ref-list selector requires at least one Ref")
	}

	tickets, err := p.readExactRefs(ctx, refs)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}
	return model.WatchlistSnapshot{
		Header:       provider.RefListHeader(len(refs)),
		Tickets:      tickets,
		Capabilities: p.Capabilities(),
	}, nil
}

// readExactRefs authoritatively reads the named issue roots in bounded aliased
// GraphQL batches. It validates the complete set before I/O and returns no
// Tickets when any chunk or member fails, so callers cannot expose a partial
// Watchlist. Query selectors can reuse this exact-root read after resolving
// their own membership.
func (p *Provider) readExactRefs(ctx context.Context, refs []ref.Ref) ([]model.Ticket, error) {
	for _, r := range refs {
		if err := checkExactRef(r); err != nil {
			return nil, err
		}
	}

	tickets := make([]model.Ticket, 0, len(refs))
	for start := 0; start < len(refs); start += maxRefListAliases {
		end := min(start+maxRefListAliases, len(refs))
		chunk, err := p.readExactRefChunk(ctx, refs[start:end])
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, chunk...)
	}
	return tickets, nil
}

func (p *Provider) readExactRefChunk(ctx context.Context, refs []ref.Ref) ([]model.Ticket, error) {
	variables := make(map[string]any, len(refs)*3)
	for i, r := range refs {
		suffix := strconv.Itoa(i)
		variables["owner"+suffix] = r.Owner
		variables["repo"+suffix] = r.Repo
		variables["number"+suffix] = r.Number
	}

	var resp refListResponse
	header, err := p.do(ctx, buildRefListQuery(len(refs)), variables, &resp)
	if err != nil {
		return nil, err
	}
	if err := resp.err(refs, p.endpoint, header); err != nil {
		return nil, err
	}

	tickets := make([]model.Ticket, 0, len(refs))
	for i, r := range refs {
		repository := resp.Data["ref"+strconv.Itoa(i)]
		if repository == nil || repository.Issue == nil {
			if repository != nil && repository.Kind != nil && repository.Kind.TypeName == "PullRequest" {
				return nil, provider.Errorf(provider.KindBadRef,
					"github: %s is a pull request, not a Ticket", refKey(r))
			}
			return nil, provider.Errorf(provider.KindBadRef,
				"github: %s not found (or you lack access)", refKey(r))
		}
		tickets = append(tickets, newTicket(*repository.Issue, ""))
	}
	return tickets, nil
}

// FetchDetail returns one Ticket's description, comments and blocked-by/blocks
// links in a single request, sending detailQuery rather than widening the polled
// epic document (ADR-0003). id is GitHub's GraphQL node ID, which is what
// Resolve put on every Ticket.
//
// Nothing here paginates: one drill-in is one request, and the caps in
// detailQuery say what a very long discussion or dependency list gives up.
func (p *Provider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	if id == "" {
		return model.Detail{}, provider.Errorf(provider.KindBadRef,
			"github: a ticket id is required to read its detail")
	}

	var resp detailResponse
	header, err := p.do(ctx, detailQuery, map[string]any{"id": string(id)}, &resp)
	if err != nil {
		return model.Detail{}, err
	}

	if err := resp.err(id, p.endpoint, header); err != nil {
		return model.Detail{}, err
	}
	// A null node and a node that is not an Issue are the same thing to the
	// reader: nothing came back. The inline fragment means a non-Issue node
	// decodes to a struct with no id at all.
	if resp.Data.Node == nil || resp.Data.Node.ID == "" {
		return model.Detail{}, provider.Errorf(provider.KindBadRef, "%s", detailNotFound(id))
	}
	return newDetail(*resp.Data.Node), nil
}

// checkRef rejects a Ref this driver cannot serve before any network call.
func checkRef(r ref.Ref) error {
	if r.Tracker != ref.TrackerGitHub {
		return provider.Errorf(provider.KindBadRef, "github: %q is not a GitHub Ref", r.Raw)
	}
	if r.Owner == "" || r.Repo == "" || r.Number <= 0 {
		return provider.Errorf(provider.KindBadRef, "github: %q does not name a GitHub issue", r.Raw)
	}
	return nil
}

func checkExactRef(r ref.Ref) error {
	if r.Tracker != ref.TrackerGitHub {
		return provider.Errorf(provider.KindBadRef, "github: %q is not a GitHub Ticket Ref", r.Raw)
	}
	if r.Owner == "" || r.Repo == "" || r.Number <= 0 {
		return provider.Errorf(provider.KindBadRef, "github: %q does not name a GitHub issue", r.Raw)
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
	header, err := p.do(ctx, epicQuery, variables, &resp)
	if err != nil {
		return issueNode{}, err
	}

	// A GraphQL response can carry both data and errors. A missing repository or
	// issue is authoritative, but an errors[] entry usually says more, so it is
	// preferred when there is one.
	if err := resp.err(r, p.endpoint, header); err != nil {
		return issueNode{}, err
	}
	if resp.Data.Repository == nil || resp.Data.Repository.Issue == nil {
		// GitHub shares one number namespace between issues and pull requests,
		// so pasting a pull request link is a common mistake — and answering it
		// with "not found" is a false claim about a page the user is looking at.
		// The aliased issueOrPullRequest selection is what tells the two apart.
		if kind := resp.Data.Repository; kind != nil && kind.Kind != nil &&
			kind.Kind.TypeName == "PullRequest" {
			return issueNode{}, provider.Errorf(provider.KindBadRef,
				"github: %s is a pull request, not a Ticket", refKey(r))
		}
		return issueNode{}, provider.Errorf(provider.KindBadRef,
			"github: %s not found (or you lack access)", refKey(r))
	}
	return *resp.Data.Repository.Issue, nil
}

// do performs one GraphQL POST of document and decodes the response into out,
// returning the response headers.
//
// The document is a parameter so that every query this driver sends — the
// polled Epic and Ref-list reads and the Detail one — shares exactly one place
// that knows about auth, headers, status handling and decoding.
//
// The headers come back because GitHub reports an exhausted GraphQL point
// budget as an errors[] entry on a *200*, and only the headers say when it
// refills; see graphQLErrors.
func (p *Provider) do(ctx context.Context, document string, variables map[string]any, out any) (http.Header, error) {
	header, _, err := p.doGraphQL(ctx, document, variables, out, false)
	return header, err
}

// doGraphQL is the transport shared by ordinary documents and native Query
// membership. GitHub normally returns GraphQL errors under HTTP 200, but also
// uses 400/422 for some rejected search strings. Only the Query path decodes
// those two statuses so its errors[] payload can decide whether the user's
// native query was bad; every other non-200 keeps the established status path.
func (p *Provider) doGraphQL(
	ctx context.Context,
	document string,
	variables map[string]any,
	out any,
	decodeQueryRejection bool,
) (http.Header, int, error) {
	token, err := p.resolveToken(ctx)
	if err != nil {
		return nil, 0, err
	}

	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{Query: document, Variables: variables})
	if err != nil {
		return nil, 0, provider.Errorf(provider.KindUnavailable, "github: encoding the query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, provider.Errorf(provider.KindUnavailable, "github: building the request: %w", err)
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
		if decodeQueryRejection {
			err = provider.RedactedTransportError(err)
		}
		return nil, 0, provider.Errorf(provider.KindUnavailable, "github: requesting %s: %w", p.endpoint, err)
	}
	defer res.Body.Close()

	queryRejection := decodeQueryRejection &&
		(res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnprocessableEntity)
	if !queryRejection {
		if err := p.checkStatus(res); err != nil {
			return nil, res.StatusCode, err
		}
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return nil, res.StatusCode, provider.Errorf(provider.KindUnavailable,
			"github: decoding the response from %s: %w", p.endpoint, err)
	}
	return res.Header, res.StatusCode, nil
}

// checkStatus turns a non-200 response into the clearest one-line explanation
// the status and headers support. No retries and no backoff: the TUI polls
// anyway.
//
// The order of these branches is the whole point of the function, and 403 is
// why. GitHub spends that one status on three unrelated situations — an
// exhausted hourly quota, a secondary (burst) limit, and a token refused for
// SAML SSO or scope reasons — which want three different sentences and two
// different classifications. So the rate-limit evidence is read first, most
// specific first, and the credential wording is what is left over.
func (p *Provider) checkStatus(res *http.Response) error {
	switch {
	case res.StatusCode == http.StatusOK:
		return nil
	case isRateLimited(res):
		return provider.Errorf(provider.KindRateLimit,
			"github: API rate limit exceeded; resets at %s", rateLimitReset(res.Header))
	case isSecondaryRateLimited(res):
		return provider.Errorf(provider.KindRateLimit,
			"github: secondary rate limit hit; retry after %s", retryAfter(res.Header))
	case res.StatusCode == http.StatusUnauthorized:
		return provider.Errorf(provider.KindAuth,
			`github: authentication failed (401) — check "gh auth status" or GITHUB_TOKEN`)
	case res.StatusCode == http.StatusForbidden:
		return provider.Errorf(provider.KindAuth, "%s", forbidden(res.Header))
	default:
		return provider.Errorf(provider.KindUnavailable,
			"github: unexpected response %d from %s", res.StatusCode, p.endpoint)
	}
}

// isRateLimited reports whether a rejection is GitHub's primary rate limit —
// the hourly quota — which both 403 and 429 carry.
func isRateLimited(res *http.Response) bool {
	if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return res.Header.Get("x-ratelimit-remaining") == "0"
}

// isSecondaryRateLimited reports whether a rejection is one of GitHub's
// secondary limits, which guard bursts rather than the hourly quota and are
// answered with 403 or 429 plus a retry-after header. A 429 with no headers at
// all is a rate limit too: GitHub spends that status on nothing else.
func isSecondaryRateLimited(res *http.Response) bool {
	switch res.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return res.Header.Get("retry-after") != ""
	default:
		return false
	}
}

// forbidden words a 403 that is not a rate limit. Both causes GitHub uses this
// status for are things the user can fix, and neither is guessable from
// "unexpected response 403".
func forbidden(header http.Header) string {
	if sso := ssoHint(header.Get("x-github-sso")); sso != "" {
		return "github: access denied (403) — this organisation requires SAML SSO; " +
			"authorise your token at the URL in GitHub's x-github-sso header (" + sso + ")"
	}
	return `github: access denied (403) — your token may lack the scopes sitrep needs ` +
		`(run "gh auth refresh -s read:project,repo")`
}

// ssoHintLimit bounds how much of the x-github-sso header is quoted. The header
// carries a URL plus a list of organisation ids and can run long; an error is
// one line.
const ssoHintLimit = 120

// ssoHint trims the SSO header down to something that fits on a terminal line.
func ssoHint(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= ssoHintLimit {
		return value
	}
	return value[:ssoHintLimit] + "…"
}

// rateLimitReset renders the reset moment in the user's own timezone, because
// "resets at 14:32" is actionable and a unix timestamp is not.
func rateLimitReset(header http.Header) string {
	secs, err := strconv.ParseInt(header.Get("x-ratelimit-reset"), 10, 64)
	if err != nil || secs <= 0 {
		return "an unknown time"
	}
	return time.Unix(secs, 0).Local().Format(time.RFC1123)
}

// retryAfter renders the retry-after header a secondary limit carries, which
// GitHub sends in seconds. It mirrors rateLimitReset: an unparseable value says
// so rather than being echoed back as a number with no unit.
func retryAfter(header http.Header) string {
	secs, err := strconv.Atoi(strings.TrimSpace(header.Get("retry-after")))
	if err != nil || secs <= 0 {
		return "an unknown time"
	}
	return (time.Duration(secs) * time.Second).String()
}

// resolveToken resolves the token once and then reuses it for the process
// lifetime: a 60s poll must not fork a gh subprocess forever. A *failure* is
// not cached, because discovery can involve a live call — `gh auth token` reads
// a keyring that may be locked — and a transient failure cached for the
// process lifetime survives only a restart. The mutex also gives the retry
// single-flight: concurrent fetches still cost at most one gh invocation.
func (p *Provider) resolveToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.token != "" {
		return p.token, nil
	}
	token, err := p.tokenSource(ctx, p.host)
	if err != nil {
		return "", provider.Errorf(provider.KindAuth, "github: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", provider.Errorf(provider.KindAuth, "github: %w", errNoToken)
	}
	p.token = token
	return token, nil
}

// The GitHub driver must always satisfy the interface it implements.
var _ provider.Provider = (*Provider)(nil)
