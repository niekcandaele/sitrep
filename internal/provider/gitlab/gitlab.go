// Package gitlab is sitrep's GitLab Tracker driver: it turns a GitLab group
// epic and its child issues — or a single project issue — into a normalized
// Epic with its Tickets.
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
// model.TicketID here encodes the addressed node: "issue:{project path}#{iid}"
// or "epic:{group path}&{iid}". A GitLab issue iid is meaningless without its
// project — iids restart at 1 in every project — and FetchDetail receives
// nothing else. It is Provider-scoped and opaque by contract; nothing outside
// this package may parse it.
//
// # Merge requests
//
// This driver correlates none. Capabilities().PullRequests is false, so the
// section is silently absent rather than empty, and no Status Category is ever
// InProgress. #15 turns all of that on together.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// hangs.
const (
	pageSize = 100
	maxPages = 50
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

	tokenOnce sync.Once
	token     string
	tokenErr  error
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
// used for an Epic Ref that names none, such as the bare reference form "&12".
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

// Name returns "gitlab". It is the --provider value and the provider field in
// --json.
func (p *Provider) Name() string { return "gitlab" }

// Capabilities declares what this driver actually returns today.
func (p *Provider) Capabilities() model.Capabilities {
	return model.Capabilities{
		Hierarchy:     true,  // an epic's child issues are how an Epic is assembled
		BlockingLinks: true,  // the issue links endpoint, with its own link_type
		Comments:      true,  // notes, minus GitLab's system notes
		PullRequests:  false, // #15 correlates merge requests; until it does, the section is silently absent
	}
}

// FetchEpic returns the Epic named by r and, for a group epic, every child
// issue GitLab lists under it.
//
// A project issue Ref comes back with no Tickets, the issue's own identity on
// Epic and its epic on Parent: GitLab's hierarchy in v1 is epic → issues, and
// an issue's own child work items (tasks) are not expanded. internal/cli decodes
// that into a Detail screen; the driver never decides which screen opens
// (ADR-0003).
//
// Child epics are not expanded either — whatever the epic-issues endpoint
// returns is exactly what sitrep shows.
func (p *Provider) FetchEpic(ctx context.Context, r ref.Ref) (model.EpicSnapshot, error) {
	t, err := targetFor(r, p.path)
	if err != nil {
		return model.EpicSnapshot{}, err
	}

	// Tickets starts non-nil so an epic with no children renders as "no
	// Tickets" rather than as null.
	snap := model.EpicSnapshot{Tickets: []model.Ticket{}, Capabilities: p.Capabilities()}

	if t.kind == kindIssue {
		var issue issueWire
		if _, err := p.do(ctx, t.issuePath(), nil, t.String(), &issue); err != nil {
			return model.EpicSnapshot{}, err
		}
		snap.Epic = newEpicFromIssue(issue)
		snap.Parent = newParentFromIssue(issue.Epic, p.host)
		return snap, nil
	}

	var epic epicWire
	if _, err := p.do(ctx, t.epicPath(), nil, t.String(), &epic); err != nil {
		return model.EpicSnapshot{}, err
	}
	snap.Epic = newEpicFromEpic(epic, p.host, t.path)
	snap.Parent = newParentFromEpic(epic, p.host, t.path)

	tickets, err := p.fetchChildren(ctx, t, snap.Epic.ID)
	if err != nil {
		return model.EpicSnapshot{}, err
	}
	snap.Tickets = append(snap.Tickets, tickets...)

	// FetchedAt is left zero for the caller to stamp.
	return snap, nil
}

// fetchChildren pages the epic's issues to the last page, in GitLab's own
// order.
func (p *Provider) fetchChildren(ctx context.Context, t target, parentID model.TicketID) ([]model.Ticket, error) {
	var tickets []model.Ticket

	page := 1
	for i := 0; ; i++ {
		if i >= maxPages {
			return nil, fmt.Errorf("gitlab: %s has more than %d children; refusing to keep paging",
				t, maxPages*pageSize)
		}

		query := url.Values{
			"per_page": {strconv.Itoa(pageSize)},
			"page":     {strconv.Itoa(page)},
		}
		var issues []issueWire
		header, err := p.do(ctx, t.epicIssuesPath(), query, t.String(), &issues)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			tickets = append(tickets, newTicketFromIssue(issue, parentID))
		}

		next := strings.TrimSpace(header.Get("x-next-page"))
		if next == "" {
			return tickets, nil
		}
		n, err := strconv.Atoi(next)
		if err != nil || n <= page {
			// A next page that is not a later page is a paging API contradicting
			// itself; following it is how a polling tool spins forever.
			return nil, fmt.Errorf("gitlab: %s reports its next page of children as %q, "+
				"which is not after page %d", t, next, page)
		}
		page = n
	}
}

// FetchDetail returns one Ticket's description, comments and, for an issue, its
// links. id is what FetchEpic put on every Ticket; see the package doc for what
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
		return nil, fmt.Errorf("gitlab: building the request: %w", err)
	}
	// Bearer covers both a personal access token and an OAuth token, which is
	// what DefaultTokenSource may return; PRIVATE-TOKEN would work for the
	// former only.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: requesting %s: %w", apiBase+path, err)
	}
	defer res.Body.Close()

	if err := checkStatus(res, resource, apiBase+path); err != nil {
		return nil, err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("gitlab: decoding the response from %s: %w", apiBase+path, err)
	}
	return res.Header, nil
}

// errorBodyLimit bounds how much of a failed response is read looking for an
// error payload. An error page is a line of JSON or an HTML error document; a
// megabyte of either says nothing more than the first kilobyte.
const errorBodyLimit = 64 << 10

// checkStatus turns a non-2xx response into the clearest one-line explanation
// the status, the headers and the body support. No retries and no backoff: the
// TUI polls anyway, so a failed refresh is retried by the next tick with the
// user watching, which is better than a driver that silently takes four times
// as long to fail.
//
// No message here ever contains a credential.
func checkStatus(res *http.Response, resource, path string) error {
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}

	switch res.StatusCode {
	case http.StatusUnauthorized:
		return errors.New(`gitlab: authentication failed (401) — ` +
			`check "glab auth status" or $GITLAB_TOKEN`)
	case http.StatusForbidden:
		if isEpicPath(path) {
			// Epics are a Premium/Ultimate feature and a Free instance answers 403
			// on exactly these paths, so the tier is the overwhelmingly likely
			// cause and the one a user can act on.
			return fmt.Errorf("gitlab: epics on %s need GitLab Premium or Ultimate (403)", resource)
		}
		return fmt.Errorf("gitlab: access denied (403) to %s", resource)
	case http.StatusNotFound:
		return fmt.Errorf("gitlab: %s not found (or you lack access)", resource)
	case http.StatusTooManyRequests:
		return fmt.Errorf("gitlab: API rate limit exceeded; retry after %s", retryAfter(res))
	}

	if msg := errorPayload(res); msg != "" {
		return fmt.Errorf("gitlab: API error: %s", msg)
	}
	return fmt.Errorf("gitlab: unexpected response %d from %s", res.StatusCode, path)
}

// isEpicPath reports whether a request was for one of the epic endpoints, which
// is what makes a 403 there a tier problem rather than a permission problem.
func isEpicPath(path string) bool {
	return strings.Contains(path, "/epics/")
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

// retryAfter renders when a rate-limited caller may try again: the Retry-After
// header when GitLab sends one, else its ratelimit-reset unix timestamp.
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
	return "an unknown time"
}

// resolveToken fetches the token once per Provider. A 60s poll must not
// re-resolve forever, and a token that could not be found once will not be
// found on the next tick either.
func (p *Provider) resolveToken(ctx context.Context) (string, error) {
	p.tokenOnce.Do(func() {
		token, err := p.tokenSource(ctx, p.host)
		if err != nil {
			// The error is wrapped, never the token.
			p.tokenErr = fmt.Errorf("gitlab: %w", err)
			return
		}
		if strings.TrimSpace(token) == "" {
			p.tokenErr = fmt.Errorf("gitlab: %w", errNoToken)
			return
		}
		p.token = strings.TrimSpace(token)
	})
	return p.token, p.tokenErr
}

// The GitLab driver must always satisfy the interface it implements.
var _ provider.Provider = (*Provider)(nil)
