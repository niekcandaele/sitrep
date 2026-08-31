package config_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// testPath is the name every parse error in this package is expected to quote.
// No test reads a real config file, a real home directory or a real
// environment.
const testPath = "/tmp/sitrep-test/config.yml"

func parse(t *testing.T, doc string) config.Config {
	t.Helper()
	cfg, err := config.Parse(strings.NewReader(doc), testPath)
	if err != nil {
		t.Fatalf("Parse:\n%s\n%v", doc, err)
	}
	return cfg
}

// The documented example document, parsed into the exact value it describes.
func TestParseTheDocumentedExample(t *testing.T) {
	cfg := parse(t, `
profiles:
  acme-jira:
    provider: jira
    host: acme.atlassian.net
    project: ABC
    auth:
      user: me@acme.test
      token_env: JIRA_API_TOKEN
    refresh_interval: 30s

  acme-gitlab:
    provider: gitlab
    host: gitlab.acme.test
    project: platform/widgets
    wont_do_labels: [ausgemustert, "workflow::no-fix"]
    auth:
      token_env: GITLAB_TOKEN

  work-github:
    provider: github
    host: ghe.acme.test
    auth:
      token_env: GHE_TOKEN
    refresh_interval: 2m
`)

	if cfg.Path != testPath {
		t.Errorf("Path = %q, want %q", cfg.Path, testPath)
	}
	if len(cfg.Profiles) != 3 {
		t.Fatalf("got %d profiles, want 3", len(cfg.Profiles))
	}

	want := config.Profile{
		Name:     "acme-jira",
		Provider: "jira",
		Host:     "acme.atlassian.net",
		Project:  "ABC",
		Auth: config.Auth{
			TokenEnv: "JIRA_API_TOKEN",
			User:     "me@acme.test",
		},
		RefreshInterval:    30 * time.Second,
		RawRefreshInterval: "30s",
		MaxTickets:         100,
	}
	if got := cfg.Profiles["acme-jira"]; !reflect.DeepEqual(got, want) {
		t.Errorf("acme-jira = %+v\nwant %+v", got, want)
	}
	if got := cfg.Profiles["work-github"].RefreshInterval; got != 2*time.Minute {
		t.Errorf("work-github refresh_interval = %s, want 2m", got)
	}
	if got := cfg.Profiles["acme-gitlab"].Name; got != "acme-gitlab" {
		t.Errorf("Name = %q, want it filled in from the map key", got)
	}
	if got := cfg.Profiles["acme-gitlab"].RefreshInterval; got != 0 {
		t.Errorf("refresh_interval = %s, want zero when unwritten", got)
	}
	// The names arrive exactly as written: normalizing them is the GitLab
	// driver's business, not this package's.
	wantLabels := []string{"ausgemustert", "workflow::no-fix"}
	if got := cfg.Profiles["acme-gitlab"].WontDoLabels; !reflect.DeepEqual(got, wantLabels) {
		t.Errorf("wont_do_labels = %#v, want %#v", got, wantLabels)
	}
	// A profile that writes none gets nil, which is what the driver reads as
	// "keep sitrep's built-in list".
	if got := cfg.Profiles["acme-jira"].WontDoLabels; got != nil {
		t.Errorf("acme-jira wont_do_labels = %#v, want nil when unwritten", got)
	}
}

func TestParseMaxTickets(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "omitted", want: provider.DefaultMaxTickets},
		{name: "one", raw: "1", want: 1},
		{name: "default explicit", raw: "100", want: 100},
		{name: "large", raw: "1000", want: 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := "profiles:\n  work:\n    provider: github\n    refresh_interval: 1h\n"
			if tt.raw != "" {
				doc += "    max_tickets: " + tt.raw + "\n"
			}
			profile := parse(t, doc).Profiles["work"]
			if profile.MaxTickets != tt.want {
				t.Errorf("MaxTickets = %d, want %d", profile.MaxTickets, tt.want)
			}
			if profile.RawMaxTickets != tt.raw {
				t.Errorf("RawMaxTickets = %q, want %q", profile.RawMaxTickets, tt.raw)
			}
			if profile.RefreshInterval != time.Hour {
				t.Errorf("RefreshInterval = %s, want 1h independent of max_tickets", profile.RefreshInterval)
			}
		})
	}
}

func TestParseEmptyMaxTicketsUsesDefault(t *testing.T) {
	profile := parse(t, "profiles:\n  work:\n    provider: github\n    max_tickets:\n").Profiles["work"]
	if profile.MaxTickets != provider.DefaultMaxTickets || profile.RawMaxTickets != "" {
		t.Errorf("MaxTickets/RawMaxTickets = %d/%q, want %d/empty",
			profile.MaxTickets, profile.RawMaxTickets, provider.DefaultMaxTickets)
	}
}

func TestParseRejectsInvalidMaxTickets(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "0", want: "max_tickets must be at least 1"},
		{raw: "-1", want: "max_tickets must be at least 1"},
		{raw: "many", want: `max_tickets "many" is not a positive integer`},
		{raw: "999999999999999999999999999999999", want: `max_tickets "999999999999999999999999999999999" is not a positive integer`},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			doc := "profiles:\n  work:\n    provider: github\n    max_tickets: " + tt.raw + "\n"
			_, err := config.Parse(strings.NewReader(doc), testPath)
			if err == nil {
				t.Fatal("invalid max_tickets parsed cleanly")
			}
			for _, want := range []string{testPath, `profile "work"`, tt.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestUnknownProfileKeyListsMaxTicketsAsValid(t *testing.T) {
	_, err := config.Parse(strings.NewReader("profiles:\n  work:\n    provider: github\n    max_ticket: 2\n"), testPath)
	if err == nil {
		t.Fatal("unknown key parsed cleanly")
	}
	if !strings.Contains(err.Error(), "valid keys are provider, host, project, auth, refresh_interval, max_tickets, wont_do_labels") {
		t.Errorf("error = %q, want max_tickets in the valid Profile keys", err)
	}
}

func TestParseEmptyDocuments(t *testing.T) {
	for _, doc := range []string{"", "\n", "# only a comment\n", "profiles: {}\n"} {
		cfg := parse(t, doc)
		if len(cfg.Profiles) != 0 {
			t.Errorf("Parse(%q) got %d profiles, want none", doc, len(cfg.Profiles))
		}
	}
}

// The acceptance criterion with its own test: a config file that writes a
// literal token is rejected, and the error explains what to do instead.
func TestParseRejectsALiteralToken(t *testing.T) {
	_, err := config.Parse(strings.NewReader(`
profiles:
  acme-jira:
    provider: jira
    host: acme.atlassian.net
    project: ABC
    auth:
      token: ATATT-this-is-not-a-real-token
`), testPath)
	if err == nil {
		t.Fatal("a literal token parsed cleanly; it must not")
	}
	for _, want := range []string{testPath, `profile "acme-jira"`, "auth.token", "token_env",
		"never stores tokens in the config file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	// The error must not echo the thing the user was told not to write.
	if strings.Contains(err.Error(), "ATATT-this-is-not-a-real-token") {
		t.Errorf("error = %q, want it to not repeat the token", err)
	}
}

func TestParseValidation(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "empty profile name",
			doc:  "profiles:\n  \"\":\n    provider: github\n",
			want: []string{"profile name must not be empty"},
		},
		{
			name: "provider missing",
			doc:  "profiles:\n  x:\n    host: acme.test\n",
			want: []string{`profile "x"`, "provider is required", "github, gitlab or jira"},
		},
		{
			name: "provider unknown",
			doc:  "profiles:\n  x:\n    provider: jirra\n",
			want: []string{`profile "x"`, `unknown provider "jirra"`, "github, gitlab or jira"},
		},
		{
			name: "host missing on jira",
			doc:  "profiles:\n  x:\n    provider: jira\n    project: ABC\n",
			want: []string{`profile "x"`, "host is required for a jira profile"},
		},
		{
			name: "host missing on gitlab",
			doc:  "profiles:\n  x:\n    provider: gitlab\n",
			want: []string{`profile "x"`, "host is required for a gitlab profile"},
		},
		{
			name: "host is a URL",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: https://acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n",
			want: []string{`profile "x"`, "host must be a hostname", "not a URL"},
		},
		{
			name: "project missing on jira",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    auth:\n      token_env: T\n",
			want: []string{`profile "x"`, "project is required for a jira profile", "key prefix Refs match on"},
		},
		{
			name: "project is not a key prefix",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ab-cd\n    auth:\n      token_env: T\n",
			want: []string{`profile "x"`, `project "ab-cd" is not a Jira project key`},
		},
		{
			name: "token_env is a token, not a name",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: glpat-abc-def\n",
			want: []string{`profile "x"`, "auth.token_env must be the upper-case NAME of an environment variable"},
		},
		{
			// A real GitHub token is a plausible environment-variable name under
			// any rule that accepts lower case, and Profile.Credential prints the
			// name it was given.
			name: "token_env is a token shaped like a name",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: ghp_16CabcdefGHI\n",
			want: []string{`profile "x"`, "auth.token_env must be the upper-case NAME of an environment variable"},
		},
		{
			name: "token_env is a lower-case name",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: jira_api_token\n",
			want: []string{`profile "x"`, "auth.token_env must be the upper-case NAME of an environment variable"},
		},
		{
			name: "user_env is a lower-case name",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n      user_env: jira_user\n",
			want: []string{`profile "x"`, "auth.user_env must be the upper-case NAME of an environment variable"},
		},
		{
			// The env-name check has to run whether or not a token_env is
			// present: a Profile that needs no token_env still reaches
			// Profile.Credential, which prints the name it was given.
			name: "user_env is a token on a gitlab profile with no token_env",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.test\n    auth:\n      user_env: glpat-Ab3cdEf\n",
			want: []string{`profile "x"`, "auth.user_env must be the upper-case NAME of an environment variable"},
		},
		{
			name: "user_env is a token on a github profile with no token_env",
			doc:  "profiles:\n  x:\n    provider: github\n    auth:\n      user_env: ghp_16CabcdefGHI\n",
			want: []string{`profile "x"`, "auth.user_env must be the upper-case NAME of an environment variable"},
		},
		{
			name: "token_env is a token on a github profile",
			doc:  "profiles:\n  x:\n    provider: github\n    auth:\n      token_env: ghp_16CabcdefGHI\n",
			want: []string{`profile "x"`, "auth.token_env must be the upper-case NAME of an environment variable"},
		},
		{
			name: "project on a github profile",
			doc:  "profiles:\n  x:\n    provider: github\n    project: acme/widgets\n",
			want: []string{`profile "x"`, "project is not used by a github profile", "a GitHub Ref carries its own owner/repo"},
		},
		{
			name: "a group-scoped gitlab project naming no group",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.test\n    project: groups/\n",
			want: []string{`profile "x"`, `project "groups/" names no group`, "write groups/<group path>"},
		},
		{
			name: "a second YAML document",
			doc:  "profiles:\n  x:\n    provider: github\n---\nprofiles:\n  y:\n    provider: github\n",
			want: []string{testPath, "more than one YAML document"},
		},
		{
			name: "token_env missing on jira",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n",
			want: []string{`profile "x"`, "auth.token_env is required for a jira profile"},
		},
		{
			name: "user and user_env together",
			doc:  "profiles:\n  x:\n    provider: github\n    auth:\n      user: me@acme.test\n      user_env: JIRA_USER\n",
			want: []string{`profile "x"`, "set auth.user or auth.user_env, not both"},
		},
		{
			name: "refresh_interval is not a duration",
			doc:  "profiles:\n  x:\n    provider: github\n    refresh_interval: banana\n",
			want: []string{`profile "x"`, `refresh_interval "banana" is not a duration`, `"30s"`},
		},
		{
			name: "refresh_interval below the floor",
			doc:  "profiles:\n  x:\n    provider: github\n    refresh_interval: 1s\n",
			want: []string{`profile "x"`, "refresh_interval must be at least 5s"},
		},
		{
			name: "refresh_interval zero",
			doc:  "profiles:\n  x:\n    provider: github\n    refresh_interval: 0s\n",
			want: []string{`profile "x"`, "refresh_interval must be at least 5s"},
		},
		{
			name: "two jira profiles claiming one key prefix on one site",
			doc: "profiles:\n" +
				"  a:\n    provider: jira\n    host: one.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n" +
				"  b:\n    provider: jira\n    host: one.atlassian.net\n    project: abc\n    auth:\n      token_env: T\n",
			want: []string{`profiles "a" and "b" both claim the Jira project key "ABC"`},
		},
		{
			// GitHub and Jira report cancellation natively, so there is nothing
			// for the key to do; accepting and ignoring it is worse.
			name: "wont_do_labels on a github profile",
			doc:  "profiles:\n  x:\n    provider: github\n    wont_do_labels: [wontfix]\n",
			want: []string{`profile "x"`, "wont_do_labels is only used by a gitlab profile"},
		},
		{
			name: "wont_do_labels on a jira profile",
			doc: "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n" +
				"    wont_do_labels: [wontfix]\n    auth:\n      token_env: T\n",
			want: []string{`profile "x"`, "wont_do_labels is only used by a gitlab profile"},
		},
		{
			// Honouring it would silently turn the inference off and flatter
			// every Epic containing abandoned work.
			name: "wont_do_labels written empty",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n    wont_do_labels: []\n",
			want: []string{`profile "x"`, "wont_do_labels names no labels", "built-in list"},
		},
		{
			name: "wont_do_labels with a blank entry",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n    wont_do_labels: [wontfix, \"  \"]\n",
			want: []string{`profile "x"`, "wont_do_labels entry", "no letters or digits"},
		},
		{
			// An entry that survives the whitespace check but normalizes away
			// to nothing: the whole list could reduce to the empty set and
			// silently restore the built-in labels the Profile meant to
			// replace. The gitlab package's own rule is what decides.
			name: "wont_do_labels with an entry that normalizes to nothing",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n    wont_do_labels: [\"::\"]\n",
			want: []string{`profile "x"`, `wont_do_labels entry "::"`, "no letters or digits"},
		},
		{
			name: "wont_do_labels with an empty scope segment",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n    wont_do_labels: [\"workflow::\"]\n",
			want: []string{`profile "x"`, `wont_do_labels entry "workflow::"`, "no letters or digits"},
		},
		{
			name: "an unknown field",
			doc:  "profiles:\n  x:\n    provider: github\n    porject: ABC\n",
			want: []string{testPath, "porject"},
		},
		{
			name: "an unknown top-level field",
			doc:  "default_profile: x\n",
			want: []string{testPath, "default_profile"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Parse(strings.NewReader(tt.doc), testPath)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", tt.doc, cfg)
			}
			// Every message quotes the file, so a user knows which one to open.
			if !strings.Contains(err.Error(), testPath) {
				t.Errorf("error = %q, want it to name the config file %q", err, testPath)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// The whole point of rejecting a token-shaped env name is that the token never
// reaches a terminal, a log or a scrollback buffer — so the rejection must not
// print the value it rejected.
func TestParseNeverEchoesARejectedCredential(t *testing.T) {
	const secret = "glpat-Ab3cdEfGhIjKlMnOpQr"
	docs := map[string]string{
		"gitlab user_env": "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.test\n    auth:\n      user_env: " + secret + "\n",
		"github user_env": "profiles:\n  x:\n    provider: github\n    auth:\n      user_env: " + secret + "\n",
		"jira token_env":  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: " + secret + "\n",
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse(strings.NewReader(doc), testPath)
			if err == nil {
				t.Fatal("a token-shaped env name must be rejected")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error = %q, want it never to quote the rejected value", err)
			}
		})
	}
}

// yaml.v3's own strict-decode prose names a Go type the user has never heard
// of and no key that would have worked. sitrep answers in its own vocabulary.
func TestParseRewritesStrictDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "a key that belongs under auth",
			doc:  "profiles:\n  x:\n    provider: github\n    token_env: GH_TOKEN\n",
			want: []string{
				`unknown key "token_env" in a profile`,
				"valid keys are provider, host, project, auth, refresh_interval, max_tickets, wont_do_labels",
				"token_env belongs under auth:",
			},
		},
		{
			name: "an unknown key under auth",
			doc:  "profiles:\n  x:\n    provider: github\n    auth:\n      tokenenv: GH_TOKEN\n",
			want: []string{`unknown key "tokenenv" in auth`, "valid keys are token_env, user, user_env"},
		},
		{
			name: "an unknown top-level key",
			doc:  "default_profile: x\n",
			want: []string{`unknown key "default_profile" in the top level`, "valid keys are profiles"},
		},
		{
			// The decoder accumulates every unknown key in one error, so all of
			// them are reported rather than one per run of the tool.
			name: "two unknown keys",
			doc:  "profiles:\n  x:\n    provider: github\n    porject: ABC\n    user: me\n",
			want: []string{`unknown key "porject"`, `unknown key "user"`, "user belongs under auth:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Parse(strings.NewReader(tt.doc), testPath)
			if err == nil {
				t.Fatal("an unknown key must be an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
			for _, leak := range []string{"config.Profile", "config.Auth", "config.Config", "unmarshal errors"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error = %q, want it never to leak %q", err, leak)
				}
			}
		})
	}
}

// A genuine syntax error is the library's business and its message is fine, so
// it keeps its pass-through.
func TestParseSyntaxErrorsStillSurface(t *testing.T) {
	_, err := config.Parse(strings.NewReader("profiles:\n  x:\n   provider: github\n    host: nope\n"), testPath)
	if err == nil {
		t.Fatal("malformed YAML must be an error")
	}
	if !strings.Contains(err.Error(), testPath) {
		t.Errorf("error = %q, want it to name the config file", err)
	}
	if !strings.Contains(err.Error(), "yaml") && !strings.Contains(err.Error(), "line") {
		t.Errorf("error = %q, want the library's own diagnosis to survive", err)
	}
}

// Legitimate documents the validator has to accept.
func TestParseAcceptsWhatTheTrackersAllow(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want config.Profile
	}{
		{
			// glab's own login is the fallback the docs promise; a profile that
			// only pins a host must not have to name a token variable to use it.
			name: "a gitlab profile with no token_env",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n    project: platform/widgets\n",
			want: config.Profile{Name: "x", Provider: "gitlab", Host: "gitlab.acme.test", Project: "platform/widgets", MaxTickets: 100},
		},
		{
			// A group-scoped path reaches the driver spelled exactly as written:
			// config documents the spelling and never strips the prefix.
			name: "a gitlab profile naming a group",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n    project: groups/acme\n",
			want: config.Profile{Name: "x", Provider: "gitlab", Host: "gitlab.acme.test", Project: "groups/acme", MaxTickets: 100},
		},
		{
			name: "a gitlab profile naming its own won't-do labels",
			doc: "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n" +
				"    wont_do_labels: [ausgemustert, \"Won't Do\"]\n",
			want: config.Profile{
				Name: "x", Provider: "gitlab", Host: "gitlab.acme.test", MaxTickets: 100,
				WontDoLabels: []string{"ausgemustert", "Won't Do"},
			},
		},
		{
			// A GitLab Profile may name labels in the site's writing system. GitLab
			// normalization retains Unicode letters and digits.
			name: "a gitlab profile whose won't-do labels are not Latin",
			doc: "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n" +
				"    wont_do_labels: [\"見送り\", \"не будет\"]\n",
			want: config.Profile{
				Name: "x", Provider: "gitlab", Host: "gitlab.acme.test", MaxTickets: 100,
				WontDoLabels: []string{"見送り", "не будет"},
			},
		},
		{
			// After credential scoping a Profile is the only way a self-hosted
			// host on a custom port can get a token, so it has to be nameable.
			name: "a host with an explicit port",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: acme.example:8443\n",
			want: config.Profile{Name: "x", Provider: "gitlab", Host: "acme.example:8443", MaxTickets: 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := parse(t, tt.doc)
			if got := cfg.Profiles["x"]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("profile = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Two Atlassian sites may each have a project called ABC. That is a config the
// file has to load, with the ambiguity resolved at selection time.
func TestParseAcceptsOneKeyOnTwoSites(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  a:\n    provider: jira\n    host: one.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n"+
		"  b:\n    provider: jira\n    host: two.atlassian.net\n    project: abc\n    auth:\n      token_env: T\n")
	if len(cfg.Profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(cfg.Profiles))
	}
}

func TestSelectMatchesAJiraKeyPrefix(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  acme-jira:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: JIRA_API_TOKEN\n"+
		"  other-jira:\n    provider: jira\n    host: other.atlassian.net\n    project: DEF\n    auth:\n      token_env: JIRA_API_TOKEN\n")

	tests := []struct {
		raw      string
		wantName string
		wantOK   bool
	}{
		{raw: "ABC-123", wantName: "acme-jira", wantOK: true},
		{raw: "abc-123", wantName: "acme-jira", wantOK: true},
		{raw: "DEF-1", wantName: "other-jira", wantOK: true},
		{raw: "XYZ-1", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			r := ref.Ref{Tracker: ref.TrackerJira, Key: strings.ToUpper(tt.raw), Raw: tt.raw}
			got, ok, err := cfg.Select(r, "")
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("Select(%q) matched = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if ok && got.Name != tt.wantName {
				t.Errorf("Select(%q) = %q, want %q", tt.raw, got.Name, tt.wantName)
			}
		})
	}
}

func TestSelectMatchesByProviderAndHost(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  dotcom:\n    provider: github\n"+
		"  enterprise:\n    provider: github\n    host: ghe.acme.test\n")

	tests := []struct {
		name     string
		r        ref.Ref
		wantName string
		wantOK   bool
	}{
		{
			name:     "github.com matches the profile with no host",
			r:        ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com"},
			wantName: "dotcom", wantOK: true,
		},
		{
			name:     "an enterprise host matches its own profile",
			r:        ref.Ref{Tracker: ref.TrackerGitHub, Host: "ghe.acme.test"},
			wantName: "enterprise", wantOK: true,
		},
		{
			name:     "host matching ignores case",
			r:        ref.Ref{Tracker: ref.TrackerGitHub, Host: "GitHub.com"},
			wantName: "dotcom", wantOK: true,
		},
		{
			// "www.ghe.example" and "ghe.example" are two origins; a credential
			// scoped to one must not follow a ref naming the other.
			name: "a www. host does not match the host without it",
			r:    ref.Ref{Tracker: ref.TrackerGitHub, Host: "www.github.com"},
		},
		{
			name: "an unknown host matches nothing, and that is not an error",
			r:    ref.Ref{Tracker: ref.TrackerGitHub, Host: "git.elsewhere.test"},
		},
		{
			name: "a ref with no tracker matches nothing",
			r:    ref.Ref{Raw: "111"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := cfg.Select(tt.r, "")
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("matched = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Name != tt.wantName {
				t.Errorf("Select = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

// Two Profiles on one host cannot be told apart, and guessing which Tracker to
// read from is worse than asking.
func TestSelectAmbiguityNamesTheCandidates(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  group-a:\n    provider: gitlab\n    host: gitlab.acme.test\n    project: a/one\n    auth:\n      token_env: T\n"+
		"  group-b:\n    provider: gitlab\n    host: gitlab.acme.test\n    project: b/two\n    auth:\n      token_env: T\n")

	_, ok, err := cfg.Select(ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.acme.test"}, "")
	if err == nil {
		t.Fatalf("Select matched = %v with no error; an ambiguous match must be an error", ok)
	}
	for _, want := range []string{`"group-a"`, `"group-b"`, "gitlab.acme.test", "--profile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestSelectProfileOverride(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  acme-jira:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n"+
		"  other-jira:\n    provider: jira\n    host: other.atlassian.net\n    project: DEF\n    auth:\n      token_env: T\n")

	// An explicit name wins over inference, even against a contradicting prefix.
	got, ok, err := cfg.Select(ref.Ref{Tracker: ref.TrackerJira, Key: "ABC-1"}, "other-jira")
	if err != nil || !ok {
		t.Fatalf("Select = %v, %v, want the named profile", ok, err)
	}
	if got.Name != "other-jira" {
		t.Errorf("Select = %q, want %q", got.Name, "other-jira")
	}

	_, _, err = cfg.Select(ref.Ref{Tracker: ref.TrackerJira, Key: "ABC-1"}, "nope")
	if err == nil {
		t.Fatal("an unknown --profile parsed cleanly; it must not")
	}
	for _, want := range []string{testPath, `"nope"`, "acme-jira", "other-jira"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// A Profile is a credential. An explicit --profile may name one for a Ref that
// says nothing about its host, but never for a Ref that names a different one:
// that would send one site's email and token to another site.
func TestSelectProfileOverrideIsBoundToTheRefsHost(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  acme-jira:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n"+
		"  dotcom:\n    provider: github\n")

	tests := []struct {
		name    string
		r       ref.Ref
		profile string
		want    []string
	}{
		{
			name:    "a jira profile for a github ref",
			r:       ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Raw: "acme/widgets#1"},
			profile: "acme-jira",
			want:    []string{`profile "acme-jira" is a jira profile`, "acme/widgets#1", "github ref"},
		},
		{
			name:    "a profile serving another site",
			r:       ref.Ref{Tracker: ref.TrackerJira, Host: "other.atlassian.net", Key: "ABC-1", Raw: "https://other.atlassian.net/browse/ABC-1"},
			profile: "acme-jira",
			want:    []string{`profile "acme-jira" serves acme.atlassian.net`, "other.atlassian.net"},
		},
		{
			name:    "a github profile for an enterprise host",
			r:       ref.Ref{Tracker: ref.TrackerGitHub, Host: "ghe.acme.test", Raw: "https://ghe.acme.test/acme/widgets/issues/1"},
			profile: "dotcom",
			want:    []string{`profile "dotcom" serves github.com`, "ghe.acme.test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := cfg.Select(tt.r, tt.profile)
			if err == nil {
				t.Fatalf("Select = (%+v, %v), want an error", got, ok)
			}
			if ok {
				t.Error("Select reported a match alongside its error")
			}
			for _, want := range append(tt.want, testPath) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// A Jira /browse/ URL names its own site. Matching it to a profile for the same
// key on a different site would hand that profile's credential to the site in
// the URL.
func TestSelectKeyPrefixHonoursAnExplicitHost(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  acme:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n"+
		"  other:\n    provider: jira\n    host: other.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n")

	t.Run("a ref naming a site selects that site's profile", func(t *testing.T) {
		got, ok, err := cfg.Select(ref.Ref{Tracker: ref.TrackerJira, Host: "other.atlassian.net",
			Key: "ABC-1", Raw: "https://other.atlassian.net/browse/ABC-1"}, "")
		if err != nil || !ok {
			t.Fatalf("Select = (%v, %v), want the other-site profile", ok, err)
		}
		if got.Name != "other" {
			t.Errorf("Select = %q, want %q", got.Name, "other")
		}
	})

	t.Run("a ref naming an unconfigured site matches nothing", func(t *testing.T) {
		got, ok, err := cfg.Select(ref.Ref{Tracker: ref.TrackerJira, Host: "third.atlassian.net",
			Key: "ABC-1", Raw: "https://third.atlassian.net/browse/ABC-1"}, "")
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if ok {
			t.Errorf("Select = %+v, want no match", got)
		}
	})

	t.Run("a bare key claimed twice is an ambiguity worth asking about", func(t *testing.T) {
		_, ok, err := cfg.Select(ref.Ref{Tracker: ref.TrackerJira, Key: "ABC-1", Raw: "ABC-1"}, "")
		if err == nil {
			t.Fatalf("Select matched = %v, want an ambiguity error", ok)
		}
		for _, want := range []string{`"acme"`, `"other"`, `"ABC"`, "--profile"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err, want)
			}
		}
	})

	t.Run("the known-prefix hint lists the key once", func(t *testing.T) {
		got := cfg.KeyPrefixes()
		if len(got) != 1 || got[0] != "ABC" {
			t.Errorf("KeyPrefixes() = %v, want [ABC]", got)
		}
	})
}

func TestKeyPrefixes(t *testing.T) {
	cfg := parse(t, "profiles:\n"+
		"  a:\n    provider: jira\n    host: one.atlassian.net\n    project: def\n    auth:\n      token_env: T\n"+
		"  b:\n    provider: jira\n    host: two.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n"+
		"  c:\n    provider: github\n")

	got := cfg.KeyPrefixes()
	want := []string{"ABC", "DEF"}
	if len(got) != len(want) {
		t.Fatalf("KeyPrefixes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("KeyPrefixes() = %v, want %v", got, want)
		}
	}
}

func TestComplete(t *testing.T) {
	jira := config.Profile{Name: "acme-jira", Provider: "jira", Host: "acme.atlassian.net", Project: "ABC"}

	t.Run("a key ref gains the profile's host and tracker", func(t *testing.T) {
		got := jira.Complete(ref.Ref{Key: "ABC-123", Raw: "abc-123"})
		want := ref.Ref{Tracker: ref.TrackerJira, Host: "acme.atlassian.net", Key: "ABC-123", Raw: "abc-123"}
		if got != want {
			t.Errorf("Complete = %+v, want %+v", got, want)
		}
	})

	t.Run("a URL's host survives a profile with a different host", func(t *testing.T) {
		in := ref.Ref{Tracker: ref.TrackerJira, Host: "other.atlassian.net", Key: "ABC-1", Raw: "https://other.atlassian.net/browse/ABC-1"}
		if got := jira.Complete(in); got != in {
			t.Errorf("Complete = %+v, want it unchanged: %+v", got, in)
		}
	})

	t.Run("owner, repo, number and raw are left alone", func(t *testing.T) {
		in := ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111, Raw: "111"}
		gh := config.Profile{Name: "work", Provider: "github", Host: "ghe.acme.test"}
		if got := gh.Complete(in); got != in {
			t.Errorf("Complete = %+v, want it unchanged: %+v", got, in)
		}
	})

	t.Run("a github profile with no host completes to github.com", func(t *testing.T) {
		gh := config.Profile{Name: "dotcom", Provider: "github"}
		got := gh.Complete(ref.Ref{Raw: "111"})
		if got.Host != "github.com" || got.Tracker != ref.TrackerGitHub {
			t.Errorf("Complete = %+v, want github.com", got)
		}
	})
}

func TestCredential(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}

	t.Run("resolves the token and the user", func(t *testing.T) {
		p := config.Profile{Name: "acme-jira", Auth: config.Auth{TokenEnv: "JIRA_API_TOKEN", User: "me@acme.test"}}
		got, err := p.Credential(env(map[string]string{"JIRA_API_TOKEN": "s3cret"}))
		if err != nil {
			t.Fatalf("Credential: %v", err)
		}
		if got.Token != "s3cret" || got.User != "me@acme.test" {
			t.Errorf("Credential = %+v, want the token and the user", got)
		}
	})

	t.Run("resolves user_env", func(t *testing.T) {
		p := config.Profile{Name: "acme-jira", Auth: config.Auth{TokenEnv: "T", UserEnv: "JIRA_USER"}}
		got, err := p.Credential(env(map[string]string{"T": "s3cret", "JIRA_USER": "me@acme.test"}))
		if err != nil {
			t.Fatalf("Credential: %v", err)
		}
		if got.User != "me@acme.test" {
			t.Errorf("User = %q, want it read from $JIRA_USER", got.User)
		}
	})

	t.Run("an unset variable names the profile and the variable", func(t *testing.T) {
		p := config.Profile{Name: "acme-jira", Auth: config.Auth{TokenEnv: "JIRA_API_TOKEN"}}
		_, err := p.Credential(env(nil))
		if err == nil {
			t.Fatal("an unset token resolved cleanly; it must not")
		}
		for _, want := range []string{`profile "acme-jira"`, "$JIRA_API_TOKEN", "is not set"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err, want)
			}
		}
	})

	t.Run("no token_env is the github profile that lets gh do the rest", func(t *testing.T) {
		p := config.Profile{Name: "dotcom", Provider: "github"}
		got, err := p.Credential(env(nil))
		if err != nil {
			t.Fatalf("Credential: %v", err)
		}
		if (got != config.Credential{}) {
			t.Errorf("Credential = %+v, want the zero value", got)
		}
	})
}

// A %v on a Credential must not print the secret.
func TestCredentialStringRedactsTheToken(t *testing.T) {
	c := config.Credential{User: "me@acme.test", Token: "s3cret-token-value"}
	if s := c.String(); strings.Contains(s, "s3cret-token-value") {
		t.Errorf("String() = %q, want the token redacted", s)
	}
	if s := c.String(); !strings.Contains(s, "REDACTED") {
		t.Errorf("String() = %q, want it to say REDACTED", s)
	}
}

// String alone is not enough: encoding/json and %#v both read the exported
// Token field and walk straight past it.
func TestCredentialIsRedactedHoweverItIsPrinted(t *testing.T) {
	const token = "s3cret-token-value"
	c := config.Credential{User: "me@acme.test", Token: token}

	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings := map[string]string{
		"json.Marshal": string(encoded),
		"String":       c.String(),
	}
	// The verbs are formatted through a variable so that this stays a test of
	// what each one prints rather than of what a linter thinks it should be
	// rewritten to.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings[verb] = fmt.Sprintf(verb, c)
	}

	for how, got := range renderings {
		if strings.Contains(got, token) {
			t.Errorf("%s = %q, want the token redacted", how, got)
		}
		// A substring is a leak too: half a token still narrows the search.
		for _, part := range []string{"s3cret", "token-value"} {
			if strings.Contains(got, part) {
				t.Errorf("%s = %q, want no part of the token", how, got)
			}
		}
		if !strings.Contains(got, "REDACTED") {
			t.Errorf("%s = %q, want it to say REDACTED", how, got)
		}
	}

	// An absent token and a hidden one are different facts.
	empty, err := json.Marshal(config.Credential{User: "me@acme.test"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(empty), "REDACTED") {
		t.Errorf("json.Marshal of a tokenless credential = %q, want no REDACTED", empty)
	}
}
