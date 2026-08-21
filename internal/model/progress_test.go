package model_test

import (
	"reflect"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

func ticket(key string, status model.StatusCategory, native string) model.Ticket {
	return model.Ticket{
		ID:           model.TicketID(key),
		Key:          key,
		Status:       status,
		NativeStatus: native,
	}
}

func TestComputeProgress(t *testing.T) {
	tests := []struct {
		name    string
		tickets []model.Ticket
		want    model.Progress
	}{
		{
			name:    "empty epic",
			tickets: nil,
			want:    model.Progress{},
		},
		{
			name: "cancelled tickets leave the denominator",
			tickets: []model.Ticket{
				ticket("a", model.StatusDone, "closed"),
				ticket("b", model.StatusDone, "Done"),
				ticket("c", model.StatusInProgress, "In Review"),
				ticket("d", model.StatusTodo, "open"),
				ticket("e", model.StatusCancelled, "not planned"),
			},
			want: model.Progress{
				Todo: 1, InProgress: 1, Done: 2, Cancelled: 1,
				Total: 5, Denominator: 4, PercentDone: 50,
			},
		},
		{
			name: "an all-cancelled epic reports zero percent",
			tickets: []model.Ticket{
				ticket("a", model.StatusCancelled, "not planned"),
				ticket("b", model.StatusCancelled, "wontfix"),
			},
			want: model.Progress{
				Cancelled: 2, Total: 2, Denominator: 0, PercentDone: 0,
			},
		},
		{
			name: "an unmapped status is counted as unknown",
			tickets: []model.Ticket{
				ticket("a", model.StatusUnknown, ""),
				ticket("b", model.StatusDone, "closed"),
			},
			want: model.Progress{
				Done: 1, Unknown: 1, Total: 2, Denominator: 2, PercentDone: 50,
			},
		},
		{
			name: "one of three rounds down",
			tickets: []model.Ticket{
				ticket("a", model.StatusDone, "closed"),
				ticket("b", model.StatusTodo, "open"),
				ticket("c", model.StatusTodo, "open"),
			},
			want: model.Progress{
				Todo: 2, Done: 1, Total: 3, Denominator: 3, PercentDone: 33,
			},
		},
		{
			name: "two of three rounds up",
			tickets: []model.Ticket{
				ticket("a", model.StatusDone, "closed"),
				ticket("b", model.StatusDone, "closed"),
				ticket("c", model.StatusTodo, "open"),
			},
			want: model.Progress{
				Todo: 1, Done: 2, Total: 3, Denominator: 3, PercentDone: 67,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.ComputeProgress(tt.tickets); got != tt.want {
				t.Errorf("ComputeProgress() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Native Status is display-only: two tickets that share a Status Category count
// and group identically no matter what their Tracker calls them.
func TestNativeStatusDoesNotAffectProgressOrGrouping(t *testing.T) {
	same := []model.Ticket{
		ticket("a", model.StatusInProgress, "In Progress"),
		ticket("b", model.StatusInProgress, "In Progress"),
	}
	differing := []model.Ticket{
		ticket("a", model.StatusInProgress, "In Review"),
		ticket("b", model.StatusInProgress, "Selected for Development"),
	}

	if model.ComputeProgress(same) != model.ComputeProgress(differing) {
		t.Errorf("progress differs by native status: %+v vs %+v",
			model.ComputeProgress(same), model.ComputeProgress(differing))
	}

	groups := model.GroupByCategory(differing)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	if groups[0].Category != model.StatusInProgress || len(groups[0].Tickets) != 2 {
		t.Errorf("differing native statuses split the group: %+v", groups[0])
	}
}

func TestGroupByCategory(t *testing.T) {
	tickets := []model.Ticket{
		ticket("done-1", model.StatusDone, "closed"),
		ticket("todo-1", model.StatusTodo, "open"),
		ticket("wip-1", model.StatusInProgress, "In Review"),
		ticket("todo-2", model.StatusTodo, "Selected for Development"),
		ticket("wip-2", model.StatusInProgress, "In Progress"),
	}

	groups := model.GroupByCategory(tickets)

	wantOrder := []model.StatusCategory{model.StatusInProgress, model.StatusTodo, model.StatusDone}
	gotOrder := make([]model.StatusCategory, 0, len(groups))
	for _, g := range groups {
		gotOrder = append(gotOrder, g.Category)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("group order = %v, want %v (empty categories must be omitted)", gotOrder, wantOrder)
	}

	wantKeys := []string{"wip-1", "wip-2"}
	gotKeys := []string{groups[0].Tickets[0].Key, groups[0].Tickets[1].Key}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("within-group order = %v, want %v", gotKeys, wantKeys)
	}
}

func TestGroupByCategoryEmpty(t *testing.T) {
	if groups := model.GroupByCategory(nil); len(groups) != 0 {
		t.Errorf("GroupByCategory(nil) = %+v, want no groups", groups)
	}
}

func TestAllStatusCategoriesDisplayOrder(t *testing.T) {
	want := []model.StatusCategory{
		model.StatusInProgress,
		model.StatusTodo,
		model.StatusDone,
		model.StatusCancelled,
		model.StatusUnknown,
	}
	if got := model.AllStatusCategories(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllStatusCategories() = %v, want %v", got, want)
	}

	// The caller gets a copy: mutating it must not move anybody else's display
	// order.
	got := model.AllStatusCategories()
	got[0] = model.StatusDone
	if model.AllStatusCategories()[0] != model.StatusInProgress {
		t.Error("AllStatusCategories() shares its backing array with callers")
	}
}

func TestIsFinished(t *testing.T) {
	finished := map[model.StatusCategory]bool{
		model.StatusUnknown:    false,
		model.StatusTodo:       false,
		model.StatusInProgress: false,
		model.StatusDone:       true,
		model.StatusCancelled:  true,
	}
	for status, want := range finished {
		if got := status.IsFinished(); got != want {
			t.Errorf("%s.IsFinished() = %v, want %v", status, got, want)
		}
	}
}
