package terminal_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/niekcandaele/sitrep/internal/terminal"
)

// TestIsRejectsNonTerminals covers the branch this package owns: a value only
// qualifies when it exposes an Fd. The positive case needs a real terminal and
// lives in the linux-only PTY test beside this one.
func TestIsRejectsNonTerminals(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		reader.Close()
		writer.Close()
	})

	for _, test := range []struct {
		name  string
		value any
	}{
		{"no file descriptor", &bytes.Buffer{}},
		{"pipe reader", reader},
		{"pipe writer", writer},
		{"nil-ish reader", io.Reader(&bytes.Buffer{})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if terminal.Is(test.value) {
				t.Errorf("Is(%s) = true, want false", test.name)
			}
		})
	}
}
