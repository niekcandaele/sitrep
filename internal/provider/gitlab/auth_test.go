package gitlab

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Real captured `glab auth status --hostname gitlab.com --show-token` output,
// with the token replaced by an obviously fake one. glab writes all of this to
// stderr; stdout is empty.
const (
	glabLoggedIn = "gitlab.com\n" +
		"  \x1b[32m✓\x1b[0m Logged in to gitlab.com as \x1b[1mniek\x1b[0m (\x1b[1m/home/niek/.config/glab-cli/config.yml\x1b[0m)\n" +
		"  \x1b[32m✓\x1b[0m Git operations for gitlab.com configured to use \x1b[1mhttps\x1b[0m protocol.\n" +
		"  \x1b[32m✓\x1b[0m API calls for gitlab.com are made over \x1b[1mhttps\x1b[0m protocol\n" +
		"  \x1b[32m✓\x1b[0m REST API Endpoint: \x1b[1mhttps://gitlab.com/api/v4/\x1b[0m\n" +
		"  \x1b[32m✓\x1b[0m Token: glpat-not-a-real-token\n"

	glabNoToken = "gitlab.com\n" +
		"  \x1b[33m!\x1b[0m No token found (checked config file, keyring, and environment variables).\n" +
		"  Run `glab auth login` to authenticate.\n"
)

func TestParseGlabToken(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "a logged-in host", stderr: glabLoggedIn, want: "glpat-not-a-real-token"},
		{name: "no token found", stderr: glabNoToken, want: ""},
		{name: "nothing at all", stderr: "", want: ""},
		{name: "without ANSI", stderr: "  ✓ Token: glpat-plain\n", want: "glpat-plain"},
		{name: "a masked token line is still a value", stderr: "  ✓ Token: **************\n", want: "**************"},
		{name: "a token line with no value", stderr: "  ✓ Token:\n", want: ""},
		{name: "prose that happens to say token", stderr: "  ! No Token: found here at all\n", want: ""},
		{name: "an error page", stderr: "x509: certificate signed by unknown authority\n", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseGlabToken(tt.stderr); got != tt.want {
				t.Errorf("parseGlabToken = %q, want %q", got, tt.want)
			}
		})
	}
}

// The chain is the environment first, in glab's own documented precedence, and
// only then glab's stored login. No test here reads or mutates the process
// environment.
func TestDefaultTokenSourceReadsTheEnvironmentFirst(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "GITLAB_TOKEN", env: map[string]string{"GITLAB_TOKEN": "first"}, want: "first"},
		{
			name: "GITLAB_ACCESS_TOKEN when the first is unset",
			env:  map[string]string{"GITLAB_ACCESS_TOKEN": "second"},
			want: "second",
		},
		{name: "OAUTH_TOKEN last", env: map[string]string{"OAUTH_TOKEN": "third"}, want: "third"},
		{
			name: "the documented order",
			env: map[string]string{
				"GITLAB_TOKEN":        "first",
				"GITLAB_ACCESS_TOKEN": "second",
				"OAUTH_TOKEN":         "third",
			},
			want: "first",
		},
		{
			name: "an empty variable is not a token",
			env:  map[string]string{"GITLAB_TOKEN": "   ", "GITLAB_ACCESS_TOKEN": "second"},
			want: "second",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := getenv
			getenv = func(name string) string { return tt.env[name] }
			t.Cleanup(func() { getenv = restore })

			got, err := DefaultTokenSource(context.Background(), "gitlab.com")
			if err != nil {
				t.Fatalf("DefaultTokenSource: %v", err)
			}
			if got != tt.want {
				t.Errorf("DefaultTokenSource = %q, want %q", got, tt.want)
			}
		})
	}
}

// With no variable set and no host to ask glab about, the chain ends in an
// error naming both fixes — and never in a token.
func TestDefaultTokenSourceWithNothingAnywhere(t *testing.T) {
	restore := getenv
	getenv = func(string) string { return "" }
	t.Cleanup(func() { getenv = restore })

	// An empty host short-circuits glabAuthToken, so this test never executes
	// glab whatever is installed on the machine running it.
	got, err := DefaultTokenSource(context.Background(), "")
	if !errors.Is(err, errNoToken) {
		t.Fatalf("DefaultTokenSource = (%q, %v), want errNoToken", got, err)
	}
	for _, want := range []string{"glab auth login", "GITLAB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to name %q", err, want)
		}
	}
}

// The ambient variables carry no host, so they belong to gitlab.com and to
// whatever $GITLAB_HOST names — and to nothing else. A self-managed host with
// no glab login is an error rather than a gitlab.com token sent abroad.
func TestDefaultTokenSourceScopesTheAmbientToken(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		env     map[string]string
		want    string
		wantErr []string
	}{
		{
			name: "gitlab.com keeps the documented order",
			host: "gitlab.com",
			env:  map[string]string{"GITLAB_TOKEN": "ambient"},
			want: "ambient",
		},
		{
			name: "an empty host is gitlab.com",
			env:  map[string]string{"GITLAB_TOKEN": "ambient"},
			want: "ambient",
		},
		{
			name:    "a self-managed host is refused the ambient token",
			host:    "git.acme.test",
			env:     map[string]string{"GITLAB_TOKEN": "ambient", "OAUTH_TOKEN": "also-ambient"},
			wantErr: []string{"git.acme.test", "glab auth login --hostname git.acme.test", "profile"},
		},
		{
			name: "GITLAB_HOST opts the ambient token in",
			host: "git.acme.test",
			env:  map[string]string{"GITLAB_TOKEN": "ambient", "GITLAB_HOST": "git.acme.test"},
			want: "ambient",
		},
		{
			name:    "GITLAB_HOST naming another host does not opt in",
			host:    "git.acme.test",
			env:     map[string]string{"GITLAB_TOKEN": "ambient", "GITLAB_HOST": "other.test"},
			wantErr: []string{"git.acme.test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreEnv := getenv
			getenv = func(name string) string { return tt.env[name] }
			restoreGlab := glabAuthToken
			glabAuthToken = func(context.Context, string) string { return "" }
			t.Cleanup(func() {
				getenv = restoreEnv
				glabAuthToken = restoreGlab
			})

			got, err := DefaultTokenSource(context.Background(), tt.host)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("DefaultTokenSource = %q, want an error", got)
				}
				if got != "" {
					t.Errorf("DefaultTokenSource returned %q alongside its error", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q, want it to name %q", err, want)
					}
				}
				for _, value := range tt.env {
					if strings.Contains(err.Error(), value) && value != tt.host {
						t.Errorf("error %q leaks an environment value", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("DefaultTokenSource: %v", err)
			}
			if got != tt.want {
				t.Errorf("DefaultTokenSource = %q, want %q", got, tt.want)
			}
		})
	}
}

// A host-scoped glab login serves a self-managed host, which is the way in the
// error above names first.
func TestDefaultTokenSourceUsesGlabForASelfManagedHost(t *testing.T) {
	restoreEnv := getenv
	getenv = func(string) string { return "" }
	restoreGlab := glabAuthToken
	glabAuthToken = func(_ context.Context, host string) string {
		if host == "git.acme.test" {
			return "glpat-self-managed"
		}
		return ""
	}
	t.Cleanup(func() {
		getenv = restoreEnv
		glabAuthToken = restoreGlab
	})

	got, err := DefaultTokenSource(context.Background(), "git.acme.test")
	if err != nil {
		t.Fatalf("DefaultTokenSource: %v", err)
	}
	if got != "glpat-self-managed" {
		t.Errorf("DefaultTokenSource = %q, want the glab token", got)
	}
}
