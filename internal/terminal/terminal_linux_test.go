//go:build linux

package terminal_test

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/niekcandaele/sitrep/internal/terminal"
)

func TestIsAcceptsPTY(t *testing.T) {
	master, slave := openPTY(t)
	defer master.Close()
	defer slave.Close()

	if !terminal.Is(slave) {
		t.Error("Is(PTY slave) = false, want true")
	}
}

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	return master, slave
}
