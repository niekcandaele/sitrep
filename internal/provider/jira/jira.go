// Package jira is sitrep's Jira Tracker driver: it turns a Jira Cloud issue and
// the issues that name it as their parent into a normalized Epic with its
// Tickets.
//
// The driver speaks REST/JSON over plain net/http and encoding/json. There is
// no Jira SDK and there will not be one (ADR-0001): hand-rolling a handful of
// requests keeps sitrep free of runtime dependencies and keeps the test seam at
// the HTTP boundary, where fixture payloads can be replayed by a local server.
//
// Everything here is read-only (ADR-0002). Jira's authoritative bulk issue read
// is a POST because its finite key list is a JSON body; it still only reads
// Tracker state. No mutation endpoint exists anywhere in this package.
//
// # REST API v2, with one v3 bulk read
//
// v3 returns descriptions and comment bodies as ADF — the Atlassian Document
// Format, a JSON document tree — which model.Detail cannot hold: it stores raw
// text exactly as the Tracker returned it, unrendered. Epic and Detail reads
// therefore stay on v2. The authoritative Ref-list read uses v3's bulk fetch
// endpoint but selects no description or comments, so ADF never enters the thin
// Ticket model. The visible consequence for Detail is that a Jira description
// reads as Jira wiki markup rather than markdown, and sitrep displays it
// verbatim either way.
//
// # Epic membership is the modern parent field
//
// Children are found through fields.parent, never through the deprecated Epic
// Link custom field, the `"Epic Link" = ABC-1` JQL form, or the agile API.
//
// # Link types are discovered once
//
// The instance's issue link types are read once per process, lazily, and mapped
// onto BlockedBy / Blocks / Relates by their wording. An unrecognized type falls
// back to Relates carrying the instance's own label, and a failed discovery
// falls back to the type object Jira inlines on every link.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// apiBase is where the existing Epic and Detail reads choose REST v2; see the
// package doc for why those paths do not use v3.
const apiBase = "/rest/api/2"

// The paths this driver reads. searchPath is the enhanced search endpoint,
// which pages with a nextPageToken cursor; the removed startAt/total /search
// endpoint is deliberately not used, and there is no fallback that probes both
// — a driver that sends two requests per refresh to find out which one works is
// worse than one that is wrong loudly. bulkFetchPath includes its v3 API base
// because it is the one intentional exception to apiBase.
const (
	searchPath    = "/search/jql"
	linkTypesPath = "/issueLinkType"
	bulkFetchPath = "/rest/api/3/issue/bulkfetch"
)

// epicFields is what the polled path reads: identity, status, the assignee, the
// parent and the project, and nothing else. Descriptions, comments and links
// belong to FetchDetail (ADR-0003).
const epicFields = "summary,status,resolution,assignee,parent,project"

// bulkFetchMaxIssues is Jira Cloud's documented bound for one bulk-fetch body.
// Ref-list membership is already finite, so exceeding it means bounded chunks,
// not search pagination or truncation.
const bulkFetchMaxIssues = 1000

// detailFields is what a drill-in reads. It adds the two expensive fields and
// keeps the identity ones, so a Detail can render its own links' targets.
const detailFields = "description,issuelinks,summary,status,resolution,project"

// pageSize is Jira's maximum page for the search endpoint, and maxPages bounds
// the loop. An epic with five thousand children is a bug somewhere, not a
// situation report, and an unbounded loop against a paging API is how a polling
// tool hangs.
const (
	pageSize = 100
	maxPages = 50
)

// commentPageSize is how many comments a drill-in reads. Nothing paginates:
// one drill-in is one request, and this cap says what a very long discussion
// gives up.
const commentPageSize = 100

// requestTimeout is the default per-request budget when the caller supplies no
// HTTP client of its own.
const requestTimeout = 30 * time.Second

// Provider is the Jira Tracker driver. Construct it with New; the zero value is
// not usable.
type Provider struct {
	host             string
	baseURL          string
	httpClient       *http.Client
	credentials      Credentials
	credentialSource CredentialSource
	userAgent        string
	maxTickets       int

	credentialsOnce sync.Once
	credentialsErr  error

	linkTypesOnce  sync.Once
	linkTypesCache map[string]linkType
	linkTypesErr   error
}

// Option configures a Provider.
type Option func(*Provider)

// New returns a Jira Provider reading from host, a Jira Cloud site such as
// "acme.atlassian.net". Construction is cheap and free of side effects: no
// credential is resolved, no link type is discovered and no request is made
// until the first fetch, so `sitrep --help` never touches Jira.
func New(host string, opts ...Option) *Provider {
	host = strings.TrimSpace(host)
	p := &Provider{
		host:       host,
		baseURL:    "https://" + host,
		httpClient: &http.Client{Timeout: requestTimeout},
		userAgent:  buildinfo.Name + "/" + buildinfo.Version,
		maxTickets: provider.DefaultMaxTickets,
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

// WithBaseURL replaces the site URL derived from the host, everything before
// the /rest/api path. Tests point it at a local server replaying recorded
// payloads.
func WithBaseURL(rawurl string) Option {
	return func(p *Provider) {
		if rawurl != "" {
			p.baseURL = strings.TrimSuffix(rawurl, "/")
		}
	}
}

// WithCredentials sets the email + API token pair to authenticate with. It is
// the ordinary constructor option: internal/cli has both values already,
// resolved from the run's Profile.
func WithCredentials(c Credentials) Option {
	return func(p *Provider) { p.credentials = c }
}

// WithCredentialSource resolves the credential lazily instead. It exists for
// tests and for a future lazy source; the source is called at most once per
// Provider.
func WithCredentialSource(cs CredentialSource) Option {
	return func(p *Provider) {
		if cs != nil {
			p.credentialSource = cs
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

// Name returns "jira".
func (p *Provider) Name() string { return "jira" }

// Capabilities declares what this driver actually returns today.
func (p *Provider) Capabilities() model.Capabilities {
	return model.Capabilities{
		Hierarchy:     true, // fields.parent is how an Epic is assembled
		BlockingLinks: true, // issuelinks, mapped through the instance's link types
		Comments:      true, // the comment endpoint
		// sitrep does not correlate Jira development-panel information to pull
		// requests: it lives behind an undocumented internal endpoint and a
		// per-instance application link, and sitrep will not guess. Ticket
		// .PullRequests is therefore never populated from a branch name or a
		// smart commit either. The capability being off is what makes the pull
		// request section silently absent rather than empty or broken.
		PullRequests: false,
		Selectors: model.SelectorCapabilities{
			Epic: true, RefList: true, Query: true,
		},
	}
}

// Resolve performs the authoritative read for selector. Epic selection keeps the
// existing root-plus-children path; Ref-list selection reads only the exact
// named issues through Jira's bulk endpoint.
func (p *Provider) Resolve(ctx context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	if err := provider.CheckSelectorSupport(p.Name(), p.Capabilities(), selector); err != nil {
		return model.WatchlistSnapshot{}, err
	}
	switch selected := selector.(type) {
	case provider.EpicSelector:
		return p.resolveEpic(ctx, selected.Ref)
	case provider.RefListSelector:
		if len(selected.Refs) == 0 {
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindBadRef,
				"jira: a Ref-list Selector must name at least one Jira issue")
		}
		tickets, err := p.fetchExactRefs(ctx, selected.Refs)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}
		return model.WatchlistSnapshot{
			Header:       provider.RefListHeader(len(tickets)),
			Tickets:      tickets,
			Capabilities: p.Capabilities(),
		}, nil
	case provider.QuerySelector:
		return p.resolveQuery(ctx, selected.Query)
	default:
		panic("provider.CheckSelectorSupport accepted an unknown Selector")
	}
}

func (p *Provider) resolveQuery(ctx context.Context, query string) (model.WatchlistSnapshot, error) {
	initialCapacity := min(p.maxTickets, pageSize)
	refs := make([]ref.Ref, 0, initialCapacity)
	seen := make(map[string]struct{}, initialCapacity)
	seenTokens := make(map[string]struct{})
	token := ""
	consumed := 0
	limitReached := false

	for {
		remaining := p.maxTickets - consumed
		response, err := p.searchQueryMembership(ctx, query, min(pageSize, remaining), token)
		if err != nil {
			return model.WatchlistSnapshot{}, err
		}

		issues := response.Issues
		overflow := len(issues) > remaining
		if overflow {
			issues = issues[:remaining]
		}
		for _, issue := range issues {
			key, ok := normalizeKey(issue.Key)
			if !ok {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"jira: query search returned an issue with invalid key %q", issue.Key)
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, ref.Ref{
				Tracker: ref.TrackerJira,
				Host:    p.host,
				Key:     key,
				Raw:     key,
			})
		}
		consumed += len(issues)
		if token != "" && len(issues) == 0 {
			return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
				"jira: query pagination returned an empty continuation page")
		}

		switch {
		case overflow:
			limitReached = true
		case consumed == p.maxTickets:
			limitReached = !response.IsLast
		case response.IsLast:
		default:
			if len(issues) == 0 {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"jira: query reports more results but returned an empty page")
			}
			if response.NextPageToken == "" {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"jira: query reports more results but returned no new page cursor")
			}
			if _, repeated := seenTokens[response.NextPageToken]; repeated {
				return model.WatchlistSnapshot{}, provider.Errorf(provider.KindUnavailable,
					"jira: query reports more results but repeated a page cursor")
			}
			seenTokens[response.NextPageToken] = struct{}{}
			token = response.NextPageToken
			continue
		}
		break
	}

	tickets := []model.Ticket{}
	if len(refs) > 0 {
		var err error
		tickets, err = p.fetchExactRefs(ctx, refs)
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

func (p *Provider) searchQueryMembership(ctx context.Context, jql string, maxResults int, token string) (searchResponse, error) {
	credentials, err := p.resolveCredentials(ctx)
	if err != nil {
		return searchResponse{}, err
	}

	query := url.Values{
		"jql":        {jql},
		"fields":     {"key"},
		"maxResults": {fmt.Sprint(maxResults)},
	}
	if token != "" {
		query.Set("nextPageToken", token)
	}
	path := apiBase + searchPath
	endpoint := p.baseURL + path + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return searchResponse{}, provider.Errorf(provider.KindUnavailable, "jira: building the request: %w", err)
	}
	req.Header.Set("Authorization", credentials.header())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return searchResponse{}, provider.Errorf(provider.KindUnavailable,
			"jira: requesting %s: %w", p.baseURL+path, provider.RedactedTransportError(err))
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusBadRequest {
		message := queryErrorPayload(res)
		if message == "" {
			message = "the Tracker rejected the query"
		}
		message = provider.RedactQuery(message, jql)
		return searchResponse{}, provider.Errorf(provider.KindBadRef, "jira: query rejected: %s", message)
	}
	if err := checkStatus(res, "query", path, p.host); err != nil {
		return searchResponse{}, provider.RedactQueryError(err, jql)
	}
	var response searchResponse
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return searchResponse{}, provider.Errorf(provider.KindUnavailable,
			"jira: decoding the response from %s: %w", path, err)
	}
	return response, nil
}

// resolveEpic returns the named Epic and every issue naming it as their parent,
// following the search endpoint's cursor to the last page. Cross-project
// children need no special handling: they arrive carrying their own project and
// are attributed to it.
//
// Only one level of the parent graph is fetched — sub-tasks of children are not
// expanded — exactly as the GitHub driver fetches one level of sub-issues.
func (p *Provider) resolveEpic(ctx context.Context, r ref.Ref) (model.WatchlistSnapshot, error) {
	key, err := checkRef(r)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}

	issue, err := p.fetchIssue(ctx, key, epicFields)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}

	epic := newEpic(issue, p.host)
	snap := model.WatchlistSnapshot{
		Header: provider.EpicHeader(epic),
		Epic:   epic,
		// The fetched issue's own parent, for a Ref that turns out to name
		// a plain Ticket. Reporting it is all this driver does about it: which
		// screen that opens is internal/cli's decision (ADR-0003).
		Parent:  newParent(issue.Fields.Parent, p.host),
		Tickets: []model.Ticket{},
	}

	tickets, err := p.fetchChildren(ctx, key)
	if err != nil {
		return model.WatchlistSnapshot{}, err
	}
	snap.Tickets = append(snap.Tickets, tickets...)

	snap.Capabilities = p.Capabilities()
	return snap, nil
}

// FetchDetail returns one Ticket's description, comments and links. id is the
// issue key, which is what Resolve put on every Ticket and what every REST
// path takes.
//
// This is three requests where the polled path is two, and that is the point of
// the split in ADR-0003: a drill-in happens once, deliberately, when a human
// presses Enter, so it can afford the catalogue, the issue and the discussion.
// The catalogue is read at most once per process, so the second drill-in is two
// requests.
//
// An empty description, no comments and no links are the ordinary state of a
// freshly filed Ticket and produce a zero-ish Detail, never an error.
func (p *Provider) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	key, ok := normalizeKey(string(id))
	if !ok {
		return model.Detail{}, provider.Errorf(provider.KindBadRef,
			"jira: %q does not name a Jira issue", string(id))
	}

	// A failed discovery is not a failed Detail: the links fall back to the type
	// object Jira inlines on every entry.
	types, _ := p.linkTypes(ctx)

	issue, err := p.fetchIssue(ctx, key, detailFields)
	if err != nil {
		return model.Detail{}, err
	}

	var comments commentsResponse
	query := url.Values{
		"maxResults": {fmt.Sprint(commentPageSize)},
		"orderBy":    {"-created"},
	}
	if err := p.do(ctx, "/issue/"+key+"/comment", query, key, &comments); err != nil {
		return model.Detail{}, err
	}

	return newDetail(issue, comments.Comments, types, p.host), nil
}

// checkRef rejects a Ref this driver cannot serve before any network call, and
// returns the issue key it names.
func checkRef(r ref.Ref) (string, error) {
	if r.Tracker != ref.TrackerJira {
		return "", provider.Errorf(provider.KindBadRef, "jira: %q is not a Jira Ref", r.Raw)
	}
	key, ok := normalizeKey(r.Key)
	if !ok {
		return "", provider.Errorf(provider.KindBadRef, "jira: %q does not name a Jira issue", r.Raw)
	}
	if strings.TrimSpace(r.Host) == "" {
		// A key names a project, not a site. A Ref that reached this driver
		// without a host means the Profile that matched it did not complete it.
		return "", provider.Errorf(provider.KindBadRef,
			"jira: %q names no Jira site — its Profile is missing a host", r.Raw)
	}
	return key, nil
}

// normalizeKey upper-cases an issue key and validates it against the Ref
// grammar's own key shape. It is both the input check and the injection defence:
// nothing that fails it can reach a JQL string, URL path or bulk-fetch body.
func normalizeKey(s string) (string, bool) {
	key := strings.ToUpper(strings.TrimSpace(s))
	if ref.KeyPrefix(key) == "" {
		return "", false
	}
	return key, true
}

// fetchExactRefs authoritatively reads the finite, already-resolved membership
// in refs. It is deliberately separate from Selector and Header handling so a
// future membership selector can reuse the same direct-read path.
func (p *Provider) fetchExactRefs(ctx context.Context, refs []ref.Ref) ([]model.Ticket, error) {
	keys := make([]string, len(refs))
	for i, r := range refs {
		key, err := checkExactRef(r)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}

	tickets := make([]model.Ticket, 0, len(keys))
	for start := 0; start < len(keys); start += bulkFetchMaxIssues {
		end := min(start+bulkFetchMaxIssues, len(keys))
		chunk := keys[start:end]
		request := bulkFetchRequest{
			IssueIDsOrKeys: chunk,
			Fields:         strings.Split(epicFields, ","),
		}

		var response bulkFetchResponse
		if err := p.doBulkFetch(ctx, request, chunk[0], &response); err != nil {
			return nil, err
		}

		issues := make(map[string]issueWire, len(response.Issues))
		for _, issue := range response.Issues {
			key, ok := normalizeKey(issue.Key)
			if !ok {
				return nil, provider.Errorf(provider.KindUnavailable,
					"jira: bulk fetch returned an issue with invalid key %q", issue.Key)
			}
			issues[key] = issue
		}

		missingKey := ""
		for _, key := range chunk {
			if _, ok := issues[key]; !ok {
				missingKey = key
				break
			}
		}
		if len(response.IssueErrors) > 0 {
			key := missingKey
			if key == "" {
				// A successful issue and an error for the same request would be a
				// contradictory response. It still cannot become partial output.
				key = chunk[0]
				if candidate, ok := normalizeKey(response.IssueErrors[0].ID); ok {
					key = candidate
				}
			}
			return nil, provider.Errorf(provider.KindBadRef,
				"jira: %s not found (or you lack access)", key)
		}
		if missingKey != "" {
			// Jira can omit missing or inaccessible issues without adding an
			// issueErrors entry. Exact membership still makes that a bad Ref.
			return nil, provider.Errorf(provider.KindBadRef,
				"jira: %s not found (or you lack access)", missingKey)
		}

		for _, key := range chunk {
			tickets = append(tickets, newTicket(issues[key], p.host, ""))
		}
	}
	return tickets, nil
}

// checkExactRef validates every explicit member before the first request. The
// Ref-list seam promises one completed Jira Ref per member, but validating here
// keeps malformed or injected keys away from both JSON bodies and Tracker I/O.
func checkExactRef(r ref.Ref) (string, error) {
	if r.Tracker != ref.TrackerJira {
		return "", provider.Errorf(provider.KindBadRef,
			"jira: %q is not a Jira issue Ref", r.Raw)
	}
	key, ok := normalizeKey(r.Key)
	if !ok {
		return "", provider.Errorf(provider.KindBadRef,
			"jira: %q does not name a Jira issue", r.Raw)
	}
	if strings.TrimSpace(r.Host) == "" {
		return "", provider.Errorf(provider.KindBadRef,
			"jira: %q names no Jira site — its Profile is missing a host", r.Raw)
	}
	return key, nil
}

// doBulkFetch performs Jira's read-only JSON POST. Existing Epic and Detail
// requests continue through do unchanged, retaining their v2 URLs, GET methods,
// headers and error wording.
func (p *Provider) doBulkFetch(
	ctx context.Context,
	body bulkFetchRequest,
	resource string,
	out *bulkFetchResponse,
) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return provider.Errorf(provider.KindUnavailable,
			"jira: encoding the bulk fetch request: %w", err)
	}

	credentials, err := p.resolveCredentials(ctx)
	if err != nil {
		return err
	}

	endpoint := p.baseURL + bulkFetchPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return provider.Errorf(provider.KindUnavailable, "jira: building the request: %w", err)
	}
	req.Header.Set("Authorization", credentials.header())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return provider.Errorf(provider.KindUnavailable,
			"jira: requesting %s: %w", endpoint, err)
	}
	defer res.Body.Close()

	if err := checkStatus(res, resource, bulkFetchPath, p.host); err != nil {
		return err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return provider.Errorf(provider.KindUnavailable,
			"jira: decoding the response from %s: %w", bulkFetchPath, err)
	}
	return nil
}

// fetchIssue reads one issue with the given field selection.
func (p *Provider) fetchIssue(ctx context.Context, key, fields string) (issueWire, error) {
	var issue issueWire
	query := url.Values{"fields": {fields}}
	if err := p.do(ctx, "/issue/"+key, query, key, &issue); err != nil {
		return issueWire{}, err
	}
	return issue, nil
}

// childJQL is the children query. Epic membership is the modern `parent` field:
// never the deprecated Epic Link custom field, never the `"Epic Link" = ABC-1`
// JQL form, and never /rest/agile/1.0/epic/{key}/issue. Do not "fix" this.
//
// The key is validated by normalizeKey before it gets here, so what is
// interpolated is a project key, a hyphen and a number and nothing else.
func childJQL(key string) string {
	return `parent = "` + key + `" ORDER BY created ASC`
}

// fetchChildren pages the children search to the last page.
func (p *Provider) fetchChildren(ctx context.Context, key string) ([]model.Ticket, error) {
	tickets := []model.Ticket{}
	var token string

	for page := 0; ; page++ {
		if page >= maxPages {
			// A collection larger than sitrep's cap is a stable property of the
			// ref: it will be exactly as large on the next tick, so retrying
			// buys nothing.
			return nil, provider.Errorf(provider.KindBadRef,
				"jira: %s has more than %d children; refusing to keep paging",
				key, maxPages*pageSize)
		}

		query := url.Values{
			"jql":        {childJQL(key)},
			"fields":     {epicFields},
			"maxResults": {fmt.Sprint(pageSize)},
		}
		if token != "" {
			query.Set("nextPageToken", token)
		}

		var resp searchResponse
		if err := p.do(ctx, searchPath, query, key, &resp); err != nil {
			return nil, err
		}
		for _, issue := range resp.Issues {
			tickets = append(tickets, newTicket(issue, p.host, key))
		}

		if resp.IsLast {
			return tickets, nil
		}
		if resp.NextPageToken == "" {
			// Jira says there is more and hands over no way to ask for it. The
			// GitHub and GitLab drivers both refuse an inconsistent cursor rather
			// than returning a partial epic that looks complete, and a situation
			// report silently missing children is worse than one that failed.
			// A server contradicting itself is a server fault, so it stays
			// retryable on the same terms as a 500.
			return nil, provider.Errorf(provider.KindUnavailable,
				"jira: %s reports more children but returned no page cursor", key)
		}
		if resp.NextPageToken == token {
			return nil, provider.Errorf(provider.KindUnavailable,
				"jira: %s reports more children but returned no new page cursor", key)
		}
		token = resp.NextPageToken
	}
}

// do performs one v2 GET and decodes the response into out. It is the single
// place that knows about auth, headers, status handling and decoding for the
// existing Epic and Detail requests, so those paths share exactly one of each.
// The read-only v3 bulk POST is deliberately narrow and lives in doBulkFetch.
//
// resource is what the request was asking for, named in the errors that have
// somewhere to point.
func (p *Provider) do(ctx context.Context, path string, query url.Values, resource string, out any) error {
	credentials, err := p.resolveCredentials(ctx)
	if err != nil {
		return err
	}

	endpoint := p.baseURL + apiBase + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.Errorf(provider.KindUnavailable, "jira: building the request: %w", err)
	}
	req.Header.Set("Authorization", credentials.header())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return provider.Errorf(provider.KindUnavailable,
			"jira: requesting %s: %w", p.baseURL+apiBase+path, err)
	}
	defer res.Body.Close()

	if err := checkStatus(res, resource, apiBase+path, p.host); err != nil {
		return err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return provider.Errorf(provider.KindUnavailable,
			"jira: decoding the response from %s: %w", apiBase+path, err)
	}
	return nil
}

// errorBodyLimit bounds how much of a failed response is read looking for an
// error payload. An error page is a line of JSON or an HTML error document; a
// megabyte of either says nothing more than the first kilobyte.
const errorBodyLimit = 64 << 10

// checkStatus turns a non-200 response into the clearest one-line explanation
// the status, the headers and the body support. No retries and no backoff: the
// TUI polls anyway.
//
// The status-specific wordings win over the body's own message, because Jira's
// errorMessages for a 401 or a 404 say less than sitrep can say about what the
// user should do next. An error payload is what a status with no specific
// wording falls back to.
//
// host names the site, which the CAPTCHA message needs so the user knows which
// browser tab to open.
func checkStatus(res *http.Response, resource, path, host string) error {
	if res.StatusCode == http.StatusOK {
		return nil
	}

	switch res.StatusCode {
	case http.StatusUnauthorized:
		return provider.Errorf(provider.KindAuth,
			"jira: authentication failed (401) — check the Atlassian email "+
				"and API token your Profile names")
	case http.StatusForbidden:
		if isCaptchaChallenge(res.Header) {
			// Atlassian answers 403 with this header after repeated failed
			// logins, and no credential change clears it — only a browser does.
			// The generic access-denied wording would send the user hunting
			// through permissions that are fine.
			return provider.Errorf(provider.KindAuth,
				"jira: Atlassian is challenging this login with a CAPTCHA — "+
					"log in to %s in a browser to clear it, then retry", host)
		}
		return provider.Errorf(provider.KindAuth, "jira: access denied (403) to %s", resource)
	case http.StatusNotFound:
		return provider.Errorf(provider.KindBadRef, "jira: %s not found (or you lack access)", resource)
	case http.StatusTooManyRequests:
		return provider.Errorf(provider.KindRateLimit,
			"jira: API rate limit exceeded; retry after %s", retryAfter(res))
	}

	if msg := errorPayload(res); msg != "" {
		return provider.Errorf(provider.KindUnavailable, "jira: API error: %s", msg)
	}
	return provider.Errorf(provider.KindUnavailable,
		"jira: unexpected response %d from %s", res.StatusCode, path)
}

// captchaHeader is the header Atlassian sets on a 403 raised by its CAPTCHA
// challenge, documented for Jira Server and Data Center and sent by Jira Cloud's
// basic-auth path too. Its value is "CAPTCHA_CHALLENGE; login-url=…", so this
// matches on the prefix and never echoes the value: the login URL it carries is
// the site's own login page, which the message already names by host.
const captchaHeader = "X-Authentication-Denied-Reason"

func isCaptchaChallenge(header http.Header) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(header.Get(captchaHeader))),
		"CAPTCHA_CHALLENGE")
}

// errorPayload reads Jira's own general error messages, or "" when the body is
// not an error document or says nothing.
func errorPayload(res *http.Response) string {
	return readErrorPayload(res).message()
}

// queryErrorPayload also reads per-field explanations because malformed JQL is
// reported in Jira's errors map rather than errorMessages on some deployments.
func queryErrorPayload(res *http.Response) string {
	return readErrorPayload(res).queryMessage()
}

func readErrorPayload(res *http.Response) errorResponse {
	body, err := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))
	if err != nil {
		return errorResponse{}
	}
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return errorResponse{}
	}
	return payload
}

// retryAfter renders when a rate-limited caller may try again: the Retry-After
// header when Jira sends one — it is in seconds — else the X-RateLimit-Reset
// timestamp, which Jira Cloud sends instead on some 429s and which is an RFC
// 3339 moment rather than a duration.
func retryAfter(res *http.Response) string {
	if value := strings.TrimSpace(res.Header.Get("Retry-After")); value != "" {
		if secs, err := time.ParseDuration(value + "s"); err == nil && secs > 0 {
			return secs.String()
		}
		return value
	}
	if value := strings.TrimSpace(res.Header.Get("X-RateLimit-Reset")); value != "" {
		// Rendered in the user's own timezone, because "13:32 CEST" is
		// actionable and a UTC timestamp read at a glance is not.
		if reset, err := time.Parse(time.RFC3339, value); err == nil {
			return reset.Local().Format(time.RFC1123)
		}
	}
	return "an unknown time"
}

// resolveCredentials resolves the email + token pair once per Provider. A 60s
// poll must not re-resolve forever, and a credential that could not be found
// once will not be found on the next tick either.
//
// Both halves are required, and a missing one is reported at the first request
// rather than at construction — construction is free of side effects, and a
// `sitrep --help` that demanded a token would be absurd.
func (p *Provider) resolveCredentials(ctx context.Context) (Credentials, error) {
	p.credentialsOnce.Do(func() {
		if p.credentialSource != nil {
			credentials, err := p.credentialSource(ctx, p.host)
			if err != nil {
				p.credentialsErr = provider.Errorf(provider.KindAuth, "jira: %w", err)
				return
			}
			p.credentials = credentials
		}
		p.credentials.Email = strings.TrimSpace(p.credentials.Email)
		p.credentials.Token = strings.TrimSpace(p.credentials.Token)

		switch {
		case p.credentials.Token == "":
			p.credentialsErr = missingTokenError(p.host)
		case p.credentials.Email == "":
			p.credentialsErr = missingEmailError(p.host)
		}
	})
	return p.credentials, p.credentialsErr
}

// The Jira driver must always satisfy the interface it implements.
var _ provider.Provider = (*Provider)(nil)
