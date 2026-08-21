package gitlab

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// glabTimeout bounds the `glab auth status` call. Unlike `gh auth token` that
// command makes a live API call to validate the token before printing it, so
// this budget is a real one rather than a formality.
const glabTimeout = 5 * time.Second

// errNoToken is what a Provider reports when no token can be found anywhere. It
// names both fixes because a user who has neither usually does not know which
// one they want.
var errNoToken = errors.New(`no GitLab token found: run "glab auth login" or set GITLAB_TOKEN`)

// tokenEnvNames are the variables glab documents as taking precedence over its
// own stored credentials, in glab's own order.
var tokenEnvNames = []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN"}

// TokenSource returns a GitLab API token for the given host. It is a function
// rather than an interface for the same reason github.TokenSource is: a test
// injects one line and no production code learns it is being tested.
type TokenSource func(ctx context.Context, host string) (string, error)

// getenv is the environment reader DefaultTokenSource uses. It is a package
// variable rather than a direct os.Getenv call so that the token chain can be
// table-tested without any test reading — or mutating — the process
// environment.
var getenv = os.Getenv

// DefaultTokenSource resolves a token the way glab itself does: the environment
// variables glab documents as taking precedence over its own storage —
// GITLAB_TOKEN, then GITLAB_ACCESS_TOKEN, then OAUTH_TOKEN — and then glab's
// stored login, read through `glab auth status --hostname H --show-token`.
//
// The environment comes first here although `gh auth token` comes first in the
// GitHub driver, because glab has no `auth token` subcommand: its status
// command validates the token over the network before printing it and writes
// its output to stderr, so asking it first would put a live API call and a
// stderr scrape in front of a token sitrep already has. glab's own documented
// precedence is the environment too, so "reuse glab auth the same way" is
// honoured rather than bent.
//
// A token is a credential: it is never logged, printed, or wrapped into an
// error.
func DefaultTokenSource(ctx context.Context, host string) (string, error) {
	for _, name := range tokenEnvNames {
		if token := strings.TrimSpace(getenv(name)); token != "" {
			return token, nil
		}
	}
	if token := glabAuthToken(ctx, host); token != "" {
		return token, nil
	}
	return "", errNoToken
}

// glabAuthToken asks the glab CLI for a stored token, returning "" whenever
// glab is absent, unauthenticated, or slow. Not being logged in is not an error
// here: it is the ordinary case where the environment serves the token instead.
//
// Two things about this command are deliberate and verified against glab
// 1.113.0. First, it writes everything to *stderr* — stdout is empty — so that
// is what is parsed. Second, --hostname is passed always, unlike gh where the
// flag is conditional: glab has no implicit default host and falls back to
// $GITLAB_HOST, which would silently answer for the wrong instance.
func glabAuthToken(ctx context.Context, host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if _, err := exec.LookPath("glab"); err != nil {
		return ""
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, glabTimeout)
		defer cancel()
	}

	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "glab", "auth", "status", "--hostname", host, "--show-token")
	cmd.Stderr = &stderr
	// A non-zero exit is the "not logged in" case, which is not an error: the
	// output is parsed either way and an absent token yields "".
	_ = cmd.Run()
	return parseGlabToken(stderr.String())
}

// parseGlabToken reads a token out of `glab auth status --show-token` output.
//
// The output is decorated prose rather than a bare token — "✓ Token:
// glpat-…", or "! No token found (checked config file, keyring, and
// environment variables)." — so this finds the first line whose trimmed form,
// after the leading glyph, begins with "Token:" and takes the remainder.
// Anything carrying whitespace is not a token and is rejected.
//
// It is a pure function so that it can be table-tested against real captured
// output without running glab.
func parseGlabToken(stderr string) string {
	for _, line := range strings.Split(stripANSI(stderr), "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field != "Token:" {
				continue
			}
			if i+1 != len(fields)-1 {
				// A "Token:" with no value, or with several words after it, is
				// not a token line sitrep understands.
				break
			}
			return fields[len(fields)-1]
		}
	}
	return ""
}

// stripANSI removes the escape sequences glab colours its output with, so the
// parser sees the text a human sees.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		// A CSI sequence runs to its final byte in the range @ to ~.
		i++
		if i < len(s) && s[i] == '[' {
			for i++; i < len(s) && (s[i] < '@' || s[i] > '~'); i++ {
			}
		}
	}
	return b.String()
}
