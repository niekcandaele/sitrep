package github

import (
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// normalizeStatus maps a GitHub issue state and state reason onto sitrep's
// Status Category and the Native Status shown to the user. It is the only place
// in sitrep allowed to look at GitHub's state strings; everything downstream
// reads Status.
//
// GitHub issues are open or closed and nothing else, so this function never
// returns StatusInProgress. In-progress is derived from the pull requests
// moving a Ticket, by statusWithPullRequests, which runs after this — that is
// where the rule lives, not here.
//
// An issue closed as not planned is Cancelled, not Done: it never happened, and
// counting it as finished work would flatter every epic containing one.
func normalizeStatus(state, reason string) (model.StatusCategory, string) {
	// GraphQL answers in SCREAMING_CASE; the Native Status a human reads does
	// not shout.
	state = strings.ToLower(strings.TrimSpace(state))
	reason = strings.ToLower(strings.TrimSpace(reason))

	switch state {
	case "open":
		return model.StatusTodo, "open"
	case "closed":
		switch reason {
		case "not_planned":
			return model.StatusCancelled, "not planned"
		case "duplicate":
			return model.StatusCancelled, "duplicate"
		case "", "completed":
			return model.StatusDone, "closed"
		default:
			return model.StatusDone, strings.ReplaceAll(reason, "_", " ")
		}
	default:
		return model.StatusUnknown, state
	}
}
