// Package config owns sitrep's global config file and the Profile: a named
// entry in that file binding a Tracker host, a project, and an auth reference.
// A Profile selects a Provider and supplies what it needs to connect.
//
// # Tokens are named, never written
//
// A Profile holds an auth *reference* — the NAME of an environment variable —
// and never a token. The token itself is a Credential: read from the
// environment at connect time and held only in process memory. This is a
// requirement rather than a convention, because a config file lives in
// dotfiles, in git, in backups, and in whatever an agent reads next. A config
// file that writes a literal token is rejected with an error that says so.
//
// # One file, one place
//
// There is exactly one config file, at $XDG_CONFIG_HOME/sitrep/config.yml (or
// $HOME/.config/sitrep/config.yml). There is deliberately no per-repository
// config, no discovery walking up from the working directory, and no merging:
// "where does this setting come from" has one answer. sitrep never writes the
// file either (ADR-0002 is about Trackers; this is the same instinct applied to
// the user's disk). The human writes config; sitrep reads it.
//
// A missing file is not an error. It yields the empty Config, because a GitHub
// user with `gh auth login` done is expected never to write one.
//
// # What a Provider is constructed from
//
// A Profile is consumed entirely at Provider-construction time, in
// internal/cli, and nothing downstream — not the polled fetch, not the
// renderers, not the TUI — knows one exists. The shape each driver takes:
//
//   - A Jira driver is constructed from Profile.Host (the site, e.g.
//     "acme.atlassian.net"), Profile.Project (the project key, which is also the
//     Ref key prefix) and a Credential carrying User (the Atlassian account
//     email) and Token (the API token) — the pair Atlassian's documented
//     basic-auth flow wants. The Ref it receives carries Key (upper-cased) and
//     Host (completed from the Profile); Number is zero.
//   - A GitLab driver is constructed from Profile.Host, Profile.Project (the
//     project or group path, project-scoped unless written "groups/<path>")
//     and Credential.Token. Reading the tracker's own CLI
//     first (`glab auth token`) belongs in its own TokenSource-shaped seam,
//     mirroring GitHub's, with the Profile's token_env layered on top as a
//     preference.
//
// Neither driver reads this package: both are constructed in cli.newProvider.
package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/ref"
)

// MinRefreshInterval is the floor on any refresh cadence, whether it arrives
// from --interval or from a Profile. sitrep polls a rate-limited API and is
// read-only and polite: a sub-second poll is a way to get throttled, not a
// feature. The floor has one definition so raising it is a one-line decision.
const MinRefreshInterval = 5 * time.Second

// Config is sitrep's whole global configuration: the user's named Profiles and
// nothing else. The zero value is a valid, empty configuration — which is what
// a user with no config file has, and what a GitHub-only user never needs to
// change.
type Config struct {
	// Profiles are the user's named Profiles, keyed by Profile name.
	Profiles map[string]Profile `yaml:"profiles"`
	// Path is the file this Config was read from, or "" for an empty Config
	// that came from no file. Error messages quote it.
	Path string `yaml:"-"`
}

// Profile is one named entry in the global config: it selects a Provider and
// supplies what that Provider needs to connect to one Tracker.
type Profile struct {
	// Name is the Profile's name, copied from its key in the config file so a
	// Profile can be passed around and still name itself in an error.
	Name string `yaml:"-"`
	// Provider names the driver this Profile selects: "github", "jira" or
	// "gitlab". The values are the Tracker names internal/ref uses.
	Provider string `yaml:"provider"`
	// Host is the Tracker host, e.g. "acme.atlassian.net". Optional for GitHub,
	// where it defaults to github.com; required otherwise.
	Host string `yaml:"host"`
	// Project is the Tracker's own project identity: a Jira project key
	// (required, and also the prefix of every Ref key in it) or a GitLab
	// project or group path (optional). A GitLab path is project-scoped —
	// "acme/widgets" — unless it is written "groups/acme/platform", which is
	// how a Profile names a group; the driver never guesses which was meant.
	// A github Profile must not set it — a GitHub Ref carries its own
	// owner/repo — and one that does is rejected rather than silently ignored.
	Project string `yaml:"project"`
	// Auth is how this Profile's credential is found. It holds references only:
	// sitrep never stores a token in the config file.
	Auth Auth `yaml:"auth"`
	// RefreshInterval overrides the monitor's default cadence for runs served by
	// this Profile. Zero means "use sitrep's default".
	RefreshInterval time.Duration `yaml:"-"`
	// RawRefreshInterval is the duration as written; parsed into
	// RefreshInterval during validation so a bad value names its Profile.
	RawRefreshInterval string `yaml:"refresh_interval"`
	// MaxTickets bounds Tracker-discovered Query membership. Parsed Profiles
	// always carry a positive effective value.
	MaxTickets int `yaml:"-"`
	// RawMaxTickets retains the scalar as written for precise validation errors.
	RawMaxTickets string `yaml:"max_tickets"`
}

// Auth is a Profile's auth reference: the names of the environment variables
// holding the credential, never the credential itself. Keeping tokens out of
// the config file is a requirement, not a convention — a config file lives in
// dotfiles, in git, in backups, and in whatever an agent reads next.
type Auth struct {
	// TokenEnv is the name of the environment variable holding the API token.
	TokenEnv string `yaml:"token_env"`
	// User is a non-secret identity some Trackers pair with the token — Jira's
	// Atlassian account email. Optional.
	User string `yaml:"user"`
	// UserEnv names an environment variable holding User instead. Optional, and
	// mutually exclusive with User.
	UserEnv string `yaml:"user_env"`
	// Token is never read. It exists so that a config file that writes a literal
	// token gets an error that says so instead of "unknown field token".
	Token string `yaml:"token"`
}

// Credential is a resolved secret, read from the environment at connect time
// and held only in memory. It must never be logged, printed, rendered,
// serialized or wrapped into an error.
type Credential struct {
	// User is the non-secret identity, when the Tracker pairs one with the
	// token.
	User string
	// Token is the secret.
	Token string
}

// String renders a Credential with its token redacted. It exists so that a %v
// somewhere in a future error cannot leak the secret.
func (c Credential) String() string {
	token := "REDACTED"
	if c.Token == "" {
		token = ""
	}
	return fmt.Sprintf("config.Credential{User:%s, Token:%s}", c.User, token)
}

// GoString renders the same redacted form for %#v, which formats struct fields
// directly and would otherwise walk straight past String.
func (c Credential) GoString() string { return c.String() }

// MarshalJSON renders the same redacted form for encoding/json, which reads the
// exported Token field rather than String. Token stays exported because the
// constructors in internal/cli read it, and because the point is that the type
// is safe however it is printed rather than safe if everybody remembers.
func (c Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		User  string `json:"user"`
		Token string `json:"token"`
	}{User: c.User, Token: redactedToken(c.Token)})
}

// redactedToken is the stand-in a Credential prints in place of its secret,
// and "" for a Credential that holds none — an absent token and a hidden one
// are different facts.
func redactedToken(token string) string {
	if token == "" {
		return ""
	}
	return "REDACTED"
}

// The Provider names a Profile may select. They are the Tracker names
// internal/ref uses, so a Profile's provider and a Ref's Tracker compare
// directly.
const (
	providerGitHub = string(ref.TrackerGitHub)
	providerGitLab = string(ref.TrackerGitLab)
	providerJira   = string(ref.TrackerJira)
)

// providerList is the set of provider values, in the order every error message
// lists them.
var providerList = []string{providerGitHub, providerGitLab, providerJira}

// defaultGitHubHost is the host a github Profile that names none serves.
const defaultGitHubHost = "github.com"

// Names returns the Profile names in this Config, sorted. Errors that ask the
// user to pick a Profile list them.
func (c Config) Names() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// KeyPrefixes returns the Jira project keys this Config's Profiles claim,
// sorted, upper-cased and de-duplicated — one key may legitimately be claimed
// on two Jira sites. The "no Profile matches this key prefix" error lists them,
// because the fastest way to fix a typo is to see the alternatives.
func (c Config) KeyPrefixes() []string {
	seen := map[string]bool{}
	var prefixes []string
	for _, name := range c.Names() {
		p := c.Profiles[name]
		if p.Provider != providerJira || p.Project == "" {
			continue
		}
		key := strings.ToUpper(p.Project)
		if seen[key] {
			continue
		}
		seen[key] = true
		prefixes = append(prefixes, key)
	}
	sort.Strings(prefixes)
	return prefixes
}

// Select returns the Profile serving r, if any. name, when non-empty, is an
// explicit --profile override and must exist; everything else is inference:
//
//  1. A Ref carrying a Jira-style key matches the Profile whose project
//     is that key's prefix, case-insensitively. This is the Ref grammar's
//     third form: "PROJ-12" means nothing until a Profile says which site PROJ
//     lives on. When the Ref also names a site — a /browse/ URL does — the
//     Profile must serve that site too.
//  2. Any other Ref matches the single Profile whose provider is the Ref's
//     Tracker and whose host is the Ref's host.
//  3. No match is not an error: GitHub with `gh` logged in is expected to run
//     with no config file at all.
//
// A Profile is a credential, so every road through here ends at a Profile whose
// host is the host the Ref names — including the explicit --profile road, which
// otherwise would hand one site's email and token to another site that merely
// asked. A Ref carrying no host of its own has nothing to conflict with and is
// accepted, which is the case --profile exists for.
//
// An ambiguous match is an error naming the candidates, because guessing which
// Tracker to read from is worse than asking. "No match" is reported as a fact
// rather than an error, and the caller decides whether that fact is fatal —
// which is what lets rule 3 and the unmatched-key-prefix error coexist.
func (c Config) Select(r ref.Ref, name string) (Profile, bool, error) {
	if name != "" {
		p, ok := c.Profiles[name]
		if !ok {
			return Profile{}, false, c.errorf("no profile named %q%s", name, c.knownProfiles())
		}
		if r.Tracker != ref.TrackerUnknown && p.Provider != string(r.Tracker) {
			return Profile{}, false, c.errorf("profile %q is a %s profile, but %s is a %s ref",
				name, p.Provider, r.Raw, r.Tracker)
		}
		if r.Host != "" && normalizeHost(p.effectiveHost()) != normalizeHost(r.Host) {
			return Profile{}, false, c.errorf("profile %q serves %s, but %s names %s",
				name, p.effectiveHost(), r.Raw, r.Host)
		}
		return p, true, nil
	}

	if prefix := ref.KeyPrefix(r.Key); prefix != "" {
		// One key prefix may be claimed on two Jira sites, so a Ref that names a
		// site narrows to it and a Ref that does not must be unambiguous.
		var matches []Profile
		for _, n := range c.Names() {
			p := c.Profiles[n]
			if p.Provider != providerJira || !strings.EqualFold(p.Project, prefix) {
				continue
			}
			if r.Host != "" && normalizeHost(p.effectiveHost()) != normalizeHost(r.Host) {
				continue
			}
			matches = append(matches, p)
		}
		switch len(matches) {
		case 0:
			return Profile{}, false, nil
		case 1:
			return matches[0], true, nil
		default:
			return Profile{}, false, c.errorf("profiles %s all claim the Jira project key %q — pass --profile to choose",
				quoteJoin(profileNames(matches)), prefix)
		}
	}

	if r.Tracker == ref.TrackerUnknown {
		return Profile{}, false, nil
	}

	var matches []Profile
	for _, n := range c.Names() {
		p := c.Profiles[n]
		if p.Provider == string(r.Tracker) && normalizeHost(p.effectiveHost()) == normalizeHost(r.Host) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return Profile{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return Profile{}, false, c.errorf("profiles %s all match %s on %s — pass --profile to choose",
			quoteJoin(profileNames(matches)), r.Tracker, r.Host)
	}
}

// Complete fills in what the Ref grammar could not know without a Profile:
// a Jira-style key carries no host, so the Profile supplies it, along with the
// Tracker. Fields the Ref already carries are never overwritten — a full URL
// always beats a Profile.
//
// Owner and Repo are left alone: they mean nothing on Jira, and the project key
// is already inside Key.
func (p Profile) Complete(r ref.Ref) ref.Ref {
	if r.Tracker == ref.TrackerUnknown && p.Provider != "" {
		r.Tracker = ref.Tracker(p.Provider)
	}
	if r.Host == "" {
		r.Host = p.effectiveHost()
	}
	return r
}

// Credential resolves this Profile's credential from the environment. It is
// called once, when a Provider is constructed, and never on a refresh. env is
// injected so tests never touch the process environment; production passes
// os.Getenv.
//
// A named environment variable that is unset or empty is an error, and the
// error names the Profile and the variable so the fix is obvious. It never
// contains the value of anything — which holds only because isEnvName ran at
// load time and rejected anything shaped like a token, so the name this error
// prints cannot itself be a secret somebody pasted into token_env.
//
// An empty TokenEnv yields a zero Credential and no error: that is the GitHub
// Profile that exists only to set a host or an interval and lets `gh` do the
// rest.
func (p Profile) Credential(env func(string) string) (Credential, error) {
	if env == nil {
		env = func(string) string { return "" }
	}

	var cred Credential
	if p.Auth.TokenEnv != "" {
		token := strings.TrimSpace(env(p.Auth.TokenEnv))
		if token == "" {
			return Credential{}, p.errorf("$%s is not set (sitrep reads tokens from the "+
				"environment; it never stores them in the config file)", p.Auth.TokenEnv)
		}
		cred.Token = token
	}

	switch {
	case p.Auth.User != "":
		cred.User = p.Auth.User
	case p.Auth.UserEnv != "":
		user := strings.TrimSpace(env(p.Auth.UserEnv))
		if user == "" {
			return Credential{}, p.errorf("$%s is not set (it names the identity paired with "+
				"this profile's token)", p.Auth.UserEnv)
		}
		cred.User = user
	}
	return cred, nil
}

// effectiveHost is the Profile's host with GitHub's default filled in, so host
// matching does not have to special-case the one Provider whose host is
// optional.
func (p Profile) effectiveHost() string {
	if p.Host == "" && p.Provider == providerGitHub {
		return defaultGitHubHost
	}
	return p.Host
}

// errorf builds an error naming this Profile. Every message a user sees about a
// Profile is built here or by Config.profileErrorf, so the shape does not
// drift.
func (p Profile) errorf(format string, args ...any) error {
	return fmt.Errorf("profile %q: %s", p.Name, fmt.Sprintf(format, args...))
}

// errorf builds an error naming the config file it came from.
func (c Config) errorf(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if c.Path == "" {
		return fmt.Errorf("%s", msg)
	}
	return fmt.Errorf("%s: %s", c.Path, msg)
}

// profileErrorf builds an error naming both the config file and the Profile.
func (c Config) profileErrorf(name, format string, args ...any) error {
	return c.errorf("profile %q: %s", name, fmt.Sprintf(format, args...))
}

// knownProfiles renders the ", known profiles: …" tail an ask-the-user error
// carries, or "" when there are none to list.
func (c Config) knownProfiles() string {
	names := c.Names()
	if len(names) == 0 {
		return " (this config file defines no profiles)"
	}
	return " (known profiles: " + strings.Join(names, ", ") + ")"
}

func profileNames(profiles []Profile) []string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
}

func quoteJoin(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, " and ")
}

// normalizeHost makes host comparison case-insensitive and nothing else. Case
// is the only difference between two spellings of one host that does not also
// change which origin a credential is being sent to: "www.ghe.example" and
// "ghe.example" are two hosts, and treating them as one is how a token scoped
// to the first reaches the second. Canonicalising the handful of hosts where
// "www." really is cosmetic is internal/ref's job, on the way into a Ref.
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}
