package cli

import (
	"context"
	"errors"
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
