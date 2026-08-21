package model_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

type stringerEnum interface {
	comparable
	fmt.Stringer
}

// checkEnum pins one enum's wire contract: every value has the expected token,
// String and JSON agree, the token survives a round trip, and an unrecognized
// token is an error rather than a silent zero value.
func checkEnum[T stringerEnum](t *testing.T, tokens map[T]string) {
	t.Helper()
	for value, token := range tokens {
		if got := value.String(); got != token {
			t.Errorf("String() = %q, want %q", got, token)
		}

		b, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", value, err)
		}
		if got, want := string(b), fmt.Sprintf("%q", token); got != want {
			t.Errorf("Marshal(%v) = %s, want %s", value, got, want)
		}

		var back T
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}
		if back != value {
			t.Errorf("round trip of %q = %v, want %v", token, back, value)
		}
	}

	var out T
	if err := json.Unmarshal([]byte(`"not-a-token"`), &out); err == nil {
		t.Error("Unmarshal of an unknown token succeeded, want an error")
	}
	if err := json.Unmarshal([]byte(`7`), &out); err == nil {
		t.Error("Unmarshal of a non-string succeeded, want an error")
	}
}

func TestStatusCategoryWireContract(t *testing.T) {
	checkEnum(t, map[model.StatusCategory]string{
		model.StatusUnknown:    "unknown",
		model.StatusTodo:       "todo",
		model.StatusInProgress: "in_progress",
		model.StatusDone:       "done",
		model.StatusCancelled:  "cancelled",
	})
}

func TestPRStateWireContract(t *testing.T) {
	checkEnum(t, map[model.PRState]string{
		model.PRUnknown: "unknown",
		model.PRDraft:   "draft",
		model.PROpen:    "open",
		model.PRMerged:  "merged",
		model.PRClosed:  "closed",
	})
}

func TestReviewStateWireContract(t *testing.T) {
	checkEnum(t, map[model.ReviewState]string{
		model.ReviewNone:             "none",
		model.ReviewPending:          "pending",
		model.ReviewApproved:         "approved",
		model.ReviewChangesRequested: "changes_requested",
	})
}

func TestCheckStateWireContract(t *testing.T) {
	checkEnum(t, map[model.CheckState]string{
		model.ChecksNone:    "none",
		model.ChecksPending: "pending",
		model.ChecksPassing: "passing",
		model.ChecksFailing: "failing",
	})
}

func TestLinkKindWireContract(t *testing.T) {
	checkEnum(t, map[model.LinkKind]string{
		model.LinkRelates:   "relates",
		model.LinkBlockedBy: "blocked_by",
		model.LinkBlocks:    "blocks",
	})
}

// The zero values are load-bearing: a Provider that forgets to map a status
// must produce a visibly unknown Ticket, while "no review" and "no CI" are
// legitimate answers rather than mapping failures.
func TestZeroValues(t *testing.T) {
	var (
		status model.StatusCategory
		review model.ReviewState
		checks model.CheckState
		kind   model.LinkKind
	)
	for _, tc := range []struct {
		got  string
		want string
	}{
		{status.String(), "unknown"},
		{review.String(), "none"},
		{checks.String(), "none"},
		{kind.String(), "relates"},
	} {
		if tc.got != tc.want {
			t.Errorf("zero value = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestParseStatusCategory(t *testing.T) {
	got, err := model.ParseStatusCategory("in_progress")
	if err != nil {
		t.Fatalf("ParseStatusCategory: %v", err)
	}
	if got != model.StatusInProgress {
		t.Errorf("ParseStatusCategory(\"in_progress\") = %v, want StatusInProgress", got)
	}

	if _, err := model.ParseStatusCategory("In Review"); err == nil {
		t.Error("ParseStatusCategory of a native status succeeded, want an error")
	}
}
