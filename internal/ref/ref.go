// Package ref owns sitrep's Ref grammar: turning what a human typed into a
// resolved pointer at one Ticket or one Epic.
//
// A Ref is a value, resolved exactly once by the caller before a Provider is
// chosen. That matters because Provider.Resolve is the polled hot path: if the
// bare-number form were re-resolved there, every refresh would fork a git
// subprocess forever.
//
// The package does no network I/O. The only I/O it may do is reading the
// working directory's git origin remote, and only for the bare-number form.
package ref

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Tracker identifies which Tracker a Ref points at.
type Tracker string

// The Trackers sitrep knows how to name. Every one of them has a Provider; a
// Ref whose Tracker is Unknown is one the CLI cannot route at all.
const (
	// TrackerUnknown is the zero value: the Ref names no recognizable Tracker.
	TrackerUnknown Tracker = ""
	// TrackerGitHub is github.com or a GitHub Enterprise host.
	TrackerGitHub Tracker = "github"
	// TrackerGitLab is gitlab.com or a self-managed GitLab host.
	TrackerGitLab Tracker = "gitlab"
	// TrackerJira is a Jira Cloud site.
	TrackerJira Tracker = "jira"
)

// Ref is a resolved pointer: everything a Provider needs to locate one Ticket
// or Epic, with the user's original input kept for error messages.
type Ref struct {
	// Tracker says which Provider serves this Ref.
	Tracker Tracker
	// Host is the Tracker host, e.g. "github.com" or a GitHub Enterprise host.
	Host string
	// Owner is the GitHub owner or organization, or the first path segment of a
	// GitLab namespace.
	Owner string
	// Repo is the repository name. For a nested GitLab path it is everything
	// after the first segment, so the data survives until the GitLab driver
	// reads it.
	Repo string
	// Number is the tracker-native issue number. Zero when the Ref names no
	// single Ticket or Epic, as ParseRemoteURL returns.
	Number int
	// Key is the Jira-style key, e.g. "PROJ-12", always upper-cased, GitLab's
	// own native epic reference, e.g. "acme/platform&12", or GitLab's milestone
	// reference, e.g. "acme/widgets%3" and "groups/acme%3" — always the form a
	// human can type back. It is empty for GitHub and for a GitLab project
	// issue. A Ref that carries only a Key carries no Host: only a Profile knows
	// which site or instance the key names.
	Key string
	// Raw is exactly what the user typed, kept for error messages.
	Raw string
}

// String renders the Ref the way a human would write it, e.g.
// "acme/widgets#111". Refs that name no repository fall back to what the user
// typed.
func (r Ref) String() string {
	switch {
	case r.Key != "":
		return r.Key
	case r.Owner != "" && r.Repo != "" && r.Number > 0:
		return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
	case r.Owner != "" && r.Repo != "":
		return r.Owner + "/" + r.Repo
	default:
		return r.Raw
	}
}

// RemoteLookup returns the URL of the given git remote for the working
// directory dir.
type RemoteLookup func(ctx context.Context, dir, remote string) (string, error)

// Option configures Parse.
type Option func(*options)

type options struct {
	lookup RemoteLookup
	dir    string
}

// WithRemoteLookup replaces the git remote lookup used to resolve a bare
// number. Tests inject a stub so no test needs a real clone.
func WithRemoteLookup(l RemoteLookup) Option {
	return func(o *options) {
		if l != nil {
			o.lookup = l
		}
	}
}

// WithDir sets the directory whose git origin remote resolves a bare number.
// The default is the process working directory.
func WithDir(dir string) Option {
	return func(o *options) { o.dir = dir }
}

// originRemote is the only remote consulted for a bare number: sitrep resolves
// "111" against where this clone came from, not against an arbitrary fork.
const originRemote = "origin"

// Parse resolves a user-supplied Ref string. It accepts, in this order, a
// full issue URL, "owner/repo#111" (or "owner/repo/111"), a Jira-style key such
// as "ABC-123", and a bare number such as "111" or "#111", which is resolved
// through the working directory's git origin remote.
//
// The order is not arbitrary. A URL is unambiguous and goes first. The
// owner/repo forms contain a "/" and a key never does, so those two cannot
// collide and either could come first; the key form is tried before the bare
// number because a bare number is the only form that does I/O, and no form that
// can be decided from the text alone should sit behind a git subprocess.
//
// Parse never touches the network.
func Parse(ctx context.Context, raw string, opts ...Option) (Ref, error) {
	o := options{lookup: gitRemoteLookup}
	for _, opt := range opts {
		opt(&o)
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Ref{}, errors.New("a Ref is required")
	}

	if strings.Contains(trimmed, "://") {
		return parseURL(trimmed, raw)
	}
	// GitLab's own reference form, "&12" or "acme/platform&12", is tried before
	// the owner/repo forms because it cannot collide with any of them: no other
	// Ref form contains an "&" at all, so a string carrying one is either
	// this form or nothing.
	if r, ok := parseGitLabReference(trimmed, raw); ok {
		return r, nil
	}
	// GitLab's milestone reference, "%3" or "acme/widgets%3", is tried here for
	// the same reason and one more: the order is load-bearing. Left to
	// parseOwnerRepoNumber, "acme/platform/core%3" would be split on its last
	// "/", found to have a head containing "/", and returned as a hard "cannot
	// parse" error rather than declined. No other Ref form contains a "%".
	if r, ok := parseGitLabMilestoneReference(trimmed, raw); ok {
		return r, nil
	}
	if r, ok, err := parseOwnerRepoNumber(trimmed, raw); ok {
		return r, err
	}
	if r, ok := parseKey(trimmed, raw); ok {
		return r, nil
	}
	if n, ok := parseNumber(strings.TrimPrefix(trimmed, "#")); ok {
		return resolveBareNumber(ctx, o, n, raw)
	}
	return Ref{}, unparseable(raw)
}

// ParseParent resolves a Ticket's parent into the Ref that names it, so
// navigating up is a re-parse of what the Provider already returned rather than
// a second lookup. child is the Ref the parent was discovered through: an
// Enterprise host, or a Tracker the URL form cannot betray, is inherited from
// it. The parent's URL is preferred over its Key because a URL carries its own
// host; the Key is the fallback for a Tracker whose parent has no URL.
//
// It never resolves a bare number and therefore touches no I/O: a Key with no
// owner/repo takes them from child rather than reaching for a git remote.
func ParseParent(key, url string, child Ref) (Ref, error) {
	if r, ok := parentFromURL(url, child); ok {
		return r, nil
	}
	if r, ok := parentFromKey(key, child); ok {
		return r, nil
	}
	return Ref{}, fmt.Errorf("cannot tell which Epic %q belongs to", child.String())
}

// parentFromURL reads the parent's own URL, which is the preferred form because
// it carries its own host.
func parentFromURL(raw string, child Ref) (Ref, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || !strings.Contains(s, "://") {
		return Ref{}, false
	}
	r, err := parseURL(s, s)
	if err != nil {
		return Ref{}, false
	}
	// A parent has to name something a Provider can fetch. On GitHub that is an
	// issue number; on Jira it is a key, and a /browse/ URL carries no number at
	// all — so either identity is enough and neither implies the other.
	if r.Number <= 0 && r.Key == "" {
		return Ref{}, false
	}
	return inherit(r, child), true
}

// parentFromKey reads the parent's display key: "acme/widgets#111" names its own
// repository, and a bare "#111" is the child's own repository — which is exactly
// what the qualified/unqualified distinction means everywhere else in sitrep.
func parentFromKey(raw string, child Ref) (Ref, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, false
	}
	// A GitLab epic reference names a group and no instance, so the host is
	// inherited from the child — the same rule inherit already encodes. It is
	// read before the owner/repo forms for the same reason Parse tries it first:
	// no other form contains an "&".
	if r, ok := parseGitLabReference(s, s); ok {
		return inherit(r, child), true
	}
	// A GitLab milestone reference names no instance either, so the same
	// inheritance applies. It is read before the owner/repo forms for the reason
	// Parse gives: a nested path would otherwise be a hard parse error.
	if r, ok := parseGitLabMilestoneReference(s, s); ok {
		return inherit(r, child), true
	}

	if r, ok, err := parseOwnerRepoNumber(s, s); ok {
		if err != nil {
			return Ref{}, false
		}
		return inherit(r, child), true
	}

	// A Jira-style key names a project and no site, so the host is inherited from
	// the child — the same rule inherit already encodes. It is read only for a
	// child that came from Jira: "PROJ-111" is not a key a GitHub parent can
	// have, and reading it as one there would turn an unresolvable parent into a
	// confident pointer at the wrong Tracker.
	if child.Tracker == TrackerJira {
		if key, ok := normalizeKey(s); ok {
			return Ref{Tracker: TrackerJira, Host: child.Host, Key: key, Raw: s}, true
		}
	}

	n, ok := parseNumber(strings.TrimPrefix(s, "#"))
	if !ok || child.Owner == "" || child.Repo == "" {
		return Ref{}, false
	}
	return inherit(Ref{
		Tracker: child.Tracker,
		Host:    child.Host,
		Owner:   child.Owner,
		Repo:    child.Repo,
		Number:  n,
		Raw:     s,
	}, child), true
}

// inherit fills in what the parent's own text cannot say. parseOwnerRepoNumber
// hard-codes github.com, and a URL on an unrecognized host names no Tracker at
// all, so a GitHub Enterprise epic reached through an Enterprise child would
// otherwise come back pointing at github.com.
func inherit(r, child Ref) Ref {
	if r.Tracker == TrackerUnknown {
		r.Tracker = child.Tracker
	}
	if child.Host != "" && (r.Host == "" || (r.Host == defaultHost && child.Host != defaultHost)) {
		r.Host = child.Host
	}
	return r
}

// defaultHost is the host the grammar assumes when the text it read named none.
const defaultHost = "github.com"

// ResolveOrigin reads and parses the current clone's origin route without
// fabricating an issue number. The returned Ref never retains the remote's raw
// text: HTTPS remotes may carry credential-bearing userinfo, while callers need
// only the parsed route.
func ResolveOrigin(ctx context.Context, opts ...Option) (Ref, error) {
	o := options{lookup: gitRemoteLookup}
	for _, opt := range opts {
		opt(&o)
	}
	remote, err := o.lookup(ctx, o.dir, originRemote)
	if err != nil {
		return Ref{}, err
	}
	r, err := ParseRemoteURL(remote)
	if err != nil {
		return Ref{}, errors.New("cannot parse git origin as a remote URL")
	}
	r.Raw = originRemote
	return r, nil
}

// ParseRemoteURL turns a git remote URL into a Ref naming its repository, with
// Number left zero. It is exported because every Tracker driver has to
// recognize its own clone URLs and they all arrive in the same handful of
// shapes.
func ParseRemoteURL(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, errors.New(`cannot parse "" as a git remote URL`)
	}

	host, path, err := splitRemoteURL(s)
	if err != nil {
		return Ref{}, err
	}

	owner, repo, err := splitOwnerRepo(path)
	if err != nil {
		return Ref{}, fmt.Errorf("cannot parse %q as a git remote URL: %w", raw, err)
	}

	tracker := trackerForHost(host)
	if tracker == TrackerUnknown {
		// An unrecognized host serving a clone is treated as GitHub Enterprise:
		// that is the only self-hosted flavour sitrep has a driver for.
		tracker = TrackerGitHub
	}
	return Ref{Tracker: tracker, Host: host, Owner: owner, Repo: repo, Raw: raw}, nil
}

// splitRemoteURL separates a git remote URL into host and path, handling both
// the scp-like "git@host:owner/repo.git" form and every scheme form (https,
// ssh, git) a real clone produces. Userinfo is dropped; an explicit port is
// kept, because a self-hosted instance is unreachable without it.
//
// The scp-like form's colon separates host from path rather than host from
// port, so "ssh://git@host:2222/owner/repo" is the only shape that carries one.
func splitRemoteURL(s string) (host, path string, err error) {
	if !strings.Contains(s, "://") {
		at := strings.Index(s, "@")
		colon := strings.Index(s, ":")
		if colon <= 0 || colon < at {
			return "", "", fmt.Errorf("cannot parse %q as a git remote URL", s)
		}
		host = s[at+1 : colon]
		if host == "" || strings.Contains(host, "/") {
			return "", "", fmt.Errorf("cannot parse %q as a git remote URL", s)
		}
		return canonicalHost(host), s[colon+1:], nil
	}

	u, parseErr := url.Parse(s)
	if parseErr != nil {
		return "", "", fmt.Errorf("cannot parse %q as a git remote URL: %w", s, parseErr)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("cannot parse %q as a git remote URL", s)
	}
	return canonicalHost(u.Host), u.Path, nil
}

// splitOwnerRepo splits a repository path into its owner and its repository
// name, dropping a single trailing ".git". A nested GitLab namespace keeps its
// whole tail in the repository name rather than being truncated.
func splitOwnerRepo(path string) (owner, repo string, err error) {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return "", "", errors.New("no owner/repository in the path")
	}
	for _, s := range segments {
		if s == "" {
			return "", "", errors.New("no owner/repository in the path")
		}
	}
	return segments[0], strings.Join(segments[1:], "/"), nil
}

// HostTracker maps a host onto the Tracker serving it, or TrackerUnknown for a
// host sitrep does not recognize. It is exported so the CLI can ask whether a
// Ref's Tracker was read off a known host or guessed: an explicit --provider is
// authoritative for a guess and must not override a host that named itself.
//
// The host may carry a port; the port plays no part in which Tracker serves it.
func HostTracker(host string) Tracker {
	return trackerForHost(host)
}

// trackerForHost maps a host onto the Tracker serving it. An unrecognized host
// is deliberately unknown here; callers decide whether the surrounding context
// justifies assuming GitHub Enterprise.
//
// It classifies on the host alone, with any port removed. A leading "www." is
// not stripped here: canonicalHost already did that, and only for the hosts
// below, so that "www.ghe.example" stays a distinct host — and therefore a
// distinct credential scope — from "ghe.example".
func trackerForHost(host string) Tracker {
	return trackerForBareHost(hostWithoutPort(strings.ToLower(strings.TrimSpace(host))))
}

func trackerForBareHost(host string) Tracker {
	switch {
	case host == "github.com":
		return TrackerGitHub
	case host == "gitlab.com":
		return TrackerGitLab
	case strings.HasSuffix(host, ".atlassian.net"):
		return TrackerJira
	default:
		return TrackerUnknown
	}
}

// canonicalHost is the one spelling of a host a Ref carries. It lower-cases,
// keeps an explicit port — a self-hosted instance on git.acme.test:8443 is
// unreachable without one — and strips a leading "www." only when the
// remainder is a host sitrep recognizes, so www.github.com becomes the
// canonical API host while www.ghe.example stays itself.
func canonicalHost(hostport string) string {
	host := strings.ToLower(strings.TrimSpace(hostport))
	if !strings.HasPrefix(host, "www.") {
		return host
	}
	stripped := strings.TrimPrefix(host, "www.")
	if trackerForBareHost(hostWithoutPort(stripped)) == TrackerUnknown {
		return host
	}
	return stripped
}

// hostWithoutPort drops a ":<port>" suffix, leaving an IPv6 literal's own
// colons alone.
func hostWithoutPort(host string) string {
	if strings.HasSuffix(host, "]") {
		return host
	}
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i+1:], "]") {
		if _, ok := parseNumber(host[i+1:]); ok {
			return host[:i]
		}
	}
	return host
}

// parseURL reads a full tracker URL. A GitHub-shaped path
// (/{owner}/{repo}/issues/{n}, or pull/{n} for a link someone copied from a
// pull request) is accepted on any host; other hosts yield a Ref with the
// Tracker their host — or their path shape — implies, so the CLI can route it
// to that Tracker's driver instead of failing with "cannot parse".
func parseURL(s, raw string) (Ref, error) {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return Ref{}, unparseable(raw)
	}

	host := canonicalHost(u.Host)
	tracker := trackerForHost(host)
	segments := pathSegments(u.Path)

	if owner, repo, number, ok := githubIssuePath(segments); ok {
		if tracker == TrackerUnknown || tracker == TrackerGitHub {
			return Ref{
				Tracker: TrackerGitHub,
				Host:    host,
				Owner:   owner,
				Repo:    repo,
				Number:  number,
				Raw:     raw,
			}, nil
		}
	}

	// GitLab's "/-/" separator is a URL shape no other Tracker produces, so it
	// identifies a self-managed GitLab instance the host name alone cannot —
	// which is what keeps https://git.acme.test/acme/widgets/-/issues/7 from
	// failing with "cannot parse". It is an additional signal, never a
	// replacement: trackerForHost still decides for a host it recognizes.
	if tracker == TrackerUnknown && hasGitLabSeparator(segments) {
		tracker = TrackerGitLab
	}

	if tracker == TrackerUnknown || tracker == TrackerGitHub {
		// A GitHub URL that is not an issue URL names no Epic, and an
		// unrecognized host with a foreign path shape names nothing at all.
		return Ref{}, unparseable(raw)
	}
	return foreignRef(tracker, host, segments, raw), nil
}

// hasGitLabSeparator reports whether a URL path carries GitLab's "/-/"
// separator.
func hasGitLabSeparator(segments []string) bool {
	for _, s := range segments {
		if s == "-" {
			return true
		}
	}
	return false
}

// githubIssuePath matches /{owner}/{repo}/issues/{n} and its pull-request
// twin.
func githubIssuePath(segments []string) (owner, repo string, number int, ok bool) {
	if len(segments) != 4 {
		return "", "", 0, false
	}
	if segments[2] != "issues" && segments[2] != "pull" {
		return "", "", 0, false
	}
	n, ok := parseNumber(segments[3])
	if !ok {
		return "", "", 0, false
	}
	return segments[0], segments[1], n, true
}

// foreignRef reads a URL belonging to a Tracker whose paths are not
// GitHub-shaped: a Jira /browse/ key, or one of GitLab's four "/-/" forms.
func foreignRef(tracker Tracker, host string, segments []string, raw string) Ref {
	r := Ref{Tracker: tracker, Host: host, Raw: raw}
	if tracker == TrackerJira {
		// Upper-cased so a /browse/ URL and the bare key form produce the same
		// Key, which is what Profile prefix matching compares.
		if len(segments) == 2 && segments[0] == "browse" {
			if key, ok := normalizeKey(segments[1]); ok {
				r.Key = key
			} else {
				r.Key = strings.ToUpper(segments[1])
			}
		}
		return r
	}

	return gitLabRef(host, segments, raw)
}

// gitLabRef reads GitLab's Epic-Ref-bearing URL shapes, all of which hang off
// the "/-/" separator:
//
//	/{namespace…}/{project}/-/issues/{n}      a project issue
//	/{namespace…}/{project}/-/work_items/{n}  the same issue, in the form
//	                                          GitLab's API now returns as web_url
//	/groups/{group path}/-/epics/{n}          a group epic
//	/groups/{group path}/-/work_items/{n}     the same epic
//	/{namespace…}/{project}/-/milestones/{n}  a project milestone
//	/groups/{group path}/-/milestones/{n}     a group milestone
//
// The leading "groups" segment is what distinguishes a group epic from a
// project issue, and the "&" it puts in Key is what the GitLab driver reads to
// tell the two apart afterwards.
func gitLabRef(host string, segments []string, raw string) Ref {
	r := Ref{Tracker: TrackerGitLab, Host: host, Raw: raw}

	sep := -1
	for i, s := range segments {
		if s == "-" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return r
	}

	head := segments[:sep]
	group := len(head) > 1 && head[0] == "groups"
	if group {
		head = head[1:]
	}

	// A group path may be one segment ("gitlab-org") or nested
	// ("acme/platform/core"), so it is not forced through splitOwnerRepo's
	// two-segment minimum: the first segment is the Owner, the rest — possibly
	// empty — is the Repo, and Key is authoritative.
	if len(head) > 0 {
		r.Owner, r.Repo = head[0], strings.Join(head[1:], "/")
	}

	if len(segments) != sep+3 {
		return r
	}
	n, ok := parseNumber(segments[sep+2])
	if !ok {
		return r
	}
	switch segments[sep+1] {
	case "issues":
		if !group {
			r.Number = n
		}
	case "epics":
		if group {
			r.Number = n
			r.Key = strings.Join(head, "/") + "&" + segments[sep+2]
		}
	case "work_items":
		// GitLab now serves work-item URLs for both, so which one this is comes
		// from the "groups" prefix rather than from the noun.
		r.Number = n
		if group {
			r.Key = strings.Join(head, "/") + "&" + segments[sep+2]
		}
	case "milestones":
		// A milestone is how GitLab Free spells an Epic. The "%" it puts in Key
		// is what the GitLab driver reads to recognize one, and the "groups/"
		// prefix is what tells it which scope to address.
		r.Number = n
		r.Key = strings.Join(head, "/") + "%" + segments[sep+2]
		if group {
			r.Key = groupScopePrefix + r.Key
		}
	}
	return r
}

// parseGitLabReference reads GitLab's own epic reference form, which its API
// returns verbatim as references.full: "acme/platform&12", or "&12" when the
// group is left for a Profile to supply.
//
// The resulting Ref carries no Host on purpose. A reference names a group, not
// an instance, exactly as a Jira key names a project and not a site; the CLI
// completes it from the Profile that serves it.
func parseGitLabReference(s, raw string) (Ref, bool) {
	amp := strings.LastIndex(s, "&")
	if amp < 0 {
		return Ref{}, false
	}
	n, ok := parseNumber(s[amp+1:])
	if !ok {
		return Ref{}, false
	}
	group := strings.Trim(s[:amp], "/")
	if strings.Contains(group, "&") {
		return Ref{}, false
	}

	r := Ref{Tracker: TrackerGitLab, Number: n, Key: group + "&" + s[amp+1:], Raw: raw}
	if group != "" {
		segments := strings.Split(group, "/")
		r.Owner, r.Repo = segments[0], strings.Join(segments[1:], "/")
	}
	return r, true
}

// parseGitLabMilestoneReference reads GitLab's own milestone reference form:
// "acme/widgets%3" for a project milestone, "groups/acme/platform%3" for a group
// one, and "%3" when the path is left for a Profile to supply.
//
// The "groups/" prefix is sitrep's spelling rather than GitLab's, and it is
// unambiguous because GitLab reserves "groups" as a top-level route — no user or
// group namespace can be named that. A bare "%3" therefore means a *project*
// milestone, which is what GitLab's own "%" syntax means too.
//
// The resulting Ref carries no Host on purpose, exactly as the epic reference
// form does not: a reference names a project or a group, not an instance.
func parseGitLabMilestoneReference(s, raw string) (Ref, bool) {
	pct := strings.LastIndex(s, "%")
	if pct < 0 {
		return Ref{}, false
	}
	n, ok := parseNumber(s[pct+1:])
	if !ok {
		return Ref{}, false
	}
	path := strings.Trim(s[:pct], "/")
	if strings.Contains(path, "%") {
		return Ref{}, false
	}

	// "groups/" is a scope marker, not a path: with nothing after it the
	// reference names no group at all. Accepting it would silently resolve
	// against the Profile's default project — a different milestone than the one
	// asked for — so it is declined and Parse reports it as unparseable.
	if path == strings.TrimSuffix(groupScopePrefix, "/") {
		return Ref{}, false
	}

	r := Ref{Tracker: TrackerGitLab, Number: n, Key: path + "%" + s[pct+1:], Raw: raw}
	// Owner and Repo carry the path GitLab addresses, without sitrep's scope
	// marker; Key stays authoritative about which scope it is.
	if scoped := strings.TrimPrefix(path, groupScopePrefix); scoped != "" {
		segments := strings.Split(scoped, "/")
		r.Owner, r.Repo = segments[0], strings.Join(segments[1:], "/")
	}
	return r, true
}

// groupScopePrefix marks a group-scoped GitLab milestone Key. See
// parseGitLabMilestoneReference for why it cannot collide with a namespace.
const groupScopePrefix = "groups/"

// parseOwnerRepoNumber reads the "acme/widgets#111" and "acme/widgets/111"
// forms. The first is what a cross-repo child's Key renders as, so a human can
// paste it straight back into sitrep.
func parseOwnerRepoNumber(s, raw string) (Ref, bool, error) {
	sep := strings.LastIndexAny(s, "#/")
	if sep <= 0 || sep == len(s)-1 {
		return Ref{}, false, nil
	}
	head, tail := s[:sep], s[sep+1:]
	if !strings.Contains(head, "/") {
		return Ref{}, false, nil
	}

	owner, repo, err := splitOwnerRepo(head)
	if err != nil {
		return Ref{}, false, nil
	}
	n, ok := parseNumber(tail)
	if !ok {
		return Ref{}, true, unparseable(raw)
	}
	return Ref{
		Tracker: TrackerGitHub,
		Host:    "github.com",
		Owner:   owner,
		Repo:    repo,
		Number:  n,
		Raw:     raw,
	}, true, nil
}

// parseKey reads the Jira-style key form, "ABC-123": a project key, a hyphen,
// and a positive decimal.
//
// The resulting Ref carries no Host on purpose. A key names a project, and only
// a Profile knows which Jira site that project lives on; the CLI completes the
// Ref from the Profile it matches by key prefix. Number stays zero too — a Jira
// key is not a number, and the Jira driver reads Key.
func parseKey(s, raw string) (Ref, bool) {
	key, ok := normalizeKey(s)
	if !ok {
		return Ref{}, false
	}
	return Ref{Tracker: TrackerJira, Key: key, Raw: raw}, true
}

// normalizeKey validates a Jira-style key and upper-cases it, so that the two
// roads to a key — the bare form and a /browse/ URL — produce the same value.
func normalizeKey(s string) (string, bool) {
	hyphen := strings.LastIndex(s, "-")
	if hyphen <= 0 {
		return "", false
	}
	if _, ok := parseNumber(s[hyphen+1:]); !ok {
		return "", false
	}
	if !isKeyPrefix(s[:hyphen]) {
		return "", false
	}
	return strings.ToUpper(s), true
}

// isKeyPrefix reports whether s is a Jira project key: a letter followed by
// letters, digits or underscores.
func isKeyPrefix(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case (c >= '0' && c <= '9' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// KeyPrefix returns the project part of a Jira-style key: "ABC" for "ABC-123",
// and "" for anything that is not one. It lives here because the shape of a key
// is the Ref grammar's business, which makes Profile prefix matching this
// function plus a comparison.
func KeyPrefix(key string) string {
	normalized, ok := normalizeKey(strings.TrimSpace(key))
	if !ok {
		return ""
	}
	return normalized[:strings.LastIndex(normalized, "-")]
}

// resolveBareNumber turns "111" into a full Ref by reading where this clone
// came from.
func resolveBareNumber(ctx context.Context, o options, number int, raw string) (Ref, error) {
	remote, err := o.lookup(ctx, o.dir, originRemote)
	if err != nil {
		return Ref{}, noOriginError(raw, err)
	}
	r, err := ParseRemoteURL(remote)
	if err != nil {
		return Ref{}, unrecognisedOriginError(raw, remote, err)
	}
	r.Number = number
	r.Raw = raw
	return r, nil
}

// noOriginError is the failure when there is no origin remote to read at all.
// It names no tracker: a bare number resolves against GitHub, GitLab and Jira
// clones alike, and naming one sends users of the others hunting for a remote
// nobody expected.
func noOriginError(raw string, err error) error {
	return fmt.Errorf("%q is a bare number and this directory has no origin remote — "+
		"pass a full issue URL instead: %w", strings.TrimSpace(raw), err)
}

// unrecognisedOriginError is the failure when there is an origin remote and it
// is not a tracker sitrep can address.
func unrecognisedOriginError(raw, remote string, err error) error {
	return fmt.Errorf("%q is a bare number and this directory's origin remote %q is not a "+
		"tracker sitrep recognises — pass a full issue URL instead: %w",
		strings.TrimSpace(raw), strings.TrimSpace(remote), err)
}

// parseNumber accepts a positive decimal issue number and nothing else: "0",
// "-3", "1.5" and anything that overflows an int are rejected rather than
// silently truncated.
func parseNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func pathSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

const maxDiagnosticRefBytes = 80

func unparseable(raw string) error {
	return fmt.Errorf(`cannot parse %s as a Ref — pass an issue URL, "owner/repo#123", `+
		`"PROJ-123" or a bare number inside a clone (run "sitrep --help" for every accepted form)`,
		diagnosticRef(raw))
}

func diagnosticRef(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) <= maxDiagnosticRefBytes {
		return fmt.Sprintf("%q", trimmed)
	}
	return fmt.Sprintf("%q… (%d bytes)", trimmed[:maxDiagnosticRefBytes], len(trimmed))
}
