package github

import (
	"fmt"
	"strings"

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

// err turns a GraphQL errors[] payload into one line. A NOT_FOUND entry is the
// same situation as a null issue and reads better said plainly.
func (r graphQLResponse) err(target ref.Ref, endpoint string) error {
	if len(r.Errors) == 0 {
		return nil
	}
	for _, e := range r.Errors {
		if strings.EqualFold(e.Type, "NOT_FOUND") {
			return fmt.Errorf("github: %s not found (or you lack access)", refKey(target))
		}
	}

	messages := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
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
