package gitlab

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The structs below mirror GitLab's API vocabulary — REST says "epic", "issue"
// and "work item", so these say epic, issue and work item. Everything sitrep
// owns downstream of the new* functions says Epic and Ticket.

// referencesWire is GitLab's own rendering of a node's identity: "&23356" and
// "gitlab-org&23356" for an epic, "#8509" and "gitlab-org/cli#8509" for an
// issue. full is what sitrep displays, because it is unambiguous across the many
// projects an epic's children come from.
type referencesWire struct {
	Short    string `json:"short"`
	Relative string `json:"relative"`
	Full     string `json:"full"`
}

// userWire is a GitLab account. Unlike Jira, GitLab has real handles, so
// username is a login and not an opaque id.
type userWire struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	WebURL    string `json:"web_url"`
}

// linksWire is an issue's _links object. Only closed_as_duplicate_of is read:
// it is the one hard signal REST gives about *why* something is closed, and
// self is a REST URL that is never shown to a human.
type linksWire struct {
	Self                string `json:"self"`
	Notes               string `json:"notes"`
	AwardEmoji          string `json:"award_emoji"`
	Project             string `json:"project"`
	ClosedAsDuplicateOf string `json:"closed_as_duplicate_of"`
}

// epicWire is one group epic. parent_iid is nullable and usually null, which is
// an ordinary state and never an error.
type epicWire struct {
	// ID is the epic's database id, which is what the epic *notes* endpoint
	// takes — not IID. See target.epicNotesPath.
	ID        int            `json:"id"`
	IID       int            `json:"iid"`
	GroupID   int            `json:"group_id"`
	ParentID  *int           `json:"parent_id"`
	ParentIID *int           `json:"parent_iid"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	WebURL    string         `json:"web_url"`
	Labels    []string       `json:"labels"`
	Author    *userWire      `json:"author"`
	Reference referencesWire `json:"references"`

	// Description is read only by FetchDetail. The epic endpoint returns it on
	// the polled path too; newEpicFromEpic deliberately does not touch it
	// (ADR-0003).
	Description string `json:"description"`
}

// issueEpicWire is the epic object a Premium instance embeds on every issue: a
// complete breadcrumb for free, with no second request.
type issueEpicWire struct {
	ID      int    `json:"id"`
	IID     int    `json:"iid"`
	GroupID int    `json:"group_id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
}

// issueWire is one project issue, as the issue endpoint, the epic-issues
// endpoint and a link's far end all return it.
type issueWire struct {
	ID        int            `json:"id"`
	IID       int            `json:"iid"`
	ProjectID int            `json:"project_id"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	WebURL    string         `json:"web_url"`
	Labels    []string       `json:"labels"`
	Assignees []userWire     `json:"assignees"`
	Author    *userWire      `json:"author"`
	Reference referencesWire `json:"references"`
	Epic      *issueEpicWire `json:"epic"`
	Links     linksWire      `json:"_links"`

	// Milestone is the breadcrumb an issue has on every tier, where Epic is
	// Premium-only. See newParentFromIssue for which of the two wins.
	Milestone *issueMilestoneWire `json:"milestone"`

	// Description is what GitLab sends whether or not sitrep wants it: the
	// epic-issues payload carries every child's full description. It is read only
	// by FetchDetail. Putting it on a model.Ticket would violate ADR-0003 — the
	// polled list model must never carry the expensive half — so
	// newTicketFromIssue drops it on the floor.
	Description string `json:"description"`
}

// issueLinksWire is one entry of the issue links endpoint: an ordinary issue
// payload with the relationship's own fields alongside it.
type issueLinkWire struct {
	issueWire
	LinkType      string `json:"link_type"`
	IssueLinkID   int    `json:"issue_link_id"`
	LinkCreatedAt string `json:"link_created_at"`
}

// noteWire is one note. GitLab's notes endpoint returns activity and
// conversation through the same list: system notes ("changed title from … to
// …") carry system: true and are dropped. internal: true notes are real
// confidential comments and are kept.
type noteWire struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	CreatedAt string    `json:"created_at"`
	System    bool      `json:"system"`
	Internal  bool      `json:"internal"`
	Author    *userWire `json:"author"`
}

// errorResponse is how GitLab answers an error. `message` is a string on some
// endpoints and an object mapping fields to reasons on others, so it is decoded
// as raw JSON and rendered by message(); `error` is the OAuth-flavoured
// alternative.
type errorResponse struct {
	Message json.RawMessage `json:"message"`
	Error   string          `json:"error"`
}

// message renders an error payload as one line, or "" when it carries nothing
// worth saying.
func (e errorResponse) message() string {
	if msg := strings.TrimSpace(e.Error); msg != "" {
		return msg
	}
	if len(e.Message) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(e.Message, &text); err == nil {
		return strings.TrimSpace(text)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(e.Message, &fields); err != nil {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, key := range sortedKeys(fields) {
		parts = append(parts, key+": "+strings.Trim(string(fields[key]), `"`))
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// A map's iteration order is random and an error message must not be.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// closedAsDuplicate reports whether GitLab says this issue was closed as a
// duplicate of another. It is a fact GitLab states, not an inference, which is
// why normalizeStatus checks it first.
func (i issueWire) closedAsDuplicate() bool {
	return strings.TrimSpace(i.Links.ClosedAsDuplicateOf) != ""
}

// projectPath is the project an issue belongs to, taken from references.full
// ("gitlab-org/cli#8509") and, when that is absent, from the path in web_url.
func (i issueWire) projectPath() string {
	if at := strings.LastIndex(i.Reference.Full, "#"); at > 0 {
		return i.Reference.Full[:at]
	}
	return projectPathFromWebURL(i.WebURL)
}

// projectPathFromWebURL reads the project path out of a GitLab web URL, which is
// everything between the host and the "/-/" separator.
func projectPathFromWebURL(webURL string) string {
	at := strings.Index(webURL, "/-/")
	if at < 0 {
		return ""
	}
	head := webURL[:at]
	// Skip past "https://host".
	if scheme := strings.Index(head, "://"); scheme >= 0 {
		head = head[scheme+len("://"):]
	}
	if slash := strings.Index(head, "/"); slash >= 0 {
		return head[slash+1:]
	}
	return ""
}

// key is the issue's display identity: references.full, which is *always*
// project-qualified, because an epic's children come from many projects and a
// bare "#12" would be ambiguous across them. A payload with no references falls
// back to "#{iid}".
func (i issueWire) key() string {
	if full := strings.TrimSpace(i.Reference.Full); full != "" {
		return full
	}
	return "#" + strconv.Itoa(i.IID)
}

// ticketID is the issue's Provider-scoped opaque id; see target.ticketID.
func (i issueWire) ticketID() model.TicketID {
	return target{kind: kindIssue, path: i.projectPath(), iid: i.IID}.ticketID()
}

// key is the epic's display identity: references.full ("gitlab-org&23356"),
// which is the form a human can type straight back into sitrep now that the Epic
// Ref grammar accepts it.
func (e epicWire) key(group string) string {
	if full := strings.TrimSpace(e.Reference.Full); full != "" {
		return full
	}
	return group + "&" + strconv.Itoa(e.IID)
}

// newEpicFromEpic maps a group epic onto sitrep's Epic. group is the path the
// Ref addressed it through, used for the fields GitLab's payload leaves for the
// caller to know.
//
// Description is deliberately not read: this runs on the polled hot path, and a
// description belongs on Detail (ADR-0003).
func newEpicFromEpic(e epicWire, host, group string, wontDo wontDoSet) model.Epic {
	// An epic has no closed-as-duplicate link in REST, so the one hard Cancelled
	// signal an issue has is unavailable here and only the labels remain.
	status, native := normalizeStatus(e.State, e.Labels, false, wontDo)
	return model.Epic{
		ID:           target{kind: kindEpic, path: group, iid: e.IID}.ticketID(),
		Key:          e.key(group),
		Title:        e.Title,
		URL:          webURLOr(e.WebURL, epicWebURL(host, group, e.IID)),
		Status:       status,
		NativeStatus: native,
		// The REST epic payload carries no assignees, so Assignees stays nil
		// rather than being invented from the author.
		Repository: group,
		// PullRequests stays nil: this driver does not declare the PullRequests
		// Capability, so the section is silently absent. The Capability and
		// this field are turned on together.
	}
}

// newEpicFromIssue maps a project issue onto sitrep's Epic, for the Ref
// that turned out to name a plain Ticket. Reporting it is all this driver does
// about it: which screen that opens is internal/cli's decision (ADR-0003).
func newEpicFromIssue(i issueWire, wontDo wontDoSet) model.Epic {
	status, native := normalizeStatus(i.State, i.Labels, i.closedAsDuplicate(), wontDo)
	return model.Epic{
		ID:           i.ticketID(),
		Key:          i.key(),
		Title:        i.Title,
		URL:          i.WebURL,
		Status:       status,
		NativeStatus: native,
		Assignees:    newAssignees(i.Assignees),
		Repository:   i.projectPath(),
	}
}

// newTicketFromEpic preserves the thin root identity when an explicit Ref-list
// member is a native epic or a milestone. In a Ref-list it is an ordinary
// Ticket; there is no synthetic outer Epic or Parent.
func newTicketFromEpic(epic model.Epic) model.Ticket {
	return model.Ticket{
		ID:           epic.ID,
		Key:          epic.Key,
		Title:        epic.Title,
		URL:          epic.URL,
		Status:       epic.Status,
		NativeStatus: epic.NativeStatus,
		Assignees:    epic.Assignees,
		Repository:   epic.Repository,
		PullRequests: epic.PullRequests,
	}
}

// newTicketFromIssue maps one of an epic's child issues onto a Ticket.
//
// parentID is the fetched Epic's own TicketID: honest for a child reached
// through the epic-issues endpoint, and it matches the model's tree shape.
// Whatever GitLab returns from that endpoint is exactly what sitrep shows —
// sub-epics are not expanded and an issue's own child work items (tasks) are
// not read, so this driver does not claim to know whether descendants are
// included.
func newTicketFromIssue(i issueWire, wontDo wontDoSet) model.Ticket {
	status, native := normalizeStatus(i.State, i.Labels, i.closedAsDuplicate(), wontDo)
	return model.Ticket{
		ID:           i.ticketID(),
		Key:          i.key(),
		Title:        i.Title,
		URL:          i.WebURL,
		Status:       status,
		NativeStatus: native,
		Assignees:    newAssignees(i.Assignees),
		// ParentID stays empty. Everything the epic-issues and milestone-issues
		// endpoints return hangs directly off the Epic — sub-epics are not
		// expanded and an issue's own tasks are not read — and model.Ticket
		// documents an empty ParentID as exactly that. Copying the Epic's own ID
		// onto every child would make the same logical shape serialize
		// differently here than on GitHub.
		// Repository is the child's own project path, per model.Ticket's doc
		// ("the display origin"), so a cross-project child identifies itself.
		Repository: i.projectPath(),
	}
}

// newParentFromIssue maps a child issue's breadcrumb onto sitrep's Parent — a
// complete one, with Title and URL, for no extra request.
//
// An epic wins; a milestone is the fallback. A Premium instance gives an issue
// both, and the epic is the collection a human means. On Free there is no epic,
// so the milestone is the Epic. A missing epic and a missing milestone together
// are the zero Parent: an ordinary state, never an error.
func newParentFromIssue(i issueWire, host string) model.Parent {
	if parent := newParentFromEpicObject(i.Epic, host); !parent.IsZero() {
		return parent
	}
	return newParentFromMilestone(i.Milestone, host)
}

// newParentFromEpicObject maps the epic object a Premium instance embeds on
// every issue.
func newParentFromEpicObject(e *issueEpicWire, host string) model.Parent {
	if e == nil || e.IID <= 0 {
		return model.Parent{}
	}
	group := groupFromWebURL(e.URL)
	return model.Parent{
		ID:    target{kind: kindEpic, path: group, iid: e.IID}.ticketID(),
		Key:   group + "&" + strconv.Itoa(e.IID),
		Title: e.Title,
		URL:   webURLOr(e.URL, epicWebURL(host, group, e.IID)),
	}
}

// newParentFromEpic maps an epic's parent_iid onto sitrep's Parent.
//
// Title is left empty, deliberately: the epic payload does not carry the
// parent's title, and Resolve is the polled hot path — spending one request
// per refresh on a breadcrumb's title is the wrong trade. Key and URL are built
// from the group and the iid, which is everything the walk-up needs.
func newParentFromEpic(e epicWire, host, group string) model.Parent {
	if e.ParentIID == nil || *e.ParentIID <= 0 {
		return model.Parent{}
	}
	iid := *e.ParentIID
	return model.Parent{
		ID:  target{kind: kindEpic, path: group, iid: iid}.ticketID(),
		Key: group + "&" + strconv.Itoa(iid),
		URL: epicWebURL(host, group, iid),
	}
}

// groupFromWebURL reads the group path out of a group-scoped GitLab web URL,
// "https://host/groups/{group path}/-/{noun}/{n}" — a group epic or a group
// milestone. A URL that is not group-scoped yields "".
func groupFromWebURL(rawurl string) string {
	const marker = "/groups/"
	at := strings.Index(rawurl, marker)
	if at < 0 {
		return ""
	}
	rest := rawurl[at+len(marker):]
	sep := strings.Index(rest, "/-/")
	if sep < 0 {
		return ""
	}
	return rest[:sep]
}

// webURLOr prefers GitLab's own web_url, which is authoritative and already
// carries the work-item form GitLab serves today, and falls back to a URL the
// driver builds for a node it only ever saw an iid of.
func webURLOr(webURL, built string) string {
	if s := strings.TrimSpace(webURL); s != "" {
		return s
	}
	return built
}

// newAssignees maps GitLab's assignees, in order. The deprecated singular
// `assignee` field is ignored: it is a duplicate of the first entry here. An
// unassigned issue gets nil rather than a placeholder.
func newAssignees(users []userWire) []model.User {
	if len(users) == 0 {
		return nil
	}
	out := make([]model.User, 0, len(users))
	for _, u := range users {
		out = append(out, newUser(&u))
	}
	return out
}

// newUser maps a GitLab account onto sitrep's User.
func newUser(u *userWire) model.User {
	if u == nil {
		return model.User{}
	}
	return model.User{
		Login:       u.Username,
		DisplayName: u.Name,
		AvatarURL:   u.AvatarURL,
	}
}

// newComments maps GitLab's notes onto sitrep's comments, dropping system notes
// and reversing what is left into oldest-first, which is what
// model.Detail.Comments requires.
//
// They arrive newest-first because the request orders by created_at descending:
// a Ticket with a thousand notes should show the most recent hundred rather than
// the most ancient hundred, the same decision the other two drivers make.
func newComments(notes []noteWire, webURL string) []model.Comment {
	out := make([]model.Comment, 0, len(notes))
	for i := len(notes) - 1; i >= 0; i-- {
		if notes[i].System {
			// A system note is activity, not conversation: "changed title from …
			// to …" is not something a human wrote to another human.
			continue
		}
		out = append(out, newComment(notes[i], webURL))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// newComment maps one note. A null author becomes the zero model.User: a
// comment must never be dropped because its writer left.
func newComment(n noteWire, webURL string) model.Comment {
	url := webURL
	if url != "" && n.ID > 0 {
		url += "#note_" + strconv.Itoa(n.ID)
	}
	return model.Comment{
		ID:        strconv.Itoa(n.ID),
		Author:    newUser(n.Author),
		Body:      n.Body,
		CreatedAt: parseTime(n.CreatedAt),
		URL:       url,
	}
}

// parseTime reads GitLab's timestamp — RFC3339 with milliseconds, e.g.
// "2026-08-21T07:21:56.022Z" — into UTC. A timestamp that will not parse yields
// the zero time rather than failing the whole Detail: an unreadable date is
// worth less than the comment it is attached to.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
