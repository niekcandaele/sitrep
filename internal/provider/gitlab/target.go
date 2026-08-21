package gitlab

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// apiBase is the one place this driver's REST API version is written down; see
// the package doc for why it is v4 REST and not the Work Items GraphQL API.
// Every path in the driver is built from it by url, so a future migration is a
// bounded change rather than a search of the package.
const apiBase = "/api/v4"

// kind says whether a target is a group epic or a project issue. GitLab
// addresses the two through different endpoints, and the Epic Ref grammar
// distinguishes them by the "&" in a reference, so the driver decides once,
// here.
type kind int

const (
	kindIssue kind = iota
	kindEpic
)

// target is one addressable node: a group path plus an epic iid, or a project
// path plus an issue iid. It is the driver's whole addressing rule — nothing
// else in the package builds a path.
type target struct {
	kind kind
	path string
	iid  int
}

// targetFor reads an Epic Ref as the GitLab node it names. defaultPath is the
// Profile's project, used for a reference that names no group or project.
//
// Every rejection this function makes happens before any network call: a Ref
// that names nothing fetchable should fail instantly and say what to write, not
// after a round trip.
func targetFor(r ref.Ref, defaultPath string) (target, error) {
	if r.Tracker != ref.TrackerGitLab {
		return target{}, fmt.Errorf("gitlab: %q is not a GitLab Epic Ref", r.Raw)
	}

	t := target{kind: kindIssue, iid: r.Number}
	if amp := strings.Index(r.Key, "&"); amp >= 0 {
		// The "&" is the discriminator internal/ref put there: a reference is an
		// epic, everything else is a project issue.
		t.kind = kindEpic
		t.path = strings.Trim(r.Key[:amp], "/")
		if iid, err := strconv.Atoi(r.Key[amp+1:]); err == nil {
			t.iid = iid
		}
	} else {
		t.path = strings.Trim(strings.Trim(r.Owner, "/")+"/"+strings.Trim(r.Repo, "/"), "/")
	}
	if t.path == "" {
		t.path = strings.Trim(strings.TrimSpace(defaultPath), "/")
	}

	if t.path == "" {
		return target{}, fmt.Errorf("gitlab: %q does not name a GitLab group or project — "+
			"write the full reference (acme/platform&12) or add a Profile whose project names it", r.Raw)
	}
	if t.iid <= 0 {
		return target{}, fmt.Errorf("gitlab: %q does not name a GitLab epic or issue", r.Raw)
	}
	return t, nil
}

// ticketID encodes a target as a model.TicketID: "issue:{project path}#{iid}"
// or "epic:{group path}&{iid}".
//
// The encoding carries the path because a GitLab issue iid is meaningless
// without its project — iids restart at 1 in every project — and FetchDetail
// receives nothing but this id. It is Provider-scoped and opaque by contract
// (model.TicketID's own doc): nothing outside this package may parse it, and it
// is not a URL.
func (t target) ticketID() model.TicketID {
	if t.kind == kindEpic {
		return model.TicketID(fmt.Sprintf("epic:%s&%d", t.path, t.iid))
	}
	return model.TicketID(fmt.Sprintf("issue:%s#%d", t.path, t.iid))
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
	return fmt.Errorf("gitlab: %q does not name a GitLab epic or issue", string(id))
}

// String names the target in an error the way a human wrote it.
func (t target) String() string {
	if t.kind == kindEpic {
		return fmt.Sprintf("%s&%d", t.path, t.iid)
	}
	return fmt.Sprintf("%s#%d", t.path, t.iid)
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
