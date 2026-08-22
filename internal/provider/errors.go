package provider

import (
	"errors"
	"fmt"
)

// Kind classifies a Provider failure by what the person at the terminal has to
// do about it. It exists to answer exactly two questions: which class of
// failure this is, so a test can prove every driver covers the named classes,
// and whether retrying in sixty seconds is worth anything, so internal/cli
// knows whether to open the monitor or print one line and exit.
//
// # The prose contract
//
// A classified error's message is the driver's own sentence, and every one of
// them satisfies the same six rules. They are written down here because they
// are what a reviewer — and the next driver — checks against, and
// providertest.CheckError is the one implementation of them:
//
//  1. One line. No newline anywhere in the message.
//  2. Attributed. It starts with "<provider name>: " — "github: ", "jira: ",
//     "gitlab: ".
//  3. It names what failed in the user's own vocabulary: the ref they typed,
//     the host, the file, the status code in parentheses.
//  4. It names the fix where one exists: `gh auth status`, $GITLAB_TOKEN, "add
//     a profile to <path>", "retry after 60s".
//  5. It never contains a credential. Not the token, not the Basic header, not
//     the password half of anything.
//  6. Kind-specific: a KindRateLimit message says when the limit clears, a
//     KindAuth message names which credential, and a KindBadRef message quotes
//     the ref as the user typed it — or the key sitrep derived from it.
type Kind uint8

const (
	// KindUnknown is an unclassified failure. It is treated as retryable: a
	// driver that forgets to classify something must not cause sitrep to give
	// up on a monitor that would have recovered.
	KindUnknown Kind = iota
	// KindBadRef is a Ref that names nothing this Tracker has, or that
	// this driver cannot parse: a typo, a wrong repo, a deleted issue, a 404.
	KindBadRef
	// KindAuth is a credential problem: none found, rejected (401), or
	// insufficient (403 that is not a rate limit).
	KindAuth
	// KindRateLimit is the Tracker saying "not now": 429, an exhausted quota, a
	// secondary limit.
	KindRateLimit
	// KindUnavailable is the Tracker or the network failing: a transport error,
	// a 5xx, a response that will not decode.
	KindUnavailable
)

// Retryable reports whether the next refresh could plausibly succeed without
// the user changing anything. A bad ref and a rejected credential will not fix
// themselves on the next tick; everything else might.
func (k Kind) Retryable() bool {
	switch k {
	case KindBadRef, KindAuth:
		return false
	default:
		return true
	}
}

// String names the Kind for a test's failure message. It is never printed to a
// user: they read the driver's sentence, not a category label, and a message
// that needs a label appended to make sense is a message that needs rewriting.
func (k Kind) String() string {
	switch k {
	case KindBadRef:
		return "bad ref"
	case KindAuth:
		return "auth"
	case KindRateLimit:
		return "rate limit"
	case KindUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// Error is a Provider failure carrying its Kind. Its message is the driver's
// own prose, unchanged: this type classifies, it does not rewrite.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }

// Unwrap keeps everything the driver wrapped reachable, so errors.Is still
// finds a context.Canceled through a *url.Error through a classified error.
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a classified error. The format string carries the driver's own
// prefix ("github: …"), exactly as fmt.Errorf did before it, so classifying a
// call site changes no message text — and %w still wraps, because the message
// is built by fmt.Errorf itself.
//
// The rendered message is sanitized here because this is the single funnel
// every driver's prose passes through, and several of those messages quote a
// server-supplied "message" field verbatim — tracker-controlled text on its way
// to a terminal, exactly like a title or a comment body. It also keeps rule 1
// of the prose contract true by construction: a message cannot carry a
// newline.
func Errorf(kind Kind, format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	if msg := SanitizeLine(err.Error()); msg != err.Error() {
		err = &sanitizedMessage{msg: msg, err: err}
	}
	return &Error{Kind: kind, Err: err}
}

// sanitizedMessage replaces an error's rendered text while keeping everything
// it wrapped reachable, so errors.Is still finds a context.Canceled through a
// classified error whose message needed cleaning.
type sanitizedMessage struct {
	msg string
	err error
}

func (e *sanitizedMessage) Error() string { return e.msg }
func (e *sanitizedMessage) Unwrap() error { return e.err }

// KindOf reports the Kind of err, walking wrapped errors, and KindUnknown when
// nothing in the chain is classified.
func KindOf(err error) Kind {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return KindUnknown
}
