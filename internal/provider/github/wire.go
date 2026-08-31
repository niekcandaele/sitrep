package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// The response structs mirror GitHub's API vocabulary — GraphQL says `issue`,
// so these say issue. Everything sitrep owns downstream of newTicket says
// Ticket.

type graphQLResponse struct {
	Data struct {
		Repository *struct {
			Issue *issueNode `json:"issue"`
			// Kind names what the number actually is. GitHub shares one number
			// namespace between issues and pull requests, so `issue(number:)`
			// is null for a pull request — and "not found" is then a false
			// claim about a link the user can open in a browser.
			Kind *struct {
				TypeName string `json:"__typename"`
			} `json:"kind"`
		} `json:"repository"`
		RateLimit rateLimitNode `json:"rateLimit"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type queryResponse struct {
	Data struct {
		Search struct {
			IssueCount int `json:"issueCount"`
			PageInfo   struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []queryMembershipNode `json:"nodes"`
		} `json:"search"`
		RateLimit rateLimitNode `json:"rateLimit"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type queryMembershipNode struct {
	TypeName   string        `json:"__typename"`
	Number     int           `json:"number"`
	Repository repositoryRef `json:"repository"`
}

func (r queryResponse) err(endpoint string, header http.Header, query string) error {
	if len(r.Errors) == 0 {
		return nil
	}
	for _, graphErr := range r.Errors {
		if isNativeQueryError(graphErr) {
			message := strings.TrimSpace(graphErr.Message)
			if message == "" {
				message = "the Tracker rejected the query"
			}
			message = provider.RedactQuery(message, query)
			return provider.Errorf(provider.KindBadRef, "github: query rejected: %s", message)
		}
	}
	redacted := append([]graphQLError(nil), r.Errors...)
	for i := range redacted {
		redacted[i].Message = provider.RedactQuery(redacted[i].Message, query)
	}
	return graphQLErrors(redacted, "github: query returned no searchable issues", endpoint, header)
}

func isNativeQueryError(graphErr graphQLError) bool {
	switch strings.ToUpper(strings.TrimSpace(graphErr.Type)) {
	case "SEARCH_QUERY_ERROR":
		return true
	case "BAD_USER_INPUT", "UNPROCESSABLE":
		return len(graphErr.Path) > 0 && graphErr.Path[0] == "search"
	default:
		return false
	}
}

type refListResponse struct {
	Data      map[string]*refListRepository `json:"data"`
	RateLimit rateLimitNode                 `json:"-"`
	Errors    []graphQLError                `json:"errors"`
}

func (r *refListResponse) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []graphQLError             `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	r.Data = make(map[string]*refListRepository, len(envelope.Data))
	r.Errors = envelope.Errors
	for key, raw := range envelope.Data {
		if key == "rateLimit" {
			if err := json.Unmarshal(raw, &r.RateLimit); err != nil {
				return err
			}
			continue
		}
		var repository *refListRepository
		if err := json.Unmarshal(raw, &repository); err != nil {
			return err
		}
		r.Data[key] = repository
	}
	return nil
}

type refListRepository struct {
	Issue *issueNode `json:"issue"`
	Kind  *struct {
		TypeName string `json:"__typename"`
	} `json:"kind"`
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

type repositoryRef struct {
	NameWithOwner string `json:"nameWithOwner"`
}

type issueNode struct {
	ID          string        `json:"id"`
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	URL         string        `json:"url"`
	State       string        `json:"state"`
	StateReason string        `json:"stateReason"`
	Repository  repositoryRef `json:"repository"`
	Parent      *parentNode   `json:"parent"`
	Assignees   struct {
		Nodes []assigneeNode `json:"nodes"`
	} `json:"assignees"`
	ClosedByPullRequestsReferences *pullRequestConnection    `json:"closedByPullRequestsReferences"`
	CrossReferences                *crossReferenceConnection `json:"crossReferences"`
	SubIssues                      struct {
		TotalCount int `json:"totalCount"`
		PageInfo   struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []issueNode `json:"nodes"`
	} `json:"subIssues"`
}

// parentNode is the issue an issue is a sub-issue of. It is nullable: most
// issues hang off nothing, which is an ordinary state and never an error.
type parentNode struct {
	ID         string        `json:"id"`
	Number     int           `json:"number"`
	Title      string        `json:"title"`
	URL        string        `json:"url"`
	Repository repositoryRef `json:"repository"`
}

type assigneeNode struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}

// detailResponse is the Detail query's answer. It is an honest small duplicate
// of graphQLResponse rather than a generic parameter over it: the two documents
// return different shapes, and a wire layer reads better as two literal structs
// than as one abstract one. The error handling is shared, not copied — both
// hand off to graphQLErrors.
type detailResponse struct {
	Data struct {
		Node *detailNode `json:"node"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

// detailBatchResponse is keyed by the generated detailN aliases. A map keeps
// response-object order irrelevant while allowing missing and unexpected aliases
// to be distinguished explicitly.
type detailBatchResponse struct {
	Data   map[string]*detailNode `json:"data"`
	Errors []graphQLError         `json:"errors"`
}

// decode classifies a completed aliased response. An error attributable to one
// alias invalidates that whole Detail while successful siblings survive. An
// error or data member that cannot be attributed invalidates the entire chunk;
// request-wide errors retain their native classification.
func (r detailBatchResponse) decode(ids []model.TicketID, endpoint string, header http.Header) (map[model.TicketID]model.Detail, map[model.TicketID]error, error) {
	aliases := make(map[string]struct{}, len(ids))
	for i := range ids {
		aliases["detail"+strconv.Itoa(i)] = struct{}{}
	}
	for alias := range r.Data {
		if _, expected := aliases[alias]; !expected {
			return nil, nil, provider.Errorf(provider.KindUnavailable,
				"github: malformed Detail response from %s: unexpected data alias %q", endpoint, alias)
		}
	}

	attributedErrors := make(map[string][]graphQLError)
	unattributedErrors := make([]graphQLError, 0, len(r.Errors))
	for _, graphErr := range r.Errors {
		if len(graphErr.Path) == 0 {
			unattributedErrors = append(unattributedErrors, graphErr)
			continue
		}
		alias, ok := graphErr.Path[0].(string)
		if !ok {
			unattributedErrors = append(unattributedErrors, graphErr)
			continue
		}
		if _, expected := aliases[alias]; !expected {
			unattributedErrors = append(unattributedErrors, graphErr)
			continue
		}
		attributedErrors[alias] = append(attributedErrors[alias], graphErr)
	}

	details := make(map[model.TicketID]model.Detail, len(ids))
	failures := make(map[model.TicketID]error)
	if len(unattributedErrors) != 0 {
		for _, id := range ids {
			failures[id] = detailBatchUnattributedError(unattributedErrors, id, endpoint, header)
		}
		return details, failures, nil
	}
	for i, id := range ids {
		alias := "detail" + strconv.Itoa(i)
		if graphErrs := attributedErrors[alias]; len(graphErrs) != 0 {
			failures[id] = graphQLErrors(graphErrs, detailNotFound(id), endpoint, header)
			continue
		}

		node, present := r.Data[alias]
		if !present {
			failures[id] = provider.Errorf(provider.KindUnavailable,
				"github: malformed Detail response from %s: missing data alias %q for %q", endpoint, alias, id)
			continue
		}
		if node == nil || node.ID == "" {
			failures[id] = provider.Errorf(provider.KindBadRef, "%s", detailNotFound(id))
			continue
		}
		detail := newDetail(*node)
		if detail.TicketID != id {
			failures[id] = fmt.Errorf("provider: detail for %q returned TicketID %q", id, detail.TicketID)
			continue
		}
		details[id] = detail
	}
	return details, failures, nil
}

// detailBatchUnattributedError classifies only errors that cannot belong to a
// generated alias. NOT_FOUND has no authoritative Ticket identity on this path,
// so it is malformed response data rather than a bad requested ID. Native
// request-wide auth and rate-limit classifications still take precedence.
func detailBatchUnattributedError(errs []graphQLError, id model.TicketID, endpoint string, header http.Header) error {
	normalized := append([]graphQLError(nil), errs...)
	hasNotFound := false
	for i := range normalized {
		if strings.EqualFold(normalized[i].Type, "NOT_FOUND") {
			hasNotFound = true
			normalized[i].Type = ""
		}
	}
	classified := graphQLErrors(normalized, detailNotFound(id), endpoint, header)
	if !hasNotFound || provider.KindOf(classified) != provider.KindUnknown {
		return classified
	}

	messages := make([]string, 0, len(errs))
	for _, graphErr := range errs {
		if graphErr.Message != "" {
			messages = append(messages, graphErr.Message)
		}
	}
	explanation := strings.Join(messages, "; ")
	if explanation == "" {
		explanation = "the API returned no explanation"
	}
	return provider.Errorf(provider.KindUnavailable,
		"github: malformed Detail response for %q from %s: unattributable NOT_FOUND error: %s",
		id, endpoint, explanation)
}

type detailNode struct {
	ID         string        `json:"id"`
	Number     int           `json:"number"`
	URL        string        `json:"url"`
	Body       string        `json:"body"`
	Repository repositoryRef `json:"repository"`
	Comments   struct {
		TotalCount int           `json:"totalCount"`
		Nodes      []commentNode `json:"nodes"`
	} `json:"comments"`
	BlockedBy issueConnection `json:"blockedBy"`
	Blocking  issueConnection `json:"blocking"`
}

// issueConnection is a dependency connection's nodes. Its members are ordinary
// issue nodes, so they carry the same fields the epic query's children do and
// run through the same issueKey and normalizeStatus.
type issueConnection struct {
	Nodes []issueNode `json:"nodes"`
}

// commentNode is one issue comment. author is nullable — a deleted account
// arrives as null — and is an Actor, so name and avatarUrl are absent for a bot.
type commentNode struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	Author    *actorNode `json:"author"`
}

type actorNode struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}

// err turns a GraphQL errors[] payload into one line. A NOT_FOUND entry is the
// same situation as a null issue and reads better said plainly.
func (r graphQLResponse) err(target ref.Ref, endpoint string, header http.Header) error {
	return graphQLErrors(r.Errors,
		fmt.Sprintf("github: %s not found (or you lack access)", refKey(target)), endpoint, header)
}

// err applies the existing GraphQL classification while naming the Ref whose
// alias GitHub attached to a NOT_FOUND error. If GitHub omits a path, the first
// Ref is the stable fallback rather than an unordered response-map member.
func (r refListResponse) err(targets []ref.Ref, endpoint string, header http.Header) error {
	target := targets[0]
	for _, graphErr := range r.Errors {
		if !strings.EqualFold(graphErr.Type, "NOT_FOUND") || len(graphErr.Path) == 0 {
			continue
		}
		alias, ok := graphErr.Path[0].(string)
		if !ok || !strings.HasPrefix(alias, "ref") {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(alias, "ref"))
		if err == nil && index >= 0 && index < len(targets) {
			target = targets[index]
			break
		}
	}
	return graphQLErrors(r.Errors,
		fmt.Sprintf("github: %s not found (or you lack access)", refKey(target)), endpoint, header)
}

// err turns the Detail query's errors[] payload into one line, naming the node
// id the caller asked for rather than a Ref it does not have.
func (r detailResponse) err(id model.TicketID, endpoint string, header http.Header) error {
	return graphQLErrors(r.Errors, detailNotFound(id), endpoint, header)
}

// detailNotFound is the one wording for "that Ticket did not come back",
// whether the node was null, was not an Issue, or came back as a NOT_FOUND
// error entry. They are the same situation to the person reading the screen.
func detailNotFound(id model.TicketID) string {
	return fmt.Sprintf("github: no ticket found for %q (or you lack access)", id)
}

// graphQLErrors renders an errors[] payload. notFound is what a NOT_FOUND entry
// means for the document that was sent, which is the only thing the two
// documents word differently.
//
// GitHub answers HTTP 200 with one of these for an exhausted GraphQL point
// budget, for a refused scope and for a missing node alike, so this — not
// checkStatus — is where those situations are told apart. header carries the
// response's rate-limit headers, because a RATE_LIMITED entry says nothing
// about when the budget refills and the headers do.
//
// It stays a pure function of its arguments so the wire tests can stay
// table-driven.
func graphQLErrors(errs []graphQLError, notFound, endpoint string, header http.Header) error {
	if len(errs) == 0 {
		return nil
	}

	kind := provider.KindUnknown
	for _, e := range errs {
		switch {
		case strings.EqualFold(e.Type, "RATE_LIMITED"):
			return provider.Errorf(provider.KindRateLimit,
				"github: API rate limit exceeded; resets at %s", rateLimitReset(header))
		case strings.EqualFold(e.Type, "NOT_FOUND"):
			return provider.Errorf(provider.KindBadRef, "%s", notFound)
		case isAuthErrorType(e.Type):
			// GitHub's own sentence about a refused scope says more than sitrep
			// could, so the wording is left exactly as it was and only the
			// classification is added: the next poll will not fix a scope.
			kind = provider.KindAuth
		}
	}

	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Message != "" {
			messages = append(messages, e.Message)
		}
	}
	if len(messages) == 0 {
		return provider.Errorf(kind, "github: API error from %s", endpoint)
	}
	return provider.Errorf(kind, "github: API error: %s", strings.Join(messages, "; "))
}

// isAuthErrorType reports whether a GraphQL error type is GitHub refusing the
// credential rather than failing to serve the query.
func isAuthErrorType(t string) bool {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "FORBIDDEN", "UNAUTHORIZED", "INSUFFICIENT_SCOPES":
		return true
	default:
		return false
	}
}

// newEpic maps the fetched issue onto sitrep's Epic. Its Key is repo-qualified
// because an Epic carries no repository field of its own and the qualified form
// is what a human can paste straight back into sitrep.
//
// The assignees and pull requests are the same selections every child already
// carries, read here because a Ref may name a plain Ticket: the decoded
// Ticket's Detail header is built from this Epic and has to read exactly the way
// the same Ticket's row reads in a list. That is also why the pull-request rule
// for in-progress runs here as it does in newTicket — one node must not describe
// itself two different ways depending on which Ref reached it.
func newEpic(n issueNode) model.Epic {
	status, native := normalizeStatus(n.State, n.StateReason)
	prs := newPullRequests(n.ClosedByPullRequestsReferences, n.CrossReferences)
	return model.Epic{
		ID:           model.TicketID(n.ID),
		Key:          issueKey(n, ""),
		Title:        n.Title,
		URL:          n.URL,
		Status:       statusWithPullRequests(status, prs),
		NativeStatus: native,
		Assignees:    newAssignees(n.Assignees.Nodes),
		Repository:   n.Repository.NameWithOwner,
		PullRequests: prs,
		// The closing connection is capped and never paginated, so the count
		// GitHub reports is what makes the cap visible downstream.
		PullRequestTotal: pullRequestTotal(n.ClosedByPullRequestsReferences, prs),
	}
}

// newParent maps the fetched issue's own parent onto sitrep's Parent. issueRepo
// is the fetched issue's repository, which decides whether the parent's display
// key needs qualifying — the same rule the children and the Detail link targets
// use. A null parent is the zero Parent: most issues hang off nothing, and that
// is an ordinary state.
func newParent(p *parentNode, issueRepo string) model.Parent {
	if p == nil || p.ID == "" {
		return model.Parent{}
	}
	key := fmt.Sprintf("#%d", p.Number)
	if repo := p.Repository.NameWithOwner; repo != "" && repo != issueRepo {
		key = fmt.Sprintf("%s#%d", repo, p.Number)
	}
	return model.Parent{
		ID:    model.TicketID(p.ID),
		Key:   key,
		Title: p.Title,
		URL:   p.URL,
	}
}

// newTicket maps one sub-issue node onto a Ticket. epicRepo is the Epic's own
// "owner/repo", which decides whether the Ticket's display key needs
// qualifying.
//
// The ID is GitHub's GraphQL node ID, which is exactly what a later node(id:)
// lookup needs — that is why the query fetches it although nothing reads it
// yet.
func newTicket(n issueNode, epicRepo string) model.Ticket {
	status, native := normalizeStatus(n.State, n.StateReason)
	prs := newPullRequests(n.ClosedByPullRequestsReferences, n.CrossReferences)
	return model.Ticket{
		ID:    model.TicketID(n.ID),
		Key:   issueKey(n, epicRepo),
		Title: n.Title,
		URL:   n.URL,
		// GitHub issues are open or closed and nothing else, so the in-progress
		// half of the situation report is derived from the pull requests moving
		// the Ticket. Native Status stays GitHub's own word for it.
		Status:       statusWithPullRequests(status, prs),
		NativeStatus: native,
		Assignees:    newAssignees(n.Assignees.Nodes),
		Repository:   n.Repository.NameWithOwner,
		PullRequests: prs,
		// The closing connection is capped and never paginated, so the count
		// GitHub reports is what makes the cap visible downstream.
		PullRequestTotal: pullRequestTotal(n.ClosedByPullRequestsReferences, prs),
		// ParentID stays empty: one level of the sub-issue graph is fetched, so
		// every Ticket hangs directly off the Epic.
	}
}

// issueKey renders the display identity: "#112" for a child of the Epic's own
// repository, "owner/repo#112" for a child from anywhere else. Both forms round
// -trip through sitrep's Ref grammar, so a key a human sees is a key they
// can type.
func issueKey(n issueNode, epicRepo string) string {
	if repo := n.Repository.NameWithOwner; repo != "" && repo != epicRepo {
		return fmt.Sprintf("%s#%d", repo, n.Number)
	}
	return fmt.Sprintf("#%d", n.Number)
}

// newDetail maps one Detail response node onto sitrep's Detail. The body is
// carried across as raw markdown, unrendered, exactly as model.Detail requires.
func newDetail(n detailNode) model.Detail {
	return model.Detail{
		TicketID:    model.TicketID(n.ID),
		Description: n.Body,
		Comments:    newComments(n.Comments.Nodes),
		Links:       newLinks(n),
	}
}

// newComments maps GitHub's comments onto sitrep's, oldest first — which is
// what comments(last:100) already returns them in.
func newComments(nodes []commentNode) []model.Comment {
	if len(nodes) == 0 {
		return nil
	}
	comments := make([]model.Comment, 0, len(nodes))
	for _, n := range nodes {
		comments = append(comments, model.Comment{
			ID:        n.ID,
			Author:    newActor(n.Author),
			Body:      n.Body,
			CreatedAt: n.CreatedAt.UTC(),
			URL:       n.URL,
		})
	}
	return comments
}

// newActor maps a nullable Actor onto a User. A deleted account arrives as null
// and becomes the zero User, which renderers show as an unknown author: losing
// the comment because its writer closed their account would be worse.
func newActor(a *actorNode) model.User {
	if a == nil {
		return model.User{}
	}
	return model.User{Login: a.Login, DisplayName: a.Name, AvatarURL: a.AvatarURL}
}

// The wording GitHub's own issue UI uses for a dependency. GitHub exposes no
// per-link label of its own the way Jira does, so the driver supplies the
// Tracker's phrasing as the native label — displayed, never interpreted
// (model.Link.NativeLabel).
const (
	blockedByLabel = "is blocked by"
	blocksLabel    = "blocks"
)

// newLinks flattens both dependency connections into Detail's flat slice:
// blocked-by first, then blocks, each in the API's own order. That order is the
// only presentation decision the driver makes about links.
func newLinks(n detailNode) []model.Link {
	total := len(n.BlockedBy.Nodes) + len(n.Blocking.Nodes)
	if total == 0 {
		return nil
	}

	links := make([]model.Link, 0, total)
	for _, target := range n.BlockedBy.Nodes {
		links = append(links, newLink(model.LinkBlockedBy, blockedByLabel, target, n.Repository.NameWithOwner))
	}
	for _, target := range n.Blocking.Nodes {
		links = append(links, newLink(model.LinkBlocks, blocksLabel, target, n.Repository.NameWithOwner))
	}
	return links
}

// newLink maps one linked issue onto a Link. ticketRepo is the *opened*
// Ticket's repository, not the Epic's: at Detail time the driver has no Ref
// in hand and re-deriving one would cost a request. So a target living beside
// the Ticket renders "#115" and anything else "owner/repo#115" — identical to
// what the list shows whenever the Epic and the Ticket share a repository, which
// is the overwhelmingly common case.
func newLink(kind model.LinkKind, label string, target issueNode, ticketRepo string) model.Link {
	status, native := normalizeStatus(target.State, target.StateReason)
	return model.Link{
		Kind:        kind,
		NativeLabel: label,
		Target: model.LinkTarget{
			ID:           model.TicketID(target.ID),
			Key:          issueKey(target, ticketRepo),
			Title:        target.Title,
			URL:          target.URL,
			Status:       status,
			NativeStatus: native,
		},
	}
}

// newAssignees maps GitHub's assignees onto sitrep's Users. The query caps the
// list at ten and deliberately does not paginate it: an eleventh assignee going
// unshown in the list model is intentional, not a bug — the hot path is polled
// and a second request per Ticket to name one more person is not worth it.
func newAssignees(nodes []assigneeNode) []model.User {
	if len(nodes) == 0 {
		return nil
	}
	users := make([]model.User, 0, len(nodes))
	for _, n := range nodes {
		users = append(users, model.User{
			Login:       n.Login,
			DisplayName: n.Name,
			AvatarURL:   n.AvatarURL,
		})
	}
	return users
}
