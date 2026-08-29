package gitlab

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// apiBase is the one place this driver's REST API version is written down; see
// the package doc for why it is v4 REST and not the Work Items GraphQL API.
// Every path in the driver is built from it by url, so a future migration is a
// bounded change rather than a search of the package.
const apiBase = "/api/v4"

// kind says which GitLab node a target names: a group epic, a project issue, or
// a milestone in either scope. GitLab addresses each through different
// endpoints, and the Ref grammar distinguishes them by the "&" or the "%"
// in a reference, so the driver decides once, here.
type kind int

const (
	kindIssue kind = iota
	kindEpic
	kindProjectMilestone
	kindGroupMilestone
)

// target is one addressable node: a group path plus an epic iid, a project path
// plus an issue iid, or either path plus a milestone iid. It is the driver's
// whole addressing rule — nothing else in the package builds a path.
type target struct {
	kind kind
	path string
	iid  int
}

// isMilestone reports whether the target names a milestone in either scope.
func (t target) isMilestone() bool {
	return t.kind == kindProjectMilestone || t.kind == kindGroupMilestone
}

// targetFor reads a Ref as the GitLab node it names. def is the Profile's
// project, used for a reference that names no group or project; its scope
// decides which references it is able to complete.
//
// Every rejection this function makes happens before any network call: a Ref
// that names nothing fetchable should fail instantly and say what to write, not
// after a round trip.
func targetFor(r ref.Ref, def defaultPath) (target, error) {
	if r.Tracker != ref.TrackerGitLab {
		return target{}, provider.Errorf(provider.KindBadRef, "gitlab: %q is not a GitLab Ref", r.Raw)
	}

	t := target{kind: kindIssue, iid: r.Number}
	switch pct, amp := strings.Index(r.Key, "%"), strings.Index(r.Key, "&"); {
	case pct >= 0:
		// The "%" is the discriminator internal/ref put there: a Key carrying one
		// is a milestone. A leading "groups/" makes it a group milestone; see
		// groupScopePrefix for why that spelling cannot collide with a namespace.
		t.kind = kindProjectMilestone
		t.path = strings.Trim(r.Key[:pct], "/")
		if scoped, ok := strings.CutPrefix(t.path, groupScopePrefix); ok {
			t.kind, t.path = kindGroupMilestone, scoped
		}
		if iid, err := strconv.Atoi(r.Key[pct+1:]); err == nil {
			t.iid = iid
		}
	case amp >= 0:
		// The "&" is the discriminator internal/ref put there: a reference is an
		// epic, everything else is a project issue.
		t.kind = kindEpic
		t.path = strings.Trim(r.Key[:amp], "/")
		if iid, err := strconv.Atoi(r.Key[amp+1:]); err == nil {
			t.iid = iid
		}
	default:
		t.path = strings.Trim(strings.Trim(r.Owner, "/")+"/"+strings.Trim(r.Repo, "/"), "/")
	}
	if t.path == "" {
		if err := def.completes(t.kind, r.Raw); err != nil {
			return target{}, err
		}
		if t.kind == kindProjectMilestone && def.scope == scopeGroup {
			t.kind = kindGroupMilestone
		}
		t.path = def.path
	}

	if t.path == "" {
		example := "acme/platform&12"
		if t.isMilestone() {
			example = "acme/widgets%3"
		}
		return target{}, provider.Errorf(provider.KindBadRef, "gitlab: %q does not name a GitLab group or project — "+
			"write the full reference (%s) or add a Profile whose project names it", r.Raw, example)
	}
	if t.iid <= 0 {
		if t.isMilestone() {
			return target{}, provider.Errorf(provider.KindBadRef, "gitlab: %q does not name a GitLab milestone", r.Raw)
		}
		return target{}, provider.Errorf(provider.KindBadRef, "gitlab: %q does not name a GitLab epic or issue", r.Raw)
	}
	return t, nil
}

// groupScopePrefix marks a group-scoped milestone Key, as internal/ref writes
// it, and is also how a Profile's project declares that it names a group. It is
// safe as a marker because GitLab reserves "groups" as a top-level route, so no
// namespace can be spelled that way.
const groupScopePrefix = "groups/"

// pathScope says which GitLab collection a Profile's path names. GitLab serves
// issues, epics and milestones from a different endpoint for a group than for a
// project, so a path that stands in for an unwritten reference has to declare
// which one it is; the driver never guesses per reference kind.
type pathScope int

const (
	scopeProject pathScope = iota
	scopeGroup
)

// defaultPath is a Profile's project as this driver reads it: the bare path
// GitLab addresses, plus the scope the user declared by writing (or not
// writing) the groupScopePrefix. The zero value is no default at all.
type defaultPath struct {
	path  string
	scope pathScope
}

// ProfilePathNamesNoGroup reports whether a Profile's project path declares a
// group and then names none: "groups/", "groups//", " /groups/ ". Config
// validation calls it rather than re-deriving the prefix rule, so parseDefaultPath
// below really is the only place that rule lives.
func ProfilePathNamesNoGroup(raw string) bool {
	parsed := parseDefaultPath(raw)
	if parsed.scope == scopeGroup || parsed.path != "" {
		return false
	}
	trimmed := strings.TrimLeft(strings.TrimSpace(raw), "/")
	_, prefixed := strings.CutPrefix(trimmed, groupScopePrefix)
	return prefixed
}

// parseDefaultPath is the only place a Profile path's "groups/" prefix is
// interpreted. An unprefixed path is a project path; a "groups/"-prefixed one is
// a group path with the prefix stripped; a path with nothing left after that,
// the degenerate "groups/" included, is no default.
func parseDefaultPath(raw string) defaultPath {
	// The leading "/" goes first and alone: trimming both ends up front would
	// turn "groups/" into the project path "groups".
	trimmed := strings.TrimLeft(strings.TrimSpace(raw), "/")
	if scoped, ok := strings.CutPrefix(trimmed, groupScopePrefix); ok {
		group := strings.Trim(scoped, "/")
		if group == "" {
			return defaultPath{}
		}
		return defaultPath{path: group, scope: scopeGroup}
	}
	return defaultPath{path: strings.Trim(trimmed, "/"), scope: scopeProject}
}

// String spells the path back the way it was written in the config file, so an
// error quoting it names the line the reader has to edit.
func (d defaultPath) String() string {
	if d.scope == scopeGroup {
		return groupScopePrefix + d.path
	}
	return d.path
}

// completes reports whether this default can stand in for a reference of kind k,
// written raw. A project path cannot complete a group epic and a group path
// cannot complete a project issue: either would address an endpoint that does
// not serve the node. A milestone exists in both scopes and follows the default.
func (d defaultPath) completes(k kind, raw string) error {
	if d.path == "" {
		return nil
	}
	switch {
	case k == kindIssue && d.scope == scopeGroup:
		return provider.Errorf(provider.KindBadRef, "gitlab: %q names a project issue, but profile path %q is a group — "+
			"write the full reference (acme/widgets#7) or point the Profile's project at a project", raw, d)
	case k == kindEpic && d.scope == scopeProject:
		return provider.Errorf(provider.KindBadRef, "gitlab: %q names a group epic, but profile path %q is a project — "+
			"write the full reference (acme/platform&12) or write the Profile's project as %q",
			raw, d, groupScopePrefix+d.path)
	}
	return nil
}

// ticketID encodes a target as a model.TicketID: "issue:{project path}#{iid}",
// "epic:{group path}&{iid}", "project-milestone:{project path}%{iid}" or
// "group-milestone:{group path}%{iid}".
//
// The encoding carries the path because a GitLab issue iid is meaningless
// without its project — iids restart at 1 in every project — and FetchDetail
// receives nothing but this id. It is Provider-scoped and opaque by contract
// (model.TicketID's own doc): nothing outside this package may parse it, and it
// is not a URL.
//
// The number a milestone id carries is deliberately the iid rather than the API
// id GitLab's milestone endpoints take: the iid is what the web URL, the "%"
// reference and every human-facing form spell, so it is what an opaque id that
// has to survive a test failure should read as. FetchDetail pays one iids[]
// lookup to recover the API id, which is the same request Resolve makes.
func (t target) ticketID() model.TicketID {
	switch t.kind {
	case kindEpic:
		return model.TicketID(fmt.Sprintf("epic:%s&%d", t.path, t.iid))
	case kindProjectMilestone:
		return model.TicketID(fmt.Sprintf("project-milestone:%s%%%d", t.path, t.iid))
	case kindGroupMilestone:
		return model.TicketID(fmt.Sprintf("group-milestone:%s%%%d", t.path, t.iid))
	default:
		return model.TicketID(fmt.Sprintf("issue:%s#%d", t.path, t.iid))
	}
}

// parseTicketID is ticketID's only reader. A malformed or empty id errors
// before any request rather than being sent as a path.
func parseTicketID(id model.TicketID) (target, error) {
	s := strings.TrimSpace(string(id))

	var (
		t   target
		sep string
	)
	switch {
	case strings.HasPrefix(s, "epic:"):
		t.kind, sep, s = kindEpic, "&", s[len("epic:"):]
	case strings.HasPrefix(s, "issue:"):
		t.kind, sep, s = kindIssue, "#", s[len("issue:"):]
	case strings.HasPrefix(s, "project-milestone:"):
		t.kind, sep, s = kindProjectMilestone, "%", s[len("project-milestone:"):]
	case strings.HasPrefix(s, "group-milestone:"):
		t.kind, sep, s = kindGroupMilestone, "%", s[len("group-milestone:"):]
	default:
		return target{}, malformedTicketID(id)
	}

	at := strings.LastIndex(s, sep)
	if at <= 0 {
		return target{}, malformedTicketID(id)
	}
	iid, err := strconv.Atoi(s[at+1:])
	if err != nil || iid <= 0 {
		return target{}, malformedTicketID(id)
	}
	t.path, t.iid = s[:at], iid
	return t, nil
}

func malformedTicketID(id model.TicketID) error {
	return provider.Errorf(provider.KindBadRef, "gitlab: %q does not name a GitLab epic, issue or milestone", string(id))
}

// String names the target in an error the way a human wrote it, which is also
// the reference form internal/ref reads back.
func (t target) String() string {
	switch t.kind {
	case kindEpic:
		return fmt.Sprintf("%s&%d", t.path, t.iid)
	case kindProjectMilestone:
		return fmt.Sprintf("%s%%%d", t.path, t.iid)
	case kindGroupMilestone:
		return fmt.Sprintf("%s%s%%%d", groupScopePrefix, t.path, t.iid)
	default:
		return fmt.Sprintf("%s#%d", t.path, t.iid)
	}
}

// escaped is the target's path as GitLab addresses it: a namespaced path is
// URL-path-escaped whole, so "acme/platform" becomes "acme%2Fplatform". That is
// GitLab's documented addressing for a namespaced group or project id.
func (t target) escaped() string { return url.PathEscape(t.path) }

// The paths this driver reads. Every one of them is built here, from apiBase
// and the escaped path, so the set of endpoints sitrep depends on is one
// readable list.
func (t target) epicPath() string { return "/groups/" + t.escaped() + "/epics/" + itoa(t.iid) }
func (t target) epicIssuesPath() string {
	return t.epicPath() + "/issues"
}

// epicNotesPath takes the epic's database id rather than its iid: GitLab's own
// documentation states that the epic notes API uses the epic ID instead of the
// epic IID, which is a real trap and the reason FetchDetail reads the epic
// before its notes.
func (t target) epicNotesPath(id int) string {
	return "/groups/" + t.escaped() + "/epics/" + itoa(id) + "/notes"
}

func (t target) issuePath() string      { return "/projects/" + t.escaped() + "/issues/" + itoa(t.iid) }
func (t target) issueNotesPath() string { return t.issuePath() + "/notes" }
func (t target) issueLinksPath() string { return t.issuePath() + "/links" }

// milestoneScope is the collection a milestone hangs off: a project or a group.
// GitLab serves the identical milestone endpoints under both.
func (t target) milestoneScope() string {
	if t.kind == kindGroupMilestone {
		return "/groups/"
	}
	return "/projects/"
}

// milestonesPath is the *list* endpoint, which is how this driver resolves a
// milestone; see Provider.fetchMilestone for why it never reads
// /milestones/:milestone_id.
func (t target) milestonesPath() string { return t.milestoneScope() + t.escaped() + "/milestones" }

// milestoneLookupQuery filters the list down to the one milestone the Ref names.
// The literal "iids[]" is GitLab's own parameter name; Go percent-encodes the
// brackets and GitLab accepts that. per_page=2 is deliberate: two answers to a
// filter documented to be unique is the contradiction fetchMilestone refuses,
// and asking for two is the cheapest way to see it.
func (t target) milestoneLookupQuery() url.Values {
	return url.Values{"iids[]": {itoa(t.iid)}, "per_page": {"2"}}
}

// milestoneIssuesPath takes the milestone's database id rather than its iid:
// GitLab's milestone endpoints are addressed by id while every human-facing form
// carries the iid, which is the trap fetchMilestone exists to bridge.
func (t target) milestoneIssuesPath(id int) string {
	return t.milestonesPath() + "/" + itoa(id) + "/issues"
}

// closedByPath names the merge requests that will close this issue, which is
// what "the merge requests moving this ticket" means: /related_merge_requests
// answers a wider question — it includes every merge request that merely
// mentions the issue — and an open mention would flip a Todo ticket to In
// Progress. This is the same linkage the GitHub driver reads through
// closedByPullRequestsReferences, so the two drivers agree about what they are
// reporting.
func (t target) closedByPath() string {
	return t.issuePath() + "/closed_by"
}

// relatedMergeRequestsPath is the wider list, read only for the head_pipeline
// closed_by omits; see mergeRequestsFor.
func (t target) relatedMergeRequestsPath() string {
	return t.issuePath() + "/related_merge_requests"
}

// mergeRequestApprovalsPath is the Free-tier approvals endpoint — not the
// Premium /approval_state — for one merge request in this target's project.
func (t target) mergeRequestApprovalsPath(mergeRequestIID int) string {
	return "/projects/" + t.escaped() + "/merge_requests/" + itoa(mergeRequestIID) + "/approvals"
}

func itoa(n int) string { return strconv.Itoa(n) }

// epicWebURL is the URL a human opens for a group epic. The driver builds it
// because an epic reached only through another payload's parent_iid has no
// web_url of its own; where GitLab does hand one over — on every epic and issue
// it returns in full — that field is authoritative and used verbatim, because
// it already carries the work-item form GitLab serves today.
func epicWebURL(host, group string, iid int) string {
	if host == "" || group == "" || iid <= 0 {
		return ""
	}
	return "https://" + host + "/groups/" + group + "/-/epics/" + itoa(iid)
}

// milestoneWebURL is the URL a human opens for a milestone. GitLab's own web_url
// is preferred wherever the payload carries one; it is built here because the
// project-milestone shape does not always, which is a documented gap rather than
// an error.
func milestoneWebURL(host string, t target) string {
	if host == "" || t.path == "" || t.iid <= 0 {
		return ""
	}
	if t.kind == kindGroupMilestone {
		return "https://" + host + "/groups/" + t.path + "/-/milestones/" + itoa(t.iid)
	}
	return "https://" + host + "/" + t.path + "/-/milestones/" + itoa(t.iid)
}
