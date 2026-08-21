package model

// PRState is the lifecycle state of a pull or merge request.
type PRState int

// The pull request states. PRUnknown is the zero value: a Provider that fails
// to map a Tracker's state says so rather than guessing "open".
const (
	PRUnknown PRState = iota
	PRDraft
	PROpen
	PRMerged
	PRClosed
)

var prStateTokens = enumTokens[PRState]{
	{PRUnknown, "unknown"},
	{PRDraft, "draft"},
	{PROpen, "open"},
	{PRMerged, "merged"},
	{PRClosed, "closed"},
}

// String returns the wire and display token for the pull request state.
func (s PRState) String() string { return prStateTokens.token(s, "pr_state") }

// MarshalJSON encodes the pull request state as its wire token.
func (s PRState) MarshalJSON() ([]byte, error) { return prStateTokens.marshal(s) }

// UnmarshalJSON decodes a pull request state from its wire token.
func (s *PRState) UnmarshalJSON(b []byte) error { return prStateTokens.unmarshal(b, s, "pr state") }

// ReviewState is the review posture of a pull request.
type ReviewState int

// The review states. Unlike PRState there is no "unknown" here: ReviewNone is
// the zero value because "nobody has been asked to review" is a real, common
// answer rather than a mapping failure.
const (
	ReviewNone ReviewState = iota
	ReviewPending
	ReviewApproved
	ReviewChangesRequested
)

var reviewStateTokens = enumTokens[ReviewState]{
	{ReviewNone, "none"},
	{ReviewPending, "pending"},
	{ReviewApproved, "approved"},
	{ReviewChangesRequested, "changes_requested"},
}

// String returns the wire and display token for the review state.
func (s ReviewState) String() string { return reviewStateTokens.token(s, "review_state") }

// MarshalJSON encodes the review state as its wire token.
func (s ReviewState) MarshalJSON() ([]byte, error) { return reviewStateTokens.marshal(s) }

// UnmarshalJSON decodes a review state from its wire token.
func (s *ReviewState) UnmarshalJSON(b []byte) error {
	return reviewStateTokens.unmarshal(b, s, "review state")
}

// CheckState is the aggregate result of a pull request's CI checks.
type CheckState int

// The check states. As with ReviewState the zero value is ChecksNone rather
// than an unknown: a repository with no CI configured legitimately reports
// nothing.
const (
	ChecksNone CheckState = iota
	ChecksPending
	ChecksPassing
	ChecksFailing
)

var checkStateTokens = enumTokens[CheckState]{
	{ChecksNone, "none"},
	{ChecksPending, "pending"},
	{ChecksPassing, "passing"},
	{ChecksFailing, "failing"},
}

// String returns the wire and display token for the check state.
func (s CheckState) String() string { return checkStateTokens.token(s, "check_state") }

// MarshalJSON encodes the check state as its wire token.
func (s CheckState) MarshalJSON() ([]byte, error) { return checkStateTokens.marshal(s) }

// UnmarshalJSON decodes a check state from its wire token.
func (s *CheckState) UnmarshalJSON(b []byte) error {
	return checkStateTokens.unmarshal(b, s, "check state")
}

// PullRequest is the code moving a Ticket, as much of it as the Tracker
// exposes. It appears on a Ticket only when the serving Provider declares the
// PullRequests Capability.
type PullRequest struct {
	// Number is the Tracker's pull or merge request number.
	Number int
	// Title is the pull request's one-line summary.
	Title string
	// URL points at the pull request in its Tracker.
	URL string
	// Repository is where the pull request lives, e.g. "acme/widgets". It can
	// differ from the Ticket's own Repository.
	Repository string
	// State is the pull request's lifecycle state.
	State PRState
	// Review summarizes the review posture.
	Review ReviewState
	// Checks summarizes the CI result.
	Checks CheckState
}
