package model_test

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The rule the TUI's Detail screen, both one-shot renderers and the fake
// Provider all ask for: without BlockingLinks a Tracker has nothing to say
// about direction, but its ordinary relationships are still real.
func TestVisibleLinks(t *testing.T) {
	relates := model.Link{Kind: model.LinkRelates, Target: model.LinkTarget{Key: "#1"}}
	blockedBy := model.Link{Kind: model.LinkBlockedBy, Target: model.LinkTarget{Key: "#2"}}
	blocks := model.Link{Kind: model.LinkBlocks, Target: model.LinkTarget{Key: "#3"}}

	tests := []struct {
		name  string
		links []model.Link
		caps  model.Capabilities
		want  []string
	}{
		{
			name:  "with the capability every link survives, in order",
			links: []model.Link{blockedBy, relates, blocks},
			caps:  model.Capabilities{BlockingLinks: true},
			want:  []string{"#2", "#1", "#3"},
		},
		{
			name:  "without it only relates survives, in order",
			links: []model.Link{blockedBy, relates, blocks},
			want:  []string{"#1"},
		},
		{
			name:  "nothing to keep is nil, not an empty slice",
			links: []model.Link{blockedBy, blocks},
		},
		{name: "no links at all"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := model.VisibleLinks(tt.links, tt.caps)

			if len(tt.want) == 0 {
				if got != nil {
					t.Fatalf("VisibleLinks = %+v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("VisibleLinks = %+v, want %v", got, tt.want)
			}
			for i, key := range tt.want {
				if got[i].Target.Key != key {
					t.Errorf("VisibleLinks[%d] = %q, want %q", i, got[i].Target.Key, key)
				}
			}
		})
	}
}
