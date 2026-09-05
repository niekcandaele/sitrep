package provider_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestRedactedTransportErrorHidesURLAndPreservesCause(t *testing.T) {
	const secretURL = "https://user:token@example.test/issues?jql=secret-query"
	wrapped := &url.Error{Op: "Get", URL: secretURL, Err: context.Canceled}
	err := provider.Errorf(provider.KindUnavailable, "jira: requesting search: %w",
		provider.RedactedTransportError(wrapped))

	if strings.Contains(err.Error(), secretURL) || strings.Contains(err.Error(), "secret-query") {
		t.Errorf("error = %q, want credential- and Query-free transport prose", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(%v, context.Canceled) = false, want true", err)
	}
}

func TestRedactQueryMasksEquivalentRepresentations(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		message    string
		want       []string
		absent     []string
		unchanged  bool
		wantMarker bool
	}{
		{
			name: "exact raw", query: "SECRET_QUERY_47",
			message: "Query SECRET_QUERY_47 is invalid: bad syntax",
			want:    []string{"bad syntax"}, absent: []string{"SECRET_QUERY_47"}, wantMarker: true,
		},
		{
			name: "percent hex case", query: "search=needs%2ftriage",
			message: "search=needs%2Ftriage is invalid",
			absent:  []string{"needs%2ftriage", "needs%2Ftriage", "needs/triage"}, wantMarker: true,
		},
		{
			name: "opaque percent hex case", query: "state%3Aopen",
			message: "state%3aopen is invalid",
			absent:  []string{"state%3Aopen", "state%3aopen", "state:open"}, wantMarker: true,
		},
		{
			name: "space spellings", query: "search=needs%20triage",
			message: "search=needs+triage and search=needs triage are invalid",
			absent:  []string{"needs%20triage", "needs+triage", "needs triage"}, wantMarker: true,
		},
		{
			name: "decoded slash and non ASCII", query: "search=Caf%C3%A9%20%2Fready",
			message: "search: Café /ready is invalid",
			absent:  []string{"Caf%C3%A9%20%2Fready", "Café /ready"}, wantMarker: true,
		},
		{
			name: "canonical component order", query: "z=needs%20triage&a=bug%2fui",
			message: "normalized as a=bug%2Fui&z=needs+triage before rejection",
			absent:  []string{"z=needs%20triage&a=bug%2fui", "a=bug%2Fui&z=needs+triage"}, wantMarker: true,
		},
		{
			name: "repeated parameters", query: "label=needs%20triage&label=bug%2fui",
			message: "labels label=needs+triage&label=bug%2Fui are invalid",
			absent:  []string{"label=needs+triage&label=bug%2Fui", "needs triage", "bug/ui"}, wantMarker: true,
		},
		{
			name: "field labelled decoded value", query: "jql=project%20%3D%20ABC",
			message: "jql: project = ABC is not valid JQL",
			want:    []string{"not valid JQL"}, absent: []string{"project%20%3D%20ABC", "project = ABC"}, wantMarker: true,
		},
		{
			name: "no match", query: "search=private-value",
			message:   "tracker service unavailable",
			unchanged: true,
		},
		{
			name: "empty query", query: "",
			message:   "tracker service unavailable",
			unchanged: true,
		},
		{
			name: "malformed percent keeps exact masking", query: "q=bad%2",
			message: "Query q=bad%2 was rejected",
			absent:  []string{"q=bad%2"}, wantMarker: true,
		},
		{
			name: "short value preserves status and guidance", query: "q=1",
			message: "authentication failed (401); retry after 60s; q: 1 was rejected",
			want:    []string{"401", "60s", "retry after"}, absent: []string{"q: 1"}, wantMarker: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.RedactQuery(tt.message, tt.query)
			if tt.unchanged && got != tt.message {
				t.Fatalf("RedactQuery = %q, want original %q", got, tt.message)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("RedactQuery = %q, want useful prose %q", got, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Errorf("RedactQuery = %q, leaked sensitive form %q", got, absent)
				}
			}
			if tt.wantMarker && !strings.Contains(got, "[query]") {
				t.Errorf("RedactQuery = %q, want replacement marker", got)
			}
		})
	}
}

func TestRedactQueryErrorPreservesKindAndCause(t *testing.T) {
	const query = "search=needs%20triage"
	cause := errors.New("server failed")
	original := provider.Errorf(provider.KindUnavailable,
		"gitlab: search: needs triage and search=needs+triage were rejected: %w", cause)
	err := provider.RedactQueryError(original, query)

	for _, sensitive := range []string{query, "needs triage", "needs+triage"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Errorf("error = %q, leaked Query form %q", err, sensitive)
		}
	}
	if !strings.Contains(err.Error(), "[query]") || !strings.Contains(err.Error(), "gitlab:") {
		t.Errorf("error = %q, want redacted Query and Provider context", err)
	}
	if provider.KindOf(err) != provider.KindUnavailable {
		t.Errorf("KindOf(error) = %s, want unavailable", provider.KindOf(err))
	}
	if !errors.Is(err, cause) || !errors.Is(err, original) {
		t.Errorf("errors.Is did not preserve the complete original chain: %v", err)
	}
}

func TestRedactQueryErrorReturnsOriginalWhenNothingChanges(t *testing.T) {
	original := provider.Errorf(provider.KindAuth, "github: authentication failed (401) — retry after 60s")
	if got := provider.RedactQueryError(original, "q=1"); !errors.Is(got, original) {
		t.Errorf("RedactQueryError returned %T %v, want original error", got, got)
	}
}

func TestRateLimitMetadataSurvivesWrappingAndRedaction(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	original := provider.RateLimitErrorf(provider.RateLimitMetadata{ResetAt: reset},
		"github: query q=secret was rate limited")
	redacted := provider.RedactQueryError(fmt.Errorf("while polling: %w", original), "q=secret")

	metadata, ok := provider.RateLimitMetadataOf(redacted)
	if !ok || !metadata.ResetAt.Equal(reset) || metadata.RetryAfter != 0 {
		t.Fatalf("RateLimitMetadataOf(redacted) = %+v, %t; want reset %s", metadata, ok, reset)
	}
	if provider.KindOf(redacted) != provider.KindRateLimit || !errors.Is(redacted, original) {
		t.Fatalf("redaction lost classification or wrapping: %v", redacted)
	}
}

func TestRateLimitMetadataResolvesRelativeTimeWithCallerClock(t *testing.T) {
	at := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	metadata := provider.RateLimitMetadata{RetryAfter: 45 * time.Second}
	if got, ok := metadata.Deadline(at); !ok || !got.Equal(at.Add(45*time.Second)) {
		t.Fatalf("Deadline = %s, %t", got, ok)
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
