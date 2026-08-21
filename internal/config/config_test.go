package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/config"
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
	}
	if got := cfg.Profiles["acme-jira"]; got != want {
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
			want: []string{`profile "x"`, "project is required for a jira profile", "key prefix"},
		},
		{
			name: "project is not a key prefix",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ab-cd\n    auth:\n      token_env: T\n",
			want: []string{`profile "x"`, `project "ab-cd" is not a Jira project key`},
		},
		{
			name: "token_env is a token, not a name",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token_env: glpat-abc-def\n",
			want: []string{`profile "x"`, "auth.token_env must be the NAME of an environment variable"},
		},
		{
			name: "token_env missing on jira",
			doc:  "profiles:\n  x:\n    provider: jira\n    host: acme.atlassian.net\n    project: ABC\n",
			want: []string{`profile "x"`, "auth.token_env is required for a jira profile"},
		},
		{
			name: "token_env missing on gitlab",
			doc:  "profiles:\n  x:\n    provider: gitlab\n    host: gitlab.acme.test\n",
			want: []string{`profile "x"`, "auth.token_env is required for a gitlab profile"},
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
			name: "two jira profiles claiming one key prefix",
			doc: "profiles:\n" +
				"  a:\n    provider: jira\n    host: one.atlassian.net\n    project: ABC\n    auth:\n      token_env: T\n" +
				"  b:\n    provider: jira\n    host: two.atlassian.net\n    project: abc\n    auth:\n      token_env: T\n",
			want: []string{`profiles "a" and "b" both claim the Jira project key "ABC"`},
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
			name:     "host matching ignores case and www.",
			r:        ref.Ref{Tracker: ref.TrackerGitHub, Host: "www.GitHub.com"},
			wantName: "dotcom", wantOK: true,
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
