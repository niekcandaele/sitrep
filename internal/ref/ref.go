// Package ref owns sitrep's Epic Ref grammar: turning what a human typed into a
// resolved pointer at one Epic.
//
// A Ref is a value, resolved exactly once by the caller before a Provider is
// chosen. That matters because Provider.FetchEpic is the polled hot path: if the
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

// Tracker identifies which Tracker an Epic Ref points at.
type Tracker string

// The Trackers sitrep knows how to name. A Ref may carry a Tracker sitrep has
// no Provider for yet; the CLI turns that into a "not supported yet" error.
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

// Ref is a resolved Epic Ref: everything a Provider needs to locate an Epic,
// with the user's original input kept for error messages.
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
	// single Epic, as ParseRemoteURL returns.
	Number int
	// Key is the Jira-style key, e.g. "PROJ-12". It is empty for GitHub and
	// nothing populates it yet: it exists now so the Config & Profiles and Jira
	// tickets can fill it in without widening this type.
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

// Parse resolves a user-supplied Epic Ref string. It accepts, in this order, a
// full issue URL, "owner/repo#111" (or "owner/repo/111"), and a bare number
// such as "111" or "#111", which is resolved through the working directory's
// git origin remote.
//
// Parse never touches the network.
func Parse(ctx context.Context, raw string, opts ...Option) (Ref, error) {
	o := options{lookup: gitRemoteLookup}
	for _, opt := range opts {
		opt(&o)
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Ref{}, errors.New("an Epic Ref is required")
	}

	if strings.Contains(trimmed, "://") {
		return parseURL(trimmed, raw)
	}
	if r, ok, err := parseOwnerRepoNumber(trimmed, raw); ok {
		return r, err
	}
	if n, ok := parseNumber(strings.TrimPrefix(trimmed, "#")); ok {
		return resolveBareNumber(ctx, o, n, raw)
	}
	return Ref{}, unparseable(raw)
}

// ParseParent resolves a Ticket's parent into the Epic Ref that names it, so
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
	if err != nil || r.Number <= 0 {
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
	if r, ok, err := parseOwnerRepoNumber(s, s); ok {
		if err != nil {
			return Ref{}, false
		}
		return inherit(r, child), true
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

	host = strings.ToLower(host)
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
// ssh, git) a real clone produces. Userinfo is dropped.
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
		return host, s[colon+1:], nil
	}

	u, parseErr := url.Parse(s)
	if parseErr != nil {
		return "", "", fmt.Errorf("cannot parse %q as a git remote URL: %w", s, parseErr)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("cannot parse %q as a git remote URL", s)
	}
	return u.Hostname(), u.Path, nil
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

// trackerForHost maps a host onto the Tracker serving it. An unrecognized host
// is deliberately unknown here; callers decide whether the surrounding context
// justifies assuming GitHub Enterprise.
func trackerForHost(host string) Tracker {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
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

// parseURL reads a full tracker URL. A GitHub-shaped path
// (/{owner}/{repo}/issues/{n}, or pull/{n} for a link someone copied from a
// pull request) is accepted on any host; other hosts still yield a Ref with the
// Tracker their host implies, so the CLI can say "not supported yet" instead of
// "cannot parse".
func parseURL(s, raw string) (Ref, error) {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return Ref{}, unparseable(raw)
	}

	host := strings.ToLower(u.Hostname())
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

	if tracker == TrackerUnknown || tracker == TrackerGitHub {
		// A GitHub URL that is not an issue URL names no Epic, and an
		// unrecognized host with a foreign path shape names nothing at all.
		return Ref{}, unparseable(raw)
	}
	return foreignRef(tracker, host, segments, raw), nil
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

// foreignRef builds a best-effort Ref for a Tracker sitrep has no driver for.
// Nothing reads these fields today beyond the Tracker; the point is to carry
// the user's input intact to the "not supported yet" message and to leave the
// Jira and GitLab drivers something to sharpen.
func foreignRef(tracker Tracker, host string, segments []string, raw string) Ref {
	r := Ref{Tracker: tracker, Host: host, Raw: raw}
	if tracker == TrackerJira {
		if len(segments) == 2 && segments[0] == "browse" {
			r.Key = segments[1]
		}
		return r
	}

	// GitLab: /{namespace...}/-/issues/{n}, where the namespace may nest.
	for i, s := range segments {
		if s != "-" {
			continue
		}
		if owner, repo, err := splitOwnerRepo(strings.Join(segments[:i], "/")); err == nil {
			r.Owner, r.Repo = owner, repo
		}
		if len(segments) == i+3 && segments[i+1] == "issues" {
			if n, ok := parseNumber(segments[i+2]); ok {
				r.Number = n
			}
		}
		break
	}
	return r
}

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

// resolveBareNumber turns "111" into a full Ref by reading where this clone
// came from.
func resolveBareNumber(ctx context.Context, o options, number int, raw string) (Ref, error) {
	remote, err := o.lookup(ctx, o.dir, originRemote)
	if err != nil {
		return Ref{}, bareNumberError(raw, err)
	}
	r, err := ParseRemoteURL(remote)
	if err != nil {
		return Ref{}, bareNumberError(raw, err)
	}
	r.Number = number
	r.Raw = raw
	return r, nil
}

func bareNumberError(raw string, err error) error {
	return fmt.Errorf("%q is a bare number and this directory has no GitHub origin remote — "+
		"pass a full issue URL instead: %w", strings.TrimSpace(raw), err)
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

func unparseable(raw string) error {
	return fmt.Errorf("cannot parse %q as an Epic Ref", strings.TrimSpace(raw))
}
