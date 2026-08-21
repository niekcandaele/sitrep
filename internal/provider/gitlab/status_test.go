package gitlab

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		name              string
		state             string
		labels            []string
		closedAsDuplicate bool
		wantStatus        model.StatusCategory
		wantNative        string
	}{
		{
			name: "an open issue", state: "opened",
			wantStatus: model.StatusTodo, wantNative: "open",
		},
		{
			name: "a plainly closed issue", state: "closed",
			wantStatus: model.StatusDone, wantNative: "closed",
		},
		{
			// GitLab states the duplicate outright, so it wins over every
			// inference and does not need a label to agree with it.
			name: "closed as a duplicate", state: "closed", closedAsDuplicate: true,
			wantStatus: model.StatusCancelled, wantNative: "duplicate",
		},
		{
			name: "a scoped won't-do label", state: "closed", labels: []string{"backend", "workflow::wontfix"},
			wantStatus: model.StatusCancelled, wantNative: "workflow::wontfix",
		},
		{
			name: "a plain won't-do label", state: "closed", labels: []string{"wontfix"},
			wantStatus: model.StatusCancelled, wantNative: "wontfix",
		},
		{
			name: "punctuation and case do not matter", state: "closed", labels: []string{"Won't Do"},
			wantStatus: model.StatusCancelled, wantNative: "Won't Do",
		},
		{
			name: "declined", state: "closed", labels: []string{"Declined"},
			wantStatus: model.StatusCancelled, wantNative: "Declined",
		},
		{
			name: "not planned", state: "closed", labels: []string{"status::not-planned"},
			wantStatus: model.StatusCancelled, wantNative: "status::not-planned",
		},
		{
			// A label on live work is a plan, not an outcome.
			name: "a won't-do label on an open issue", state: "opened", labels: []string{"workflow::wontfix"},
			wantStatus: model.StatusTodo, wantNative: "open",
		},
		{
			name: "an ordinary label changes nothing", state: "closed", labels: []string{"backend", "workflow::in dev"},
			wantStatus: model.StatusDone, wantNative: "closed",
		},
		{
			// StatusUnknown is the model's "a Provider forgot to map something"
			// signal: a broken or future instance is visible rather than quietly
			// Todo.
			name: "a state sitrep does not know", state: "archived",
			wantStatus: model.StatusUnknown, wantNative: "archived",
		},
		{
			name: "no state at all", state: "",
			wantStatus: model.StatusUnknown, wantNative: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, native := normalizeStatus(tt.state, tt.labels, tt.closedAsDuplicate)
			if status != tt.wantStatus || native != tt.wantNative {
				t.Errorf("normalizeStatus(%q, %v, %v) = (%v, %q), want (%v, %q)",
					tt.state, tt.labels, tt.closedAsDuplicate, status, native, tt.wantStatus, tt.wantNative)
			}
		})
	}
}

// The absence that #15 fills: nothing this driver can be handed produces
// InProgress, because REST tells it nothing that would justify one.
func TestNormalizeStatusNeverReportsInProgress(t *testing.T) {
	states := []string{"opened", "closed", "locked", "merged", ""}
	labels := [][]string{nil, {"workflow::in dev"}, {"in progress"}, {"status::doing"}, {"wontfix"}}

	for _, state := range states {
		for _, ls := range labels {
			for _, dup := range []bool{false, true} {
				if status, _ := normalizeStatus(state, ls, dup); status == model.StatusInProgress {
					t.Fatalf("normalizeStatus(%q, %v, %v) reported InProgress; that is #15's to add",
						state, ls, dup)
				}
			}
		}
	}
}
