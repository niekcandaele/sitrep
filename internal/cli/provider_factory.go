// Provider construction: turning a resolved route and Profile into the driver
// that serves it, including the --provider fake fixtures.

package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/provider/jira"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// The --provider names. The default auto-detects the Tracker from the route or
// Refs; the others force one driver, which is how a development run reaches the
// fake and how a GitHub Enterprise Ref can be pinned to the GitHub driver.
const (
	providerAuto   = "auto"
	providerGitHub = "github"
	providerGitLab = "gitlab"
	providerJira   = "jira"
	providerFake   = "fake"

	fakeFixtureBlocking        fakeFixture = "blocking"
	fakeFixtureNoBlockingLinks fakeFixture = "no-blocking-links"
)

// fakeFixture names a --fake-fixture Watchlist. Validity lives on the type, so
// the flag check and the Provider constructor cannot drift apart.
type fakeFixture string

// valid reports whether f names a fixture newFakeProvider can build.
func (f fakeFixture) valid() bool {
	switch f {
	case fakeFixtureBlocking, fakeFixtureNoBlockingLinks:
		return true
	default:
		return false
	}
}

type fakeProviderSettings struct {
	fixture    fakeFixture
	fixtureSet bool
	delay      time.Duration
	delaySet   bool
}

// defaultProviderName auto-detects the Provider from the Ref: sitrep can
// tell a GitHub URL from a GitLab one, so it should not make the user say.
const defaultProviderName = providerAuto

func knownProviderName(name string) bool {
	switch name {
	case providerAuto, providerGitHub, providerGitLab, providerJira, providerFake:
		return true
	default:
		return false
	}
}

// newProvider chooses the driver that serves this Ref and constructs it from
// the Profile, when there is one. The Ref's Tracker is the only input: an
// explicit --provider has already been applied to it by forceTracker, so there
// is one answer to "which driver is this" rather than two that can disagree.
// The fake is the exception, because it serves any Ref and belongs to no
// Tracker.
//
// This is where a Profile is consumed and where it stops existing: everything
// past this call sees a Provider and a Ref.
func (d Deps) newProvider(name string, route connectionRoute, prof *config.Profile, configPath string,
	fakeSettings fakeProviderSettings,
) (provider.Provider, error) {
	maxTickets := effectiveMaxTickets(prof)
	if name == providerFake {
		return newFakeProvider(maxTickets, fakeSettings), nil
	}

	switch route.tracker {
	case ref.TrackerGitHub:
		return d.newGitHub(route.host, prof, maxTickets), nil
	case ref.TrackerJira:
		return d.newJira(route.host, prof, configPath, maxTickets)
	case ref.TrackerGitLab:
		return d.newGitLab(route.host, route.gitLabPath, prof, maxTickets)
	default:
		return nil, fmt.Errorf("cannot tell which tracker serves %q", route.raw)
	}
}

func newFakeProvider(maxTickets int, settings fakeProviderSettings) *fake.Provider {
	options := []fake.Option{fake.WithMaxTickets(maxTickets)}
	switch settings.fixture {
	case "":
	case fakeFixtureBlocking:
		options = append(options, fake.WithBlockingFixture())
	case fakeFixtureNoBlockingLinks:
		caps := fake.FixtureBlockingSnapshot().Capabilities
		caps.BlockingLinks = false
		options = append(options, fake.WithBlockingFixture(), fake.WithCapabilities(caps))
	default:
		panic("unvalidated fake fixture " + string(settings.fixture))
	}
	if settings.delay > 0 {
		options = append(options, fake.WithDelay(settings.delay))
	}
	return fake.New(options...)
}

// forceTracker applies an explicit --provider to a Ref, which is the one thing
// the flag exists for: telling sitrep which driver serves an unrecognized host.
// A self-managed GitLab URL with no "/-/" in it, or a bare number inside such a
// clone, parses as GitHub Enterprise — the grammar's documented guess — and
// --provider is how a user corrects it.
//
// It overrides a guess, never a fact. A ref whose host names its own Tracker,
// or which carries a Jira-style key, said what it is; contradicting that is a
// mistake worth reporting rather than obeying.
func forceTracker(name string, r ref.Ref) (ref.Tracker, error) {
	forced := ref.Tracker(name)
	if r.Tracker == forced {
		return forced, nil
	}

	known := ref.HostTracker(r.Host)
	keyed := ref.KeyPrefix(r.Key) != "" && forced != ref.TrackerJira
	if (known != ref.TrackerUnknown && known != forced) || keyed {
		return "", fmt.Errorf("%q is not a %s Ref", r.Raw, providerDisplayName(name))
	}
	return forced, nil
}

// providerDisplayName is how an error spells a provider: the Trackers' own
// capitalisation, because that is how the user reads them everywhere else.
func providerDisplayName(name string) string {
	switch name {
	case providerGitHub:
		return "GitHub"
	case providerGitLab:
		return "GitLab"
	case providerJira:
		return "Jira"
	default:
		return name
	}
}

// newJira constructs the Jira driver from the Profile that matched this Ref.
//
// A Jira Ref always needs a Profile, including the /browse/ URL form that names
// its own site: the site is not the problem, the credential is, and a Profile is
// the only place an Atlassian email and a token reference come from. Saying so
// here is more useful than letting the driver fail at its first request.
func (d Deps) newJira(
	host string,
	prof *config.Profile,
	configPath string,
	maxTickets int,
) (provider.Provider, error) {
	if prof == nil {
		return nil, fmt.Errorf("jira: reading %s needs a Profile — add one to %s with your "+
			"Atlassian email and the environment variable holding your API token",
			host, configLocation(configPath))
	}
	cred, err := prof.Credential(d.env())
	if err != nil {
		return nil, err
	}
	// Translating a config.Credential into the driver's own Credentials happens
	// here, deliberately: it is what keeps internal/provider/jira free of any
	// knowledge of the config file.
	return jira.New(host,
		jira.WithCredentials(jira.Credentials{
			Email: cred.User,
			Token: cred.Token,
		}),
		jira.WithMaxTickets(maxTickets)), nil
}

// newGitLab constructs the GitLab driver from the Profile that matched this
// Ref, when there is one.
//
// Unlike Jira, a GitLab Ref needs no Profile: a user with `glab auth login`
// done is a supported zero-config setup, exactly as on GitHub. What a Profile
// adds is a default path — which is what makes the bare "&12" reference form
// typeable — a named token variable, and the site's own won't-do labels. The
// path declares its own scope in its spelling, which only the driver reads: the
// Profile's project is passed through verbatim.
//
// Translating a config.Credential into the driver's own token happens here,
// deliberately: it is what keeps internal/provider/gitlab free of any knowledge
// of the config file.
func (d Deps) newGitLab(host, path string, prof *config.Profile, maxTickets int) (provider.Provider, error) {
	// The Profile's credential is resolved first so that a Profile naming a
	// variable nobody set reports *that* rather than a vaguer failure later.
	//
	// An unset auth.token_env is not one of those cases on GitLab: it falls
	// through to glab (see gitLabTokenSource), so demanding it here would break
	// the very setup this driver supports. Blanking token_env is how this asks
	// config for exactly the other half — auth.user_env — whose absence is a
	// real error whichever way the token arrives.
	if prof != nil {
		identityOnly := *prof
		identityOnly.Auth.TokenEnv = ""
		if _, err := identityOnly.Credential(d.env()); err != nil {
			return nil, err
		}
	}
	return gitlab.New(host,
		gitlab.WithPath(path),
		gitlab.WithTokenSource(d.gitLabTokenSource(prof)),
		gitlab.WithMaxTickets(maxTickets),
		gitlab.WithWontDoLabels(profileWontDoLabels(prof))), nil
}

// gitLabTokenSource layers a Profile's auth reference on top of the GitLab
// driver's own token discovery, the same way gitHubTokenSource does. An unset
// named variable falls through to gitlab.DefaultTokenSource rather than being
// fatal, because a user with `glab auth login` done is a supported
// zero-token-variable setup.
func (d Deps) gitLabTokenSource(prof *config.Profile) gitlab.TokenSource {
	return profileTokenSource(d.GitLabTokenSource, prof, d.env(), gitlab.DefaultTokenSource)
}

// profileProject is a Profile's project path, or "" when there is no Profile.
func profileProject(prof *config.Profile) string {
	if prof == nil {
		return ""
	}
	return prof.Project
}

// profileWontDoLabels is a Profile's won't-do label names, or nil when there is
// no Profile — which is the driver's "keep the built-in list" input.
func profileWontDoLabels(prof *config.Profile) []string {
	if prof == nil {
		return nil
	}
	return prof.WontDoLabels
}

func (d Deps) newGitHub(host string, prof *config.Profile, maxTickets int) provider.Provider {
	return github.New(host,
		github.WithTokenSource(d.gitHubTokenSource(prof)),
		github.WithMaxTickets(maxTickets))
}

// gitHubTokenSource layers a Profile's auth reference on top of the GitHub
// driver's own token discovery. Naming a variable is a preference, not a
// demand: a GitHub user whose named variable happens to be unset still gets
// `gh auth token`, so writing a Profile to set a host or a refresh interval can
// never break a working GitHub setup. For Jira and GitLab an unset named
// variable is an error, because there is no other way in.
func (d Deps) gitHubTokenSource(prof *config.Profile) github.TokenSource {
	return profileTokenSource(d.TokenSource, prof, d.env(), github.DefaultTokenSource)
}

// profileTokenSource is gitHubTokenSource and gitLabTokenSource with their
// fallback named, so a test can prove the fallback is reached without shelling
// out to `gh` or `glab`. A nil result means "the driver's own default", which is
// the zero-config path unchanged.
//
// It is generic over the two drivers' token source types, which are the same
// function shape under two names, because the layering rule is one rule: an
// injected source wins, then a Profile's named variable, then the driver's own
// discovery.
func profileTokenSource[T ~func(context.Context, string) (string, error)](injected T,
	prof *config.Profile, env func(string) string, fallback T) T {
	if injected != nil {
		return injected
	}
	if prof == nil || prof.Auth.TokenEnv == "" {
		return nil
	}

	name := prof.Auth.TokenEnv
	return T(func(ctx context.Context, host string) (string, error) {
		if token := strings.TrimSpace(env(name)); token != "" {
			return token, nil
		}
		return fallback(ctx, host)
	})
}

// effectiveMaxTickets reads the construction-time Query budget from a selected
// Profile. A nil Profile and a hand-built zero-value Profile both use the shared
// Provider default; validated production Profiles always carry a positive value.
func effectiveMaxTickets(prof *config.Profile) int {
	if prof == nil || prof.MaxTickets <= 0 {
		return provider.DefaultMaxTickets
	}
	return prof.MaxTickets
}
