package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/tui"
)

// README states ctrl+c prints nothing and exits 130. That was true of the
// one-shot path, where a signal reaches the process, and false in the monitor,
// where raw mode delivers ctrl+c as a key press and tea.Quit returns nil.
func TestMonitorExit(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name       string
		ctx        context.Context
		err        error
		wantCode   int
		wantStderr string
	}{
		{
			name:     "a clean quit",
			ctx:      context.Background(),
			wantCode: exitOK,
		},
		{
			name:     "ctrl+c at the monitor",
			ctx:      context.Background(),
			err:      tui.ErrInterrupted,
			wantCode: exitInterrupted,
		},
		{
			name:     "a wrapped interrupt is still an interrupt",
			ctx:      context.Background(),
			err:      errors.Join(errors.New("closing"), tui.ErrInterrupted),
			wantCode: exitInterrupted,
		},
		{
			name:     "a signal from outside, with no error",
			ctx:      cancelled,
			wantCode: exitInterrupted,
		},
		{
			name:       "no terminal",
			ctx:        context.Background(),
			err:        tui.ErrNoTerminal,
			wantCode:   exitFailure,
			wantStderr: "needs a terminal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, failure := monitorExit(tt.ctx, tt.err)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStderr == "" {
				if failure != nil {
					t.Errorf("failure = %v, want none: this path prints nothing", failure)
				}
				return
			}
			if failure == nil {
				t.Fatalf("failure = nil, want one mentioning %q", tt.wantStderr)
			}
			if !strings.Contains(failure.Error(), tt.wantStderr) {
				t.Errorf("failure = %q, want it to mention %q", failure, tt.wantStderr)
			}
		})
	}
}

func TestReadStdinRefsUsesUnicodeWhitespaceOnly(t *testing.T) {
	input := "  acme/widgets#112\t#38\r\n\n-\v--json\fABC-123 acme/widgets#121  "
	got, err := readStdinRefs(strings.NewReader(input))
	if err != nil {
		t.Fatalf("readStdinRefs: %v", err)
	}
	want := []string{
		"acme/widgets#112",
		"#38",
		"-",
		"--json",
		"ABC-123",
		"acme/widgets#121",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Refs = %#v, want %#v", got, want)
	}
}

func TestReadStdinRefsEnforcesByteBoundary(t *testing.T) {
	exact := strings.Repeat("x", maxStdinSelectorBytes)
	got, err := readStdinRefs(strings.NewReader(exact))
	if err != nil {
		t.Fatalf("exact-boundary read: %v", err)
	}
	if !reflect.DeepEqual(got, []string{exact}) {
		t.Errorf("exact-boundary Refs = %#v, want the one complete token", got)
	}

	got, err = readStdinRefs(strings.NewReader(exact + "x"))
	if err == nil || !strings.Contains(err.Error(), "stdin Selector input exceeds the 1048576-byte limit") {
		t.Fatalf("one-byte-over error = %v, want the stdin Selector byte boundary", err)
	}
	if got != nil {
		t.Errorf("one-byte-over Refs = %#v, want nil", got)
	}

	if _, err := readStdinRefs(strings.NewReader(" \n\t\r")); err == nil || err.Error() != "no Refs were provided on stdin" {
		t.Errorf("whitespace-only error = %v, want the empty-input contract", err)
	}
}

type selectorReadError struct{ err error }

func (r selectorReadError) Read([]byte) (int, error) { return 0, r.err }

func TestReadStdinRefsWrapsReaderFailure(t *testing.T) {
	cause := errors.New("selector stream failed")
	refs, err := readStdinRefs(selectorReadError{err: cause})
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "reading Refs from stdin") {
		t.Fatalf("error = %v, want wrapped reader cause", err)
	}
	if refs != nil {
		t.Errorf("Refs = %#v, want nil", refs)
	}
}
