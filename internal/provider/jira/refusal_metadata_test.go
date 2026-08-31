package jira

import (
	"net/http"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestRateLimitRefusalMetadataHeaderPrecedence(t *testing.T) {
	reset := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name   string
		header http.Header
		want   provider.RateLimitMetadata
	}{
		{"retry after wins", http.Header{"Retry-After": {"30"}, "X-Ratelimit-Reset": {reset.Format(time.RFC3339)}}, provider.RateLimitMetadata{RetryAfter: 30 * time.Second}},
		{"rfc3339 reset", http.Header{"X-Ratelimit-Reset": {reset.Format(time.RFC3339)}}, provider.RateLimitMetadata{ResetAt: reset}},
		{"malformed retry-after falls through", http.Header{"Retry-After": {"bad"}, "X-Ratelimit-Reset": {reset.Format(time.RFC3339)}}, provider.RateLimitMetadata{ResetAt: reset}},
		{"malformed is absent", http.Header{"Retry-After": {"0"}, "X-Ratelimit-Reset": {"bad"}}, provider.RateLimitMetadata{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkStatus(&http.Response{StatusCode: http.StatusTooManyRequests, Header: test.header}, "resource", "path", "host")
			got, ok := provider.RateLimitMetadataOf(err)
			if got != test.want || ok != test.want.Valid() {
				t.Fatalf("metadata = %+v, %t; want %+v, %t", got, ok, test.want, test.want.Valid())
			}
		})
	}
}
