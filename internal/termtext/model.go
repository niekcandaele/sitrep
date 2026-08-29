package termtext

import "github.com/niekcandaele/sitrep/internal/model"

// The walkers below are hand-written and name every field they touch, so the
// policy each field takes — Line, Body, or deliberately untouched — is readable
// at the field. TestWalkersCleanEveryModelField proves the coverage is total:
// a string field added to a walked type fails that test until someone decides
// what policy it takes.
//
// Identity is outside the boundary. model.TicketID is never drawn — a screen
// shows a Key, never an ID — and cleaning it would corrupt the Detail cache key
// and the Provider re-reads that use it.
//
// Slice walkers clean in place and return the same slice: a caller handing one
// over has handed it over.

// Header cleans a Watchlist's display identity.
func Header(h model.WatchlistHeader) model.WatchlistHeader {
	h.Key = Line(h.Key)
	h.Title = Line(h.Title)
	h.URL = Line(h.URL)
	return h
}

// Epic cleans a Watchlist's outer root.
func Epic(e model.Epic) model.Epic {
	e.Key = Line(e.Key)
	e.Title = Line(e.Title)
	e.URL = Line(e.URL)
	e.NativeStatus = Line(e.NativeStatus)
	e.Repository = Line(e.Repository)
	e.Assignees = Users(e.Assignees)
	e.PullRequests = PullRequests(e.PullRequests)
	return e
}

// Ticket cleans one list-model Ticket.
func Ticket(t model.Ticket) model.Ticket {
	t.Key = Line(t.Key)
	t.Title = Line(t.Title)
	t.URL = Line(t.URL)
	t.NativeStatus = Line(t.NativeStatus)
	t.Repository = Line(t.Repository)
	t.Assignees = Users(t.Assignees)
	t.PullRequests = PullRequests(t.PullRequests)
	return t
}

// Tickets cleans a Watchlist's members.
func Tickets(tickets []model.Ticket) []model.Ticket {
	for i := range tickets {
		tickets[i] = Ticket(tickets[i])
	}
	return tickets
}

// Parent cleans a breadcrumb Ticket.
func Parent(p model.Parent) model.Parent {
	p.Key = Line(p.Key)
	p.Title = Line(p.Title)
	p.URL = Line(p.URL)
	return p
}

// User cleans one referenced person.
func User(u model.User) model.User {
	u.Login = Line(u.Login)
	u.DisplayName = Line(u.DisplayName)
	u.AvatarURL = Line(u.AvatarURL)
	return u
}

// Users cleans a set of referenced people.
func Users(users []model.User) []model.User {
	for i := range users {
		users[i] = User(users[i])
	}
	return users
}

// PullRequests cleans the code moving a Ticket.
func PullRequests(prs []model.PullRequest) []model.PullRequest {
	for i := range prs {
		prs[i].Title = Line(prs[i].Title)
		prs[i].URL = Line(prs[i].URL)
		prs[i].Repository = Line(prs[i].Repository)
	}
	return prs
}

// Links cleans a Ticket's relationships and their targets.
func Links(links []model.Link) []model.Link {
	for i := range links {
		links[i].NativeLabel = Line(links[i].NativeLabel)
		links[i].Target.Key = Line(links[i].Target.Key)
		links[i].Target.Title = Line(links[i].Target.Title)
		links[i].Target.URL = Line(links[i].Target.URL)
		links[i].Target.NativeStatus = Line(links[i].Target.NativeStatus)
	}
	return links
}

// Detail cleans the expensive per-Ticket data. Description and comment bodies
// take the Body policy; everything else on a Detail is a single line.
func Detail(d model.Detail) model.Detail {
	d.Description = Body(d.Description)
	for i := range d.Comments {
		d.Comments[i].ID = Line(d.Comments[i].ID)
		d.Comments[i].Author = User(d.Comments[i].Author)
		d.Comments[i].Body = Body(d.Comments[i].Body)
		d.Comments[i].URL = Line(d.Comments[i].URL)
	}
	d.Links = Links(d.Links)
	return d
}

// Snapshot cleans one batched reading of a Watchlist.
func Snapshot(snap model.WatchlistSnapshot) model.WatchlistSnapshot {
	snap.Header = Header(snap.Header)
	snap.Epic = Epic(snap.Epic)
	snap.Parent = Parent(snap.Parent)
	snap.Tickets = Tickets(snap.Tickets)
	return snap
}
