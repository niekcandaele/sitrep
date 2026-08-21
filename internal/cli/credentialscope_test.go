package cli_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/config"
)

// countingServer answers nothing and counts what it was asked. A run that is
// supposed to refuse before it sends anything is proved by the count, not by
// the error text.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	return s, &requests
}

// An ambient token belongs to github.com. An unrecognized host — one an
// attacker can put in a pasted URL or a git origin remote — has to be logged in
// to, or named by a Profile, and until it is sitrep sends nothing at all: the
// request count, not the error text, is what proves that.
func TestAmbientTokenIsNotSentToAnUnknownHost(t *testing.T) {
	// The ambient variables are read from the real environment, so this is the
	// one place a test in this package sets it. GH_HOST is blanked so the
	// machine running the test cannot opt the ambient token in.
	t.Setenv("GITHUB_TOKEN", "ambient-token-not-a-real-secret")
	t.Setenv("GH_TOKEN", "ambient-token-not-a-real-secret")
	t.Setenv("GH_HOST", "")

	server, requests := countingServer(t)
	host := strings.TrimPrefix(server.URL, "http://")

	got := runWith([]string{"https://" + host + "/acme/widgets/issues/42"}, cli.Deps{})

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the run made %d request(s); it must make none before it has a credential for the host", n)
	}
	for _, want := range []string{"no GitHub token for", host, "gh auth login --hostname", "profile"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
	if strings.Contains(got.stderr, "ambient-token-not-a-real-secret") {
		t.Errorf("stderr = %q, want it to never contain a token", got.stderr)
	}
}

// A --profile the user names is still a credential, and a credential may only
// be sent to the host it was configured for. The mismatch is caught before a
// Provider exists, so nothing is sent.
func TestProfileForAnotherHostIsRefusedBeforeAnyRequest(t *testing.T) {
	server, requests := countingServer(t)
	host := strings.TrimPrefix(server.URL, "http://")

	cfg := config.Config{
		Path: "/tmp/sitrep-test/config.yml",
		Profiles: map[string]config.Profile{
			"acme": {
				Name:     "acme",
				Provider: "github",
				Host:     "ghe.acme.test",
				Auth:     config.Auth{TokenEnv: "GHE_TOKEN"},
			},
		},
	}

	got := runWith(
		[]string{"https://" + host + "/acme/widgets/issues/42", "--profile", "acme"},
		cli.Deps{
			Config: &cfg,
			Env:    func(string) string { return "token-not-a-real-secret" },
		},
	)

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", got.code, got.stderr)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the run made %d request(s); it must make none when the profile serves another host", n)
	}
	for _, want := range []string{`profile "acme" serves ghe.acme.test`, host} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", got.stderr, want)
		}
	}
	if strings.Contains(got.stderr, "token-not-a-real-secret") {
		t.Errorf("stderr = %q, want it to never contain a token", got.stderr)
	}
}
