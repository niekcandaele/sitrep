package provider

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/niekcandaele/sitrep/internal/termtext"
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

// RateLimitMetadata is optional machine-readable timing supplied with a
// rate-limit refusal. Providers only parse headers into it; deciding whether to
// wait belongs to their caller.
type RateLimitMetadata struct {
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Valid reports whether metadata carries one usable timing fact.
func (m RateLimitMetadata) Valid() bool {
	return !m.ResetAt.IsZero() || m.RetryAfter > 0
}

// Deadline resolves an absolute reset or a relative retry delay using the
// caller's clock. It deliberately does not read the wall clock.
func (m RateLimitMetadata) Deadline(now time.Time) (time.Time, bool) {
	if !m.ResetAt.IsZero() {
		return m.ResetAt, true
	}
	if m.RetryAfter > 0 {
		return now.Add(m.RetryAfter), true
	}
	return time.Time{}, false
}

// Error is a Provider failure carrying its Kind. Its message is the driver's
// own prose, unchanged: this type classifies, it does not rewrite.
type Error struct {
	Kind      Kind
	Err       error
	RateLimit RateLimitMetadata
}

func (e *Error) Error() string {
	if e == nil {
		return "provider: typed-nil Error"
	}
	return e.Err.Error()
}

// Unwrap keeps everything the driver wrapped reachable, so errors.Is still
// finds a context.Canceled through a *url.Error through a classified error.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

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
	if msg := termtext.Line(err.Error()); msg != err.Error() {
		err = &sanitizedMessage{msg: msg, err: err}
	}
	return &Error{Kind: kind, Err: err}
}

// RateLimitErrorf builds a KindRateLimit error while retaining the optional
// header timing that accompanied the refusal. Existing callers keep using
// Errorf so their message and wrapping behaviour remain unchanged.
func RateLimitErrorf(metadata RateLimitMetadata, format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	if msg := termtext.Line(err.Error()); msg != err.Error() {
		err = &sanitizedMessage{msg: msg, err: err}
	}
	return &Error{Kind: KindRateLimit, Err: err, RateLimit: metadata}
}

// RateLimitMetadataOf finds timing attached to a classified rate-limit error,
// including one that has been wrapped or redacted.
func RateLimitMetadataOf(err error) (RateLimitMetadata, bool) {
	var classified *Error
	if errors.As(err, &classified) && classified.Kind == KindRateLimit && classified.RateLimit.Valid() {
		return classified.RateLimit, true
	}
	return RateLimitMetadata{}, false
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

// RedactedTransportError keeps a transport failure available to errors.Is while
// replacing its rendered text. net/http wraps RoundTripper failures in a
// *url.Error whose text contains the complete request URL, and custom transports
// may include request context of their own; Query Providers use this at their
// request boundary so an opaque native Query never appears in stderr.
func RedactedTransportError(err error) error {
	return &redactedTransportError{err: err}
}

type redactedTransportError struct{ err error }

func (e *redactedTransportError) Error() string { return "transport failure" }
func (e *redactedTransportError) Unwrap() error { return e.err }

// RedactQuery removes equivalent representations of an opaque Query value from
// Tracker-controlled error prose. Trackers may decode or normalize a rejected
// query before quoting it, so exact-byte replacement alone is not sufficient.
// All decoding happens on private copies used only for diagnostics; request
// bytes remain untouched.
func RedactQuery(message, query string) string {
	if query == "" {
		return message
	}
	for _, candidate := range queryRedactionCandidates(query) {
		if candidate.standalone {
			message = replaceStandaloneQueryValue(message, candidate.text)
		} else {
			message = strings.ReplaceAll(message, candidate.text, "[query]")
		}
	}
	return message
}

type queryRedactionCandidate struct {
	text       string
	standalone bool
}

const minStandaloneQueryValueBytes = 4

func queryRedactionCandidates(query string) []queryRedactionCandidate {
	candidates := make(map[string]bool)
	store := func(text string, standalone bool) {
		if text == "" {
			return
		}
		if existing, ok := candidates[text]; !ok || existing && !standalone {
			candidates[text] = standalone
		}
	}
	add := func(text string, standalone bool) {
		store(text, standalone)
		store(queryPercentHexCase(text, false), standalone)
		store(queryPercentHexCase(text, true), standalone)
	}
	addEscaped := func(value string, standalone bool) {
		add(value, standalone)
		escaped := url.QueryEscape(value)
		add(escaped, standalone)
		add(strings.ReplaceAll(escaped, "+", "%20"), standalone)
	}

	add(query, false)
	if decoded, err := url.QueryUnescape(query); err == nil {
		addEscaped(decoded, false)
	}
	if values, err := url.ParseQuery(query); err == nil {
		encoded := values.Encode()
		add(encoded, false)
		add(strings.ReplaceAll(encoded, "+", "%20"), false)
		if decoded, err := url.QueryUnescape(encoded); err == nil {
			add(decoded, false)
		}
	}

	for _, component := range strings.Split(query, "&") {
		rawName, rawValue, found := strings.Cut(component, "=")
		if !found || rawName == "" || rawValue == "" {
			continue
		}
		add(component, false)

		name, nameErr := url.QueryUnescape(rawName)
		value, valueErr := url.QueryUnescape(rawValue)
		if nameErr != nil || valueErr != nil || name == "" || value == "" {
			continue
		}
		nameForms := queryEncodingForms(rawName, name)
		valueForms := queryEncodingForms(rawValue, value)
		for _, nameForm := range nameForms {
			for _, valueForm := range valueForms {
				add(nameForm+"="+valueForm, false)
			}
		}
		for _, separator := range []string{": ", " = "} {
			add(name+separator+value, false)
		}
		if len(value) >= minStandaloneQueryValueBytes {
			for _, valueForm := range valueForms {
				add(valueForm, true)
			}
		}
	}

	ordered := make([]queryRedactionCandidate, 0, len(candidates))
	for text, standalone := range candidates {
		ordered = append(ordered, queryRedactionCandidate{text: text, standalone: standalone})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i].text) != len(ordered[j].text) {
			return len(ordered[i].text) > len(ordered[j].text)
		}
		return ordered[i].text < ordered[j].text
	})
	return ordered
}

func queryPercentHexCase(value string, upper bool) string {
	var normalized []byte
	for i := 0; i+2 < len(value); i++ {
		if value[i] != '%' || !isQueryHex(value[i+1]) || !isQueryHex(value[i+2]) {
			continue
		}
		for _, at := range []int{i + 1, i + 2} {
			want := value[at]
			if upper && want >= 'a' && want <= 'f' {
				want -= 'a' - 'A'
			} else if !upper && want >= 'A' && want <= 'F' {
				want += 'a' - 'A'
			}
			if want != value[at] {
				if normalized == nil {
					normalized = []byte(value)
				}
				normalized[at] = want
			}
		}
		i += 2
	}
	if normalized == nil {
		return value
	}
	return string(normalized)
}

func isQueryHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func queryEncodingForms(raw, decoded string) []string {
	forms := []string{raw, decoded}
	escaped := url.QueryEscape(decoded)
	forms = append(forms, escaped, strings.ReplaceAll(escaped, "+", "%20"))

	seen := make(map[string]struct{}, len(forms))
	unique := forms[:0]
	for _, form := range forms {
		if form == "" {
			continue
		}
		if _, ok := seen[form]; ok {
			continue
		}
		seen[form] = struct{}{}
		unique = append(unique, form)
	}
	return unique
}

func replaceStandaloneQueryValue(message, candidate string) string {
	if candidate == "" || !strings.Contains(message, candidate) {
		return message
	}

	var redacted strings.Builder
	start := 0
	changed := false
	for start < len(message) {
		relative := strings.Index(message[start:], candidate)
		if relative < 0 {
			break
		}
		at := start + relative
		end := at + len(candidate)
		if queryValueBoundaries(message, at, end, candidate) {
			redacted.WriteString(message[start:at])
			redacted.WriteString("[query]")
			start = end
			changed = true
			continue
		}
		redacted.WriteString(message[start:end])
		start = end
	}
	if !changed {
		return message
	}
	redacted.WriteString(message[start:])
	return redacted.String()
}

func queryValueBoundaries(message string, start, end int, candidate string) bool {
	first, _ := utf8.DecodeRuneInString(candidate)
	last, _ := utf8.DecodeLastRuneInString(candidate)
	if queryWordRune(first) && start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(message[:start])
		if queryWordRune(previous) {
			return false
		}
	}
	if queryWordRune(last) && end < len(message) {
		next, _ := utf8.DecodeRuneInString(message[end:])
		if queryWordRune(next) {
			return false
		}
	}
	return true
}

func queryWordRune(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsNumber(value)
}

// RedactQueryError applies RedactQuery without losing classification or wrapped
// causes such as context cancellation.
func RedactQueryError(err error, query string) error {
	message := RedactQuery(err.Error(), query)
	if message == err.Error() {
		return err
	}
	metadata, _ := RateLimitMetadataOf(err)
	return &Error{
		Kind:      KindOf(err),
		Err:       &sanitizedMessage{msg: message, err: err},
		RateLimit: metadata,
	}
}

// KindOf reports the Kind of err, walking wrapped errors, and KindUnknown when
// nothing in the chain is classified.
func KindOf(err error) Kind {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return KindUnknown
}
