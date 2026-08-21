package gitlab

import (
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/ref"
)

func TestTargetFor(t *testing.T) {
	tests := []struct {
		name        string
		r           ref.Ref
		defaultPath string
		want        target
		wantErr     string
	}{
		{
			name: "a group epic URL",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "gitlab-org", Number: 23356, Key: "gitlab-org&23356",
				Raw: "https://gitlab.com/groups/gitlab-org/-/epics/23356"},
			want: target{kind: kindEpic, path: "gitlab-org", iid: 23356},
		},
		{
			name: "a nested group epic reference",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "platform/core",
				Number: 12, Key: "acme/platform/core&12", Raw: "acme/platform/core&12"},
			want: target{kind: kindEpic, path: "acme/platform/core", iid: 12},
		},
		{
			name:        "a bare reference falls back to the Profile's project",
			r:           ref.Ref{Tracker: ref.TrackerGitLab, Number: 12, Key: "&12", Raw: "&12"},
			defaultPath: "acme/platform",
			want:        target{kind: kindEpic, path: "acme/platform", iid: 12},
		},
		{
			name: "a written group beats the Profile's project",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "other", Number: 9,
				Key: "other&9", Raw: "other&9"},
			defaultPath: "acme/platform",
			want:        target{kind: kindEpic, path: "other", iid: 9},
		},
		{
			name: "a project issue URL",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "widgets", Number: 7,
				Raw: "https://gitlab.com/acme/widgets/-/issues/7"},
			want: target{kind: kindIssue, path: "acme/widgets", iid: 7},
		},
		{
			name: "a nested project issue URL",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "platform/core", Number: 7, Raw: "…"},
			want: target{kind: kindIssue, path: "acme/platform/core", iid: 7},
		},
		{
			name:        "a bare number in a clone falls back to the Profile's project",
			r:           ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com", Number: 7, Raw: "7"},
			defaultPath: "acme/widgets",
			want:        target{kind: kindIssue, path: "acme/widgets", iid: 7},
		},
		{
			name:    "a GitHub Ref",
			r:       ref.Ref{Tracker: ref.TrackerGitHub, Owner: "acme", Repo: "widgets", Number: 7, Raw: "acme/widgets#7"},
			wantErr: "is not a GitLab Epic Ref",
		},
		{
			name:    "no path and no Profile",
			r:       ref.Ref{Tracker: ref.TrackerGitLab, Number: 12, Key: "&12", Raw: "&12"},
			wantErr: "does not name a GitLab group or project",
		},
		{
			name:    "no iid",
			r:       ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "widgets", Raw: "acme/widgets"},
			wantErr: "does not name a GitLab epic or issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := targetFor(tt.r, tt.defaultPath)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("targetFor = %+v, want an error mentioning %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("targetFor: %v", err)
			}
			if got != tt.want {
				t.Errorf("targetFor = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A TicketID is written by one function and read by exactly one other, so the
// round trip is the whole contract.
func TestTicketIDRoundTrip(t *testing.T) {
	targets := []target{
		{kind: kindIssue, path: "acme/widgets", iid: 7},
		{kind: kindIssue, path: "acme/platform/core", iid: 4321},
		{kind: kindEpic, path: "acme", iid: 12},
		{kind: kindEpic, path: "acme/platform", iid: 23356},
	}

	for _, want := range targets {
		t.Run(want.String(), func(t *testing.T) {
			got, err := parseTicketID(want.ticketID())
			if err != nil {
				t.Fatalf("parseTicketID(%q): %v", want.ticketID(), err)
			}
			if got != want {
				t.Errorf("round trip = %+v, want %+v", got, want)
			}
		})
	}
}

func TestTicketIDEncoding(t *testing.T) {
	if got := (target{kind: kindIssue, path: "acme/widgets", iid: 7}).ticketID(); got != "issue:acme/widgets#7" {
		t.Errorf("ticketID = %q, want issue:acme/widgets#7", got)
	}
	if got := (target{kind: kindEpic, path: "acme", iid: 12}).ticketID(); got != "epic:acme&12" {
		t.Errorf("ticketID = %q, want epic:acme&12", got)
	}
}

func TestParseTicketIDRejects(t *testing.T) {
	rejects := []model.TicketID{
		"",
		"   ",
		"acme/widgets#7",
		"issue:acme/widgets",
		"issue:#7",
		"issue:acme/widgets#0",
		"issue:acme/widgets#abc",
		"epic:acme",
		"epic:&12",
		"ABC-12",
		"MDU6SXNzdWUx",
	}
	for _, id := range rejects {
		if got, err := parseTicketID(id); err == nil {
			t.Errorf("parseTicketID(%q) = %+v, want an error", string(id), got)
		}
	}
}

// A namespaced path is escaped whole, which is GitLab's documented addressing.
func TestPathsAreEscapedWhole(t *testing.T) {
	tr := target{kind: kindEpic, path: "acme/platform", iid: 12}
	if got := tr.epicPath(); got != "/groups/acme%2Fplatform/epics/12" {
		t.Errorf("epicPath = %q", got)
	}
	if got := tr.epicIssuesPath(); got != "/groups/acme%2Fplatform/epics/12/issues" {
		t.Errorf("epicIssuesPath = %q", got)
	}
	// The trap: epic notes are addressed by the epic's database id, not its iid.
	if got := tr.epicNotesPath(5270098); got != "/groups/acme%2Fplatform/epics/5270098/notes" {
		t.Errorf("epicNotesPath = %q", got)
	}

	ti := target{kind: kindIssue, path: "acme/widgets", iid: 7}
	if got := ti.issuePath(); got != "/projects/acme%2Fwidgets/issues/7" {
		t.Errorf("issuePath = %q", got)
	}
	if got := ti.issueNotesPath(); got != "/projects/acme%2Fwidgets/issues/7/notes" {
		t.Errorf("issueNotesPath = %q", got)
	}
	if got := ti.issueLinksPath(); got != "/projects/acme%2Fwidgets/issues/7/links" {
		t.Errorf("issueLinksPath = %q", got)
	}
}
