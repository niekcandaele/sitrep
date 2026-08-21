package provider_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestKindRetryable(t *testing.T) {
	tests := []struct {
		kind provider.Kind
		want bool
	}{
		// An unclassified failure stays retryable on purpose: a driver that
		// forgets to classify something must not make the monitor give up on a
		// situation that would have recovered.
		{provider.KindUnknown, true},
		{provider.KindBadRef, false},
		{provider.KindAuth, false},
		{provider.KindRateLimit, true},
		{provider.KindUnavailable, true},
	}

	for _, tt := range tests {
		t.Run(tt.kind.String(), func(t *testing.T) {
			if got := tt.kind.Retryable(); got != tt.want {
				t.Errorf("Kind(%s).Retryable() = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	tests := map[provider.Kind]string{
		provider.KindUnknown:     "unknown",
		provider.KindBadRef:      "bad ref",
		provider.KindAuth:        "auth",
		provider.KindRateLimit:   "rate limit",
		provider.KindUnavailable: "unavailable",
		provider.Kind(200):       "unknown",
	}

	for kind, want := range tests {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", uint8(kind), got, want)
		}
	}
}

// Classifying a call site must not change one byte of what the user reads.
func TestErrorfKeepsTheDriversProse(t *testing.T) {
	const want = `github: authentication failed (401) — check "gh auth status" or GITHUB_TOKEN`

	err := provider.Errorf(provider.KindAuth,
		`github: authentication failed (%d) — check %q or GITHUB_TOKEN`, 401, "gh auth status")
	if got := err.Error(); got != want {
		t.Errorf("Errorf message = %q, want %q", got, want)
	}
}

// Errorf is fmt.Errorf with a label, so %w still wraps: a cancelled fetch has
// to stay recognisable as a cancellation through a *url.Error and a
// classification both.
func TestErrorfPreservesWrapping(t *testing.T) {
	wrapped := &url.Error{Op: "Post", URL: "https://api.github.com/graphql", Err: context.Canceled}
	err := provider.Errorf(provider.KindUnavailable, "github: requesting %s: %w", "the API", wrapped)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(%v, context.Canceled) = false, want true", err)
	}
	var target *url.Error
	if !errors.As(err, &target) {
		t.Fatalf("errors.As(%v, *url.Error) = false, want true", err)
	}
	if target.Op != "Post" {
		t.Errorf("unwrapped *url.Error.Op = %q, want %q", target.Op, "Post")
	}
}

func TestKindOf(t *testing.T) {
	classified := provider.Errorf(provider.KindRateLimit, "github: API rate limit exceeded")

	tests := []struct {
		name string
		err  error
		want provider.Kind
	}{
		{"nil", nil, provider.KindUnknown},
		{"an unclassified error", errors.New("boom"), provider.KindUnknown},
		{"a classified error", classified, provider.KindRateLimit},
		{"a classified error wrapped again", fmt.Errorf("while polling: %w", classified), provider.KindRateLimit},
		{
			"a classified error two wraps deep",
			fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", classified)),
			provider.KindRateLimit,
		},
		{
			"the outermost classification wins",
			provider.Errorf(provider.KindAuth, "jira: %w", classified),
			provider.KindAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.KindOf(tt.err); got != tt.want {
				t.Errorf("KindOf(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

// The Kind rides along; the message stays the driver's own.
func TestErrorUnwrapsToTheDriversError(t *testing.T) {
	inner := errors.New("gitlab: 404 Group Not Found")
	err := provider.Errorf(provider.KindBadRef, "%w", inner)

	if !errors.Is(err, inner) {
		t.Errorf("errors.Is(%v, inner) = false, want true", err)
	}
	if got := err.Error(); got != inner.Error() {
		t.Errorf("message = %q, want %q", got, inner.Error())
	}
}
