package jira

import "testing"

// A Ref now carries an explicit port through as part of the host, so the site
// URL has to keep it.
func TestNewKeepsAPortInTheSiteURL(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "acme.atlassian.net", want: "https://acme.atlassian.net"},
		{host: "jira.acme.test:8443", want: "https://jira.acme.test:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := New(tt.host).baseURL; got != tt.want {
				t.Errorf("New(%q).baseURL = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
