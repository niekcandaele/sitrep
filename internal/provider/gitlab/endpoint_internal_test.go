package gitlab

import (
	"strings"
	"testing"
)

// A self-managed instance may listen on a non-default port, which the Ref now
// carries through as part of the host. The instance URL has to keep it.
func TestNewKeepsAPortInTheInstanceURL(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "gitlab.com", want: "https://gitlab.com"},
		{host: "git.acme.test", want: "https://git.acme.test"},
		{host: "git.acme.test:8443", want: "https://git.acme.test:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			p := New(tt.host)
			if p.baseURL != tt.want {
				t.Errorf("New(%q).baseURL = %q, want %q", tt.host, p.baseURL, tt.want)
			}
			if !strings.HasPrefix(p.baseURL, "https://") {
				t.Errorf("New(%q).baseURL = %q, want an https URL", tt.host, p.baseURL)
			}
		})
	}
}
