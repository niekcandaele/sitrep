package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// gitlabFixtures is where the GitLab driver's payloads live. These tests replay
// the driver's own fixtures rather than copying the bytes: the point of the
// whole-program test is that a real payload survives the whole way to stdout,
// and two copies of a payload drift.
const gitlabFixtures = "../provider/gitlab/testdata"

// The token the replayed GitLab run connects with. It is obviously fake, so a
// test asserting that it never reaches the program's output is asserting
// something visible.
const gitlabToken = "s3cret-gitlab-token-value"

// noGitLabToken is what a run with no credential anywhere resolves to. Injecting
// it is what keeps a CLI-seam test from executing glab or reaching a network to
// find that out.
var noGitLabToken gitlab.TokenSource = func(context.Context, string) (string, error) {
	return "", errors.New(`no GitLab token found: run "glab auth login" or set GITLAB_TOKEN`)
}

const gitlabConfig = `
profiles:
  acme-gitlab:
    provider: gitlab
    host: gitlab.com
    project: gitlab-org
    auth:
      token_env: GITLAB_TOKEN
`

// replayGitLab serves the two-page fixture epic, routed by path the way the
// driver's own replay server does.
func replayGitLab(t *testing.T) *httptest.Server {
	t.Helper()

	pages := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var file string
		switch r.URL.EscapedPath() {
		case "/api/v4/groups/gitlab-org/epics/23356":
			file = "epic.json"
		case "/api/v4/groups/gitlab-org/epics/23356/issues":
			file = "epic_children_page1.json"
			if pages == 0 {
				w.Header().Set("x-next-page", "2")
			} else {
				file = "epic_children_page2.json"
			}
			pages++
		default:
			t.Errorf("the driver requested %s, which this test serves no fixture for", r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		payload, err := os.ReadFile(filepath.Join(gitlabFixtures, file))
		if err != nil {
			t.Errorf("reading fixture %s: %v", file, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(s.Close)
	return s
}

// gitlabProvider is the driver a GitLab Profile would construct, pointed at the
// replay server. There is deliberately no command-line way to redirect a
// constructed driver's base URL: injecting the constructed Provider is the seam
// Deps already has, and widening Deps for a test would put a test-only knob in
// the production path.
func gitlabProvider(t *testing.T) provider.Provider {
	t.Helper()
	return gitlab.New("gitlab.com",
		gitlab.WithBaseURL(replayGitLab(t).URL),
		gitlab.WithPath("gitlab-org"),
		gitlab.WithTokenSource(func(context.Context, string) (string, error) {
			return gitlabToken, nil
		}))
}

func gitlabEnv(name string) string {
	if name == "GITLAB_TOKEN" {
		return gitlabToken
	}
	return ""
}

// The headline test of the GitLab driver: a group epic URL, fetched through the
// Provider interface and rendered by the whole program.
func TestJSONGitLabEpicDocument(t *testing.T) {
	got := runWith([]string{"https://gitlab.com/groups/gitlab-org/-/epics/23356", "--json"}, cli.Deps{
		Provider: gitlabProvider(t),
		Config:   parseConfig(t, gitlabConfig),
		Env:      gitlabEnv,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}

	var doc struct {
		Provider struct {
			Name         string          `json:"name"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"provider"`
		Tickets []map[string]json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v\n%s", err, got.stdout)
	}
	if doc.Provider.Name != "gitlab" {
		t.Errorf("provider.name = %q, want gitlab", doc.Provider.Name)
	}
	if doc.Provider.Capabilities["pull_requests"] {
		t.Error("provider.capabilities.pull_requests = true, want false: that is #15's")
	}
	if len(doc.Tickets) != 10 {
		t.Errorf("got %d tickets, want the fixture epic's 10", len(doc.Tickets))
	}

	// The capability-differences acceptance criterion, end to end: GitLab
	// declares no merge request correlation, so no Ticket carries a section and
	// nothing errors.
	for _, ticket := range doc.Tickets {
		if _, ok := ticket["pull_requests"]; ok {
			t.Error("a ticket carries pull_requests, which the GitLab driver does not serve")
		}
	}
}

// The same run in text: a GitLab epic renders through the shared path, with no
// merge request section and no error about its absence.
func TestPlainGitLabEpicReport(t *testing.T) {
	got := runWith([]string{"https://gitlab.com/groups/gitlab-org/-/epics/23356", "--plain"}, cli.Deps{
		Provider: gitlabProvider(t),
		Config:   parseConfig(t, gitlabConfig),
		Env:      gitlabEnv,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"gitlab-org&23356", "gitlab-org/cli#101", "workflow::wontfix", "« éclair »"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", got.stdout, want)
		}
	}
	for _, unwanted := range []string{"pull request", "merge request"} {
		if strings.Contains(strings.ToLower(got.stdout), unwanted) {
			t.Errorf("the report mentions %q, which the GitLab driver does not serve", unwanted)
		}
	}
}

// A run that resolves a real token prints it nowhere. This is the promise #12
// made against the then-unimplemented GitLab seam; now that the driver exists it
// is kept against a replay server instead, which is both stronger and hermetic.
func TestResolvedProfileNeverPrintsItsToken(t *testing.T) {
	got := runWith([]string{"https://gitlab.com/groups/gitlab-org/-/epics/23356", "--json"}, cli.Deps{
		Provider: gitlabProvider(t),
		Config:   parseConfig(t, gitlabConfig),
		Env:      gitlabEnv,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	for _, secret := range []string{gitlabToken, "Bearer "} {
		if strings.Contains(got.stdout, secret) || strings.Contains(got.stderr, secret) {
			t.Errorf("%q reached the program's output; a credential is never printed", secret)
		}
	}
}

// A failing run prints no credential either.
func TestFailingGitLabRunNeverPrintsItsToken(t *testing.T) {
	got := runWith([]string{"https://gitlab.com/groups/gitlab-org/-/epics/23356", "--json"}, cli.Deps{
		Config: parseConfig(t, gitlabConfig),
		Env:    gitlabEnv,
		GitLabTokenSource: func(context.Context, string) (string, error) {
			return gitlabToken, nil
		},
	})

	// No replay server, so the fetch fails on connection or on GitLab's answer;
	// either way the token stays out of the output.
	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if strings.Contains(got.stdout, gitlabToken) || strings.Contains(got.stderr, gitlabToken) {
		t.Error("the token reached the program's output; a credential is never printed")
	}
}

// The auto-detection acceptance criterion: a bare number inside a gitlab.com
// clone reaches the GitLab driver with the right Ref, with no Profile involved.
func TestBareNumberInAGitLabCloneReachesTheGitLabDriver(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"7", "--json"}, cli.Deps{
		Provider: p,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@gitlab.com:acme/widgets.git", nil
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	want := ref.Ref{
		Tracker: ref.TrackerGitLab, Host: "gitlab.com",
		Owner: "acme", Repo: "widgets", Number: 7, Raw: "7",
	}
	if p.LastRef() != want {
		t.Errorf("the Provider was given %+v, want %+v", p.LastRef(), want)
	}
}

// A self-managed host the grammar has to guess at: ref.ParseRemoteURL calls an
// unrecognized host GitHub Enterprise, and a gitlab Profile claiming that host
// is what corrects it.
func TestSelfManagedCloneIsRetaggedByItsProfile(t *testing.T) {
	const selfManaged = `
profiles:
  acme-gitlab:
    provider: gitlab
    host: git.acme.test
    project: acme/widgets
    auth:
      token_env: GITLAB_TOKEN
`
	p := fake.New()

	got := runWith([]string{"7", "--json"}, cli.Deps{
		Provider: p,
		Config:   parseConfig(t, selfManaged),
		Env:      gitlabEnv,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@git.acme.test:acme/widgets.git", nil
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if r := p.LastRef(); r.Tracker != ref.TrackerGitLab || r.Host != "git.acme.test" {
		t.Errorf("the Provider was given %+v, want gitlab at git.acme.test", r)
	}
}

// The mirror image, unchanged: the same clone with no Profile is still read as
// GitHub Enterprise, which is what the grammar has always done.
func TestSelfManagedCloneWithNoProfileIsStillGitHub(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"7", "--json"}, cli.Deps{
		Provider: p,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@git.acme.test:acme/widgets.git", nil
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if r := p.LastRef(); r.Tracker != ref.TrackerGitHub {
		t.Errorf("the Provider was given %+v, want the GitHub Enterprise reading unchanged", r)
	}
}

// GitLab's own reference form, through a Profile.
func TestGitLabReferenceFormsResolveThroughAProfile(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want ref.Ref
	}{
		{
			name: "a qualified reference",
			arg:  "acme&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Number: 12, Key: "acme&12", Raw: "acme&12"},
		},
		{
			// A bare reference names no instance at all, so the single gitlab
			// Profile is what says which GitLab this is.
			name: "a bare reference",
			arg:  "&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Number: 12, Key: "&12", Raw: "&12"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fake.New()

			got := runWith([]string{tt.arg, "--json"}, cli.Deps{
				Provider: p,
				Config:   parseConfig(t, gitlabConfig),
				Env:      gitlabEnv,
			})

			if got.code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
			}
			if p.LastRef() != tt.want {
				t.Errorf("the Provider was given %+v, want %+v", p.LastRef(), tt.want)
			}
		})
	}
}

// A bare reference with nothing to resolve it against says exactly what to add.
func TestBareGitLabReferenceWithNoProfile(t *testing.T) {
	got := runWith([]string{"&12", "--json"}, cli.Deps{ConfigPath: noConfig})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", got.stdout)
	}
	for _, want := range []string{"which GitLab instance", `"&12"`, "add a gitlab profile", "full URL"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// Two gitlab Profiles cannot both answer a hostless reference, so sitrep asks
// rather than guessing.
func TestBareGitLabReferenceWithSeveralProfiles(t *testing.T) {
	cfg := parseConfig(t, `
profiles:
  one-gitlab:
    provider: gitlab
    host: gitlab.com
    project: acme
    auth:
      token_env: GITLAB_TOKEN
  two-gitlab:
    provider: gitlab
    host: git.acme.test
    project: acme
    auth:
      token_env: GITLAB_TOKEN
`)

	got := runWith([]string{"&12", "--json"}, cli.Deps{Config: cfg, Env: gitlabEnv})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{`"one-gitlab"`, `"two-gitlab"`, "--profile"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// --provider is an override, not a way to point one driver at another Tracker's
// Ref.
func TestProviderGitLabRejectsAGitHubRef(t *testing.T) {
	got := runWith([]string{"--provider", "gitlab", "acme/widgets#111", "--json"}, cli.Deps{})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"is not a GitLab Epic Ref", "acme/widgets#111"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// --provider gitlab is also the escape hatch for a self-managed host with no
// Profile: it forces the driver the grammar guessed wrong about.
func TestProviderGitLabForcesTheDriver(t *testing.T) {
	got := runWith([]string{"--provider", "gitlab",
		"https://git.acme.test/acme/widgets/-/issues/7", "--json"}, cli.Deps{
		GitLabTokenSource: noGitLabToken,
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"gitlab:", "glab auth login"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// A GitLab Profile naming an identity variable nobody set is reported at
// construction time, before any request — the same promise #12 made for Jira.
func TestGitLabProfileWithAnUnsetUserEnv(t *testing.T) {
	cfg := parseConfig(t, `
profiles:
  acme-gitlab:
    provider: gitlab
    host: gitlab.com
    project: acme
    auth:
      token_env: GITLAB_TOKEN
      user_env: GITLAB_USER
`)

	got := runWith([]string{"&12", "--json"}, cli.Deps{
		Config:            cfg,
		Env:               gitlabEnv,
		GitLabTokenSource: noGitLabToken,
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", got.stdout)
	}
	for _, want := range []string{`profile "acme-gitlab"`, "$GITLAB_USER", "is not set"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
	if strings.Contains(got.stderr, gitlabToken) {
		t.Error("the token reached the program's output; a credential is never printed")
	}
}

// The mirror: an unset token_env is *not* fatal on GitLab, because glab may
// still be logged in. The run reaches the driver, which is what fails.
func TestGitLabProfileWithAnUnsetTokenEnvFallsThrough(t *testing.T) {
	got := runWith([]string{"&12", "--json"}, cli.Deps{
		Config:            parseConfig(t, gitlabConfig),
		Env:               func(string) string { return "" },
		GitLabTokenSource: noGitLabToken,
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	// The failure is the driver's own, not the Profile's: an unset variable is a
	// preference that went unheeded, not a broken configuration.
	if strings.Contains(got.stderr, "is not set") {
		t.Errorf("stderr = %q, want the driver's message: an unset token_env falls through to glab", got.stderr)
	}
	for _, want := range []string{"gitlab:", "glab auth login"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}
