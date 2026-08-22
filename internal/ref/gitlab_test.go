package ref_test

import (
	"context"
	"testing"

	"github.com/niekcandaele/sitrep/internal/ref"
)

// The GitLab URL shapes, on gitlab.com and on a self-managed host. The
// work_items forms are not a nicety: they are what GitLab's API returns as
// web_url today, and therefore what a user copies out of the address bar.
func TestParseGitLabURLShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want ref.Ref
	}{
		{
			name: "a project issue on gitlab.com",
			raw:  "https://gitlab.com/acme/widgets/-/issues/7",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "widgets", Number: 7},
		},
		{
			name: "the same issue in the work_items form",
			raw:  "https://gitlab.com/acme/widgets/-/work_items/7",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "widgets", Number: 7},
		},
		{
			name: "a nested project namespace",
			raw:  "https://gitlab.com/acme/platform/core/-/issues/7",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "platform/core", Number: 7},
		},
		{
			name: "a project issue on a self-managed host",
			raw:  "https://git.acme.test/acme/widgets/-/issues/7",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "widgets", Number: 7},
		},
		{
			name: "a group epic",
			raw:  "https://gitlab.com/groups/acme/-/epics/12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Number: 12, Key: "acme&12"},
		},
		{
			name: "a nested group epic",
			raw:  "https://gitlab.com/groups/acme/platform/-/epics/12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "platform", Number: 12, Key: "acme/platform&12"},
		},
		{
			name: "a group epic in the work_items form",
			raw:  "https://gitlab.com/groups/acme/-/work_items/12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Number: 12, Key: "acme&12"},
		},
		{
			name: "a group epic on a self-managed host",
			raw:  "https://git.acme.test/groups/acme/platform/-/epics/12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "platform", Number: 12, Key: "acme/platform&12"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			tt.want.Raw = tt.raw
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// GitLab's own reference form is what makes the Profile path typeable: it is
// exactly what the API prints as references.full.
func TestParseGitLabReferenceForm(t *testing.T) {
	tests := []struct {
		raw  string
		want ref.Ref
	}{
		{
			raw:  "&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Number: 12, Key: "&12"},
		},
		{
			raw:  "acme&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Number: 12, Key: "acme&12"},
		},
		{
			raw: "acme/platform&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "platform",
				Number: 12, Key: "acme/platform&12"},
		},
		{
			raw:  "gitlab-org&23356",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "gitlab-org", Number: 23356, Key: "gitlab-org&23356"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			tt.want.Raw = tt.raw
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
			// A reference names a group, not an instance: only a Profile knows
			// which GitLab that is.
			if got.Host != "" {
				t.Errorf("Host = %q, want empty: a reference names no instance", got.Host)
			}
		})
	}
}

// The milestone URL shapes. A milestone is how GitLab Free spells an Epic, so
// these are Refs like any other; "/-/" recognition already carries them to
// a self-managed host.
func TestParseGitLabMilestoneURLShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want ref.Ref
	}{
		{
			name: "a project milestone on gitlab.com",
			raw:  "https://gitlab.com/acme/widgets/-/milestones/3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "widgets", Number: 3, Key: "acme/widgets%3"},
		},
		{
			name: "a nested project namespace",
			raw:  "https://gitlab.com/acme/platform/core/-/milestones/3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Repo: "platform/core", Number: 3, Key: "acme/platform/core%3"},
		},
		{
			name: "a project milestone on a self-managed host",
			raw:  "https://git.acme.test/acme/widgets/-/milestones/3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "widgets", Number: 3, Key: "acme/widgets%3"},
		},
		{
			name: "a group milestone",
			raw:  "https://gitlab.com/groups/acme/-/milestones/3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "gitlab.com",
				Owner: "acme", Number: 3, Key: "groups/acme%3"},
		},
		{
			name: "a nested group milestone on a self-managed host",
			raw:  "https://git.acme.test/groups/acme/platform/-/milestones/3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "platform", Number: 3, Key: "groups/acme/platform%3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			tt.want.Raw = tt.raw
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// GitLab's own milestone reference form, plus sitrep's "groups/" spelling for
// the group scope.
func TestParseGitLabMilestoneReferenceForm(t *testing.T) {
	tests := []struct {
		raw  string
		want ref.Ref
	}{
		{
			raw:  "%3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Number: 3, Key: "%3"},
		},
		{
			raw: "acme/widgets%3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "widgets",
				Number: 3, Key: "acme/widgets%3"},
		},
		{
			// The form parseOwnerRepoNumber would otherwise turn into a hard
			// "cannot parse", which is why the order in Parse is load-bearing.
			raw: "acme/platform/core%3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "platform/core",
				Number: 3, Key: "acme/platform/core%3"},
		},
		{
			raw:  "groups/acme%3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Number: 3, Key: "groups/acme%3"},
		},
		{
			raw: "groups/acme/platform%3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Owner: "acme", Repo: "platform",
				Number: 3, Key: "groups/acme/platform%3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			tt.want.Raw = tt.raw
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
			if got.Host != "" {
				t.Errorf("Host = %q, want empty: a reference names no instance", got.Host)
			}
		})
	}
}

func TestParseRejectsMalformedGitLabMilestoneReferences(t *testing.T) {
	// "groups/%3" and "groups/" name no group: accepting either would resolve
	// silently against the Profile's default project, which is a different
	// milestone than the one asked for.
	for _, raw := range []string{"%", "%0", "%abc", "acme%", "acme%x", "a%b%3", "groups/%3", "groups/"} {
		t.Run(raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), raw,
				ref.WithRemoteLookup(stubLookup("git@github.com:acme/widgets.git", nil)))
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", raw, got)
			}
		})
	}
}

// A GitLab reference must never be mistaken for a Jira key by config.Select,
// which routes on exactly this function.
func TestKeyPrefixIgnoresGitLabReferences(t *testing.T) {
	for _, key := range []string{
		"&12", "acme&12", "acme/platform&12", "gitlab-org&23356", "a-1&12",
		// The milestone forms, including a path carrying a hyphen — which is the
		// shape a Jira key has and the one a naive prefix reader would claim.
		"%3", "acme/widgets%3", "groups/acme%3", "groups/acme-corp%3", "acme-corp/widgets%3",
	} {
		if got := ref.KeyPrefix(key); got != "" {
			t.Errorf("KeyPrefix(%q) = %q, want empty", key, got)
		}
	}
}

func TestParseRejectsMalformedGitLabReferences(t *testing.T) {
	for _, raw := range []string{"&", "&0", "&abc", "acme&", "acme&x", "a&b&12"} {
		t.Run(raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), raw,
				ref.WithRemoteLookup(stubLookup("git@github.com:acme/widgets.git", nil)))
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", raw, got)
			}
		})
	}
}

// The walk-up from a GitLab child issue into its epic, from the URL the issue
// payload carries and from the reference alone.
func TestParseParentOnGitLab(t *testing.T) {
	child := ref.Ref{
		Tracker: ref.TrackerGitLab,
		Host:    "git.acme.test",
		Owner:   "acme",
		Repo:    "widgets",
		Number:  7,
		Raw:     "https://git.acme.test/acme/widgets/-/issues/7",
	}

	tests := []struct {
		name string
		key  string
		url  string
		want ref.Ref
	}{
		{
			name: "from the epic URL",
			key:  "acme/platform&12",
			url:  "https://git.acme.test/groups/acme/platform/-/epics/12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "platform", Number: 12, Key: "acme/platform&12"},
		},
		{
			name: "from the reference alone, inheriting the instance",
			key:  "acme/platform&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "platform", Number: 12, Key: "acme/platform&12"},
		},
		{
			name: "from an unqualified reference",
			key:  "&12",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test", Number: 12, Key: "&12"},
		},
		{
			// #11's walk-up from a child issue to the milestone that is its Epic
			// on a Free instance.
			name: "from the milestone URL",
			key:  "acme/widgets%3",
			url:  "https://git.acme.test/acme/widgets/-/milestones/3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "widgets", Number: 3, Key: "acme/widgets%3"},
		},
		{
			name: "from a group milestone reference alone, inheriting the instance",
			key:  "groups/acme/platform%3",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test",
				Owner: "acme", Repo: "platform", Number: 3, Key: "groups/acme/platform%3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ref.ParseParent(tt.key, tt.url, child)
			if err != nil {
				t.Fatalf("ParseParent: %v", err)
			}
			got.Raw = ""
			if got != tt.want {
				t.Errorf("ParseParent = %+v, want %+v", got, tt.want)
			}
		})
	}
}
