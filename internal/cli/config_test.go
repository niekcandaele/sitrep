package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// parseConfig builds a Config from a document, the way a test injects one. No
// test in this package reads the developer's home directory, real
// XDG_CONFIG_HOME, or real environment variables.
func parseConfig(t *testing.T, doc string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(strings.NewReader(doc), "/tmp/sitrep-test/config.yml")
	if err != nil {
		t.Fatalf("parsing the test config:\n%s\n%v", doc, err)
	}
	return &cfg
}

func writeConfig(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

const jiraConfig = `
profiles:
  acme-jira:
    provider: jira
    host: acme.atlassian.net
    project: ABC
    auth:
      user: me@acme.test
      token_env: JIRA_API_TOKEN
`

// The acceptance criterion, executable: a Jira-style key is matched to its
// Profile by key prefix, and the Provider is handed a Ref that knows which site
// to read.
func TestJiraKeyRefReachesTheProviderWithTheProfilesHost(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"ABC-123", "--json"}, cli.Deps{
		Provider: p,
		Config:   parseConfig(t, jiraConfig),
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	r := p.LastRef()
	if r.Key != "ABC-123" || r.Tracker != ref.TrackerJira || r.Host != "acme.atlassian.net" {
		t.Errorf("the Provider was given %+v, want ABC-123 on jira at acme.atlassian.net", r)
	}
}

// A lower-cased key is the same key: prefix matching is case-insensitive on
// both sides.
func TestJiraKeyRefMatchesCaseInsensitively(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"abc-123", "--json"}, cli.Deps{
		Provider: p,
		Config:   parseConfig(t, jiraConfig),
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if r := p.LastRef(); r.Key != "ABC-123" || r.Host != "acme.atlassian.net" {
		t.Errorf("the Provider was given %+v, want ABC-123 at acme.atlassian.net", r)
	}
}

func TestUnmatchedKeyPrefixIsAnError(t *testing.T) {
	got := runWith([]string{"XYZ-1", "--json"}, cli.Deps{Config: parseConfig(t, jiraConfig)})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", got.stdout)
	}
	for _, want := range []string{`"XYZ"`, "/tmp/sitrep-test/config.yml", "ABC"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

func TestMalformedConfigIsARuntimeError(t *testing.T) {
	path := writeConfig(t, "profiles:\n  acme-jira:\n    provider: jirra\n")

	got := runWith([]string{"111", "--json"}, cli.Deps{ConfigPath: path})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty: a bad config produces no report", got.stdout)
	}
	for _, want := range []string{path, `profile "acme-jira"`, `unknown provider "jirra"`} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

func TestConfigWithALiteralTokenIsRejected(t *testing.T) {
	path := writeConfig(t, "profiles:\n  acme-jira:\n    provider: jira\n"+
		"    host: acme.atlassian.net\n    project: ABC\n    auth:\n      token: not-a-real-token\n")

	got := runWith([]string{"111", "--json"}, cli.Deps{ConfigPath: path})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"auth.token", "token_env", "never stores tokens in the config file"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
	if strings.Contains(got.stderr, "not-a-real-token") {
		t.Errorf("stderr = %q, want it to not repeat the token", got.stderr)
	}
}

// A Profile that names a variable nobody set is the commonest way for a config
// to look right and do nothing, so the error names both the Profile and the
// variable — and no value of anything.
func TestJiraProfileWithAnUnsetTokenEnv(t *testing.T) {
	got := runWith([]string{"ABC-123", "--json"}, cli.Deps{
		Config: parseConfig(t, jiraConfig),
		Env:    func(string) string { return "" },
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{`profile "acme-jira"`, "$JIRA_API_TOKEN", "is not set"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// A Ref that resolves onto a driver that does not exist yet still ends in "not
// supported yet" — now able to say which Profile it would have used — and the
// credential it resolved on the way never reaches stdout or stderr.
//
// GitLab is the remaining unwritten driver; the Jira half of this promise moved
// to jira_test.go when #13 wrote that driver, because a Jira Profile now
// constructs one and a test that constructs one has to serve it too.
func TestResolvedProfileNeverPrintsItsToken(t *testing.T) {
	const secret = "s3cret-token-value"

	cfg := parseConfig(t, `
profiles:
  acme-gitlab:
    provider: gitlab
    host: gitlab.com
    auth:
      token_env: GITLAB_TOKEN
`)

	got := runWith([]string{"https://gitlab.com/acme/widgets/-/issues/7", "--json"}, cli.Deps{
		Config: cfg,
		Env: func(name string) string {
			if name == "GITLAB_TOKEN" {
				return secret
			}
			return ""
		},
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"gitlab is not supported yet", `profile "acme-gitlab"`} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
	if strings.Contains(got.stdout, secret) || strings.Contains(got.stderr, secret) {
		t.Error("the token reached the program's output; a credential is never printed")
	}
}

func TestUnknownProfileNameListsTheRealOnes(t *testing.T) {
	got := runWith([]string{"--profile", "nope", "ABC-123", "--json"}, cli.Deps{
		Provider: fake.New(),
		Config:   parseConfig(t, jiraConfig),
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{`"nope"`, "acme-jira", "/tmp/sitrep-test/config.yml"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// --profile is the escape hatch: it wins over inference, including over a
// contradicting prefix match.
func TestProfileFlagOverridesInference(t *testing.T) {
	p := fake.New()
	cfg := parseConfig(t, `
profiles:
  acme-jira:
    provider: jira
    host: acme.atlassian.net
    project: ABC
    auth:
      token_env: JIRA_API_TOKEN
  other-jira:
    provider: jira
    host: other.atlassian.net
    project: DEF
    auth:
      token_env: JIRA_API_TOKEN
`)

	got := runWith([]string{"--profile", "other-jira", "ABC-123", "--json"}, cli.Deps{
		Provider: p,
		Config:   cfg,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if r := p.LastRef(); r.Host != "other.atlassian.net" {
		t.Errorf("the Provider was given %+v, want the named profile's host", r)
	}
}

// A one-shot mode ignores the cadence entirely, so a Profile that sets one must
// not make --json fail.
func TestProfileRefreshIntervalDoesNotBreakOneShotModes(t *testing.T) {
	cfg := parseConfig(t, "profiles:\n  work:\n    provider: github\n"+
		"    host: github.com\n    refresh_interval: 30s\n")

	got := runWith([]string{"acme/widgets#111", "--json"}, cli.Deps{
		Provider: fake.New(),
		Config:   cfg,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
}

// An out-of-range --interval is still the user's mistake, with today's exact
// words: a Profile in play changes nothing about that.
func TestOutOfRangeIntervalFlagIsStillAUsageError(t *testing.T) {
	cfg := parseConfig(t, "profiles:\n  work:\n    provider: github\n"+
		"    host: github.com\n    refresh_interval: 30s\n")

	got := runWith([]string{"--interval", "1s", "acme/widgets#111"}, cli.Deps{
		Provider: fake.New(),
		Config:   cfg,
	})

	if got.code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr: %q)", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "refresh interval must be at least 5s") {
		t.Errorf("stderr = %q, want today's exact wording", got.stderr)
	}
}

// The strongest possible statement that --version and --help read nothing: a
// config file that could not possibly parse, and a clean exit anyway.
func TestVersionAndHelpReadNoConfig(t *testing.T) {
	path := writeConfig(t, "this is not: [valid: yaml\n")

	for _, arg := range []string{"--version", "--help"} {
		t.Run(arg, func(t *testing.T) {
			got := runWith([]string{arg}, cli.Deps{ConfigPath: path})

			if got.code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want empty", got.stderr)
			}
		})
	}
}

// The headline regression gate: zero config, GitHub, everything exactly as it
// was. Every existing golden in this package stays byte-identical and every
// existing test passes untouched; this one states the promise out loud.
func TestZeroConfigGitHubIsUnchanged(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"acme/widgets#111", "--json"}, cli.Deps{Provider: p})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "epic.golden.json", []byte(got.stdout))
	if r := p.LastRef(); r.Owner != "acme" || r.Repo != "widgets" || r.Number != 111 {
		t.Errorf("the Provider was given %+v, want acme/widgets#111 untouched by any Profile", r)
	}
}
