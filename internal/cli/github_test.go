package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// githubFixtures is where the GitHub driver's recorded payloads live. This test
// replays the driver's own fixtures rather than copying the bytes: the point of
// the whole-program test is that a real payload survives the whole way to
// stdout, and two copies of a payload drift.
const githubFixtures = "../provider/github/testdata"

// replayGitHub serves the two-page fixture epic, one page per request.
func replayGitHub(t *testing.T) *httptest.Server {
	t.Helper()

	pages := []string{"epic_page1.json", "epic_page2.json"}
	var mu sync.Mutex
	var served int

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n := served
		served++
		mu.Unlock()

		if n >= len(pages) {
			t.Errorf("the driver asked for page %d; the fixture epic has %d", n+1, len(pages))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		payload, err := os.ReadFile(filepath.Join(githubFixtures, pages[n]))
		if err != nil {
			t.Errorf("reading fixture %s: %v", pages[n], err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(s.Close)
	return s
}

func githubProvider(t *testing.T) provider.Provider {
	t.Helper()
	return github.New("github.com",
		github.WithEndpoint(replayGitHub(t).URL),
		github.WithTokenSource(func(context.Context, string) (string, error) {
			return "fixture-token-not-a-real-secret", nil
		}),
	)
}

// noConfig points a run at a config file that does not exist, which Load reads
// as the empty Config. It is how a run with no injected Provider stays
// hermetic: no test in this package may read — or be broken by — whatever is in
// the developer's home directory.
const noConfig = "testdata/there-is-no-config-here.yml"

func runWith(args []string, deps cli.Deps) result {
	var stdout, stderr bytes.Buffer
	deps.Now = fixedClock
	if deps.Config == nil && deps.ConfigPath == "" {
		deps.ConfigPath = noConfig
	}
	if deps.Env == nil {
		deps.Env = func(string) string { return "" }
	}
	code := cli.RunWith(args, &stdout, &stderr, deps)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// The headline test of the GitHub driver: a real recorded payload, fetched
// through the Provider interface, rendered by the whole program, byte for byte.
func TestJSONGitHubEpicDocument(t *testing.T) {
	got := runWith(
		[]string{"https://github.com/niekcandaele/sitrep/issues/2", "--json"},
		cli.Deps{Provider: githubProvider(t)},
	)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "epic_github.golden.json", []byte(got.stdout))

	// The GitHub driver declares the PullRequests capability, so the pull
	// requests moving a Ticket reach stdout — and a Ticket nothing is working on
	// carries no key at all rather than an empty list.
	//
	// The mirror image, a Provider that does not declare the capability, is
	// pinned by TestJSONOmitsUndeclaredCapabilities against
	// epic_no_pr_capability.golden.json.
	var doc struct {
		Tickets []map[string]json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v", err)
	}
	if len(doc.Tickets) == 0 {
		t.Fatal("the document has no tickets")
	}
	var withPRs int
	for _, ticket := range doc.Tickets {
		if _, ok := ticket["pull_requests"]; ok {
			withPRs++
		}
	}
	if withPRs == 0 {
		t.Error("no ticket carries pull_requests, which the GitHub driver serves")
	}
	if withPRs == len(doc.Tickets) {
		t.Error("every ticket carries pull_requests; the fixture epic has Tickets with none")
	}
}

// Acceptance criterion: a bare number run inside a clone is resolved through
// that clone's origin remote before any Provider sees it.
func TestBareNumberResolvesThroughTheOriginRemote(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"111", "--json"}, cli.Deps{
		Provider: p,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@github.com:acme/widgets.git", nil
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	want := ref.Ref{
		Tracker: ref.TrackerGitHub, Host: "github.com",
		Owner: "acme", Repo: "widgets", Number: 111, Raw: "111",
	}
	if p.LastRef() != want {
		t.Errorf("the Provider was given %+v, want %+v", p.LastRef(), want)
	}
	// ADR-0003 still holds on the resolved path.
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: rendering a list must not fetch detail", n)
	}
}

// Acceptance criterion: a full URL works from any directory, with no clone and
// no git at all.
func TestFullURLNeedsNoClone(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"https://github.com/other/repo/issues/7", "--json"}, cli.Deps{
		Provider: p,
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "", errors.New("not a git repository")
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if r := p.LastRef(); r.Owner != "other" || r.Repo != "repo" || r.Number != 7 {
		t.Errorf("the Provider was given %+v, want other/repo#7", r)
	}
}

func TestOwnerRepoNumberForm(t *testing.T) {
	p := fake.New()

	got := runWith([]string{"acme/widgets#111", "--json"}, cli.Deps{Provider: p})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if r := p.LastRef(); r.Owner != "acme" || r.Repo != "widgets" || r.Number != 111 {
		t.Errorf("the Provider was given %+v, want acme/widgets#111", r)
	}
}

func TestRefResolutionFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// gitLabToken is injected for the cases that now reach a real GitLab
		// driver, so that no case in this table executes glab or touches a
		// network.
		gitLabToken gitlab.TokenSource
		lookup      ref.RemoteLookup
		wantStderr  []string
	}{
		{
			name: "a bare number outside a clone",
			args: []string{"111", "--json"},
			lookup: func(context.Context, string, string) (string, error) {
				return "", errors.New("not a git repository")
			},
			wantStderr: []string{"111", "bare number", "origin remote", "URL"},
		},
		{
			// Both of these reach a real GitLab driver rather than a
			// "not supported yet" stub, so what they prove is that the
			// *GitLab* driver was
			// chosen: no token means no fetch, and the failure names the driver
			// that produced it, exactly as TestGitHubIsChosenFromTheRef does for
			// GitHub.
			name: "a GitLab clone reaches the GitLab driver",
			args: []string{"111", "--json"},
			lookup: func(context.Context, string, string) (string, error) {
				return "git@gitlab.com:acme/widgets.git", nil
			},
			gitLabToken: noGitLabToken,
			wantStderr:  []string{"gitlab:", "glab auth login", "GITLAB_TOKEN"},
		},
		{
			name:        "a GitLab URL reaches the GitLab driver",
			args:        []string{"https://gitlab.com/acme/widgets/-/issues/7", "--json"},
			gitLabToken: noGitLabToken,
			wantStderr:  []string{"gitlab:", "glab auth login", "GITLAB_TOKEN"},
		},
		{
			// A Jira URL names its own site, but not the credential to read it
			// with: that only comes from a Profile, so the driver asks for one.
			name:       "a Jira URL with no Profile",
			args:       []string{"https://acme.atlassian.net/browse/PROJ-12", "--json"},
			wantStderr: []string{"needs a Profile", "acme.atlassian.net"},
		},
		{
			name:       "something that is not a ref at all",
			args:       []string{"what even is this", "--json"},
			wantStderr: []string{"cannot parse"},
		},
		{
			name:       "a GitLab URL forced onto the GitHub driver",
			args:       []string{"--provider", "github", "https://gitlab.com/acme/widgets/-/issues/7", "--json"},
			wantStderr: []string{"is not a GitHub Epic Ref"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runWith(tt.args, cli.Deps{
				RemoteLookup:      tt.lookup,
				GitLabTokenSource: tt.gitLabToken,
			})

			if got.code != 1 {
				t.Errorf("exit code = %d, want 1", got.code)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want empty on failure", got.stdout)
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
				}
			}
		})
	}
}

// --provider fake serves any Epic Ref, including one no git remote can resolve:
// development must not need a clone.
func TestFakeProviderServesAnUnresolvableRef(t *testing.T) {
	got := runWith([]string{"--provider", "fake", "111", "--json"}, cli.Deps{
		RemoteLookup: func(context.Context, string, string) (string, error) {
			return "", errors.New("not a git repository")
		},
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "epic.golden.json", []byte(got.stdout))
}

// The GitHub driver is what an unqualified GitHub ref resolves to, without
// anyone naming it.
func TestGitHubIsChosenFromTheRef(t *testing.T) {
	got := runWith([]string{"https://github.com/acme/widgets/issues/1", "--json"}, cli.Deps{
		TokenSource: func(context.Context, string) (string, error) {
			return "", errors.New(`no GitHub token found: run "gh auth login" or set GITHUB_TOKEN`)
		},
	})

	// No token means no fetch, and the failure names the driver that produced
	// it — which is how we know the GitHub driver was chosen.
	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"github:", "gh auth login", "GITHUB_TOKEN"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}
