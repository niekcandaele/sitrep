// Package providertest holds the one implementation of the error prose
// contract described in internal/provider/errors.go, so that three drivers
// cannot drift apart on what a failure reads like.
//
// It is an ordinary package rather than a _test one because three driver test
// packages import it. Nothing outside a test may.
package providertest

import (
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/provider"
)

// Want is what one failure case must produce.
type Want struct {
	// Kind is the classification the driver must attach.
	Kind provider.Kind
	// Contains are substrings the message must carry: the thing that failed and
	// the thing to do about it.
	Contains []string
	// Secret is a credential that must not appear anywhere in the message.
	// Optional — an error raised before any credential exists has none to leak.
	Secret string
}

// CheckError asserts the whole error contract for one driver failure: it is
// non-nil, one line, attributed to the driver, carries every Contains
// substring, leaks no Secret, and is classified as want.Kind. driver is
// "github", "jira" or "gitlab".
//
// Every failure message quotes the whole error, because that is the sentence a
// human reads at 2am and the only way to tell a near-miss from a nonsense one.
func CheckError(t *testing.T, driver string, err error, want Want) {
	t.Helper()

	if err == nil {
		t.Fatalf("no error, want a %s failure classified %s", driver, want.Kind)
	}
	msg := err.Error()

	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("error %q spans more than one line; a terminal error is one sentence", msg)
	}
	if prefix := driver + ": "; !strings.HasPrefix(msg, prefix) {
		t.Errorf("error %q is not attributed to the driver (want the prefix %q)", msg, prefix)
	}
	for _, substr := range want.Contains {
		if !strings.Contains(msg, substr) {
			t.Errorf("error %q does not mention %q", msg, substr)
		}
	}
	if want.Secret != "" && strings.Contains(msg, want.Secret) {
		t.Errorf("error %q leaks a credential; a credential never reaches an error message", msg)
	}
	if got := provider.KindOf(err); got != want.Kind {
		t.Errorf("error %q is classified %s, want %s", msg, got, want.Kind)
	}
}

// CheckCoversTheNamedClasses asserts that a driver's failure table exercises
// the three classes the user story names — a bad ref, an auth failure and rate
// limiting — so that deleting the only rate-limit row fails loudly rather than
// quietly.
func CheckCoversTheNamedClasses(t *testing.T, driver string, kinds []provider.Kind) {
	t.Helper()

	seen := map[provider.Kind]bool{}
	for _, k := range kinds {
		seen[k] = true
	}
	for _, want := range []provider.Kind{provider.KindBadRef, provider.KindAuth, provider.KindRateLimit} {
		if !seen[want] {
			t.Errorf("the %s failure table has no %s case; the ticket promises all three explain themselves",
				driver, want)
		}
	}
}
