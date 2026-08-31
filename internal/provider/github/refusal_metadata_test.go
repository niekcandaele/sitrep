package github

import (
	"net/http"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestRateLimitRefusalsCarryHeaderMetadata(t *testing.T) {
	p := New("github.example.test")
	primary := p.checkStatus(&http.Response{StatusCode: http.StatusForbidden, Header: http.Header{
		"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1893553445"},
	}})
	metadata, ok := provider.RateLimitMetadataOf(primary)
	if !ok || !metadata.ResetAt.Equal(time.Unix(1893553445, 0)) || metadata.RetryAfter != 0 {
		t.Fatalf("primary metadata = %+v, %t", metadata, ok)
	}
	secondary := p.checkStatus(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}})
	metadata, ok = provider.RateLimitMetadataOf(secondary)
	if !ok || metadata.RetryAfter != 30*time.Second || !metadata.ResetAt.IsZero() {
		t.Fatalf("secondary metadata = %+v, %t", metadata, ok)
	}
	graphql := graphQLErrors([]graphQLError{{Type: "RATE_LIMITED"}}, "missing", "endpoint", http.Header{"X-Ratelimit-Reset": {"1893553445"}})
	metadata, ok = provider.RateLimitMetadataOf(graphql)
	if !ok || !metadata.ResetAt.Equal(time.Unix(1893553445, 0)) {
		t.Fatalf("graphql metadata = %+v, %t", metadata, ok)
	}

	// A primary refusal is intentionally timed only by X-RateLimit-Reset. The
	// secondary Retry-After contract does not cross over when remaining=0.
	primaryWithoutReset := p.checkStatus(&http.Response{StatusCode: http.StatusForbidden, Header: http.Header{
		"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"30"},
	}})
	if metadata, ok := provider.RateLimitMetadataOf(primaryWithoutReset); ok {
		t.Fatalf("primary refusal fell back to Retry-After metadata = %+v", metadata)
	}
}
