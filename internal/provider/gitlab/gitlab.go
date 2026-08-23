// Package gitlab is sitrep's GitLab Tracker driver: it turns a GitLab group
// epic, a project or group milestone, and their child issues — or a single
// project issue — into a normalized Epic with its Tickets.
//
// The driver speaks REST/JSON over plain net/http and encoding/json. There is
// no GitLab SDK and there will not be one (ADR-0001): hand-rolling a handful of
// GET requests keeps sitrep free of runtime dependencies and keeps the test seam
// at the HTTP boundary, where recorded payloads can be replayed by a local
// server.
//
// Everything here is read-only (ADR-0002): every request this package sends is
// a GET. No POST, PUT, PATCH or DELETE exists anywhere in it, not even behind a
// flag.
//
// # REST v4, not the Work Items GraphQL API
//
// GitLab deprecated the Epics REST API in 17.0, but its own migration guide
// says it "continues to work with existing endpoints" and gives no removal date
// beyond "v5 of the API"; it was verified working on gitlab.com on 2026-08-21.
// The replacement is GraphQL-only and needs GitLab 18.1+, so choosing it would
// drop every self-managed instance the REST path still serves. sitrep therefore
// speaks REST and says so out loud rather than probing both: a driver that
// sends two requests per refresh to find out which API works is worse than one
// that is wrong loudly. The exposure is bounded — every path is built by the
// helpers in target.go from one apiBase constant.
//
// # Auth
//
// The token chain is the environment first — GITLAB_TOKEN, GITLAB_ACCESS_TOKEN,
// OAUTH_TOKEN — and then glab's stored login. `glab auth token` does not exist
// (verified against glab 1.113.0); the nearest command validates the token over
// the network and prints it to stderr, so asking it before an environment
// variable sitrep already has would be a live API call for nothing. See
// DefaultTokenSource.
//
// # TicketID
//
// model.TicketID here encodes the addressed node: "issue:{project path}#{iid}",
// "epic:{group path}&{iid}", "project-milestone:{project path}%{iid}" or
// "group-milestone:{group path}%{iid}". A GitLab issue iid is meaningless
// without its project — iids restart at 1 in every project — and FetchDetail
// receives nothing else. It is Provider-scoped and opaque by contract; nothing
// outside this package may parse it.
//
// # Milestone as Epic
//
// Native epics are a Premium/Ultimate feature. On GitLab Free the collection a
// team actually delegates work into is a milestone, so sitrep reports a
// milestone as an Epic: a milestone Ref renders a full Epic view, with the same
// children, the same progress and the same drill-in as an epic. It is a Ref and
// not a probe — sitrep never fetches two APIs to find out which tier this is —
// and the human-facing half of the fallback is the 403 message on the epic path,
// which now names the milestone route.
//
// A milestone is addressed by its iid, which is the number its web URL and its
// "%3" reference carry, but GitLab's milestone endpoints take the milestone's
// database *id*. The list endpoint's iids[] filter bridges the two and returns
// the whole milestone in the same request, which is why this driver never reads
// /milestones/:milestone_id. (Verified against gitlab.com on 2026-08-21; the
// milestone endpoints answer 401 even for public projects, so the milestone
// fixtures are hand-written — testdata/README.md says so per file.)
//
// # Merge requests
//
// Every Ticket carries the merge requests moving it, with their state, their
// review and approval posture and their CI pipeline status, and an open Ticket
// with an open or draft merge request is the only way this driver produces
// StatusInProgress.
//
// Which merge requests those are comes from GET
// /projects/:id/issues/:iid/closed_by — GitLab's own closing linkage, the same
// question the GitHub driver asks through closedByPullRequestsReferences.
// /related_merge_requests answers a wider one: it includes every merge request
// that merely *mentions* the issue, and an open mention would flip a Todo
// Ticket to In Progress and skew both grouping and progress.
//
// The wider list is still read, for one field. The two endpoints serialize a
// merge request differently and only related_merge_requests carries
// head_pipeline, which is the whole of "is CI green"; closed_by therefore
// decides membership and the related payload supplies the pipeline for the
// merge requests it named. /projects/:id/merge_requests (including the
// milestone-scoped form) was considered and rejected: it is the list shape too,
// and milestone assignment is not issue correlation anyway.
//
// # The request budget
//
// An expanded Epic refresh costs 1 + ceil(N/100) + N + C + A requests,
// where N is the Ticket count, C ≤ N is the number of Tickets something is
// actually closing — only those pay the second correlation request — and A ≤ C
// is the number whose lead merge request is still live. An explicit Ref-list
// costs R + I + C + A: one direct root read for each of R Refs, then correlation
// for its I issue roots; a Query also pays its membership pages before those
// same exact-root and correlation costs. Epic and milestone roots skip
// correlation. Exact roots run with at most exactRootWorkers requests in flight;
// correlation is separately bounded to mergeRequestWorkers concurrent requests
// and is fully context-cancellable. For a thirty-Ticket Epic that is roughly
// forty to ninety requests a refresh; a reader deciding whether to raise
// --interval deserves the number.
package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// pageSize is GitLab's documented maximum page, and maxPages bounds the loop.
// An epic with five thousand children is a bug somewhere, not a situation
// report, and an unbounded loop against a paging API is how a polling tool
// hangs. exactRootWorkers separately bounds Query and Ref-list direct reads.
const (
	pageSize         = 100
	maxPages         = 50
	exactRootWorkers = 8
)

// notePageSize is how many notes a drill-in reads. Nothing paginates: one
// drill-in is one request, and this cap says what a very long discussion gives
// up.
const notePageSize = 100

// requestTimeout is the default per-request budget when the caller supplies no
// HTTP client of its own.
const requestTimeout = 30 * time.Second

// Provider is the GitLab Tracker driver. Construct it with New; the zero value
// is not usable.
type Provider struct {
	host        string
	baseURL     string
	path        string
	httpClient  *http.Client
	tokenSource TokenSource
	userAgent   string
	maxTickets  int

	tokenMu sync.Mutex
	token   string
}

// Option configures a Provider.
type Option func(*Provider)

// New returns a GitLab Provider reading from host, which is "gitlab.com" or a
// self-managed GitLab host. Construction is cheap and free of side effects: no
// token is resolved and no request is made until the first fetch, so
// `sitrep --help` never shells out to glab.
func New(host string, opts ...Option) *Provider {
	host = strings.TrimSpace(host)
	p := &Provider{
		host:        host,
		baseURL:     "https://" + host,
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

// WithBaseURL replaces the instance URL derived from the host, everything
// before the /api/v4 path. Tests point it at a local server replaying recorded
// payloads.
func WithBaseURL(rawurl string) Option {
	return func(p *Provider) {
		if rawurl != "" {
			p.baseURL = strings.TrimSuffix(rawurl, "/")
		}
	}
}

// WithPath sets the default group or project path — the Profile's project —
// used for a Ref that names none, such as the bare reference form "&12".
func WithPath(path string) Option {
	return func(p *Provider) { p.path = strings.TrimSpace(path) }
}

// WithTokenSource replaces the token discovery chain. The source is called at
// most once per Provider.
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

// Name returns "gitlab". It is the --provider value and the provider field in
// --json.
func (p *Provider) Name() string { return "gitlab" }

// Capabilities declares what this driver actually returns today.
func (p *Provider) Capabilities() model.Capabilities {
	return model.Capabilities{
		Hierarchy:     true, // an epic's or milestone's child issues are how an Epic is assembled
		BlockingLinks: true, // the issue links endpoint, with its own link_type
		Comments:      true, // notes, minus GitLab's system notes
		PullRequests:  true, // related merge requests, with review posture and pipeline status
		Selectors: model.SelectorCapabilities{
			Epic: true, RefList: true, Query: true,
		},
	}
}

// Resolve performs the authoritative read for selector. An Epic Selector expands
// its GitLab root according to resolveEpic, a Ref-list Selector reads its exact
// roots, and a Query Selector chooses membership before authoritatively reading
// those roots.
func (p *Provider) Resolve(ctx context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	if err := provider.CheckSelectorSupport(p.Name(), p.Capabilities(), selector); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	switch s := selector.(type) {
	case provider.EpicSelector:
		return p.resolveEpic(ctx, s)
	case provider.RefListSelector:
		return p.resolveRefList(ctx, s)
	case provider.QuerySelector:
		return p.resolveQuery(ctx, s.Query)
	default:
		panic("provider.CheckSelectorSupport accepted an unknown Selector")
	}
}

// resolveEpic expands a group Epic or milestone by one child level. A project
// issue returns as the decoded root with no child Tickets.
func (p *Provider) resolveEpic(ctx context.Context, selector provider.EpicSelector) (model.WatchlistSnapshot, error) {
	t, err := targetFor(selector.Ref, p.path)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}

	// Tickets starts non-nil so an Epic with no children renders as "no
	// Tickets" rather than as null.
	snap := model.WatchlistSnapshot{Tickets: []model.Ticket{}, Capabilities: p.Capabilities()}

	switch {
	case t.kind == kindIssue:
		if err := p.fetchIssueSnapshot(ctx, t, &snap); err != nil {
			return model.WatchlistSnapshot{}, err
		}
		snap.Header = provider.EpicHeader(snap.Epic)
		// FetchedAt is left zero for the caller to stamp.
		return snap, nil

	case t.isMilestone():
		if err := p.fetchMilestoneSnapshot(ctx, t, &snap); err != nil {
			return model.WatchlistSnapshot{}, err
		}

	default:
		if err := p.fetchEpicSnapshot(ctx, t, &snap); err != nil {
			return model.WatchlistSnapshot{}, err
		}
	}

	if err := p.correlate(ctx, snap.Tickets); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	snap.Header = provider.EpicHeader(snap.Epic)
	return snap, nil
}

func (p *Provider) resolveRefList(ctx context.Context, selector provider.RefListSelector) (model.WatchlistSnapshot, error) {
	if len(selector.Refs) == 0 {
		return model.WatchlistSnapshot{}, provider.Errorf(provider.KindBadRef,
			"gitlab: a Ref-list Selector must contain at least one Ref")
	}

	tickets, err := p.readExactRefs(ctx, selector.Refs)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}
	return model.WatchlistSnapshot{
		Header:       provider.RefListHeader(len(tickets)),
		Tickets:      tickets,
		Capabilities: p.Capabilities(),
	}, nil
}

func (p *Provider) resolveQuery(ctx context.Context, query string) (model.WatchlistSnapshot, error) {
	membershipPath := p.queryMembershipPath()
	membershipPageSize := min(pageSize, p.maxTickets)
	initialCapacity := membershipPageSize
	refs := make([]ref.Ref, 0, initialCapacity)
	seen := make(map[string]struct{}, initialCapacity)
	page := 1
	rawQuery := queryMembershipRawQuery(query, membershipPageSize, page)
	seenRequests := map[string]struct{}{
		queryMembershipRequestIdentity(membershipPath, rawQuery): {},
	}
	consumed := 0
	followedContinuation := false
	limitReached := false

	for {
		remaining := p.maxTickets - consumed
		issues, header, err := p.searchQueryMembership(ctx, membershipPath, rawQuery, query)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}

		overflow := len(issues) > remaining
		if overflow {
			issues = issues[:remaining]
		}
		for _, issue := range issues {
			path := issue.projectPath()
			if path == "" || issue.ProjectID <= 0 || issue.IID <= 0 {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query search returned an issue with an invalid identity")
			}
			identity := strconv.Itoa(issue.ProjectID) + "#" + strconv.Itoa(issue.IID)
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			owner, repo, _ := strings.Cut(path, "/")
			if owner == "" || repo == "" {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query search returned an issue with invalid project path %q", path)
			}
			refs = append(refs, ref.Ref{
				Tracker: ref.TrackerGitLab,
				Host:    p.host,
				Owner:   owner,
				Repo:    repo,
				Number:  issue.IID,
				Raw:     path + "#" + strconv.Itoa(issue.IID),
			})
		}
		consumed += len(issues)
		if followedContinuation && len(issues) == 0 {
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
				"gitlab: query pagination returned an empty continuation page")
		}

		nextPage := strings.TrimSpace(header.Get("x-next-page"))
		nextCursor := strings.TrimSpace(header.Get("x-next-cursor"))
		switch {
		case overflow:
			limitReached = true
		case consumed == p.maxTickets:
			limitReached = nextPage != "" || nextCursor != ""
			if !limitReached {
				_, limitReached, err = nextLink(header.Values("Link"))
				if err != nil {
					return model.WatchlistSnapshot{}, err
				}
			}
		case nextPage != "":
			if len(issues) == 0 {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query reports more results but returned an empty page")
			}
			nextPageNumber, err := strconv.Atoi(nextPage)
			if err != nil || nextPageNumber <= page {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query reports a malformed or non-forward next page")
			}
			nextRawQuery := queryMembershipRawQuery(query, membershipPageSize, nextPageNumber)
			identity := queryMembershipRequestIdentity(membershipPath, nextRawQuery)
			if _, repeated := seenRequests[identity]; repeated {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query pagination repeated a continuation request")
			}
			seenRequests[identity] = struct{}{}
			page = nextPageNumber
			rawQuery = nextRawQuery
			followedContinuation = true
			continue
		default:
			next, found, err := nextLink(header.Values("Link"))
			if err != nil {
				return model.WatchlistSnapshot{}, err
			}
			if !found {
				if nextCursor != "" {
					return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
						"gitlab: query returned a cursor without a usable next link")
				}
				break
			}
			if len(issues) == 0 {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query reports more results but returned an empty page")
			}
			nextRawQuery, identity, err := p.validateQueryContinuation(
				membershipPath, rawQuery, query, next, membershipPageSize)
			if err != nil {
				return model.WatchlistSnapshot{}, err
			}
			if _, repeated := seenRequests[identity]; repeated {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"gitlab: query pagination repeated a continuation request")
			}
			seenRequests[identity] = struct{}{}
			rawQuery = nextRawQuery
			followedContinuation = true
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

func (p *Provider) queryMembershipPath() string {
	if p.path != "" {
		return "/projects/" + url.PathEscape(p.path) + "/issues"
	}
	return "/issues"
}

func queryMembershipRawQuery(query string, perPage, page int) string {
	rawQuery := query
	if rawQuery != "" && !strings.HasSuffix(rawQuery, "&") {
		rawQuery += "&"
	}
	return rawQuery + "per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
}

func queryMembershipRequestIdentity(path, rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return path + "?" + rawQuery
	}
	return path + "?" + values.Encode()
}

func (p *Provider) searchQueryMembership(
	ctx context.Context,
	path, rawQuery, query string,
) ([]issueWire, http.Header, error) {
	var issues []issueWire
	header, err := p.doQuery(ctx, path, rawQuery, query, &issues)
	if err != nil {
		return nil, nil, err
	}
	return issues, header, nil
}

var queryPaginationParameters = map[string]struct{}{
	"cursor":     {},
	"id_after":   {},
	"id_before":  {},
	"order_by":   {},
	"page":       {},
	"pagination": {},
	"sort":       {},
}

func (p *Provider) validateQueryContinuation(
	membershipPath, currentRawQuery, originalQuery, next string,
	perPage int,
) (string, string, error) {
	current, err := url.Parse(p.baseURL + apiBase + membershipPath)
	if err != nil {
		return "", "", provider.Errorf(provider.KindUnavailable,
			"gitlab: building the query pagination endpoint: %w", err)
	}
	current.RawQuery = currentRawQuery

	reference, err := url.Parse(next)
	if err != nil {
		return "", "", malformedQueryPaginationError()
	}
	candidate := current.ResolveReference(reference)
	if candidate.Opaque != "" || candidate.User != nil || candidate.Fragment != "" ||
		!strings.EqualFold(candidate.Scheme, current.Scheme) ||
		!strings.EqualFold(candidate.Host, current.Host) || candidate.Path != current.Path {
		return "", "", provider.Errorf(provider.KindUnavailable,
			"gitlab: query pagination returned an unsafe next link")
	}

	values, err := url.ParseQuery(candidate.RawQuery)
	if err != nil {
		return "", "", malformedQueryPaginationError()
	}
	membership, err := url.ParseQuery(originalQuery)
	if err != nil {
		return "", "", malformedQueryPaginationError()
	}
	remaining := cloneQueryValues(values)
	for name, expected := range membership {
		for _, value := range expected {
			actual := remaining[name]
			match := -1
			for i := range actual {
				if actual[i] == value {
					match = i
					break
				}
			}
			if match < 0 {
				return "", "", changedQueryMembershipError()
			}
			remaining[name] = append(actual[:match], actual[match+1:]...)
		}
	}

	perPageValues := remaining["per_page"]
	if len(perPageValues) != 1 || perPageValues[0] != strconv.Itoa(perPage) {
		return "", "", provider.Errorf(provider.KindUnavailable,
			"gitlab: query pagination changed the Provider page size")
	}
	delete(remaining, "per_page")
	for name, entries := range remaining {
		if len(entries) == 0 {
			continue
		}
		if _, paging := queryPaginationParameters[name]; !paging {
			return "", "", changedQueryMembershipError()
		}
	}
	return candidate.RawQuery, membershipPath + "?" + values.Encode(), nil
}

func cloneQueryValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for name, entries := range values {
		clone[name] = append([]string(nil), entries...)
	}
	return clone
}

func changedQueryMembershipError() error {
	return provider.Errorf(provider.KindUnavailable,
		"gitlab: query pagination changed the Selector membership")
}

func malformedQueryPaginationError() error {
	return provider.Errorf(provider.KindUnavailable,
		"gitlab: query returned malformed pagination links")
}

func nextLink(headers []string) (string, bool, error) {
	if len(headers) == 0 {
		return "", false, nil
	}
	values, err := splitLinkHeader(strings.Join(headers, ","))
	if err != nil {
		return "", false, malformedQueryPaginationError()
	}

	var next string
	for _, value := range values {
		target, relations, err := parseLinkValue(value)
		if err != nil {
			return "", false, malformedQueryPaginationError()
		}
		for _, relation := range relations {
			if !strings.EqualFold(relation, "next") {
				continue
			}
			if next != "" {
				return "", false, malformedQueryPaginationError()
			}
			next = target
		}
	}
	return next, next != "", nil
}

func splitLinkHeader(header string) ([]string, error) {
	var values []string
	start := 0
	inTarget := false
	inQuote := false
	escaped := false
	for i, r := range header {
		switch {
		case escaped:
			escaped = false
		case inQuote && r == '\\':
			escaped = true
		case inQuote && r == '"':
			inQuote = false
		case inQuote:
		case inTarget && r == '>':
			inTarget = false
		case inTarget:
		case r == '<':
			inTarget = true
		case r == '"':
			inQuote = true
		case r == ',':
			value := strings.TrimSpace(header[start:i])
			if value == "" {
				return nil, malformedQueryPaginationError()
			}
			values = append(values, value)
			start = i + 1
		}
	}
	if inTarget || inQuote || escaped {
		return nil, malformedQueryPaginationError()
	}
	value := strings.TrimSpace(header[start:])
	if value == "" {
		return nil, malformedQueryPaginationError()
	}
	return append(values, value), nil
}

func parseLinkValue(value string) (string, []string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "<") {
		return "", nil, malformedQueryPaginationError()
	}
	end := strings.IndexByte(value, '>')
	if end < 2 {
		return "", nil, malformedQueryPaginationError()
	}
	target := strings.TrimSpace(value[1:end])
	rest := strings.TrimSpace(value[end+1:])
	if target == "" || (rest != "" && !strings.HasPrefix(rest, ";")) {
		return "", nil, malformedQueryPaginationError()
	}

	var relations []string
	seenRel := false
	for rest != "" {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, ";"))
		if rest == "" {
			return "", nil, malformedQueryPaginationError()
		}
		parameter, remaining, err := takeLinkParameter(rest)
		if err != nil {
			return "", nil, err
		}
		rest = remaining
		name, rawValue, ok := strings.Cut(parameter, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return "", nil, malformedQueryPaginationError()
		}
		decoded, err := decodeLinkParameter(strings.TrimSpace(rawValue))
		if err != nil {
			return "", nil, err
		}
		if strings.EqualFold(strings.TrimSpace(name), "rel") {
			if seenRel {
				return "", nil, malformedQueryPaginationError()
			}
			seenRel = true
			relations = strings.Fields(decoded)
		}
	}
	return target, relations, nil
}

func takeLinkParameter(rest string) (string, string, error) {
	inQuote := false
	escaped := false
	for i, r := range rest {
		switch {
		case escaped:
			escaped = false
		case inQuote && r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case !inQuote && r == ';':
			return strings.TrimSpace(rest[:i]), rest[i:], nil
		}
	}
	if inQuote || escaped {
		return "", "", malformedQueryPaginationError()
	}
	return strings.TrimSpace(rest), "", nil
}

func decodeLinkParameter(value string) (string, error) {
	if value == "" {
		return "", malformedQueryPaginationError()
	}
	if value[0] != '"' {
		if strings.ContainsAny(value, " \t\r\n\"") {
			return "", malformedQueryPaginationError()
		}
		return value, nil
	}

	var decoded strings.Builder
	escaped := false
	for i := 1; i < len(value); i++ {
		switch {
		case escaped:
			decoded.WriteByte(value[i])
			escaped = false
		case value[i] == '\\':
			escaped = true
		case value[i] == '"':
			if strings.TrimSpace(value[i+1:]) != "" {
				return "", malformedQueryPaginationError()
			}
			return decoded.String(), nil
		default:
			decoded.WriteByte(value[i])
		}
	}
	return "", malformedQueryPaginationError()
}

func (p *Provider) doQuery(ctx context.Context, path, rawQuery, query string, out any) (http.Header, error) {
	token, err := p.resolveToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint, err := url.Parse(p.baseURL + apiBase + path)
	if err != nil {
		return nil, provider.Errorf(provider.KindUnavailable, "gitlab: building the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, provider.Errorf(provider.KindUnavailable, "gitlab: building the request: %w", err)
	}
	// Query is already a tracker-native URL query component. Assign it only after
	// NewRequest has parsed the endpoint: converting a URL carrying a literal '#'
	// to a string and parsing it again would reinterpret the rest as a fragment.
	req.URL.RawQuery = rawQuery
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, provider.Errorf(provider.KindUnavailable, "gitlab: requesting %s: %w",
			apiBase+path, provider.RedactedTransportError(err))
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnprocessableEntity {
		message := errorPayload(res)
		if message == "" {
			message = "the Tracker rejected the query"
		}
		message = provider.RedactQuery(message, query)
		return nil, provider.Errorf(provider.KindBadRef, "gitlab: query rejected: %s", message)
	}
	if err := checkStatus(res, "query", apiBase+path, p.host); err != nil {
		return nil, provider.RedactQueryError(err, query)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return nil, provider.Errorf(provider.KindUnavailable,
			"gitlab: decoding the response from %s: %w", apiBase+path, err)
	}
	return res.Header, nil
}

// readExactRefs authoritatively reads each named root as one thin Ticket. All
// Refs are validated before the first request. The bounded root fan-out writes
// by Selector index and reports errors in that same order; merge-request
// correlation runs only after every direct read succeeds.
func (p *Provider) readExactRefs(ctx context.Context, refs []ref.Ref) ([]model.Ticket, error) {
	targets := make([]target, len(refs))
	for i, r := range refs {
		t, err := targetFor(r, p.path)
		if err != nil {
			return nil, err
		}
		targets[i] = t
	}

	tickets := make([]model.Ticket, len(targets))
	errs := make([]error, len(targets))
	indices := make(chan int, len(targets))
	for i := range targets {
		indices <- i
	}
	close(indices)

	var wg sync.WaitGroup
	for range min(len(targets), exactRootWorkers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				if err := ctx.Err(); err != nil {
					errs[i] = err
					continue
				}
				tickets[i], errs[i] = p.fetchRootTicket(ctx, targets[i])
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if err := p.correlate(ctx, tickets); err != nil {
		return nil, err
	}
	return tickets, nil
}

func (p *Provider) fetchRootTicket(ctx context.Context, t target) (model.Ticket, error) {
	switch {
	case t.kind == kindIssue:
		var issue issueWire
		if _, err := p.do(ctx, t.issuePath(), nil, t.String(), &issue); err != nil {
			return model.Ticket{}, err
		}
		return newTicketFromIssue(issue), nil

	case t.isMilestone():
		milestone, err := p.fetchMilestone(ctx, t)
		if err != nil {
			return model.Ticket{}, err
		}
		return newTicketFromEpic(newEpicFromMilestone(milestone, p.host, t)), nil

	default:
		var epic epicWire
		if _, err := p.do(ctx, t.epicPath(), nil, t.String(), &epic); err != nil {
			return model.Ticket{}, err
		}
		return newTicketFromEpic(newEpicFromEpic(epic, p.host, t.path)), nil
	}
}

// fetchIssueSnapshot reads the single issue a Ref turned out to name,
// including the merge requests moving it: model.Epic.PullRequests exists for
// exactly this decoded Detail header, and cli.decodedTicket copies it onto the
// Ticket it renders. One extra request, on a path that is a drill-in by
// definition.
func (p *Provider) fetchIssueSnapshot(ctx context.Context, t target, snap *model.WatchlistSnapshot) error {
	var issue issueWire
	if _, err := p.do(ctx, t.issuePath(), nil, t.String(), &issue); err != nil {
		return err
	}
	snap.Epic = newEpicFromIssue(issue)
	snap.Parent = newParentFromIssue(issue, p.host)

	prs, err := p.mergeRequestsFor(ctx, t)
	if err != nil {
		return err
	}
	snap.Epic.PullRequests = prs
	snap.Epic.Status = statusWithMergeRequests(snap.Epic.Status, prs)
	return nil
}

// fetchEpicSnapshot reads a group epic and its children.
func (p *Provider) fetchEpicSnapshot(ctx context.Context, t target, snap *model.WatchlistSnapshot) error {
	var epic epicWire
	if _, err := p.do(ctx, t.epicPath(), nil, t.String(), &epic); err != nil {
		return err
	}
	snap.Epic = newEpicFromEpic(epic, p.host, t.path)
	snap.Parent = newParentFromEpic(epic, p.host, t.path)

	tickets, err := p.fetchChildren(ctx, t, t.epicIssuesPath())
	if err != nil {
		return err
	}
	snap.Tickets = append(snap.Tickets, tickets...)
	return nil
}

// fetchMilestoneSnapshot reads a milestone and its issues. One function serves
// both scopes: a project milestone and a group milestone differ only in the
// segment target builds their paths from, and writing the branch twice is how
// the two drift.
//
// Parent stays the zero model.Parent. A milestone belongs to nothing sitrep
// models — a project milestone's group is not its parent — and a zero Parent is
// an ordinary state, not an error.
//
// A group milestone's issues span projects, so each Ticket's Repository and
// project-qualified Key differ; newTicketFromIssue already derives both from the
// issue's own references.full, so this needs no code of its own.
func (p *Provider) fetchMilestoneSnapshot(ctx context.Context, t target, snap *model.WatchlistSnapshot) error {
	milestone, err := p.fetchMilestone(ctx, t)
	if err != nil {
		return err
	}
	snap.Epic = newEpicFromMilestone(milestone, p.host, t)

	tickets, err := p.fetchChildren(ctx, t, t.milestoneIssuesPath(milestone.ID))
	if err != nil {
		return err
	}
	snap.Tickets = append(snap.Tickets, tickets...)
	return nil
}

// fetchChildren pages a collection's issues to the last page, in GitLab's own
// order. path is the children endpoint — an epic's or a milestone's — because
// everything after the addressing is identical.
func (p *Provider) fetchChildren(ctx context.Context, t target, path string) ([]model.Ticket, error) {
	issues, err := pagedGet[issueWire](ctx, p, t, path, "children")
	if err != nil {
		return nil, err
	}
	var tickets []model.Ticket
	for _, issue := range issues {
		tickets = append(tickets, newTicketFromIssue(issue))
	}
	return tickets, nil
}

// pagedGet reads every page of one GitLab list endpoint into a single slice.
// It is the package's one paging loop: every list this driver reads is subject
// to the same maxPages cap and the same refusal to follow a next-page header
// that does not point forwards, so the rule lives in one place rather than
// being re-derived per endpoint.
//
// noun names the collection in the two errors, in the user's vocabulary rather
// than the endpoint's.
func pagedGet[T any](ctx context.Context, p *Provider, t target, path, noun string) ([]T, error) {
	var all []T

	page := 1
	for i := 0; ; i++ {
		if i >= maxPages {
			// A collection larger than sitrep's cap is a stable property of the
			// ref: it will be exactly as large on the next tick, so retrying
			// buys nothing.
			return nil, provider.Errorf(provider.KindBadRef,
				"gitlab: %s has more than %d %s; refusing to keep paging",
				t, maxPages*pageSize, noun)
		}

		query := url.Values{
			"per_page": {strconv.Itoa(pageSize)},
			"page":     {strconv.Itoa(page)},
		}
		var batch []T
		header, err := p.do(ctx, path, query, t.String(), &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)

		next := strings.TrimSpace(header.Get("x-next-page"))
		if next == "" {
			return all, nil
		}
		n, err := strconv.Atoi(next)
		if err != nil || n <= page {
			// A next page that is not a later page is a paging API contradicting
			// itself; following it is how a polling tool spins forever. That is
			// a server fault, so it stays retryable on the same terms as a 500 —
			// and the header is tracker-supplied text, which provider.Errorf's
			// funnel strips of escapes on its way to a terminal.
			return nil, provider.Errorf(provider.KindUnavailable,
				"gitlab: %s reports its next page of %s as %q, "+
					"which is not after page %d", t, noun, next, page)
		}
		page = n
	}
}

// FetchDetail returns one Ticket's description, comments and, for an issue, its
// links. id is what Resolve put on every Ticket; see the package doc for what
// it encodes.
//
// This is three requests where the polled path is two, and that is the point of
// the split in ADR-0003: a drill-in happens once, deliberately, when a human
// presses Enter, so it can afford the issue, the discussion and the links.
//
// An empty description, no comments and no links are the ordinary state of a
// freshly filed Ticket and produce a zero-ish Detail, never an error.
func (p *Provider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	t, err := parseTicketID(id)
	if err != nil {
		return model.Detail{}, err
	}
	if t.kind == kindEpic {
		return p.fetchEpicDetail(ctx, t, id)
	}
	if t.isMilestone() {
		return p.fetchMilestoneDetail(ctx, t, id)
	}

	var issue issueWire
	if _, err := p.do(ctx, t.issuePath(), nil, t.String(), &issue); err != nil {
		return model.Detail{}, err
	}

	notes, err := p.fetchNotes(ctx, t.issueNotesPath(), t.String())
	if err != nil {
		return model.Detail{}, err
	}

	var links []issueLinkWire
	if _, err := p.do(ctx, t.issueLinksPath(), nil, t.String(), &links); err != nil {
		return model.Detail{}, err
	}

	return model.Detail{
		TicketID:    id,
		Description: issue.Description,
		Comments:    newComments(notes, issue.WebURL),
		Links:       newLinks(links),
	}, nil
}

// fetchEpicDetail reads a group epic's Detail, which a Ref naming an epic with
// no children decodes to.
//
// The order is fixed rather than incidental: GitLab's epic *notes* endpoint is
// addressed by the epic's database id, not its iid — its own documentation says
// so — and the only place that id comes from is the epic payload fetched first.
//
// An epic gets no Links. The linked-epics API is separately deprecated and
// epic-to-epic blocking is out of this driver's scope, so Links stays nil, which
// is an ordinary Detail and not an error.
func (p *Provider) fetchEpicDetail(ctx context.Context, t target, id model.TicketID) (model.Detail, error) {
	var epic epicWire
	if _, err := p.do(ctx, t.epicPath(), nil, t.String(), &epic); err != nil {
		return model.Detail{}, err
	}

	notes, err := p.fetchNotes(ctx, t.epicNotesPath(epic.ID), t.String())
	if err != nil {
		return model.Detail{}, err
	}

	return model.Detail{
		TicketID:    id,
		Description: epic.Description,
		Comments:    newComments(notes, webURLOr(epic.WebURL, epicWebURL(p.host, t.path, t.iid))),
	}, nil
}

// fetchMilestoneDetail reads a milestone's Detail, which a Ref naming a
// milestone with no issues decodes to. It is one request: the same iids[] lookup
// Resolve makes, which is what the iid-carrying TicketID trades for.
//
// Comments stays nil because GitLab has no milestone notes endpoint at all —
// not because the Comments Capability is a lie. A Capability is a Provider-level
// declaration about the Tracker, and a node that happens to carry no comments is
// the same ordinary state as an issue nobody has replied to. Do not "fix" this
// by inventing a request.
//
// Links stays nil for the reason an epic's does: there is no milestone links
// endpoint either.
func (p *Provider) fetchMilestoneDetail(ctx context.Context, t target, id model.TicketID) (model.Detail, error) {
	milestone, err := p.fetchMilestone(ctx, t)
	if err != nil {
		return model.Detail{}, err
	}
	return model.Detail{
		TicketID:    id,
		Description: milestone.Description,
	}, nil
}

// fetchNotes reads one page of notes, newest first. Nothing paginates; see
// notePageSize.
func (p *Provider) fetchNotes(ctx context.Context, path, resource string) ([]noteWire, error) {
	query := url.Values{
		"per_page": {strconv.Itoa(notePageSize)},
		"sort":     {"desc"},
		"order_by": {"created_at"},
	}
	var notes []noteWire
	if _, err := p.do(ctx, path, query, resource, &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

// do performs one GET and decodes the response into out, returning the response
// headers the page loop reads. It is the single place that knows about auth,
// headers, status handling and decoding, so every request this driver sends
// shares exactly one of each.
//
// resource is what the request was asking for, named in the errors that have
// somewhere to point.
func (p *Provider) do(ctx context.Context, path string, query url.Values, resource string, out any) (http.Header, error) {
	token, err := p.resolveToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + apiBase + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, provider.Errorf(provider.KindUnavailable, "gitlab: building the request: %w", err)
	}
	// Bearer covers both a personal access token and an OAuth token, which is
	// what DefaultTokenSource may return; PRIVATE-TOKEN would work for the
	// former only.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, provider.Errorf(provider.KindUnavailable, "gitlab: requesting %s: %w", apiBase+path, err)
	}
	defer res.Body.Close()

	if err := checkStatus(res, resource, apiBase+path, p.host); err != nil {
		return nil, err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return nil, provider.Errorf(provider.KindUnavailable,
			"gitlab: decoding the response from %s: %w", apiBase+path, err)
	}
	return res.Header, nil
}

// errorBodyLimit bounds how much of a failed response is read looking for an
// error payload. An error page is a line of JSON or an HTML error document; a
// megabyte of either says nothing more than the first kilobyte.
const errorBodyLimit = 64 << 10

// statusError is a checkStatus error that still remembers which status produced
// it. The message is unchanged and is what every caller prints; the code exists
// for the one caller that has to *branch* on it — correlate, which degrades a
// Ticket on a 403 or 404 and fails the whole snapshot on anything else.
//
// It wraps the classified error statusMessage built rather than replacing it,
// so provider.KindOf still reaches the Kind through Unwrap: an HTTP status and
// a failure class are two different questions about one failure, and each has
// exactly one answer here.
type statusError struct {
	status int
	err    error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// checkStatus turns a non-2xx response into the clearest one-line explanation
// the status, the headers and the body support. No retries and no backoff: the
// TUI polls anyway, so a failed refresh is retried by the next tick with the
// user watching, which is better than a driver that silently takes four times
// as long to fail.
//
// host names the instance, which the Premium-403 message needs to spell out a
// milestone URL the user can actually paste.
//
// No message here ever contains a credential.
func checkStatus(res *http.Response, resource, path, host string) error {
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}

	return &statusError{status: res.StatusCode, err: statusMessage(res, resource, path, host)}
}

func statusMessage(res *http.Response, resource, path, host string) error {
	switch res.StatusCode {
	case http.StatusUnauthorized:
		return provider.Errorf(provider.KindAuth, `gitlab: authentication failed (401) — `+
			`check "glab auth status" or $GITLAB_TOKEN`)
	case http.StatusForbidden:
		if isMilestonePath(path) {
			// Milestones are Free tier, so a 403 here is an access problem rather
			// than a tier one, and saying "Premium" would send the user shopping.
			return provider.Errorf(provider.KindAuth,
				"gitlab: access denied (403) to the milestone %s — "+
					"milestones need at least Reporter access", resource)
		}
		if isEpicPath(path) {
			// Epics are a Premium/Ultimate feature and a Free instance answers 403
			// on exactly these paths, so the tier is the overwhelmingly likely
			// cause and the one a user can act on. It is also actionable now that
			// sitrep reports a milestone as an Epic, so the message says how.
			return provider.Errorf(provider.KindAuth,
				"gitlab: epics on %s need GitLab Premium or Ultimate (403) — "+
					"point sitrep at a milestone instead, e.g. https://%s/groups/<group>/-/milestones/<n>",
				resource, host)
		}
		// Everywhere else a 403 is an ordinary permission problem — and the
		// commonest cause is a token that authenticated fine but was created
		// without a scope that may read the API at all.
		return provider.Errorf(provider.KindAuth,
			"gitlab: access denied (403) to %s — your token needs the api or read_api scope", resource)
	case http.StatusNotFound:
		return provider.Errorf(provider.KindBadRef, "gitlab: %s not found (or you lack access)", resource)
	case http.StatusTooManyRequests:
		return provider.Errorf(provider.KindRateLimit,
			"gitlab: API rate limit exceeded; retry after %s", retryAfter(res))
	}

	if msg := errorPayload(res); msg != "" {
		return provider.Errorf(provider.KindUnavailable, "gitlab: API error: %s", msg)
	}
	return provider.Errorf(provider.KindUnavailable,
		"gitlab: unexpected response %d from %s", res.StatusCode, path)
}

// isEpicPath reports whether a request was for one of the epic endpoints, which
// is what makes a 403 there a tier problem rather than a permission problem.
func isEpicPath(path string) bool {
	return strings.Contains(path, "/epics/")
}

// isMilestonePath reports whether a request was for one of the milestone
// endpoints. They are Free tier, so a 403 there is the mirror image of an epic's.
func isMilestonePath(path string) bool {
	return strings.Contains(path, "/milestones")
}

// errorPayload reads GitLab's own error document, or "" when the body is not
// one or says nothing.
func errorPayload(res *http.Response) string {
	body, err := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	if err != nil {
		return ""
	}
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.message()
}

// retryAfter renders when a rate-limited caller may try again, in the order
// GitLab's headers are worth believing: Retry-After when it sends one, then the
// ratelimit-reset unix timestamp, then ratelimit-resettime, which carries the
// same moment as an HTTP date and is sometimes the only one present.
//
// The two reset headers render as a local time, because "3:04PM" is actionable
// and a unix timestamp is not.
func retryAfter(res *http.Response) string {
	if value := strings.TrimSpace(res.Header.Get("Retry-After")); value != "" {
		if secs, err := time.ParseDuration(value + "s"); err == nil && secs > 0 {
			return secs.String()
		}
		return value
	}
	if value := strings.TrimSpace(res.Header.Get("ratelimit-reset")); value != "" {
		if unix, err := strconv.ParseInt(value, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).Local().Format(time.Kitchen)
		}
	}
	if value := strings.TrimSpace(res.Header.Get("ratelimit-resettime")); value != "" {
		if reset, err := http.ParseTime(value); err == nil {
			return reset.Local().Format(time.Kitchen)
		}
	}
	return "an unknown time"
}

// resolveToken resolves the token once and then reuses it for the process
// lifetime: a 60s poll must not re-resolve forever. A *failure* is not cached,
// because discovery can involve a live call — glabAuthToken shells out to
// `glab auth status --show-token`, which a locked keyring or a slow network
// can fail transiently — and a transient failure cached for the process
// lifetime survives only a restart. The mutex also gives the retry
// single-flight: the correlation workers still cost at most one glab
// invocation between them.
func (p *Provider) resolveToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.token != "" {
		return p.token, nil
	}
	token, err := p.tokenSource(ctx, p.host)
	if err != nil {
		// The error is wrapped, never the token.
		return "", provider.Errorf(provider.KindAuth, "gitlab: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", provider.Errorf(provider.KindAuth, "gitlab: %w", errNoToken)
	}
	p.token = token
	return token, nil
}

// The GitLab driver must always satisfy the interface it implements.
var _ provider.Provider = (*Provider)(nil)
