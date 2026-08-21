package github

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// The response structs mirror GitHub's API vocabulary — GraphQL says `issue`,
// so these say issue. Everything sitrep owns downstream of newTicket says
// Ticket.

type graphQLResponse struct {
	Data struct {
		Repository *struct {
			Issue *issueNode `json:"issue"`
		} `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
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
	Assignees   struct {
		Nodes []assigneeNode `json:"nodes"`
	} `json:"assignees"`
	ClosedByPullRequestsReferences *pullRequestConnection `json:"closedByPullRequestsReferences"`
	SubIssues                      struct {
		TotalCount int `json:"totalCount"`
		PageInfo   struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []issueNode `json:"nodes"`
	} `json:"subIssues"`
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
func (r graphQLResponse) err(target ref.Ref, endpoint string) error {
	return graphQLErrors(r.Errors, fmt.Sprintf("github: %s not found (or you lack access)", refKey(target)), endpoint)
}

// err turns the Detail query's errors[] payload into one line, naming the node
// id the caller asked for rather than an Epic Ref it does not have.
func (r detailResponse) err(id model.TicketID, endpoint string) error {
	return graphQLErrors(r.Errors, detailNotFound(id), endpoint)
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
func graphQLErrors(errs []graphQLError, notFound, endpoint string) error {
	if len(errs) == 0 {
		return nil
	}
	for _, e := range errs {
		if strings.EqualFold(e.Type, "NOT_FOUND") {
			return errors.New(notFound)
		}
	}

	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Message != "" {
			messages = append(messages, e.Message)
		}
	}
	if len(messages) == 0 {
		return fmt.Errorf("github: API error from %s", endpoint)
	}
	return fmt.Errorf("github: API error: %s", strings.Join(messages, "; "))
}

// newEpic maps the parent issue onto sitrep's Epic. Its Key is repo-qualified
// because an Epic carries no repository field of its own and the qualified form
// is what a human can paste straight back into sitrep.
func newEpic(n issueNode) model.Epic {
	status, native := normalizeStatus(n.State, n.StateReason)
	return model.Epic{
		ID:           model.TicketID(n.ID),
		Key:          issueKey(n, ""),
		Title:        n.Title,
		URL:          n.URL,
		Status:       status,
		NativeStatus: native,
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
	prs := newPullRequests(n.ClosedByPullRequestsReferences)
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
		// ParentID stays empty: one level of the sub-issue graph is fetched, so
		// every Ticket hangs directly off the Epic.
	}
}

// issueKey renders the display identity: "#112" for a child of the Epic's own
// repository, "owner/repo#112" for a child from anywhere else. Both forms round
// -trip through sitrep's Epic Ref grammar, so a key a human sees is a key they
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
// Ticket's repository, not the Epic's: at Detail time the driver has no Epic Ref
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
