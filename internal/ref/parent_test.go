package ref_test

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/ref"
)

// A parent is re-parsed rather than looked up again, and it does no I/O: every
// case here resolves from the two strings the Provider already returned plus the
// Ref the child was reached through.
func TestParseParent(t *testing.T) {
	child := ref.Ref{
		Tracker: ref.TrackerGitHub,
		Host:    "github.com",
		Owner:   "acme",
		Repo:    "widgets",
		Number:  112,
		Raw:     "112",
	}
	enterprise := ref.Ref{
		Tracker: ref.TrackerGitHub,
		Host:    "ghe.acme.test",
		Owner:   "acme",
		Repo:    "widgets",
		Number:  112,
		Raw:     "https://ghe.acme.test/acme/widgets/issues/112",
	}

	jiraChild := ref.Ref{
		Tracker: ref.TrackerJira,
		Host:    "acme.atlassian.net",
		Key:     "ABC-12",
		Raw:     "ABC-12",
	}

	tests := []struct {
		name    string
		key     string
		url     string
		child   ref.Ref
		wantErr bool
		want    ref.Ref
	}{
		{
			name:  "a bare key is the child's own repository",
			key:   "#111",
			child: child,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name:  "a qualified key names its own repository",
			key:   "acme/widgets#111",
			child: child,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name:  "a cross-repo parent",
			key:   "other/repo#4",
			child: child,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "other", Repo: "repo", Number: 4},
		},
		{
			name:  "a full URL",
			key:   "#111",
			url:   "https://github.com/acme/widgets/issues/111",
			child: child,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			// The hole parseOwnerRepoNumber leaves: it hard-codes github.com, so a
			// walk-up from an Enterprise child has to inherit the host.
			name:  "an Enterprise child keeps its host",
			key:   "acme/widgets#111",
			child: enterprise,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "ghe.acme.test", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name:  "an Enterprise URL carries its own host",
			url:   "https://ghe.acme.test/acme/widgets/issues/111",
			child: enterprise,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "ghe.acme.test", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			// A URL carries its own host and repository, so it wins over a Key
			// this grammar cannot read.
			name:  "a good URL beats a garbage key",
			key:   "PROJ-111",
			url:   "https://github.com/acme/widgets/issues/111",
			child: child,
			want:  ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			// A Jira parent carries a key and no number, which is the whole reason
			// parentFromURL cannot demand one.
			name:  "a Jira parent resolves from its browse URL",
			key:   "ABC-1",
			url:   "https://acme.atlassian.net/browse/ABC-1",
			child: jiraChild,
			want:  ref.Ref{Tracker: ref.TrackerJira, Host: "acme.atlassian.net", Key: "ABC-1"},
		},
		{
			name:  "a Jira parent resolves from its key alone, inheriting the site",
			key:   "ABC-1",
			child: jiraChild,
			want:  ref.Ref{Tracker: ref.TrackerJira, Host: "acme.atlassian.net", Key: "ABC-1"},
		},
		{
			name:  "a lower-cased Jira parent key is the same key",
			key:   "abc-1",
			child: jiraChild,
			want:  ref.Ref{Tracker: ref.TrackerJira, Host: "acme.atlassian.net", Key: "ABC-1"},
		},
		{
			name:    "nothing usable at all",
			child:   child,
			wantErr: true,
		},
		{
			name:    "a key this grammar cannot read and no URL",
			key:     "PROJ-111",
			child:   child,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ref.ParseParent(tt.key, tt.url, tt.child)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseParent = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParent: %v", err)
			}
			// Raw is whatever text was read; the resolved fields are the contract.
			got.Raw = ""
			if got != tt.want {
				t.Errorf("ParseParent = %+v, want %+v", got, tt.want)
			}
		})
	}
}
