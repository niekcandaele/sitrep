package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ghTimeout bounds the `gh auth token` call. Reading a stored token is a local
// file read behind a CLI; five seconds is generous.
const ghTimeout = 5 * time.Second

// errNoToken is what a Provider reports when no token can be found for
// github.com. It names both fixes because a user who has neither usually does
// not know which one they want.
var errNoToken = errors.New(`no GitHub token found: run "gh auth login" or set GITHUB_TOKEN`)

// TokenSource returns a GitHub API token for the given host. It is a function
// rather than an interface on purpose: a test injects one line and no
// production code learns that it is being tested.
type TokenSource func(ctx context.Context, host string) (string, error)

// getenv is the environment reader DefaultTokenSource uses. It is a package
// variable rather than a direct os.Getenv call so that the token chain can be
// table-tested without any test reading — or mutating — the process
// environment.
var getenv = os.Getenv

// tokenEnvNames are the variables gh documents as carrying an ambient GitHub
// token, in gh's own order.
var tokenEnvNames = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// DefaultTokenSource resolves a token the way the user's own tooling does:
// `gh auth token` first, then GITHUB_TOKEN, then GH_TOKEN. It never reads
// gh's own state file — `gh auth token` is the documented interface, and
// reading ~/.config/gh/hosts.yml directly is the kind of coupling that breaks
// silently.
//
// A credential may only be sent to a host it was configured for. `gh auth
// token` is inherently host-scoped: gh refuses to print a token for a host it
// is not logged in to. The ambient variables are not — they carry no host at
// all — so they are only offered to github.com, or to the host $GH_HOST names,
// which is gh's own way of pointing an ambient token at an Enterprise
// instance. Any other host reaching for an ambient token would be sitrep
// handing a github.com credential to whoever named that host.
//
// A token is a credential: it is never logged, printed, or wrapped into an
// error.
func DefaultTokenSource(ctx context.Context, host string) (string, error) {
	if token := ghAuthToken(ctx, host); token != "" {
		return token, nil
	}
	if !ambientTokenBelongsTo(host) {
		return "", fmt.Errorf("no GitHub token for %s: sitrep only sends $GITHUB_TOKEN to %s — "+
			`run "gh auth login --hostname %s", or add a profile naming that host and its token variable`,
			host, defaultHost, host)
	}
	for _, name := range tokenEnvNames {
		if token := strings.TrimSpace(getenv(name)); token != "" {
			return token, nil
		}
	}
	return "", errNoToken
}

// ambientTokenBelongsTo reports whether $GITHUB_TOKEN/$GH_TOKEN can be assumed
// to be a credential for host. An empty host is the caller not having said, in
// which case the driver is talking to github.com.
func ambientTokenBelongsTo(host string) bool {
	switch {
	case host == "", strings.EqualFold(host, defaultHost):
		return true
	default:
		ghHost := strings.TrimSpace(getenv("GH_HOST"))
		return ghHost != "" && strings.EqualFold(host, ghHost)
	}
}

// ghAuthToken asks the gh CLI for a token, returning "" whenever gh is absent
// or has nothing to give. Not being logged in is not an error here: it is the
// ordinary case where the environment serves the token instead.
//
// It is a package variable so that a test can exercise the precedence chain
// without depending on whether gh is installed on the machine running it.
var ghAuthToken = func(ctx context.Context, host string) string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ghTimeout)
		defer cancel()
	}

	out, err := exec.CommandContext(ctx, "gh", ghAuthTokenArgs(host)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ghAuthTokenArgs builds the `gh auth token` argument list. --hostname is
// omitted for github.com because gh's default is already that host.
func ghAuthTokenArgs(host string) []string {
	args := []string{"auth", "token"}
	if host != "" && host != defaultHost {
		args = append(args, "--hostname", host)
	}
	return args
}
