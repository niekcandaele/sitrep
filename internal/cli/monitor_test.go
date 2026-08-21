package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// Neither --json nor --plain means the live monitor, and the monitor needs a
// terminal. In a test — a pipe on both ends — it cannot start, and the failure
// has to name the way out rather than leaving the user staring at a terminfo
// error.
func TestMonitorWithoutATerminalExplainsTheOneShotModes(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.RunWith([]string{"111"}, &stdout, &stderr, cli.Deps{
		Provider: fake.New(),
		Stdin:    strings.NewReader(""),
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty: a failed monitor writes nothing to stdout", stdout.String())
	}
	for _, want := range []string{"sitrep: ", "needs a terminal", "--plain", "--json"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr.String(), want)
		}
	}
}

// --interval is meaningless to a one-shot render, but a script that sets it
// once and switches modes should not start failing over a setting it is not
// using. The one-shot modes ignore it, including values the monitor rejects.
func TestOneShotModesIgnoreTheInterval(t *testing.T) {
	for _, mode := range []string{"--json", "--plain"} {
		got := run([]string{"111", mode, "--interval", "0"}, fake.New())

		if got.code != 0 {
			t.Errorf("%s with --interval 0: exit code = %d, want 0 (stderr: %q)", mode, got.code, got.stderr)
		}
		if got.stdout == "" {
			t.Errorf("%s with --interval 0 produced no report", mode)
		}
	}
}
