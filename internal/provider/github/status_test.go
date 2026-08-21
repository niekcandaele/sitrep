package github

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// normalizeStatus is the documented business rule this driver exists for, so it
// is tested directly rather than only through the HTTP seam. Nothing else
// inside this package gets that exception.
func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		state  string
		reason string
		status model.StatusCategory
		native string
	}{
		{state: "OPEN", reason: "", status: model.StatusTodo, native: "open"},
		{state: "OPEN", reason: "REOPENED", status: model.StatusTodo, native: "open"},
		{state: "open", reason: "", status: model.StatusTodo, native: "open"},
		{state: "CLOSED", reason: "COMPLETED", status: model.StatusDone, native: "closed"},
		{state: "CLOSED", reason: "", status: model.StatusDone, native: "closed"},
		{state: "CLOSED", reason: "NOT_PLANNED", status: model.StatusCancelled, native: "not planned"},
		{state: "closed", reason: "not_planned", status: model.StatusCancelled, native: "not planned"},
		{state: "CLOSED", reason: "DUPLICATE", status: model.StatusCancelled, native: "duplicate"},
		{state: "CLOSED", reason: "REASON_FROM_THE_FUTURE", status: model.StatusDone, native: "reason from the future"},
		{state: "MERGED", reason: "", status: model.StatusUnknown, native: "merged"},
		{state: "", reason: "", status: model.StatusUnknown, native: ""},
	}

	for _, tt := range tests {
		t.Run(tt.state+"/"+tt.reason, func(t *testing.T) {
			status, native := normalizeStatus(tt.state, tt.reason)
			if status != tt.status {
				t.Errorf("status = %v, want %v", status, tt.status)
			}
			if native != tt.native {
				t.Errorf("native status = %q, want %q", native, tt.native)
			}
		})
	}
}

// GitHub issues are open or closed and nothing else. In-progress will be
// derived from an open pull request when PR correlation lands; until then no
// GitHub Ticket may claim it.
func TestNothingIsInProgressYet(t *testing.T) {
	states := []string{"OPEN", "CLOSED", "REOPENED", "anything"}
	reasons := []string{"", "COMPLETED", "NOT_PLANNED", "DUPLICATE", "REOPENED"}

	for _, state := range states {
		for _, reason := range reasons {
			if status, _ := normalizeStatus(state, reason); status == model.StatusInProgress {
				t.Errorf("normalizeStatus(%q, %q) claimed in-progress", state, reason)
			}
		}
	}
}
