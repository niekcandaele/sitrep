//go:build linux

package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

func TestJSONLinksTTYRedrawsProgressAndClearsBeforeWarning(t *testing.T) {
	master, slave := openLinksPTY(t)
	var stdout bytes.Buffer
	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, slave, cli.Deps{
		Provider: fake.New(fake.WithBlockingFixture()),
		Now:      fixedClock,
	})
	status := closeAndReadPTY(t, master, slave)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	members := len(fake.FixtureBlockingSnapshot().Tickets)
	for done := 0; done <= members; done++ {
		want := fmt.Sprintf("\rreading Detail %d/%d", done, members)
		if !strings.Contains(status, want) {
			t.Errorf("stderr does not contain progress redraw %q: %q", want, status)
		}
	}
	cleanup := "\r\x1b[2K"
	if !strings.Contains(status, cleanup) {
		t.Errorf("stderr does not clear the transient line: %q", status)
	}
	warning := strings.TrimSuffix(singularLinksNotice, "\n")
	if got := strings.Count(status, warning); got != 1 {
		t.Errorf("durable warning count = %d, want 1: %q", got, status)
	}
	if cleanupAt, warningAt := strings.LastIndex(status, cleanup), strings.Index(status, warning); cleanupAt < 0 || warningAt < cleanupAt {
		t.Errorf("cleanup at %d and warning at %d, want cleanup first: %q", cleanupAt, warningAt, status)
	}
	checkGolden(t, "blocking.golden.json", stdout.Bytes())
}

func TestJSONLinksTTYInterruptClearsProgressWithoutWarning(t *testing.T) {
	master, slave := openLinksPTY(t)
	var stdout bytes.Buffer
	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, slave, cli.Deps{
		Provider: interruptingDetailProvider{Provider: fake.New(fake.WithBlockingFixture())},
		Now:      fixedClock,
	})
	status := closeAndReadPTY(t, master, slave)

	if code != 130 {
		t.Errorf("exit code = %d, want 130", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no partial document", stdout.String())
	}
	if !strings.Contains(status, "\rreading Detail 0/") {
		t.Errorf("stderr does not contain initial progress: %q", status)
	}
	if !strings.Contains(status, "\r\x1b[2K") {
		t.Errorf("stderr does not clear progress on interrupt: %q", status)
	}
	if strings.Contains(status, "Links could not be read") {
		t.Errorf("stderr contains an aggregate warning after interrupt: %q", status)
	}
}

type emptyPlanProvider struct {
	*fake.Provider
}

func (p emptyPlanProvider) Resolve(ctx context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	snapshot, err := p.Provider.Resolve(ctx, selector)
	for i := range snapshot.Tickets {
		snapshot.Tickets[i].ID = ""
	}
	return snapshot, err
}

func TestJSONLinksTTYDoesNotDrawEmptyProgress(t *testing.T) {
	master, slave := openLinksPTY(t)
	var stdout bytes.Buffer
	code := cli.RunWith([]string{"200", "--json", "--links"}, &stdout, slave, cli.Deps{
		Provider: emptyPlanProvider{Provider: fake.New(fake.WithBlockingFixture())},
		Now:      fixedClock,
	})
	status := closeAndReadPTY(t, master, slave)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if status != "" {
		t.Errorf("stderr = %q, want no reading Detail 0/0 progress", status)
	}
	if stdout.Len() == 0 {
		t.Error("stdout is empty, want the completed Watchlist document")
	}
}

func openLinksPTY(t *testing.T) (*os.File, *os.File) {
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

func closeAndReadPTY(t *testing.T, master, slave *os.File) string {
	t.Helper()

	if err := slave.Close(); err != nil {
		master.Close()
		t.Fatal(err)
	}
	status, err := io.ReadAll(master)
	if closeErr := master.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil && !errors.Is(err, unix.EIO) {
		t.Fatal(err)
	}
	return string(status)
}
