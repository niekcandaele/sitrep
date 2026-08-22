package cli

import (
	"context"
	"io"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/render/jsonout"
	"github.com/niekcandaele/sitrep/internal/render/plain"
	"github.com/niekcandaele/sitrep/internal/tui"
)

// decodesToTicket reports whether the Ref that produced snap named a plain
// Ticket rather than a Watchlist. It is the decode rule, and it lives here
// because a Provider reports what a Tracker says and never picks a screen.
//
// Children are the signal, because they are the only one that is true
// everywhere: GitHub has no epic type in the API sitrep uses, Jira's is an issue
// type a site can rename, and GitLab's depends on the tier. The cost is that an
// Epic created but not yet populated opens as its own Detail — which shows the
// plan the agent is about to work from, and is a better screen than "This
// Watchlist has no Tickets.". Changing that decision is changing this line.
func decodesToTicket(snap model.WatchlistSnapshot) bool { return len(snap.Tickets) == 0 }

// decodedTicket reads the snapshot's root node as the Ticket it turned out to
// name. It is the only place that conversion happens: one node has one
// representation, and the Ticket is what both the Detail screen and the one-shot
// reports are built from.
func decodedTicket(snap model.WatchlistSnapshot) model.Ticket {
	return model.Ticket{
		ID:           snap.Epic.ID,
		Key:          snap.Epic.Key,
		Title:        snap.Epic.Title,
		URL:          snap.Epic.URL,
		Status:       snap.Epic.Status,
		NativeStatus: snap.Epic.NativeStatus,
		Assignees:    snap.Epic.Assignees,
		Repository:   snap.Epic.Repository,
		PullRequests: snap.Epic.PullRequests,
	}
}

// runDecodedOneShot writes the decoded Ticket as a one-shot report: the JSON
// ticket document, or its text twin.
//
// The Detail is fetched here and only here (ADR-0003). A Detail that cannot be
// read is a runtime failure rather than a report: the decoded identity alone is
// not worth printing as a success.
func runDecodedOneShot(ctx context.Context, stdout, stderr io.Writer, p provider.Provider,
	snap model.WatchlistSnapshot, asJSON bool) int {
	detail, err := p.FetchDetail(ctx, snap.Epic.ID)
	if err != nil {
		if code, ok := interrupted(ctx); ok {
			return code
		}
		return runtimeError(stderr, err)
	}

	ticket := decodedTicket(snap)
	if asJSON {
		return writeReport(stdout, stderr, func(w io.Writer) error {
			return jsonout.RenderTicket(w, jsonout.TicketDocument{
				Ticket:       ticket,
				Parent:       snap.Parent,
				Detail:       detail,
				Capabilities: snap.Capabilities,
				ProviderName: p.Name(),
				GeneratedAt:  snap.FetchedAt,
			})
		})
	}
	return writeReport(stdout, stderr, func(w io.Writer) error {
		return plain.RenderTicket(w, plain.TicketSnapshot{
			Ticket:       ticket,
			Parent:       snap.Parent,
			Detail:       detail,
			Capabilities: snap.Capabilities,
		})
	})
}

// runDecodedMonitor opens the monitor on the decoded Ticket's Detail, with a
// breadcrumb to the Epic it belongs to and a Source to walk up into.
//
// The Source is what the walk-up key needs, and it is built here because the TUI
// knows nothing about Refs: the parent's Key and URL are written in forms the
// grammar accepts, so walking up is a re-parse of what the Provider already
// returned. A Ticket with no parent, or a parent that resolves to nothing
// usable, gets no Source — there is no Watchlist to monitor, and the key is
// simply not offered.
func runDecodedMonitor(ctx context.Context, stdout, stderr io.Writer, deps Deps,
	p provider.Provider, r ref.Ref, snap model.WatchlistSnapshot, interval time.Duration) int {
	open, source := decodedMonitorOptions(p, r, snap, deps.clock())

	return runMonitor(ctx, stdout, stderr, deps, tui.Options{
		Source:       source,
		DetailSource: tui.TicketDetailSource(p),
		Open:         &open,
		Interval:     interval,
	})
}

// decodedMonitorOptions builds what the decoded monitor opens on: the Ticket
// itself, its breadcrumb, and the Source the walk-up key reads.
//
// It is separate from runDecodedMonitor because everything above is a pure
// function of the snapshot and the Ref, while runMonitor needs a terminal — so
// this half can be tested and that half is one call.
func decodedMonitorOptions(p provider.Provider, r ref.Ref, snap model.WatchlistSnapshot,
	clock func() time.Time) (tui.OpenTicket, tui.Source) {
	open := tui.OpenTicket{
		Ticket:       decodedTicket(snap),
		Capabilities: snap.Capabilities,
	}
	if snap.Parent.IsZero() {
		return open, nil
	}

	open.Parent = tui.Header{Key: snap.Parent.Key, Title: snap.Parent.Title, URL: snap.Parent.URL}
	parent, err := ref.ParseParent(snap.Parent.Key, snap.Parent.URL, r)
	if err != nil {
		// The breadcrumb still shows what the Ticket belongs to; there is just
		// nothing to walk up into, so the key is not offered rather than
		// offered and broken.
		return open, nil
	}
	return open, tui.SelectorSource(p, provider.EpicSelector{Ref: parent}, clock)
}
