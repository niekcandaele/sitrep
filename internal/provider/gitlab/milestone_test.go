package gitlab

import (
	"reflect"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestMilestoneStatus(t *testing.T) {
	tests := []struct {
		state      string
		want       model.StatusCategory
		wantNative string
	}{
		{"active", model.StatusTodo, "active"},
		{"closed", model.StatusDone, "closed"},
		{"ACTIVE", model.StatusTodo, "active"},
		// A broken or future instance is visible rather than quietly Todo.
		{"archived", model.StatusUnknown, "archived"},
		{"", model.StatusUnknown, ""},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			got, native := milestoneStatus(tt.state)
			if got != tt.want || native != tt.wantNative {
				t.Errorf("milestoneStatus(%q) = %v, %q, want %v, %q",
					tt.state, got, native, tt.want, tt.wantNative)
			}
		})
	}
}

// A milestone past its due date is late work, not finished work: `expired` never
// reaches a Status Category.
func TestExpiredIsNotAStatus(t *testing.T) {
	m := milestoneWire{IID: 3, Title: "v1.4", State: "active", Expired: true}
	epic := newEpicFromMilestone(m, "gitlab.com", target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3})
	if epic.Status != model.StatusTodo || epic.NativeStatus != "active" {
		t.Errorf("Epic Status = %v / %q, want Todo / active despite expired: true",
			epic.Status, epic.NativeStatus)
	}
}

func TestNewEpicFromMilestone(t *testing.T) {
	tests := []struct {
		name string
		m    milestoneWire
		t    target
		want model.Epic
	}{
		{
			name: "a project milestone with its own web_url",
			m: milestoneWire{
				ID: 6239395, IID: 3, ProjectID: 1, Title: "v1.4",
				State: "active", WebURL: "https://gitlab.com/acme/widgets/-/milestones/3",
				Description: "never read on the polled path",
			},
			t: target{kind: kindProjectMilestone, path: "acme/widgets", iid: 3},
			want: model.Epic{
				ID:           "project-milestone:acme/widgets%3",
				Key:          "acme/widgets%3",
				Title:        "v1.4",
				URL:          "https://gitlab.com/acme/widgets/-/milestones/3",
				Status:       model.StatusTodo,
				NativeStatus: "active",
				Repository:   "acme/widgets",
			},
		},
		{
			name: "a project milestone whose payload carries no web_url",
			m:    milestoneWire{ID: 6239397, IID: 4, Title: "v1.5", State: "active"},
			t:    target{kind: kindProjectMilestone, path: "acme/widgets", iid: 4},
			want: model.Epic{
				ID:           "project-milestone:acme/widgets%4",
				Key:          "acme/widgets%4",
				Title:        "v1.5",
				URL:          "https://gitlab.com/acme/widgets/-/milestones/4",
				Status:       model.StatusTodo,
				NativeStatus: "active",
				Repository:   "acme/widgets",
			},
		},
		{
			name: "a closed group milestone",
			m: milestoneWire{
				ID: 6239400, IID: 3, GroupID: 9970, Title: "19.4", State: "closed",
				WebURL: "https://gitlab.com/groups/acme/-/milestones/3",
			},
			t: target{kind: kindGroupMilestone, path: "acme", iid: 3},
			want: model.Epic{
				ID:           "group-milestone:acme%3",
				Key:          "groups/acme%3",
				Title:        "19.4",
				URL:          "https://gitlab.com/groups/acme/-/milestones/3",
				Status:       model.StatusDone,
				NativeStatus: "closed",
				Repository:   "acme",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newEpicFromMilestone(tt.m, "gitlab.com", tt.t)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newEpicFromMilestone = %+v, want %+v", got, tt.want)
			}
			// A milestone has no assignees, and a placeholder would be a lie.
			if got.Assignees != nil {
				t.Errorf("Assignees = %+v, want nil", got.Assignees)
			}
			// A collection has no merge requests of its own.
			if got.PullRequests != nil {
				t.Errorf("PullRequests = %+v, want nil", got.PullRequests)
			}
		})
	}
}

func TestNewParentFromMilestone(t *testing.T) {
	tests := []struct {
		name string
		m    *issueMilestoneWire
		want model.Parent
	}{
		{
			name: "no milestone at all",
			m:    nil,
			want: model.Parent{},
		},
		{
			name: "a project milestone",
			m: &issueMilestoneWire{
				ID: 6239395, IID: 3, ProjectID: 1, Title: "v1.4", State: "active",
				WebURL: "https://gitlab.com/acme/widgets/-/milestones/3",
			},
			want: model.Parent{
				ID:    "project-milestone:acme/widgets%3",
				Key:   "acme/widgets%3",
				Title: "v1.4",
				URL:   "https://gitlab.com/acme/widgets/-/milestones/3",
			},
		},
		{
			name: "a group milestone",
			m: &issueMilestoneWire{
				ID: 6239400, IID: 3, GroupID: 9970, Title: "19.4", State: "closed",
				WebURL: "https://gitlab.com/groups/acme/platform/-/milestones/3",
			},
			want: model.Parent{
				ID:    "group-milestone:acme/platform%3",
				Key:   "groups/acme/platform%3",
				Title: "19.4",
				URL:   "https://gitlab.com/groups/acme/platform/-/milestones/3",
			},
		},
		{
			// No path can be derived, so the URL carries the walk-up alone —
			// which is the form ref.ParseParent prefers anyway.
			name: "a milestone whose path cannot be named",
			m:    &issueMilestoneWire{ID: 1, IID: 3, GroupID: 9970, Title: "19.4"},
			want: model.Parent{Title: "19.4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newParentFromMilestone(tt.m, "gitlab.com"); got != tt.want {
				t.Errorf("newParentFromMilestone = %+v, want %+v", got, tt.want)
			}
		})
	}
}
