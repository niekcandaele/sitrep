// Ref and profile resolution: turning raw user selectors into a connectionRoute
// plus the config Profile that serves it, and reporting the conflicts that make
// a selection unservable.

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

type connectionRoute struct {
	tracker    ref.Tracker
	host       string
	gitLabPath string
	raw        string
}

type resolvedSelection struct {
	selector provider.Selector
	first    ref.Ref
	profile  *config.Profile
	route    connectionRoute
}

// resolveSelection resolves every supplied Ref once and turns the invocation
// form into the Selector reused by preflight and every refresh. Routing stays a
// startup concern: Providers never read git remotes or Profiles. forceRefList
// preserves stdin's explicit-list meaning when it carries only one Ref.
func (d Deps) resolveSelection(ctx context.Context, cfg config.Config, rawRefs []string, forceRefList, querySelected bool,
	query, providerName, profileName string) (resolvedSelection, error) {
	if querySelected {
		return d.resolveQuerySelection(ctx, cfg, query, providerName, profileName)
	}

	refs := make([]ref.Ref, len(rawRefs))
	for i, raw := range rawRefs {
		r, err := d.resolveRef(ctx, raw, providerName)
		if err != nil {
			return resolvedSelection{}, err
		}
		r = retagProfileClaimedHost(cfg, r)
		if providerName != providerAuto && providerName != providerFake {
			if r.Tracker, err = forceTracker(providerName, r); err != nil {
				return resolvedSelection{}, err
			}
		}
		refs[i] = r
	}

	if left, right, ok := firstTrackerConflict(refs); ok {
		completed := append([]ref.Ref(nil), refs...)
		for i, r := range completed {
			if prof, err := selectProfile(cfg, r, profileName); err == nil && prof != nil {
				completed[i] = prof.Complete(r)
			}
		}
		return resolvedSelection{}, mixedTrackerError(completed[left], completed[right])
	}

	profiles := make([]*config.Profile, len(refs))
	for i, r := range refs {
		prof, err := selectProfile(cfg, r, profileName)
		if err != nil {
			return resolvedSelection{}, err
		}
		profiles[i] = prof
		if prof != nil {
			refs[i] = prof.Complete(r)
		}
	}

	if left, right, ok := firstRouteConflict(refs); ok {
		return resolvedSelection{}, routeConflictError(refs[left], refs[right])
	}
	if left, right, ok := firstProfileConflict(profiles); ok {
		//nolint:staticcheck // The specified CLI sentence starts with the domain term "Refs".
		return resolvedSelection{}, fmt.Errorf("Refs in one Watchlist resolve through different Profiles (%q and %q); pass --profile to choose one",
			profileIdentity(profiles[left]), profileIdentity(profiles[right]))
	}

	selection := resolvedSelection{
		first:   refs[0],
		profile: profiles[0],
		route:   routeFromRef(refs[0], profiles[0]),
	}
	if len(rawRefs) == 1 && !forceRefList {
		selection.selector = provider.EpicSelector{Ref: refs[0]}
		return selection, nil
	}

	unique := deduplicateRefs(refs)
	selection.first = unique[0]
	selection.route = routeFromRef(unique[0], profiles[0])
	selection.selector = provider.RefListSelector{Refs: unique}
	return selection, nil
}

func routeFromRef(r ref.Ref, prof *config.Profile) connectionRoute {
	return connectionRoute{
		tracker:    r.Tracker,
		host:       r.Host,
		gitLabPath: profileProject(prof),
		raw:        r.Raw,
	}
}

func (d Deps) resolveQuerySelection(ctx context.Context, cfg config.Config, query, providerName, profileName string) (resolvedSelection, error) {
	selection := resolvedSelection{selector: provider.QuerySelector{Query: query}}

	if profileName != "" {
		prof, ok, err := cfg.Select(ref.Ref{}, profileName)
		if err != nil {
			return resolvedSelection{}, err
		}
		if !ok {
			panic("an explicit Profile selection returned no Profile")
		}
		if providerName != providerAuto && providerName != prof.Provider {
			return resolvedSelection{}, fmt.Errorf("provider %q does not match profile %q provider %q",
				providerName, prof.Name, prof.Provider)
		}
		r := prof.Complete(ref.Ref{Raw: "--query"})
		selection.profile = &prof
		selection.route = connectionRoute{
			tracker:    ref.Tracker(prof.Provider),
			host:       r.Host,
			gitLabPath: prof.Project,
			raw:        "--query",
		}
		return selection, nil
	}

	if d.Provider != nil || providerName == providerFake {
		return selection, nil
	}

	origin, err := ref.ResolveOrigin(ctx, ref.WithRemoteLookup(d.RemoteLookup), ref.WithDir(d.Dir))
	if err != nil {
		return resolvedSelection{}, fmt.Errorf("--query needs --profile or an unambiguous git origin in the current directory: %w", err)
	}

	knownTracker := ref.HostTracker(origin.Host)
	switch providerName {
	case providerAuto:
		if knownTracker != ref.TrackerUnknown {
			origin.Tracker = knownTracker
		} else {
			matches := profilesForHost(cfg, origin.Host)
			switch len(matches) {
			case 1:
				selection.profile = &matches[0]
				origin.Tracker = ref.Tracker(matches[0].Provider)
			default:
				if len(matches) > 1 {
					names := make([]string, len(matches))
					for i := range matches {
						names[i] = matches[i].Name
					}
					return resolvedSelection{}, fmt.Errorf("profiles %s all match %s — pass --profile to choose",
						quoteJoin(names), origin.Host)
				}
				return resolvedSelection{}, fmt.Errorf("cannot tell whether %s uses GitHub or GitLab — pass --profile or --provider",
					origin.Host)
			}
		}
	case providerGitHub, providerGitLab:
		forced := ref.Tracker(providerName)
		if knownTracker != ref.TrackerUnknown && knownTracker != forced {
			return resolvedSelection{}, fmt.Errorf("git origin host %s is %s, not %s",
				origin.Host, providerDisplayName(string(knownTracker)), providerDisplayName(providerName))
		}
		origin.Tracker = forced
	default:
		return resolvedSelection{}, fmt.Errorf("--query with provider %q requires --profile", providerName)
	}

	if selection.profile == nil {
		prof, err := selectProfile(cfg, origin, "")
		if err != nil {
			return resolvedSelection{}, err
		}
		selection.profile = prof
	}
	selection.route = connectionRoute{
		tracker: origin.Tracker,
		host:    origin.Host,
		raw:     origin.Raw,
	}
	if origin.Tracker == ref.TrackerGitLab {
		selection.route.gitLabPath = strings.Trim(origin.Owner+"/"+origin.Repo, "/")
		if selection.profile != nil && selection.profile.Project != "" {
			selection.route.gitLabPath = selection.profile.Project
		}
	}
	return selection, nil
}

func profilesForHost(cfg config.Config, host string) []config.Profile {
	matches := make([]config.Profile, 0, len(cfg.Profiles))
	for _, name := range cfg.Names() {
		prof := cfg.Profiles[name]
		effectiveHost := prof.Host
		if prof.Provider == providerGitHub && effectiveHost == "" {
			effectiveHost = "github.com"
		}
		if (prof.Provider == providerGitHub || prof.Provider == providerGitLab) &&
			strings.EqualFold(strings.TrimSpace(effectiveHost), strings.TrimSpace(host)) {
			matches = append(matches, prof)
		}
	}
	return matches
}

func firstTrackerConflict(refs []ref.Ref) (int, int, bool) {
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			if refs[i].Tracker != ref.TrackerUnknown && refs[j].Tracker != ref.TrackerUnknown &&
				refs[i].Tracker != refs[j].Tracker {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func firstRouteConflict(refs []ref.Ref) (int, int, bool) {
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			if refs[i].Tracker != refs[j].Tracker || !strings.EqualFold(refs[i].Host, refs[j].Host) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func mixedTrackerError(left, right ref.Ref) error {
	//nolint:staticcheck // The specified CLI sentence starts with the domain term "Refs".
	return fmt.Errorf("Refs in one Watchlist must use one Tracker; %q resolves to %s (%s), while %q resolves to %s (%s)",
		selectorRefLabel(left), trackerDisplayName(left.Tracker), left.Host,
		selectorRefLabel(right), trackerDisplayName(right.Tracker), right.Host)
}

func routeConflictError(left, right ref.Ref) error {
	//nolint:staticcheck // The specified CLI sentence starts with the domain term "Refs".
	return fmt.Errorf("Refs in one Watchlist must use one Tracker connection and host; %q resolves to the %s Provider at %s, while %q resolves to the %s Provider at %s",
		selectorRefLabel(left), trackerDisplayName(left.Tracker), left.Host,
		selectorRefLabel(right), trackerDisplayName(right.Tracker), right.Host)
}

func selectorRefLabel(r ref.Ref) string {
	if raw := strings.TrimSpace(r.Raw); raw != "" {
		return raw
	}
	return r.String()
}

func trackerDisplayName(tracker ref.Tracker) string {
	if tracker == ref.TrackerUnknown {
		return "Unknown"
	}
	return providerDisplayName(string(tracker))
}

func firstProfileConflict(profiles []*config.Profile) (int, int, bool) {
	for i := range profiles {
		for j := i + 1; j < len(profiles); j++ {
			if profileIdentity(profiles[i]) != profileIdentity(profiles[j]) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func profileIdentity(prof *config.Profile) string {
	if prof == nil {
		return "none"
	}
	return prof.Name
}

type refIdentity struct {
	tracker ref.Tracker
	host    string
	owner   string
	repo    string
	number  int
	key     string
}

func deduplicationIdentity(r ref.Ref) refIdentity {
	identity := refIdentity{
		tracker: r.Tracker,
		host:    strings.ToLower(r.Host),
		owner:   r.Owner,
		repo:    r.Repo,
		number:  r.Number,
		key:     r.Key,
	}
	if r.Tracker == ref.TrackerGitHub {
		identity.owner = strings.ToLower(identity.owner)
		identity.repo = strings.ToLower(identity.repo)
	}
	return identity
}

func deduplicateRefs(refs []ref.Ref) []ref.Ref {
	unique := make([]ref.Ref, 0, len(refs))
	seen := make(map[refIdentity]struct{}, len(refs))
	for _, r := range refs {
		identity := deduplicationIdentity(r)
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}

		// Parsed fields may alias one bulk stdin string. The selector survives for
		// every monitor refresh, so retain only the unique Ref's own bytes.
		r.Host = strings.Clone(r.Host)
		r.Owner = strings.Clone(r.Owner)
		r.Repo = strings.Clone(r.Repo)
		r.Key = strings.Clone(r.Key)
		r.Raw = strings.Clone(r.Raw)
		unique = append(unique, r)
	}
	if !shouldCopyDeduplicatedRefs(len(unique), cap(unique)) {
		return unique[:len(unique):len(unique)]
	}
	retained := make([]ref.Ref, len(unique))
	copy(retained, unique)
	return retained
}

// A clipped near-full slice intentionally keeps its small hidden slack rather
// than paying for a second large allocation. Duplicate-heavy slices are always
// copied; shallower reductions are copied only when at least 1024 Ref slots are
// reclaimed and the copy is at most four times larger than that slack. Counting
// slots keeps this policy independent of Ref's architecture-specific byte size.
const compactRefSlackSlots = 1024

func shouldCopyDeduplicatedRefs(retained, capacity int) bool {
	slack := capacity - retained
	if slack == 0 {
		return false
	}
	return slack >= retained ||
		slack >= compactRefSlackSlots && slack*4 >= retained
}

// selectProfile finds the Profile serving this Ref, returning nil when none
// does. A Ref with no Profile is the GitHub zero-config path: `gh auth login`
// and nothing else is a supported way to run sitrep.
//
// A Ref that carries a Jira-style key and no host is the exception. Such
// a Ref names a project and nothing more; without a Profile there is no site to
// ask, so an unmatched key prefix is fatal and the error says exactly what to
// add. A Jira URL is not that case — it names its own site, and its unserved
// Tracker is reported downstream like any other.
func selectProfile(cfg config.Config, r ref.Ref, name string) (*config.Profile, error) {
	prof, ok, err := cfg.Select(r, name)
	if err != nil {
		return nil, err
	}
	if ok {
		return &prof, nil
	}
	if prefix := ref.KeyPrefix(r.Key); prefix != "" && r.Host == "" {
		return nil, unmatchedKeyPrefix(cfg, prefix)
	}
	if r.Tracker == ref.TrackerGitLab && r.Host == "" {
		return gitLabProfileForHostlessRef(cfg, r)
	}
	return nil, nil
}

// gitLabProfileForHostlessRef serves the "&12" reference form, which names a
// group and no instance. config.Select matches a non-Jira Ref on (provider,
// host), so a hostless GitLab Ref matches nothing there — this is the mirror of
// the unmatched-key-prefix rule above: a single gitlab Profile serves it,
// several are an ambiguity worth asking about, and none is fatal because there
// is no instance to read.
func gitLabProfileForHostlessRef(cfg config.Config, r ref.Ref) (*config.Profile, error) {
	// A bare "&12" or "%3" carries no path of its own, so only a Profile that
	// names a project can resolve it. A projectless gitlab Profile matched here
	// would fail downstream with "does not name a GitLab group or project",
	// which is the same problem reported later and less clearly. A milestone
	// reference like "acme/widgets%3" does carry its own path, so a projectless
	// Profile stays a valid match there.
	needsProject := r.Owner == ""

	var matches []config.Profile
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		if p.Provider != string(ref.TrackerGitLab) {
			continue
		}
		if needsProject && p.Project == "" {
			continue
		}
		matches = append(matches, p)
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		if needsProject {
			return nil, fmt.Errorf("no Profile tells sitrep which GitLab instance %q is on, "+
				"and which group or project it names — add a gitlab profile with a host and a "+
				"project to %s, or pass the Ref's full URL",
				r.Raw, configLocation(cfg.Path))
		}
		return nil, fmt.Errorf("no Profile tells sitrep which GitLab instance %q is on — "+
			"add a gitlab profile to %s, or pass the Ref's full URL",
			r.Raw, configLocation(cfg.Path))
	default:
		names := make([]string, len(matches))
		for i, p := range matches {
			names[i] = p.Name
		}
		return nil, fmt.Errorf("profiles %s could all serve %q — pass --profile to choose",
			quoteJoin(names), r.Raw)
	}
}

// quoteJoin renders a list of names the way an ask-the-user error does.
func quoteJoin(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, " and ")
}

// retagProfileClaimedHost fixes the one Ref the grammar has to guess at.
// ref.ParseRemoteURL deliberately treats an unrecognized host as GitHub
// Enterprise, so a bare number inside a clone of a self-managed GitLab resolves
// to a GitHub Ref. A Profile that claims a host names that host's Tracker, and
// that is better evidence than the guess.
//
// Only a guessed Ref is retagged — Tracker GitHub on a host that is not
// github.com — so a URL that named its own Tracker is never overridden.
// gitlab.com needs none of this: the grammar already knows it.
func retagProfileClaimedHost(cfg config.Config, r ref.Ref) ref.Ref {
	if r.Tracker != ref.TrackerGitHub || r.Host == "" || strings.EqualFold(r.Host, "github.com") {
		return r
	}
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		if p.Provider == string(ref.TrackerGitLab) && strings.EqualFold(p.Host, r.Host) {
			r.Tracker = ref.TrackerGitLab
			return r
		}
	}
	return r
}

// configLocation names the config file an error tells the user to edit: the
// real path when one was resolved, and the documented default when the run read
// no file at all.
func configLocation(path string) string {
	if path == "" {
		return "~/.config/sitrep/config.yml"
	}
	return path
}

func unmatchedKeyPrefix(cfg config.Config, prefix string) error {
	msg := fmt.Sprintf("no Profile matches the key prefix %q — add one to %s",
		prefix, configLocation(cfg.Path))
	if known := cfg.KeyPrefixes(); len(known) > 0 {
		msg += " (known prefixes: " + strings.Join(known, ", ") + ")"
	}
	return errors.New(msg)
}

// profileInterval reads a Profile's refresh cadence, or zero when there is no
// Profile.
func profileInterval(prof *config.Profile) time.Duration {
	if prof == nil {
		return 0
	}
	return prof.RefreshInterval
}

// resolveRef parses the user's Ref, reading the working directory's git
// origin remote when it is a bare number.
func (d Deps) resolveRef(ctx context.Context, raw, providerName string) (ref.Ref, error) {
	r, err := ref.Parse(ctx, raw, ref.WithRemoteLookup(d.RemoteLookup), ref.WithDir(d.Dir))
	if err == nil {
		return r, nil
	}
	// The fake serves any Ref, so a Ref it cannot resolve is not fatal:
	// development runs and the golden tests must not need a git remote.
	if d.Provider != nil || providerName == providerFake {
		return ref.Ref{Raw: raw}, nil
	}
	return ref.Ref{}, err
}
