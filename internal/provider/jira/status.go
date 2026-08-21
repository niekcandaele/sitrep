package jira

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// Jira's own status category keys. They are the stable identifiers on the
// status object; statusCategory.name and colorName are localized and must never
// be read.
const (
	categoryTodo       = "new"
	categoryInProgress = "indeterminate"
	categoryDone       = "done"
)

// wontDoResolutions are the resolution names that mean "nobody did this", as
// opposed to "this is finished". Counting work nobody did as finished flatters
// every epic containing one, so these become Cancelled and leave the progress
// denominator, exactly as GitHub's not_planned does.
//
// The list is a judgement call: Jira lets a site invent any resolution name.
// Names are matched after normalization — lower-cased with everything that is
// not a letter or a digit stripped — so "Won't Do", "wont-do" and "WONTDO" are
// one entry. A site with a bespoke wording adds one row here and nothing else.
var wontDoResolutions = map[string]bool{
	"wontdo":             true,
	"wontfix":            true,
	"willnotdo":          true,
	"willnotfix":         true,
	"declined":           true,
	"rejected":           true,
	"duplicate":          true,
	"cannotreproduce":    true,
	"cannotreproducebug": true,
	"notreproducible":    true,
	"abandoned":          true,
	"obsolete":           true,
	"invalid":            true,
	"notabug":            true,
	"asdesigned":         true,
	"incomplete":         true,
}

// normalizeStatus maps a Jira status category, status name and resolution onto
// sitrep's Status Category and the Native Status shown to the user. It is the
// only place in sitrep allowed to look at Jira's status vocabulary; everything
// downstream reads Status.
//
// The status category key is the authority for the category. A key sitrep does
// not know — including an empty one — is StatusUnknown rather than a guess:
// StatusUnknown is the model's deliberate "a Provider forgot to map something"
// signal, and a broken instance should be visible rather than quietly Todo.
//
// A won't-do resolution turns a finished Ticket into a Cancelled one, and only
// then: an unresolved Ticket carrying a stray resolution string is not a real
// state. When that happens the Native Status becomes the resolution's own name
// ("Won't Do") rather than the status name, mirroring the GitHub driver's "not
// planned" — it is what makes a Cancelled group readable.
func normalizeStatus(categoryKey, statusName, resolution string) (model.StatusCategory, string) {
	native := strings.TrimSpace(statusName)

	var status model.StatusCategory
	switch strings.ToLower(strings.TrimSpace(categoryKey)) {
	case categoryTodo:
		status = model.StatusTodo
	case categoryInProgress:
		status = model.StatusInProgress
	case categoryDone:
		status = model.StatusDone
	default:
		return model.StatusUnknown, native
	}

	if status == model.StatusDone && wontDoResolutions[normalizeResolution(resolution)] {
		return model.StatusCancelled, strings.TrimSpace(resolution)
	}
	return status, native
}

// normalizeResolution reduces a resolution name to its letters and digits,
// lower-cased, so that the one table above does not have to spell every way a
// site punctuates "Won't Do".
func normalizeResolution(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
