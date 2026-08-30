package terminal_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/niekcandaele/sitrep/internal/terminal"
)

func TestIsRejectsValueWithoutFileDescriptor(t *testing.T) {
	if terminal.Is(&bytes.Buffer{}) {
		t.Error("Is(buffer) = true, want false")
	}
}

func TestIsRejectsPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	if terminal.Is(reader) {
		t.Error("Is(pipe reader) = true, want false")
	}
	if terminal.Is(writer) {
		t.Error("Is(pipe writer) = true, want false")
	}
}
