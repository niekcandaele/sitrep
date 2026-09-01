package tui

import (
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/termtext"
)

// This file is the terminal-visible-text boundary for every screen in this
// package (ADR-0006):
//
//	A model value becomes safe to draw at the moment it enters a screen's
//	state, not at the moment it is rendered, and not because of who produced
//	it.
//
// The monitor takes model data through five funnels — Options.Initial,
// Options.Open, Options.Source, Options.DetailSource and Options.DetailFanout —
// and none of them has to come from a Provider: each source may be an arbitrary
// closure, and a caller that never went near provider.Sanitized can seat whatever
// it likes. So the package makes its own inputs safe here, at intake, and the
// renderers below stop being where safety is decided. A new screen consuming a
// ListInput or DetailInput inherits that without doing anything.
//
// The functions here are the only sanitizing calls in this package, with one
// stated exception: renderHyperlink cleans its text and URI for scope
// integrity, which is about the OSC 8 sequence it writes rather than about the
// model. See its comment.
//
// The policy itself lives in internal/termtext. A new rule about what text may
// reach a terminal belongs there, not here.

// safeHeader cleans a breadcrumb or Watchlist header.
func safeHeader(h Header) Header {
	cleaned := termtext.Header(model.WatchlistHeader{Key: h.Key, Title: h.Title, URL: h.URL})
	return Header{Key: cleaned.Key, Title: cleaned.Title, URL: cleaned.URL}
}

// safeListInput cleans one reading of the Watchlist on its way into Model
// state, whether it was seated by the caller or produced by a refresh.
func safeListInput(in ListInput) ListInput {
	in.Header = safeHeader(in.Header)
	in.Tickets = termtext.Tickets(in.Tickets)
	return in
}

// safeFrontierInput cleans one Frontier seat. The Links map's key presence is
// the tri-state the blocking graph reads, so a key is copied across whether or
// not its slice has entries: cleaning must not turn "read, and there are none"
// into "never read".
func safeFrontierInput(in FrontierInput) FrontierInput {
	in.Header = safeHeader(in.Header)
	in.Tickets = termtext.Tickets(in.Tickets)
	if in.Links != nil {
		links := make(map[model.TicketID][]model.Link, len(in.Links))
		for id, l := range in.Links {
			links[id] = termtext.Links(l)
		}
		in.Links = links
	}
	return in
}

// safeOpen cleans the decoder's entry Ticket and its breadcrumb.
func safeOpen(open OpenTicket) OpenTicket {
	open.Ticket = termtext.Ticket(open.Ticket)
	open.Parent = safeHeader(open.Parent)
	return open
}

// safeDetail cleans one Detail reading. Capabilities are bools and carry no
// text.
func safeDetail(d model.Detail) model.Detail {
	return termtext.Detail(d)
}

// safeErr cleans error text from Source, DetailSource, or DetailFanout before a
// footer, error document, or Frontier failure renders it. provider.Errorf already
// cleans a classified Provider error; a directly constructed closure can return
// any error at all.
func safeErr(err error) error {
	return termtext.Err(err)
}
