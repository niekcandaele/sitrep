package termtexttest_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

// The sweep's promise is that a field added tomorrow is covered. A kind it has
// no policy for used to fall through silently, which turns that promise into a
// field nobody is checking; it now says so.
func TestFillPanicsOnAKindItHasNoPolicyFor(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"map", &struct{ Labels map[string]string }{}},
		{"interface", &struct{ Payload any }{}},
		{"channel", &struct{ Updates chan string }{}},
		{"func", &struct{ Render func() string }{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				recovered, ok := recover().(string)
				if !ok {
					t.Fatal("Fill accepted a field it has no policy for")
				}
				if !strings.Contains(recovered, "no policy for") {
					t.Errorf("panic = %q, want it to say a policy is needed", recovered)
				}
			}()
			termtexttest.Fill(tt.value)
		})
	}
}

// AssertClean fails the test rather than panicking: it already holds a
// testing.TB, and a failure there names the field path the way its other
// failures do.
func TestAssertCleanFailsOnAKindItHasNoPolicyFor(t *testing.T) {
	fake := &recordingTB{TB: t}
	termtexttest.AssertClean(fake, "value", struct{ Labels map[string]string }{})

	if !fake.failed {
		t.Fatal("AssertClean accepted a field it has no policy for")
	}
	if !strings.Contains(fake.message, "Labels") || !strings.Contains(fake.message, "no policy for") {
		t.Errorf("failure = %q, want it to name the field and say a policy is needed", fake.message)
	}
}

// recordingTB captures the one Fatalf AssertClean makes without ending this
// test. Fatalf must not call runtime.Goexit here, so it returns normally and
// the sweep unwinds on its own.
type recordingTB struct {
	testing.TB
	failed  bool
	message string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.record(format, args...)
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.record(format, args...)
}

func (r *recordingTB) record(format string, args ...any) {
	r.failed = true
	r.message = fmt.Sprintf(format, args...)
}
