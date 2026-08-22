package jira

import (
	"sort"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The response structs mirror Jira's API vocabulary — REST says `issue` and
// `issuelinks`, so these say issue and issuelinks. Everything sitrep owns
// downstream of the new* functions says Epic and Ticket.

// issueWire is one issue, as every endpoint here returns it. The same struct
// serves the epic read, the children search and a link's far end, because Jira
// answers all three with the same document and asking for different `fields`.
type issueWire struct {
	ID     string     `json:"id"`
	Key    string     `json:"key"`
	Self   string     `json:"self"`
	Fields fieldsWire `json:"fields"`
}

// fieldsWire is the union of every field this driver asks for. Which of them
// are populated depends on the `fields` parameter the request sent: the polled
// path never asks for description or issuelinks (ADR-0003).
type fieldsWire struct {
	Summary     string          `json:"summary"`
	Status      *statusWire     `json:"status"`
	Resolution  *resolutionWire `json:"resolution"`
	Assignee    *userWire       `json:"assignee"`
	Parent      *parentWire     `json:"parent"`
	Project     *projectWire    `json:"project"`
	Description string          `json:"description"`
	IssueLinks  []issueLinkWire `json:"issuelinks"`
}

type statusWire struct {
	Name           string             `json:"name"`
	StatusCategory statusCategoryWire `json:"statusCategory"`
}

// statusCategoryWire is Jira's own bucket for a status. Only the key is read:
// it is the stable identifier, while name and colorName are localized.
type statusCategoryWire struct {
	Key string `json:"key"`
}

type resolutionWire struct {
	Name string `json:"name"`
}

// userWire is an Atlassian account. accountId is an opaque identifier and
// emailAddress is usually hidden by the site's privacy settings, so displayName
// is the only field that reliably names a person.
type userWire struct {
	AccountID   string            `json:"accountId"`
	DisplayName string            `json:"displayName"`
	AvatarUrls  map[string]string `json:"avatarUrls"`
}

type projectWire struct {
	Key string `json:"key"`
}

// parentWire is the issue an issue hangs off, through the modern `parent`
// field. It is nullable: most issues hang off nothing, which is an ordinary
// state and never an error.
type parentWire struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

// issueLinkWire is one entry of fields.issuelinks. It carries either
// outwardIssue or inwardIssue, never both, and inlines the whole link type.
type issueLinkWire struct {
	ID           string       `json:"id"`
	Type         linkTypeWire `json:"type"`
	InwardIssue  *issueWire   `json:"inwardIssue"`
	OutwardIssue *issueWire   `json:"outwardIssue"`
}

// linkTypeWire is one of the instance's issue link types, as both the
// catalogue endpoint and every inline link entry return it.
type linkTypeWire struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// searchResponse is the enhanced search endpoint's answer. Pagination lives
// here and in fetchChildren's loop, and nowhere else: nextPageToken is a cursor,
// and the removed startAt/total fields are deliberately absent.
type searchResponse struct {
	Issues        []issueWire `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        bool        `json:"isLast"`
}

// bulkFetchRequest and bulkFetchResponse are Jira v3's authoritative finite
// issue read. IssueError.ID is Jira's issue id (not reliably the requested key),
// so the driver identifies a failed member by comparing returned issue keys with
// the requested chunk.
type bulkFetchRequest struct {
	IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
	Fields         []string `json:"fields"`
}

type bulkFetchResponse struct {
	Issues      []issueWire      `json:"issues"`
	IssueErrors []issueErrorWire `json:"issueErrors"`
}

type issueErrorWire struct {
	ID           string `json:"id"`
	ErrorMessage string `json:"errorMessage"`
}

type commentsResponse struct {
	Comments []commentWire `json:"comments"`
}

// commentWire is one comment. author is nullable — an account that left the
// company arrives as null — and created is Jira's own timestamp format.
type commentWire struct {
	ID      string    `json:"id"`
	Body    string    `json:"body"`
	Created string    `json:"created"`
	Author  *userWire `json:"author"`
}

type linkTypesResponse struct {
	IssueLinkTypes []linkTypeWire `json:"issueLinkTypes"`
}

// errorResponse is how Jira answers an error: a list of human messages plus a
// per-field map. Only the messages are rendered; the map names form fields this
// read-only driver never submits.
type errorResponse struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

// message joins Jira's general error messages into one line, or returns ""
// when it carries nothing worth saying. Existing non-Query requests deliberately
// ignore per-field form errors, as they submit no form fields.
func (e errorResponse) message() string {
	messages := make([]string, 0, len(e.ErrorMessages))
	for _, m := range e.ErrorMessages {
		if strings.TrimSpace(m) != "" {
			messages = append(messages, strings.TrimSpace(m))
		}
	}
	return strings.Join(messages, "; ")
}

// queryMessage includes Jira's per-field explanations because the JQL field is
// exactly what a Query Selector submitted. Keys are sorted so one native error
// payload always renders deterministically.
func (e errorResponse) queryMessage() string {
	messages := make([]string, 0, len(e.ErrorMessages)+len(e.Errors))
	if message := e.message(); message != "" {
		messages = append(messages, message)
	}
	keys := make([]string, 0, len(e.Errors))
	for key := range e.Errors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if message := strings.TrimSpace(e.Errors[key]); message != "" {
			messages = append(messages, key+": "+message)
		}
	}
	return strings.Join(messages, "; ")
}

// categoryKey, statusName and resolutionName read the nullable status and
// resolution objects, so that every caller of normalizeStatus reads them one
// way.
func (f fieldsWire) categoryKey() string {
	if f.Status == nil {
		return ""
	}
	return f.Status.StatusCategory.Key
}

func (f fieldsWire) statusName() string {
	if f.Status == nil {
		return ""
	}
	return f.Status.Name
}

func (f fieldsWire) resolutionName() string {
	if f.Resolution == nil {
		return ""
	}
	return f.Resolution.Name
}

func (f fieldsWire) projectKey() string {
	if f.Project == nil {
		return ""
	}
	return f.Project.Key
}

// browseURL is the URL a human opens. The API's `self` field is a REST URL and
// is not it, so the driver builds the browse form from the site and the key.
func browseURL(host, key string) string {
	if host == "" || key == "" {
		return ""
	}
	return "https://" + host + "/browse/" + key
}

// newEpic maps the fetched issue onto sitrep's Epic. Key is the issue key
// verbatim: Jira keys are globally unique on a site, so there is no
// qualification rule to mirror from GitHub, and the key round-trips through the
// Ref grammar — a key a human sees is a key they can type.
func newEpic(issue issueWire, host string) model.Epic {
	status, native := normalizeStatus(
		issue.Fields.categoryKey(), issue.Fields.statusName(), issue.Fields.resolutionName())
	return model.Epic{
		ID:           model.TicketID(issue.Key),
		Key:          issue.Key,
		Title:        issue.Fields.Summary,
		URL:          browseURL(host, issue.Key),
		Status:       status,
		NativeStatus: native,
		Assignees:    newAssignees(issue.Fields.Assignee),
		Repository:   issue.Fields.projectKey(),
		// PullRequests stays nil: this driver does not declare the PullRequests
		// Capability, so the section is silently absent.
	}
}

// newTicket maps one child issue onto a Ticket.
//
// The ID is the issue key, which is what every REST path takes — so reaching
// this one Ticket again for its Detail needs no second lookup. TicketID is
// Provider-scoped by contract, so nothing outside this package may parse it.
//
// Sub-tasks of children are not expanded: one level of the parent graph is
// fetched, exactly as the GitHub driver fetches one level of sub-issues.
//
// ParentID carries the child's own fields.parent key only when that key is not
// the Epic being fetched. A child hanging directly off the Epic gets an empty
// ParentID, which is what model.Ticket documents and what the GitHub mapping
// produces — otherwise one logical shape would serialize differently per
// Tracker. epicKey is the fetched Epic's key, for that comparison.
func newTicket(issue issueWire, host, epicKey string) model.Ticket {
	status, native := normalizeStatus(
		issue.Fields.categoryKey(), issue.Fields.statusName(), issue.Fields.resolutionName())

	var parentID model.TicketID
	if issue.Fields.Parent != nil && !strings.EqualFold(issue.Fields.Parent.Key, epicKey) {
		parentID = model.TicketID(issue.Fields.Parent.Key)
	}

	return model.Ticket{
		ID:           model.TicketID(issue.Key),
		Key:          issue.Key,
		Title:        issue.Fields.Summary,
		URL:          browseURL(host, issue.Key),
		Status:       status,
		NativeStatus: native,
		Assignees:    newAssignees(issue.Fields.Assignee),
		ParentID:     parentID,
		// Repository is the Jira project key, per model.Ticket's own doc ("the
		// project on Jira"), so a cross-project child identifies itself.
		Repository: issue.Fields.projectKey(),
	}
}

// newParent maps the fetched issue's own parent onto sitrep's Parent. A missing
// parent is the zero Parent — an ordinary state, never an error.
func newParent(p *parentWire, host string) model.Parent {
	if p == nil || p.Key == "" {
		return model.Parent{}
	}
	return model.Parent{
		ID:    model.TicketID(p.Key),
		Key:   p.Key,
		Title: p.Fields.Summary,
		URL:   browseURL(host, p.Key),
	}
}

// newAssignees maps Jira's single assignee onto sitrep's slice. Jira has one
// assignee per issue, so the slice holds zero or one User; an unassigned issue
// gets nil rather than a placeholder.
func newAssignees(a *userWire) []model.User {
	if a == nil {
		return nil
	}
	return []model.User{newUser(a)}
}

// newUser maps an Atlassian account onto sitrep's User.
//
// Login carries the displayName, because Jira Cloud has no handle: accountId is
// an opaque hex string that would render as @5b10a2844c20165700ede21g in the
// meta line, which is worse for the person reading it than a name. If a
// maintainer prefers the accountId or an email local-part, it is this one line.
func newUser(u *userWire) model.User {
	if u == nil {
		return model.User{}
	}
	return model.User{
		Login:       u.DisplayName,
		DisplayName: u.DisplayName,
		AvatarURL:   u.AvatarUrls["48x48"],
	}
}

// newDetail assembles one Ticket's Detail from the three answers FetchDetail
// collected. The description is carried across verbatim and unrendered, exactly
// as model.Detail requires — on REST v2 that is Jira wiki markup rather than
// markdown, which sitrep displays as it received it.
func newDetail(issue issueWire, comments []commentWire, types map[string]linkType, host string) model.Detail {
	return model.Detail{
		TicketID:    model.TicketID(issue.Key),
		Description: issue.Fields.Description,
		Comments:    newComments(comments, issue.Key, host),
		Links:       newLinks(types, issue.Fields.IssueLinks, host),
	}
}

// newComments maps Jira's comments onto sitrep's, reversing them into
// oldest-first, which is what model.Detail.Comments requires. They arrive
// newest-first because the request orders by -created: a Ticket with a thousand
// comments should show the most recent hundred rather than the most ancient
// hundred, the same decision the GitHub driver's comments(last:100) makes.
func newComments(comments []commentWire, key, host string) []model.Comment {
	if len(comments) == 0 {
		return nil
	}
	out := make([]model.Comment, 0, len(comments))
	for i := len(comments) - 1; i >= 0; i-- {
		out = append(out, newComment(comments[i], key, host))
	}
	return out
}

// newComment maps one comment. A null author becomes the zero model.User: a
// comment must never be dropped because its writer left the company.
func newComment(c commentWire, key, host string) model.Comment {
	url := browseURL(host, key)
	if url != "" && c.ID != "" {
		url += "?focusedCommentId=" + c.ID
	}
	return model.Comment{
		ID:        c.ID,
		Author:    newUser(c.Author),
		Body:      c.Body,
		CreatedAt: parseTime(c.Created),
		URL:       url,
	}
}

// jiraTimeLayout is Jira's own timestamp format: "2026-01-12T09:14:33.123+0100".
// It is not RFC3339 — the zone offset carries no colon — and parsing it as one
// is a real trap.
const jiraTimeLayout = "2006-01-02T15:04:05.999-0700"

// parseTime reads a Jira timestamp into UTC. A timestamp neither layout
// understands yields the zero time rather than failing the whole Detail: an
// unreadable date is worth less than the comment it is attached to.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(jiraTimeLayout, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
