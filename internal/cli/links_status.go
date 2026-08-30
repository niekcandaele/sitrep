package cli

import (
	"fmt"
	"io"

	"github.com/niekcandaele/sitrep/internal/buildinfo"
	"github.com/niekcandaele/sitrep/internal/detailfanout"
	"github.com/niekcandaele/sitrep/internal/terminal"
)

const noBlockingLinksCapabilityNotice = "--links: blocking keys are absent because this Provider does not declare the blocking_links Capability"

type linksStatus struct {
	writer      io.Writer
	interactive bool
	planned     int
	done        int
	failed      int
	notice      string
	displayed   bool
}

func newLinksStatus(writer io.Writer, planned int) *linksStatus {
	status := &linksStatus{
		writer:      writer,
		interactive: terminal.Is(writer),
		planned:     planned,
	}
	if status.interactive && planned > 0 {
		status.redraw()
	}
	return status
}

func (s *linksStatus) record(outcome detailfanout.Outcome) {
	s.done++
	if outcome.Err != nil {
		s.failed++
	}
	if s.displayed {
		s.redraw()
	}
}

func newMissingBlockingLinksStatus(writer io.Writer) *linksStatus {
	return &linksStatus{writer: writer, notice: noBlockingLinksCapabilityNotice}
}

func (s *linksStatus) complete() {
	if !s.displayed {
		return
	}
	// The progress line is transient and ends with the fan-out, before the CLI
	// commits to either an interrupt or a completed report.
	_, _ = fmt.Fprint(s.writer, "\r\x1b[2K")
	s.displayed = false
}

func (s *linksStatus) report() {
	notice := s.notice
	if notice == "" {
		notice = detailfanout.UnreadableLinksNotice(s.failed)
	}
	if notice != "" {
		_, _ = fmt.Fprintf(s.writer, "%s: %s\n", buildinfo.Name, notice)
	}
}

func (s *linksStatus) redraw() {
	_, _ = fmt.Fprintf(s.writer, "\rreading Detail %d/%d", s.done, s.planned)
	s.displayed = true
}
