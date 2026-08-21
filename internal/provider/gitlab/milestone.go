package gitlab

import (
	"context"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
)

// milestoneWire is one milestone, as the milestone list endpoint returns it in
// either scope. A project milestone carries project_id and a group one carries
// group_id; the two are never both set.
//
// The struct says "milestone" because GitLab's API does. Everything downstream
// of newEpicFromMilestone says Epic: a milestone is how GitLab Free spells the
// normalized parent, and the Provider hides the flavour.
type milestoneWire struct {
	// ID is the milestone's database id, which is what the milestone *issues*
	// endpoint takes — not IID. See target.milestoneIssuesPath.
	ID          int    `json:"id"`
	IID         int    `json:"iid"`
	ProjectID   int    `json:"project_id"`
	GroupID     int    `json:"group_id"`
	Title       string `json:"title"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	DueDate     string `json:"due_date"`
	StartDate   string `json:"start_date"`
	Expired     bool   `json:"expired"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Description string `json:"description"`
}

// issueMilestoneWire is the milestone object GitLab embeds on every issue that
// has one: a complete breadcrumb for free, with no second request.
type issueMilestoneWire struct {
	ID        int    `json:"id"`
	IID       int    `json:"iid"`
	ProjectID int    `json:"project_id"`
	GroupID   int    `json:"group_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
}

// The milestone states GitLab exposes. There are two and there is no third:
// no resolution field, no cancelled state.
const (
	milestoneActive = "active"
	milestoneClosed = "closed"
)

// fetchMilestone resolves a milestone by its iid — the number in its web URL and
// in GitLab's own "%3" reference — and returns the whole milestone.
//
// It lists with iids[] rather than reading /milestones/:milestone_id, because
// GitLab's milestone endpoints are addressed by the milestone's *id* while every
// human-facing form carries its *iid*. The list form bridges the two and hands
// back the full object in the same request, so resolving costs nothing extra.
// (Verified 2026-08-21; see the package doc.)
func (p *Provider) fetchMilestone(ctx context.Context, t target) (milestoneWire, error) {
	var milestones []milestoneWire
	if _, err := p.do(ctx, t.milestonesPath(), t.milestoneLookupQuery(), t.String(), &milestones); err != nil {
		return milestoneWire{}, err
	}

	switch len(milestones) {
	case 0:
		return milestoneWire{}, provider.Errorf(provider.KindBadRef, "gitlab: %s not found (or you lack access)", t)
	case 1:
		return milestones[0], nil
	default:
		// iids[] is documented to filter on a number unique within its scope, so
		// two answers mean sitrep's addressing assumption is wrong. Saying so
		// loudly is cheaper than a report about the wrong milestone. An iid that
		// does not name one milestone will be exactly as ambiguous on the next
		// tick, so this is a property of the ref rather than a transient.
		return milestoneWire{}, provider.Errorf(provider.KindBadRef,
			"gitlab: %s matched %d milestones; "+
				"an iid names at most one, so sitrep will not guess which", t, len(milestones))
	}
}

// milestoneStatus maps a milestone's state onto sitrep's Status Category and the
// Native Status shown to the user.
//
// It is a separate function from normalizeStatus rather than a widening of it:
// a milestone speaks a different vocabulary ("active"/"closed", where an issue
// says "opened"/"closed"), and folding two vocabularies into one function is how
// a mapping starts guessing.
//
// The `expired` flag is deliberately ignored. A milestone past its due date is
// late work, not finished or cancelled work, and folding a date into a Status
// Category would put "we missed the date" into the progress bar.
//
// There is no Cancelled milestone state, and the won't-do label inference does
// not apply: a milestone carries no labels.
func milestoneStatus(state string) (model.StatusCategory, string) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case milestoneActive:
		return model.StatusTodo, milestoneActive
	case milestoneClosed:
		return model.StatusDone, milestoneClosed
	default:
		// StatusUnknown is the model's deliberate "a Provider forgot to map
		// something" signal.
		return model.StatusUnknown, strings.TrimSpace(state)
	}
}

// newEpicFromMilestone maps a milestone onto sitrep's Epic. t is the target the
// Ref addressed it through, which is what names the scope GitLab's payload
// leaves for the caller to know.
//
// Description is deliberately not read: this runs on the polled hot path, and a
// description belongs on Detail (ADR-0003).
func newEpicFromMilestone(m milestoneWire, host string, t target) model.Epic {
	status, native := milestoneStatus(m.State)
	return model.Epic{
		ID:           t.ticketID(),
		Key:          t.String(),
		Title:        m.Title,
		URL:          webURLOr(m.WebURL, milestoneWebURL(host, t)),
		Status:       status,
		NativeStatus: native,
		// A milestone has no assignees, and a placeholder would be a lie.
		// Repository is the Epic's display origin: the project or group path the
		// milestone lives in, as newEpicFromEpic maps a group.
		Repository: t.path,
		// PullRequests stays nil: a milestone is a collection, not an issue, so
		// there is no endpoint and no meaning. See correlate.
	}
}

// newParentFromMilestone maps a child issue's embedded milestone onto sitrep's
// Parent — a complete breadcrumb, with Title and URL, for no extra request.
//
// The scope comes from group_id when GitLab sets one and from the embedded
// web_url otherwise, because the ids alone cannot name a path. Where no path can
// be derived, Key and ID are left empty and URL carries the walk-up on its own;
// ref.ParseParent prefers the URL anyway.
func newParentFromMilestone(m *issueMilestoneWire, host string) model.Parent {
	if m == nil || m.IID <= 0 {
		return model.Parent{}
	}

	path, group := pathFromMilestoneURL(m.WebURL)
	if m.GroupID > 0 {
		group = true
	}
	parent := model.Parent{Title: m.Title, URL: strings.TrimSpace(m.WebURL)}
	if path == "" {
		return parent
	}

	t := target{kind: kindProjectMilestone, path: path, iid: m.IID}
	if group {
		t.kind = kindGroupMilestone
	}
	parent.ID = t.ticketID()
	parent.Key = t.String()
	parent.URL = webURLOr(m.WebURL, milestoneWebURL(host, t))
	return parent
}

// pathFromMilestoneURL reads the group or project path out of a milestone's web
// URL. The group form is checked first because a group URL's own path begins
// with the reserved "groups" segment, which the project reading would hand back
// verbatim.
func pathFromMilestoneURL(rawurl string) (path string, group bool) {
	if g := groupFromWebURL(rawurl); g != "" {
		return g, true
	}
	return projectPathFromWebURL(rawurl), false
}
