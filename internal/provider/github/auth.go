package github

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ghTimeout bounds the `gh auth token` call. Reading a stored token is a local
// file read behind a CLI; five seconds is generous.
const ghTimeout = 5 * time.Second

// errNoToken is what a Provider reports when no token can be found anywhere. It
// names both fixes because a user who has neither usually does not know which
// one they want.
var errNoToken = errors.New(`no GitHub token found: run "gh auth login" or set GITHUB_TOKEN`)

// TokenSource returns a GitHub API token for the given host. It is a function
// rather than an interface on purpose: a test injects one line and no
// production code learns that it is being tested.
type TokenSource func(ctx context.Context, host string) (string, error)

// DefaultTokenSource resolves a token the way the user's own tooling does:
// `gh auth token` first, then GITHUB_TOKEN, then GH_TOKEN. It never reads
// gh's own state file — `gh auth token` is the documented interface, and
// reading ~/.config/gh/hosts.yml directly is the kind of coupling that breaks
// silently.
//
// A token is a credential: it is never logged, printed, or wrapped into an
// error.
func DefaultTokenSource(ctx context.Context, host string) (string, error) {
	if token := ghAuthToken(ctx, host); token != "" {
		return token, nil
	}
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, nil
		}
	}
	return "", errNoToken
}

// ghAuthToken asks the gh CLI for a token, returning "" whenever gh is absent
// or has nothing to give. Not being logged in is not an error here: it is the
// ordinary case where the environment serves the token instead.
func ghAuthToken(ctx context.Context, host string) string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ghTimeout)
		defer cancel()
	}

	args := []string{"auth", "token"}
	if host != "" && host != defaultHost {
		args = append(args, "--hostname", host)
	}

	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
