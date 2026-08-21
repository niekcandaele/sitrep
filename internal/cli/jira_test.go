package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/jira"
)

// jiraFixtures is where the Jira driver's payloads live. This test replays the
// driver's own fixtures rather than copying the bytes: the point of the
// whole-program test is that a real payload survives the whole way to stdout,
// and two copies of a payload drift.
const jiraFixtures = "../provider/jira/testdata"

// The credential the replayed Jira run connects with. Both halves are obviously
// fake, so a test asserting that neither reaches the program's output is
// asserting something visible.
const (
	jiraEmail = "me@acme.test"
	jiraToken = "s3cret-jira-token-value"
)

// replayJira serves the two-page fixture epic, routed by path the way the
// driver's own replay server does.
func replayJira(t *testing.T) *httptest.Server {
	t.Helper()

	searches := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var file string
		switch r.URL.Path {
		case "/rest/api/2/issue/ABC-1":
			file = "epic_issue.json"
		case "/rest/api/2/search/jql":
			file = "epic_children_page1.json"
			if searches > 0 {
				file = "epic_children_page2.json"
			}
			searches++
		default:
			t.Errorf("the driver requested %s, which this test serves no fixture for", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		payload, err := os.ReadFile(filepath.Join(jiraFixtures, file))
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

// jiraProvider is the driver a Jira Profile would construct, pointed at the
// replay server. There is no way to redirect a constructed driver's base URL
// from the command line, and there should not be one: injecting the constructed
// Provider is the seam Deps already has, and widening Deps for a test would put
// a test-only knob in the production path.
func jiraProvider(t *testing.T) provider.Provider {
	t.Helper()
	return jira.New("acme.atlassian.net",
		jira.WithBaseURL(replayJira(t).URL),
		jira.WithCredentials(jira.Credentials{Email: jiraEmail, Token: jiraToken}))
}

// The headline test of the Jira driver: a key Ref, matched to its Profile by
// key prefix, fetched through the Provider interface and rendered by the whole
// program.
func TestJSONJiraEpicDocument(t *testing.T) {
	got := runWith([]string{"ABC-1", "--json"}, cli.Deps{
		Provider: jiraProvider(t),
		Config:   parseConfig(t, jiraConfig),
		Env:      func(string) string { return jiraToken },
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
	if doc.Provider.Name != "jira" {
		t.Errorf("provider.name = %q, want jira", doc.Provider.Name)
	}
	if doc.Provider.Capabilities["pull_requests"] {
		t.Error("provider.capabilities.pull_requests = true, want false")
	}
	if len(doc.Tickets) != 10 {
		t.Errorf("got %d tickets, want the fixture epic's 10", len(doc.Tickets))
	}

	// The capability-differences acceptance criterion, end to end: Jira declares
	// no pull requests, so no Ticket carries a section and nothing errors. The
	// document still says the capability is off — that declaration is what a
	// consumer reads to know the absence is deliberate.
	for _, ticket := range doc.Tickets {
		if _, ok := ticket["pull_requests"]; ok {
			t.Error("a ticket carries pull_requests, which the Jira driver does not serve")
		}
	}
}

// The same run in text: a Jira epic renders through the shared path, with no
// pull request section and no error about its absence.
func TestPlainJiraEpicReport(t *testing.T) {
	got := runWith([]string{"ABC-1", "--plain"}, cli.Deps{
		Provider: jiraProvider(t),
		Config:   parseConfig(t, jiraConfig),
		Env:      func(string) string { return jiraToken },
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"ABC-1", "ABC-7", "Won't Do", "« éclair »"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", got.stdout, want)
		}
	}
	if strings.Contains(strings.ToLower(got.stdout), "pull request") {
		t.Error("the report mentions pull requests, which the Jira driver does not serve")
	}
}

// A run that resolves a real credential prints neither half of it, anywhere.
func TestJiraRunNeverPrintsItsCredential(t *testing.T) {
	got := runWith([]string{"ABC-1", "--json"}, cli.Deps{
		Provider: jiraProvider(t),
		Config:   parseConfig(t, jiraConfig),
		Env:      func(string) string { return jiraToken },
	})

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	for _, secret := range []string{jiraToken, "Basic "} {
		if strings.Contains(got.stdout, secret) || strings.Contains(got.stderr, secret) {
			t.Errorf("%q reached the program's output; a credential is never printed", secret)
		}
	}
}

// A Jira Ref with no Profile cannot be served: the site is known and the
// credential is not, so the error says which file to write.
func TestJiraRefWithNoProfile(t *testing.T) {
	got := runWith([]string{"https://acme.atlassian.net/browse/ABC-1", "--json"}, cli.Deps{
		ConfigPath: noConfig,
	})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", got.stdout)
	}
	for _, want := range []string{"jira:", "needs a Profile", "acme.atlassian.net", "Atlassian email"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}

// --provider is an override, not a way to point one driver at another
// Tracker's Ref.
func TestProviderJiraRejectsAGitHubRef(t *testing.T) {
	got := runWith([]string{"--provider", "jira", "acme/widgets#111", "--json"}, cli.Deps{})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	for _, want := range []string{"is not a Jira Epic Ref", "acme/widgets#111"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
}
