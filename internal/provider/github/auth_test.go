package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubGH replaces the `gh auth token` call for the duration of a test, so no
// test here depends on whether gh is installed or logged in.
func stubGH(t *testing.T, token func(host string) string) {
	t.Helper()
	restore := ghAuthToken
	ghAuthToken = func(_ context.Context, host string) string { return token(host) }
	t.Cleanup(func() { ghAuthToken = restore })
}

func stubEnv(t *testing.T, env map[string]string) {
	t.Helper()
	restore := getenv
	getenv = func(name string) string { return env[name] }
	t.Cleanup(func() { getenv = restore })
}

// The chain is `gh auth token` first, then the ambient variables — and the
// ambient variables only for a host they can be assumed to belong to.
func TestDefaultTokenSourcePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		gh      map[string]string
		env     map[string]string
		want    string
		wantErr []string
	}{
		{
			name: "gh wins on github.com",
			host: "github.com",
			gh:   map[string]string{"github.com": "gh-token"},
			env:  map[string]string{"GITHUB_TOKEN": "ambient"},
			want: "gh-token",
		},
		{
			name: "GITHUB_TOKEN when gh is silent",
			host: "github.com",
			env:  map[string]string{"GITHUB_TOKEN": "ambient"},
			want: "ambient",
		},
		{
			name: "GH_TOKEN after GITHUB_TOKEN",
			host: "github.com",
			env:  map[string]string{"GH_TOKEN": "second"},
			want: "second",
		},
		{
			name: "the documented order",
			host: "github.com",
			env:  map[string]string{"GITHUB_TOKEN": "first", "GH_TOKEN": "second"},
			want: "first",
		},
		{
			name: "an empty variable is not a token",
			host: "github.com",
			env:  map[string]string{"GITHUB_TOKEN": "   ", "GH_TOKEN": "second"},
			want: "second",
		},
		{
			name: "an empty host is github.com",
			env:  map[string]string{"GITHUB_TOKEN": "ambient"},
			want: "ambient",
		},
		{
			name: "gh serves an enterprise host",
			host: "ghe.example",
			gh:   map[string]string{"ghe.example": "enterprise-token"},
			want: "enterprise-token",
		},
		{
			name:    "an ambient token is not offered to an enterprise host",
			host:    "ghe.example",
			env:     map[string]string{"GITHUB_TOKEN": "ambient", "GH_TOKEN": "second"},
			wantErr: []string{"ghe.example", "gh auth login --hostname ghe.example", "profile"},
		},
		{
			name: "GH_HOST opts the ambient token in",
			host: "ghe.example",
			env:  map[string]string{"GITHUB_TOKEN": "ambient", "GH_HOST": "ghe.example"},
			want: "ambient",
		},
		{
			name:    "GH_HOST naming another host does not opt in",
			host:    "ghe.example",
			env:     map[string]string{"GITHUB_TOKEN": "ambient", "GH_HOST": "other.example"},
			wantErr: []string{"ghe.example"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubGH(t, func(host string) string { return tt.gh[host] })
			stubEnv(t, tt.env)

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
				// The error must never carry a token value.
				for _, token := range tt.env {
					if strings.TrimSpace(token) == "" {
						continue
					}
					if strings.Contains(err.Error(), token) {
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

// With nothing anywhere, github.com's error names both fixes.
func TestDefaultTokenSourceWithNothingAnywhere(t *testing.T) {
	stubGH(t, func(string) string { return "" })
	stubEnv(t, nil)

	got, err := DefaultTokenSource(context.Background(), "github.com")
	if !errors.Is(err, errNoToken) {
		t.Fatalf("DefaultTokenSource = (%q, %v), want errNoToken", got, err)
	}
	for _, want := range []string{"gh auth login", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to name %q", err, want)
		}
	}
}

func TestGHAuthTokenArgs(t *testing.T) {
	tests := []struct {
		name string
		host string
		want []string
	}{
		{name: "github.com passes no hostname", host: "github.com", want: []string{"auth", "token"}},
		{name: "an empty host passes none either", host: "", want: []string{"auth", "token"}},
		{
			name: "an enterprise host is named",
			host: "ghe.example",
			want: []string{"auth", "token", "--hostname", "ghe.example"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ghAuthTokenArgs(tt.host)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("ghAuthTokenArgs(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
