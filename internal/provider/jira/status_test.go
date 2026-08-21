package jira

import (
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

// normalizeStatus is the documented business rule this driver exists for, so it
// is tested directly rather than only through the HTTP seam.
func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		name       string
		category   string
		status     string
		resolution string
		want       model.StatusCategory
		wantNative string
	}{
		{
			name: "the new category is Todo", category: "new", status: "To Do",
			want: model.StatusTodo, wantNative: "To Do",
		},
		{
			name: "the indeterminate category is InProgress", category: "indeterminate", status: "In Review",
			want: model.StatusInProgress, wantNative: "In Review",
		},
		{
			name:     "a site's own in-progress status keeps its wording",
			category: "indeterminate", status: "Selected for Development",
			want: model.StatusInProgress, wantNative: "Selected for Development",
		},
		{
			name: "the done category is Done", category: "done", status: "Done", resolution: "Done",
			want: model.StatusDone, wantNative: "Done",
		},
		{
			name: "a resolved status with no resolution is still Done", category: "done", status: "Closed",
			want: model.StatusDone, wantNative: "Closed",
		},
		{
			name:     "a site's invented positive resolution stays Done",
			category: "done", status: "Closed", resolution: "Delivered",
			want: model.StatusDone, wantNative: "Closed",
		},
		{
			// The acceptance criterion: work nobody did is not finished work.
			name:     "won't do is Cancelled and shows the resolution",
			category: "done", status: "Closed", resolution: "Won't Do",
			want: model.StatusCancelled, wantNative: "Won't Do",
		},
		{
			name: "won't fix is Cancelled", category: "done", status: "Resolved", resolution: "Won't Fix",
			want: model.StatusCancelled, wantNative: "Won't Fix",
		},
		{
			name: "will not do is Cancelled", category: "done", status: "Closed", resolution: "Will Not Do",
			want: model.StatusCancelled, wantNative: "Will Not Do",
		},
		{
			name: "declined is Cancelled", category: "done", status: "Closed", resolution: "Declined",
			want: model.StatusCancelled, wantNative: "Declined",
		},
		{
			name: "rejected is Cancelled", category: "done", status: "Closed", resolution: "Rejected",
			want: model.StatusCancelled, wantNative: "Rejected",
		},
		{
			name: "duplicate is Cancelled", category: "done", status: "Closed", resolution: "Duplicate",
			want: model.StatusCancelled, wantNative: "Duplicate",
		},
		{
			name:     "cannot reproduce is Cancelled",
			category: "done", status: "Closed", resolution: "Cannot Reproduce",
			want: model.StatusCancelled, wantNative: "Cannot Reproduce",
		},
		{
			name: "obsolete is Cancelled", category: "done", status: "Closed", resolution: "Obsolete",
			want: model.StatusCancelled, wantNative: "Obsolete",
		},
		{
			name: "as designed is Cancelled", category: "done", status: "Closed", resolution: "As Designed",
			want: model.StatusCancelled, wantNative: "As Designed",
		},
		{
			name: "incomplete is Cancelled", category: "done", status: "Closed", resolution: "Incomplete",
			want: model.StatusCancelled, wantNative: "Incomplete",
		},
		{
			// A resolution on an unfinished Ticket is not a real state, so it
			// cannot cancel anything.
			name:     "a resolution on a non-done status is ignored",
			category: "indeterminate", status: "In Progress", resolution: "Won't Do",
			want: model.StatusInProgress, wantNative: "In Progress",
		},
		{
			name: "an unknown category key is Unknown", category: "backlogged", status: "Icebox",
			want: model.StatusUnknown, wantNative: "Icebox",
		},
		{
			name: "an empty category key is Unknown", status: "Icebox",
			want: model.StatusUnknown, wantNative: "Icebox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, native := normalizeStatus(tt.category, tt.status, tt.resolution)
			if status != tt.want || native != tt.wantNative {
				t.Errorf("normalizeStatus(%q, %q, %q) = (%v, %q), want (%v, %q)",
					tt.category, tt.status, tt.resolution, status, native, tt.want, tt.wantNative)
			}
		})
	}
}
