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
		// wontDo is the Profile's own label list. Nil is the built-in list, so
		// every case that leaves it nil is a regression test on the default.
		wontDo     []string
		wantStatus model.StatusCategory
		wantNative string
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
			name: "a configured label the built-in list has never heard of", state: "closed",
			labels: []string{"backend", "ausgemustert"}, wontDo: []string{"ausgemustert"},
			wantStatus: model.StatusCancelled, wantNative: "ausgemustert",
		},
		{
			// Replace, not extend: a site that configures its own wording stops
			// sitrep reading sitrep's guesses as cancellation.
			name: "a built-in name the configured list drops", state: "closed",
			labels: []string{"wontfix"}, wontDo: []string{"ausgemustert"},
			wantStatus: model.StatusDone, wantNative: "closed",
		},
		{
			// The configured name and the label are normalized by one rule, so
			// the Profile's spelling need not match GitLab's.
			name: "a configured name spelled differently from the label", state: "closed",
			labels: []string{"workflow::No-Fix"}, wontDo: []string{"no fix"},
			wantStatus: model.StatusCancelled, wantNative: "workflow::No-Fix",
		},
		{
			name: "a configured label on an open issue", state: "opened",
			labels: []string{"ausgemustert"}, wontDo: []string{"ausgemustert"},
			wantStatus: model.StatusTodo, wantNative: "open",
		},
		{
			// closed_as_duplicate_of is a fact GitLab states, not a label
			// inference, so a configured list that omits "duplicate" cannot
			// disable it.
			name: "closed as a duplicate under a configured list", state: "closed", closedAsDuplicate: true,
			wontDo:     []string{"ausgemustert"},
			wantStatus: model.StatusCancelled, wantNative: "duplicate",
		},
		{
			// Every entry normalizes away to nothing, so the built-in list
			// stands rather than the rule silently switching off.
			name: "a configured list of unmatchable names", state: "closed",
			labels: []string{"wontfix"}, wontDo: []string{"!!!", "---"},
			wantStatus: model.StatusCancelled, wantNative: "wontfix",
		},
		{
			// A junk label must not match a junk entry: both sides normalize to
			// the empty string, which is nobody's label.
			name: "a junk label under a configured list", state: "closed",
			labels: []string{"!!!"}, wontDo: []string{"ausgemustert"},
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
			status, native := normalizeStatus(tt.state, tt.labels, tt.closedAsDuplicate, newWontDoSet(tt.wontDo))
			if status != tt.wantStatus || native != tt.wantNative {
				t.Errorf("normalizeStatus(%q, %v, %v, %v) = (%v, %q), want (%v, %q)",
					tt.state, tt.labels, tt.closedAsDuplicate, tt.wontDo, status, native, tt.wantStatus, tt.wantNative)
			}
		})
	}
}

// Nothing normalizeStatus can be handed produces InProgress: GitLab issues are
// opened or closed in REST and a scoped label is a plan, not an outcome. The
// in-progress half of the report comes from statusWithMergeRequests instead.
func TestNormalizeStatusNeverReportsInProgress(t *testing.T) {
	states := []string{"opened", "closed", "locked", "merged", ""}
	labels := [][]string{nil, {"workflow::in dev"}, {"in progress"}, {"status::doing"}, {"wontfix"}}
	sets := map[string]wontDoSet{
		"the built-in list": newWontDoSet(nil),
		"a configured list": newWontDoSet([]string{"in progress", "status::doing", "ausgemustert"}),
		"the zero set":      nil,
	}

	for name, set := range sets {
		for _, state := range states {
			for _, ls := range labels {
				for _, dup := range []bool{false, true} {
					if status, _ := normalizeStatus(state, ls, dup, set); status == model.StatusInProgress {
						t.Fatalf("normalizeStatus(%q, %v, %v) under %s reported InProgress; only merge requests may",
							state, ls, dup, name)
					}
				}
			}
		}
	}
}

// The two layers agree: every list config validation accepts replaces the
// built-in labels rather than silently falling back to them. UsableWontDoLabel
// is the rule both sides read, so a list that passes it can never normalize
// away to nothing.
func TestAnAcceptedWontDoListNeverFallsBackToTheDefaults(t *testing.T) {
	// None of these normalizes to a built-in label, so a set that still
	// matches "wontfix" can only be defaultWontDoLabels handed back.
	lists := [][]string{
		{"ausgemustert"},
		{"workflow::abgebrochen"},
		{"Niet Doen"},
		{"a", "b"},
		{"見送り"},
		{"не будет", "workflow::見送り"},
	}

	for _, names := range lists {
		for _, name := range names {
			if !UsableWontDoLabel(name) {
				t.Fatalf("UsableWontDoLabel(%q) = false; this list would be rejected by config", name)
			}
		}
		set := newWontDoSet(names)
		if len(set) != len(names) {
			t.Errorf("newWontDoSet(%v) has %d entries, want %d", names, len(set), len(names))
		}
		if set.matches("wontfix") {
			t.Errorf("newWontDoSet(%v) still matches a built-in label: the override was ignored", names)
		}
	}
}

// A label written outside the Latin alphabet round-trips: the configured name
// matches the Tracker's label, and does not match a built-in one.
func TestWontDoSetMatchesANonLatinLabel(t *testing.T) {
	set := newWontDoSet([]string{"見送り", "не будет"})
	for _, label := range []string{"見送り", "workflow::見送り", "Не Будет"} {
		if !set.matches(label) {
			t.Errorf("newWontDoSet did not match %q, so the configured list matches nothing", label)
		}
	}
	if set.matches("wontfix") {
		t.Error("the configured list fell back to the built-in defaults")
	}
}

// The rule config validation reads, stated as a table so the two layers cannot
// drift apart quietly.
func TestUsableWontDoLabel(t *testing.T) {
	tests := map[string]bool{
		"wontfix":           true,
		"Won't Fix":         true,
		"workflow::wontfix": true,
		"ausgemustert":      true,
		"404":               true,
		// A label is usable in the site's writing system because normalizeLabel
		// retains Unicode letters and digits.
		"見送り":                true,
		"не будет":           true,
		"لن يتم":             true,
		"不做":                 true,
		"workflow::見送り":      true,
		"::":                 false,
		"---":                false,
		"workflow::":         false,
		"":                   false,
		"   ":                false,
		"workflow:: - - -  ": false,
	}

	for name, want := range tests {
		if got := UsableWontDoLabel(name); got != want {
			t.Errorf("UsableWontDoLabel(%q) = %v, want %v", name, got, want)
		}
	}
}
