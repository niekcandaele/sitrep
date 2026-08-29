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
// driver's own replay server does. It answers the epic's routes and the
// milestone's, so one server serves every GitLab case in this file.
func replayGitLab(t *testing.T) *httptest.Server {
	t.Helper()

	var epicPages, milestonePages int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()

		var file string
		switch {
		case path == "/api/v4/groups/gitlab-org/epics/23356":
			file = "epic.json"
		case path == "/api/v4/groups/gitlab-org/epics/23356/issues":
			file = "epic_children_page1.json"
			if epicPages == 0 {
				w.Header().Set("x-next-page", "2")
			} else {
				file = "epic_children_page2.json"
			}
			epicPages++
		// The milestone routes are matched by suffix so that one server answers a
		// milestone addressed through a project path, a group path, or the
		// Profile's default project.
		case strings.HasSuffix(path, "/milestones"):
			file = milestoneFixture(path, r)
		case strings.HasSuffix(path, "/milestones/6239395/issues"):
			file = "milestone_issues_page1.json"
			if milestonePages == 0 {
				w.Header().Set("x-next-page", "2")
			} else {
				file = "milestone_issues_page2.json"
			}
			milestonePages++
		case strings.HasSuffix(path, "/issues") && strings.Contains(path, "/milestones/"):
			file = "milestone_issues_empty.json"
		// The first Ticket of each Watchlist carries a real merge request, so
		// the whole-program assertion has something to find; the rest carry none.
		case strings.HasSuffix(path, "/issues/101/closed_by"),
			strings.HasSuffix(path, "/issues/201/closed_by"),
			// The wider list is read for head_pipeline alone, and the recorded
			// payload is the same merge-request shape, so it answers both.
			strings.HasSuffix(path, "/issues/101/related_merge_requests"),
			strings.HasSuffix(path, "/issues/201/related_merge_requests"):
			file = "closed_by.json"
		case strings.HasSuffix(path, "/closed_by"),
			strings.HasSuffix(path, "/related_merge_requests"):
			file = "closed_by_empty.json"
		case strings.HasSuffix(path, "/merge_requests/3761/approvals"):
			file = "approvals_approved.json"
		default:
			t.Errorf("the driver requested %s, which this test serves no fixture for", path)
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

// milestoneFixture picks which milestone the lookup resolves to: the group one
// when the path says so, the populated project one by default, and the empty
// project one — which also carries no web_url — for iid 4.
func milestoneFixture(path string, r *http.Request) string {
	switch {
	case strings.Contains(path, "/groups/"):
		return "milestone_group.json"
	case r.URL.Query().Get("iids[]") == "4":
		return "milestone_project_no_web_url.json"
	default:
		return "milestone_project.json"
	}
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
			Name         string                     `json:"name"`
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"provider"`
		Tickets []map[string]json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v\n%s", err, got.stdout)
	}
	if doc.Provider.Name != "gitlab" {
		t.Errorf("provider.name = %q, want gitlab", doc.Provider.Name)
	}
	if string(doc.Provider.Capabilities["pull_requests"]) != "true" {
		t.Error("provider.capabilities.pull_requests = false; this driver correlates merge requests")
	}
	if len(doc.Tickets) != 10 {
		t.Errorf("got %d tickets, want the fixture epic's 10", len(doc.Tickets))
	}

	// The capability acceptance criterion, end to end: the declared Capability
	// is backed by data that survives the whole way to stdout.
	if _, ok := doc.Tickets[0]["pull_requests"]; !ok {
		t.Errorf("the first ticket carries no pull_requests:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, `"number": 3761`) {
		t.Errorf("the document does not carry the merge request's number:\n%s", got.stdout)
	}
}

// The milestone-as-Epic acceptance criterion, executable: a milestone URL on a
// Free-tier-shaped instance renders a full Epic document with merge requests.
func TestJSONGitLabMilestoneDocument(t *testing.T) {
	got := runWith([]string{"https://gitlab.com/gitlab-org/cli/-/milestones/3", "--json"}, cli.Deps{
		Provider: gitlabProvider(t),
		Config:   parseConfig(t, gitlabConfig),
		Env:      gitlabEnv,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}

	var doc struct {
		Provider struct {
			Name         string                     `json:"name"`
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"provider"`
		Watchlist struct {
			Epic map[string]json.RawMessage `json:"epic"`
		} `json:"watchlist"`
		Tickets []map[string]json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v\n%s", err, got.stdout)
	}
	if doc.Provider.Name != "gitlab" {
		t.Errorf("provider.name = %q, want gitlab", doc.Provider.Name)
	}
	if string(doc.Provider.Capabilities["pull_requests"]) != "true" {
		t.Error("provider.capabilities.pull_requests = false, want true")
	}
	if len(doc.Tickets) != 7 {
		t.Errorf("got %d tickets, want the fixture milestone's 7", len(doc.Tickets))
	}
	if got := string(doc.Watchlist.Epic["key"]); got != `"gitlab-org/cli%3"` {
		t.Errorf("epic.key = %s, want GitLab's own milestone reference", got)
	}
	if _, ok := doc.Tickets[0]["pull_requests"]; !ok {
		t.Errorf("the first ticket carries no pull_requests:\n%s", got.stdout)
	}
}

// A milestone with no issues decodes to a Ticket exactly as an empty epic does,
// carrying the milestone's own Detail.
func TestGitLabEmptyMilestoneDecodesToATicket(t *testing.T) {
	got := runWith([]string{"gitlab-org/cli%4", "--json"}, cli.Deps{
		Provider: gitlabProvider(t),
		Config:   parseConfig(t, gitlabConfig),
		Env:      gitlabEnv,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}

	var doc struct {
		Ticket map[string]json.RawMessage `json:"ticket"`
		Parent map[string]json.RawMessage `json:"parent"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the ticket document: %v\n%s", err, got.stdout)
	}
	if len(doc.Ticket) == 0 {
		t.Fatalf("the document carries no ticket; a milestone with no issues decodes to one:\n%s", got.stdout)
	}
	if got := string(doc.Ticket["key"]); got != `"gitlab-org/cli%4"` {
		t.Errorf("ticket.key = %s, want the milestone reference", got)
	}
	// A milestone belongs to nothing sitrep models.
	if len(doc.Parent) != 0 {
		t.Errorf("parent = %v, want none", doc.Parent)
	}
	// The built URL, for a milestone whose payload carries no web_url.
	if !strings.Contains(got.stdout, "https://gitlab.com/gitlab-org/cli/-/milestones/4") {
		t.Errorf("the document does not carry the built milestone URL:\n%s", got.stdout)
	}
}

// The milestone reference forms, through a Profile — the group one included,
// which is where sitrep's "groups/" spelling earns its keep.
func TestGitLabMilestoneReferenceFormsResolve(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{"a qualified project milestone", "gitlab-org/cli%3", `"gitlab-org/cli%3"`},
		{"a group milestone", "groups/gitlab-org%3", `"groups/gitlab-org%3"`},
		// The Profile's project is gitlab-org, which a bare reference resolves
		// against as a *project* path.
		{"a bare milestone reference", "%3", `"gitlab-org%3"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runWith([]string{tt.arg, "--json"}, cli.Deps{
				Provider: gitlabProvider(t),
				Config:   parseConfig(t, gitlabConfig),
				Env:      gitlabEnv,
			})

			if got.code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
			}
			if !strings.Contains(got.stdout, tt.want) {
				t.Errorf("the document does not carry %s:\n%s", tt.want, got.stdout)
			}
		})
	}
}

// The same run in text: a GitLab epic renders through the shared path, merge
// request summary included. The assertions are substrings rather than a golden
// because the line's shape is the plain renderer's to own, not this driver's.
func TestPlainGitLabEpicReport(t *testing.T) {
	got := runWith([]string{"https://gitlab.com/groups/gitlab-org/-/epics/23356", "--plain"}, cli.Deps{
		Provider: gitlabProvider(t),
		Config:   parseConfig(t, gitlabConfig),
		Env:      gitlabEnv,
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{
		"gitlab-org&23356", "gitlab-org/cli#101", "workflow::wontfix", "« éclair »",
		// The merge request moving the first Ticket: its number, its state, its
		// CI result and its review posture.
		"#3761", "open", "ci ok", "approved",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", got.stdout, want)
		}
	}
	// The merge request is open, so the Ticket moved out of TODO.
	if !strings.Contains(got.stdout, "IN PROGRESS") {
		t.Errorf("stdout = %q, want an In Progress group: an open merge request is work happening", got.stdout)
	}
}

// A run that resolves a real token prints it nowhere. The token reaches a
// real Provider and a real fetch, against a replay server.
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
	if p.LastSelector().(provider.EpicSelector).Ref != want {
		t.Errorf("the Provider was given %+v, want %+v", p.LastSelector().(provider.EpicSelector).Ref, want)
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
	if r := p.LastSelector().(provider.EpicSelector).Ref; r.Tracker != ref.TrackerGitLab || r.Host != "git.acme.test" {
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
	if r := p.LastSelector().(provider.EpicSelector).Ref; r.Tracker != ref.TrackerGitHub {
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
			if p.LastSelector().(provider.EpicSelector).Ref != tt.want {
				t.Errorf("the Provider was given %+v, want %+v", p.LastSelector().(provider.EpicSelector).Ref, tt.want)
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
	for _, want := range []string{"which GitLab instance", `"&12"`, "add a gitlab profile", "pass the Ref's full URL"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// A gitlab Profile with no project cannot supply the group path a bare "&12"
// needs, so it is not a candidate — matching it would trade a selection-time
// error for a confusing one two requests later.
func TestBareGitLabReferenceIgnoresAProjectlessProfile(t *testing.T) {
	cfg := parseConfig(t, `
profiles:
  host-only:
    provider: gitlab
    host: gitlab.acme.test
`)

	got := runWith([]string{"&12", "--json"}, cli.Deps{Config: cfg})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"which GitLab instance", "group or project", "project", `"&12"`} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// A milestone reference carries its own path, so a Profile that names only a
// host still resolves it.
func TestMilestoneReferenceAcceptsAProjectlessProfile(t *testing.T) {
	cfg := parseConfig(t, `
profiles:
  host-only:
    provider: gitlab
    host: gitlab.acme.test
`)

	got := runWith([]string{"acme/widgets%3", "--json"}, cli.Deps{
		Config:            cfg,
		GitLabTokenSource: noGitLabToken,
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	// Reaching the driver's own token error means the Profile was accepted and
	// the run got as far as needing a credential.
	for _, want := range []string{"gitlab:", "glab auth login"} {
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
	for _, want := range []string{"is not a GitLab Ref", "acme/widgets#111"} {
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

// The case --provider exists for. A self-managed GitLab URL with no "/-/" in
// it is indistinguishable from a GitHub Enterprise one, so the grammar guesses
// GitHub Enterprise — and until this, --provider gitlab could not correct that
// guess, because it demanded the parser already agree.
func TestProviderGitLabForcesAGuessedHost(t *testing.T) {
	got := runWith([]string{"--provider", "gitlab",
		"https://git.acme.test/acme/widgets/issues/7", "--json"}, cli.Deps{
		GitLabTokenSource: noGitLabToken,
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	// Reaching the GitLab driver's own token error is the proof: the run got
	// past driver selection to a driver that would have talked to GitLab.
	for _, want := range []string{"gitlab:", "glab auth login"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// A GitLab Profile naming an identity variable nobody set is reported at
// construction time, before any request — the same promise the Jira path makes.
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
