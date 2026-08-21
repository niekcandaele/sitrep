package gitlab

import (
	"context"
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
			name: "a project milestone URL",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "widgets", Number: 3, Key: "acme/widgets%3",
				Raw: "https://gitlab.com/acme/widgets/-/milestones/3"},
			want: target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3},
		},
		{
			name: "a group milestone URL",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Number: 3, Key: "groups/acme%3",
				Raw: "https://gitlab.com/groups/acme/-/milestones/3"},
			want: target{kind: kindGroupMilestone, path: "acme", iid: 3},
		},
		{
			name: "a nested group milestone reference",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "platform/core",
				Number: 3, Key: "groups/acme/platform/core%3", Raw: "groups/acme/platform/core%3"},
			want: target{kind: kindGroupMilestone, path: "acme/platform/core", iid: 3},
		},
		{
			// GitLab's own "%" syntax is project-scoped, so a bare reference means
			// a project milestone in the Profile's project.
			name:        "a bare milestone reference falls back to the Profile's project",
			r:           ref.Ref{Tracker: ref.TrackerGitLab, Number: 3, Key: "%3", Raw: "%3"},
			defaultPath: "acme/widgets",
			want:        target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3},
		},
		{
			name:    "a bare milestone reference with no Profile",
			r:       ref.Ref{Tracker: ref.TrackerGitLab, Number: 3, Key: "%3", Raw: "%3"},
			wantErr: "acme/widgets%3",
		},
		{
			name: "a milestone iid of zero",
			r: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "widgets",
				Key: "acme/widgets%0", Raw: "acme/widgets%0"},
			wantErr: "does not name a GitLab milestone",
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
		{kind: kindProjectMilestone, path: "acme/widgets", iid: 3},
		{kind: kindGroupMilestone, path: "acme", iid: 3},
		{kind: kindGroupMilestone, path: "acme/platform/core", iid: 3},
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
	// The number a milestone id carries is the iid, not the API id: it is what
	// every human-facing form spells, and FetchDetail re-resolves the rest.
	if got := (target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3}).ticketID(); got != "project-milestone:acme/widgets%3" {
		t.Errorf("ticketID = %q, want project-milestone:acme/widgets%%3", got)
	}
	if got := (target{kind: kindGroupMilestone, path: "acme", iid: 3}).ticketID(); got != "group-milestone:acme%3" {
		t.Errorf("ticketID = %q, want group-milestone:acme%%3", got)
	}
}

// A milestone Epic's Key is exactly the reference form internal/ref reads back,
// which is what makes it typeable — and what #11's walk-up relies on.
func TestMilestoneStringIsAReferenceRefAccepts(t *testing.T) {
	targets := []target{
		{kind: kindProjectMilestone, path: "acme/widgets", iid: 3},
		{kind: kindProjectMilestone, path: "acme/platform/core", iid: 3},
		{kind: kindGroupMilestone, path: "acme", iid: 3},
		{kind: kindGroupMilestone, path: "acme/platform", iid: 3},
	}

	for _, want := range targets {
		t.Run(want.String(), func(t *testing.T) {
			r, err := ref.Parse(context.Background(), want.String())
			if err != nil {
				t.Fatalf("ref.Parse(%q): %v", want.String(), err)
			}
			got, err := targetFor(r, "")
			if err != nil {
				t.Fatalf("targetFor: %v", err)
			}
			if got != want {
				t.Errorf("round trip = %+v, want %+v", got, want)
			}
		})
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
	if got := ti.closedByPath(); got != "/projects/acme%2Fwidgets/issues/7/closed_by" {
		t.Errorf("closedByPath = %q", got)
	}
	if got := ti.mergeRequestApprovalsPath(3761); got != "/projects/acme%2Fwidgets/merge_requests/3761/approvals" {
		t.Errorf("mergeRequestApprovalsPath = %q", got)
	}

	mp := target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3}
	if got := mp.milestonesPath(); got != "/projects/acme%2Fwidgets/milestones" {
		t.Errorf("milestonesPath = %q", got)
	}
	// The trap: milestone issues are addressed by the milestone's database id.
	if got := mp.milestoneIssuesPath(6239395); got != "/projects/acme%2Fwidgets/milestones/6239395/issues" {
		t.Errorf("milestoneIssuesPath = %q", got)
	}
	if got := mp.milestoneLookupQuery().Encode(); got != "iids%5B%5D=3&per_page=2" {
		t.Errorf("milestoneLookupQuery = %q, want GitLab's own iids[] parameter", got)
	}

	mg := target{kind: kindGroupMilestone, path: "acme/platform", iid: 3}
	if got := mg.milestonesPath(); got != "/groups/acme%2Fplatform/milestones" {
		t.Errorf("milestonesPath = %q", got)
	}
}

// The web URL a milestone Epic falls back to when GitLab's payload carries none.
func TestMilestoneWebURL(t *testing.T) {
	project := target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3}
	if got := milestoneWebURL("gitlab.com", project); got != "https://gitlab.com/acme/widgets/-/milestones/3" {
		t.Errorf("milestoneWebURL = %q", got)
	}
	group := target{kind: kindGroupMilestone, path: "acme", iid: 3}
	if got := milestoneWebURL("gitlab.com", group); got != "https://gitlab.com/groups/acme/-/milestones/3" {
		t.Errorf("milestoneWebURL = %q", got)
	}
	if got := milestoneWebURL("", project); got != "" {
		t.Errorf("milestoneWebURL with no host = %q, want empty", got)
	}
}
