package plain

import (
	"fmt"
	"io"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// TicketSnapshot is everything the one-shot Ticket report renders: the decoded
// Ticket, the collection it belongs to, and the Detail read for it. It is the
// --plain twin of the Detail screen, and like everything else in this package it
// is rendered as a pure function of its input.
type TicketSnapshot struct {
	// Ticket is the decoded Ticket in list-model form.
	Ticket model.Ticket
	// Parent is the collection it belongs to. The zero Parent draws no Epic
	// line.
	Parent model.Parent
	// Detail is the expensive per-ticket data, exactly as the Provider returned
	// it.
	Detail model.Detail
	// Capabilities decide which sections may be drawn; an undeclared Capability
	// is silently absent, never an error.
	Capabilities model.Capabilities
}

// commentTimeLayout formats a comment's timestamp. It is absolute and in UTC:
// a comment's time is a fact about the Tracker, and time.Local in a report is a
// line that reads differently in Amsterdam and in CI.
const commentTimeLayout = "2006-01-02 15:04"

// commentIndent is the two columns a comment body is inset by, so a comment
// reads as one block under its author.
const commentIndent = "  "

// RenderTicket writes the one-shot text snapshot of one Ticket to w: its
// identity and the same meta line the epic report shows for it, a breadcrumb to
// the Epic it belongs to, then its description, comments and links.
//
// The description is written exactly as the Tracker returned it — markdown
// source, its own line breaks, no re-wrapping. This mode has no width to wrap
// to: it never probes the terminal, and a markdown body already carries the
// author's line breaks.
func RenderTicket(w io.Writer, in TicketSnapshot) error {
	var b strings.Builder

	writeTicketHeader(&b, in)
	writeDescription(&b, in.Detail.Description)
	writeComments(&b, in)
	writeLinks(&b, in)

	_, err := io.WriteString(w, b.String())
	return err
}

// writeTicketHeader writes the Ticket's identity, its meta line, its URL and the
// Epic breadcrumb. A Ticket that hangs off nothing gets no Epic line at all.
func writeTicketHeader(b *strings.Builder, in TicketSnapshot) {
	// Output that starts flush against a shell prompt is hard to read.
	b.WriteString("\n")
	t := in.Ticket
	fmt.Fprintf(b, "Ticket %s  %s\n", t.Key, Truncate(t.Title, maxTitleWidth))

	if meta := ticketMeta(t, in.Capabilities); meta != "" {
		fmt.Fprintf(b, "%s\n", meta)
	}
	if t.URL != "" {
		fmt.Fprintf(b, "%s\n", t.URL)
	}
	if !in.Parent.IsZero() {
		fmt.Fprintf(b, "Epic %s  %s\n", in.Parent.Key, Truncate(in.Parent.Title, maxTitleWidth))
	}
	b.WriteString("\n")
}

// writeDescription writes the description section, which is always drawn: an
// empty description is a fact about the Ticket, and a report that answers with
// nothing at all reads as a bug.
func writeDescription(b *strings.Builder, description string) {
	b.WriteString("DESCRIPTION\n\n")
	if strings.TrimSpace(description) == "" {
		b.WriteString("No description.\n\n")
		return
	}
	fmt.Fprintf(b, "%s\n\n", strings.TrimRight(description, "\n"))
}

// writeComments writes the comments section, or nothing at all when the Provider
// does not declare the Comments Capability. That silence is the point: an
// undeclared Capability is absent, never an error and never a placeholder. With
// the Capability and no comments there is something to say, so it is said.
func writeComments(b *strings.Builder, in TicketSnapshot) {
	if !in.Capabilities.Comments {
		return
	}
	comments := in.Detail.Comments
	if len(comments) == 0 {
		b.WriteString("COMMENTS\n\nNo comments yet.\n\n")
		return
	}

	// The heading counts what arrived: model.Detail has nowhere to carry the
	// Tracker's own total, and inventing one here would be a guess.
	fmt.Fprintf(b, "COMMENTS (%d)\n\n", len(comments))
	for _, c := range comments {
		fmt.Fprintf(b, "%s\n", commentByline(c))
		for _, line := range strings.Split(strings.TrimRight(c.Body, "\n"), "\n") {
			fmt.Fprintf(b, "%s%s\n", commentIndent, line)
		}
		b.WriteString("\n")
	}
}

// commentByline renders "@login · 2026-01-12 09:14 UTC", the same way the
// monitor's Detail screen does. A comment whose author has no login — a deleted
// account — is shown as @unknown rather than dropped.
func commentByline(c model.Comment) string {
	login := c.Author.Login
	if login == "" {
		login = "unknown"
	}
	return "@" + login + " · " + c.CreatedAt.UTC().Format(commentTimeLayout) + " UTC"
}

// writeLinks writes the links section as aligned columns: the Tracker's own
// label, the target's key, its title and its Native Status. The label is the
// meaning — displayed, never interpreted — so nothing here branches on the Kind.
func writeLinks(b *strings.Builder, in TicketSnapshot) {
	links := model.VisibleLinks(in.Detail.Links, in.Capabilities)
	if len(links) == 0 {
		return
	}

	// Every column is measured over the links actually shown, so the report
	// lines up without a fixed width guessing at the Tracker's wording.
	labelColumn, keyColumn, titleColumn := 0, 0, 0
	titles := make([]string, len(links))
	for i, l := range links {
		titles[i] = Truncate(l.Target.Title, maxTitleWidth)
		labelColumn = max(labelColumn, len([]rune(linkLabel(l)))+keyColumnPadding)
		keyColumn = max(keyColumn, len([]rune(l.Target.Key))+keyColumnPadding)
		titleColumn = max(titleColumn, len([]rune(titles[i]))+keyColumnPadding)
	}

	fmt.Fprintf(b, "LINKS (%d)\n\n", len(links))
	for i, l := range links {
		line := PadKey(linkLabel(l), labelColumn) + PadKey(l.Target.Key, keyColumn)
		if l.Target.NativeStatus == "" {
			fmt.Fprintf(b, "%s%s\n", line, titles[i])
			continue
		}
		fmt.Fprintf(b, "%s%s[%s]\n", line, PadKey(titles[i], titleColumn), l.Target.NativeStatus)
	}
	b.WriteString("\n")
}

// linkLabel is the Tracker's own wording for a relationship, falling back to the
// LinkKind's own token when the Tracker supplied none — spelled with spaces,
// because "blocked_by" is a wire token and this is a sentence a human reads.
func linkLabel(l model.Link) string {
	if l.NativeLabel != "" {
		return l.NativeLabel
	}
	return strings.ReplaceAll(l.Kind.String(), "_", " ")
}
