package plain

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// The rule: a Native Status is suppressed only when its normalised spelling is
// one of the words its own Status Category already says. Anything else — a
// word from another bucket, a Tracker's own vocabulary, a category sitrep
// could not read — is kept, because that is when it carries information.
func TestShowsNativeStatus(t *testing.T) {
	tests := []struct {
		native string
		status model.StatusCategory
		want   bool
	}{
		{"open", model.StatusTodo, false},
		{"OPEN", model.StatusTodo, false},
		{" To  Do ", model.StatusTodo, false},
		{"todo", model.StatusTodo, false},
		// GitHub derives InProgress from an open pull request while the issue
		// state is still "open": that differs from the bucket's default word,
		// so it is worth its space.
		{"open", model.StatusInProgress, true},
		{"In Review", model.StatusInProgress, true},
		{"in progress", model.StatusInProgress, false},
		{"Done", model.StatusDone, false},
		{"closed", model.StatusDone, false},
		{"completed", model.StatusDone, false},
		{"reason from the future", model.StatusDone, true},
		// A Jira vocabulary that distinguishes states inside one bucket keeps
		// the one that differs.
		{"Reopened", model.StatusTodo, true},
		{"Selected for Development", model.StatusTodo, true},
		{"not planned", model.StatusCancelled, false},
		{"won't do", model.StatusCancelled, false},
		{"won’t do", model.StatusCancelled, false},
		{"duplicate", model.StatusCancelled, true},
		{"", model.StatusDone, false},
		// A Provider that failed to categorise shows whatever word it produced.
		{"open", model.StatusUnknown, true},
	}

	for _, tc := range tests {
		ticket := model.Ticket{Status: tc.status, NativeStatus: tc.native}
		if got := ShowsNativeStatus(ticket); got != tc.want {
			t.Errorf("ShowsNativeStatus(%q under %v) = %v, want %v", tc.native, tc.status, got, tc.want)
		}
	}
}
