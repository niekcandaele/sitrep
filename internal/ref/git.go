package ref

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds the remote lookup when the caller's context has no deadline
// of its own. Reading a remote URL is a local config read; if git has not
// answered in five seconds something is wrong with the environment, not with
// the repository.
const gitTimeout = 5 * time.Second

// gitRemoteLookup is the production RemoteLookup: it asks git itself rather
// than reading .git/config. Shelling out is what makes subdirectories,
// worktrees, submodules and insteadOf rewrites work for free, and git is
// present wherever a clone is.
func gitRemoteLookup(ctx context.Context, dir, remote string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is not on PATH: %w", err)
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gitTimeout)
		defer cancel()
	}

	args := []string{}
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, "remote", "get-url", remote)

	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git remote get-url %s: %s", remote, msg)
		}
		return "", fmt.Errorf("git remote get-url %s: %w", remote, err)
	}

	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", errors.New("git remote get-url " + remote + " returned nothing")
	}
	return url, nil
}
