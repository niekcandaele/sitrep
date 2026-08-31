package gitlab

import (
	"net/http"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/provider"
)

func TestRateLimitRefusalMetadataHeaderPrecedence(t *testing.T) {
	for _, test := range []struct {
		name   string
		header http.Header
		want   provider.RateLimitMetadata
	}{
		{"retry after wins", http.Header{"Retry-After": {"30"}, "Ratelimit-Reset": {"1893553445"}}, provider.RateLimitMetadata{RetryAfter: 30 * time.Second}},
		{"unix reset", http.Header{"Ratelimit-Reset": {"1893553445"}}, provider.RateLimitMetadata{ResetAt: time.Unix(1893553445, 0)}},
		{"http date fallback", http.Header{"Ratelimit-Resettime": {"Tue, 02 Jan 2030 03:04:05 GMT"}}, provider.RateLimitMetadata{ResetAt: time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)}},
		{"malformed retry-after falls through", http.Header{"Retry-After": {"bad"}, "Ratelimit-Reset": {"1893553445"}}, provider.RateLimitMetadata{ResetAt: time.Unix(1893553445, 0)}},
		{"malformed is absent", http.Header{"Retry-After": {"0"}, "Ratelimit-Reset": {"bad"}}, provider.RateLimitMetadata{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := statusMessage(&http.Response{StatusCode: http.StatusTooManyRequests, Header: test.header}, "resource", "path", "host")
			got, ok := provider.RateLimitMetadataOf(err)
			if got != test.want || ok != test.want.Valid() {
				t.Fatalf("metadata = %+v, %t; want %+v, %t", got, ok, test.want, test.want.Valid())
			}
		})
	}
}
