package plain

import "fmt"

// PullRequestOverflow renders the trailing "+N more" fragment for the pull
// requests a row does not show, or "" when there are none. shown is how many
// pull requests the renderer actually holds; total is what the Tracker says
// the Ticket has, which is zero when the serving Provider cannot supply one.
//
// It follows the rule LimitNotice follows: count from the best number actually
// available, and never claim a total that was not supplied. max(shown, total)
// is what carries that. A Provider with no total passes zero and gets the
// pre-existing shown-1 count, and a total below what the Provider handed over
// cannot shrink the count below the truth, so no path here produces a
// fabricated or negative number. Showing nothing renders nothing whatever the
// total says: "+N more" counts the pull requests beyond the one on the row, so
// with no row there is nothing for it to be more than.
func PullRequestOverflow(shown, total int) string {
	if shown == 0 {
		return ""
	}
	rest := max(shown, total) - 1
	if rest <= 0 {
		return ""
	}
	return fmt.Sprintf("+%d more", rest)
}
