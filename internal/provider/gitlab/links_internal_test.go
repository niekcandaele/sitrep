package gitlab

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// One row per documented link_type, on both the Kind and the native label the
// renderer actually shows. link_type is expressed from the queried issue's
// perspective, which is model.Link's own direction: getting this backwards
// inverts every dependency on screen.
func TestLinkKind(t *testing.T) {
	tests := []struct {
		linkType   string
		wantKind   model.LinkKind
		wantNative string
	}{
		{"is_blocked_by", model.LinkBlockedBy, "is blocked by"},
		{"blocks", model.LinkBlocks, "blocks"},
		{"relates_to", model.LinkRelates, "relates to"},
		// An invented type falls back to Relates carrying GitLab's own wording
		// rather than being dropped.
		{"is_caused_by", model.LinkRelates, "is caused by"},
		{"", model.LinkRelates, ""},
	}

	for _, tt := range tests {
		t.Run(tt.linkType, func(t *testing.T) {
			kind, native := linkKind(tt.linkType)
			if kind != tt.wantKind || native != tt.wantNative {
				t.Errorf("linkKind(%q) = (%v, %q), want (%v, %q)",
					tt.linkType, kind, native, tt.wantKind, tt.wantNative)
			}
		})
	}
}

func TestNewLinksOnAnEmptyList(t *testing.T) {
	if got := newLinks(nil); got != nil {
		t.Errorf("newLinks(nil) = %+v, want nil", got)
	}
	if got := newLinks([]issueLinkWire{}); got != nil {
		t.Errorf("newLinks([]) = %+v, want nil", got)
	}
}

func TestProjectPathFallsBackToTheWebURL(t *testing.T) {
	i := issueWire{IID: 109, WebURL: "https://gitlab.com/gitlab-org/cli/-/work_items/109"}
	if got := i.projectPath(); got != "gitlab-org/cli" {
		t.Errorf("projectPath = %q, want gitlab-org/cli", got)
	}
	// With no references object at all the key falls back to the bare iid.
	if got := i.key(); got != "#109" {
		t.Errorf("key = %q, want #109", got)
	}
}
