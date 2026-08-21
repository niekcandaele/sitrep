package github

import "testing"

// A self-hosted instance may listen on a non-default port, which the Ref now
// carries through as part of the host. The endpoint has to keep it.
func TestEndpointFor(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "github.com", want: "https://api.github.com/graphql"},
		{host: "ghe.example", want: "https://ghe.example/api/graphql"},
		{host: "ghe.example:8443", want: "https://ghe.example:8443/api/graphql"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := endpointFor(tt.host); got != tt.want {
				t.Errorf("endpointFor(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
