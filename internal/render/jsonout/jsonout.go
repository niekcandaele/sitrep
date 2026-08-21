// Package jsonout renders sitrep's machine-readable output: the --json epic
// document, the matching Ticket detail document, and the decoder's ticket
// document — the detail document plus the Ticket's own identity and its parent,
// which is what --json emits for an Epic Ref that named a Ticket.
//
// The wire format is a contract, so it lives here as its own layer of DTO
// structs with json tags rather than tags scattered over the domain model.
// Refactoring internal/model then forces a deliberate decision about the wire
// format instead of silently changing it under consumers.
//
// Conventions, all pinned by golden tests:
//
//   - Keys are snake_case; schema_version comes first and is always 1.
//   - Times are RFC 3339 in UTC.
//   - tickets is always present and always an array, never null.
//   - Optional strings and capability-gated arrays are omitted when empty. An
//     undeclared Capability means the key is absent everywhere — absence is the
//     normal, silent way to say "this Tracker does not do that".
//
// Tickets are emitted flat, in Provider order, with a status field on each and
// a computed progress block alongside. Grouping is presentation: it belongs to
// the --plain renderer and the TUI, and any consumer can regroup a flat array
// trivially. Do not add grouping here.
package jsonout

import (
	"encoding/json"
	"io"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
)

// schemaVersion is the version of the documents this package emits. Bump it
// only for a breaking change to the shape.
const schemaVersion = 1

type providerDoc struct {
	Name         string          `json:"name"`
	Capabilities capabilitiesDoc `json:"capabilities"`
}

type capabilitiesDoc struct {
	Hierarchy     bool `json:"hierarchy"`
	BlockingLinks bool `json:"blocking_links"`
	Comments      bool `json:"comments"`
	PullRequests  bool `json:"pull_requests"`
}

type userDoc struct {
	Login       string `json:"login"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

type epicDoc struct {
	ID           model.TicketID       `json:"id"`
	Key          string               `json:"key"`
	Title        string               `json:"title"`
	URL          string               `json:"url"`
	Status       model.StatusCategory `json:"status"`
	NativeStatus string               `json:"native_status,omitempty"`
}

type progressDoc struct {
	Todo        int `json:"todo"`
	InProgress  int `json:"in_progress"`
	Done        int `json:"done"`
	Cancelled   int `json:"cancelled"`
	Unknown     int `json:"unknown"`
	Total       int `json:"total"`
	Denominator int `json:"denominator"`
	PercentDone int `json:"percent_done"`
}

type pullRequestDoc struct {
	Number     int               `json:"number"`
	Title      string            `json:"title"`
	URL        string            `json:"url"`
	Repository string            `json:"repository,omitempty"`
	State      model.PRState     `json:"state"`
	Review     model.ReviewState `json:"review"`
	Checks     model.CheckState  `json:"checks"`
}

type ticketDoc struct {
	ID           model.TicketID       `json:"id"`
	Key          string               `json:"key"`
	Title        string               `json:"title"`
	URL          string               `json:"url"`
	Status       model.StatusCategory `json:"status"`
	NativeStatus string               `json:"native_status,omitempty"`
	Repository   string               `json:"repository,omitempty"`
	ParentID     model.TicketID       `json:"parent_id,omitempty"`
	Assignees    []userDoc            `json:"assignees,omitempty"`
	PullRequests []pullRequestDoc     `json:"pull_requests,omitempty"`
}

type epicDocument struct {
	SchemaVersion int         `json:"schema_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Provider      providerDoc `json:"provider"`
	Epic          epicDoc     `json:"epic"`
	Progress      progressDoc `json:"progress"`
	Tickets       []ticketDoc `json:"tickets"`
}

type commentDoc struct {
	ID        string    `json:"id"`
	Author    userDoc   `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	URL       string    `json:"url,omitempty"`
}

type linkTargetDoc struct {
	ID           model.TicketID       `json:"id"`
	Key          string               `json:"key"`
	Title        string               `json:"title"`
	URL          string               `json:"url"`
	Status       model.StatusCategory `json:"status"`
	NativeStatus string               `json:"native_status,omitempty"`
}

type linkDoc struct {
	Kind        model.LinkKind `json:"kind"`
	NativeLabel string         `json:"native_label,omitempty"`
	Target      linkTargetDoc  `json:"target"`
}

type parentDoc struct {
	ID    model.TicketID `json:"id"`
	Key   string         `json:"key,omitempty"`
	Title string         `json:"title,omitempty"`
	URL   string         `json:"url,omitempty"`
}

// detailDocument is both the detail document and the decoder's ticket document:
// the second is the first plus an optional ticket and parent. They are one
// struct rather than two because they are one shape — RenderDetail simply leaves
// the two optional objects nil, which is what keeps its output byte-identical
// and schema_version at 1.
type detailDocument struct {
	SchemaVersion int            `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Provider      providerDoc    `json:"provider"`
	Ticket        *ticketDoc     `json:"ticket,omitempty"`
	Parent        *parentDoc     `json:"parent,omitempty"`
	TicketID      model.TicketID `json:"ticket_id"`
	Description   string         `json:"description"`
	Comments      []commentDoc   `json:"comments,omitempty"`
	Links         []linkDoc      `json:"links,omitempty"`
}

// TicketDocument is the decoder's one-shot output: one Ticket's identity, the
// Epic it belongs to, and its Detail.
type TicketDocument struct {
	// Ticket is the decoded Ticket in list-model form.
	Ticket model.Ticket
	// Parent is the collection it belongs to. The zero Parent emits no parent
	// key at all.
	Parent model.Parent
	// Detail is the expensive per-ticket data, exactly as the Provider returned
	// it.
	Detail model.Detail
	// Capabilities decide which optional sections appear at all.
	Capabilities model.Capabilities
	// ProviderName is the serving Provider's Name.
	ProviderName string
	// GeneratedAt is the caller's clock reading, matching generated_at in the
	// epic document.
	GeneratedAt time.Time
}

// RenderEpic writes the epic document for one snapshot to w. providerName is
// the serving Provider's Name; the snapshot supplies everything else, including
// the generated_at timestamp its caller stamped into FetchedAt.
func RenderEpic(w io.Writer, snap model.EpicSnapshot, providerName string) error {
	doc := epicDocument{
		SchemaVersion: schemaVersion,
		GeneratedAt:   wireTime(snap.FetchedAt),
		Provider:      newProviderDoc(providerName, snap.Capabilities),
		Epic: epicDoc{
			ID:           snap.Epic.ID,
			Key:          snap.Epic.Key,
			Title:        snap.Epic.Title,
			URL:          snap.Epic.URL,
			Status:       snap.Epic.Status,
			NativeStatus: snap.Epic.NativeStatus,
		},
		Progress: newProgressDoc(model.ComputeProgress(snap.Tickets)),
		Tickets:  make([]ticketDoc, 0, len(snap.Tickets)),
	}
	for _, t := range snap.Tickets {
		doc.Tickets = append(doc.Tickets, newTicketDoc(t, snap.Capabilities))
	}
	return encode(w, doc)
}

// RenderDetail writes the detail document for one Ticket to w. caps decides
// which optional sections appear at all, and generatedAt is the caller's clock
// reading, matching generated_at in the epic document.
func RenderDetail(w io.Writer, d model.Detail, caps model.Capabilities, providerName string, generatedAt time.Time) error {
	return encode(w, newDetailDocument(d, caps, providerName, generatedAt))
}

// RenderTicket writes the decoder's document for one Ticket to w: the same
// detail document with the Ticket's own identity and its parent added, so an
// agent decoding a number it was handed gets the Ticket rather than an epic
// document with an empty array.
func RenderTicket(w io.Writer, doc TicketDocument) error {
	out := newDetailDocument(doc.Detail, doc.Capabilities, doc.ProviderName, doc.GeneratedAt)

	ticket := newTicketDoc(doc.Ticket, doc.Capabilities)
	out.Ticket = &ticket
	if !doc.Parent.IsZero() {
		out.Parent = &parentDoc{
			ID:    doc.Parent.ID,
			Key:   doc.Parent.Key,
			Title: doc.Parent.Title,
			URL:   doc.Parent.URL,
		}
	}
	return encode(w, out)
}

// newDetailDocument builds the document both RenderDetail and RenderTicket
// emit, so the two cannot drift about capability gating or timestamps.
func newDetailDocument(d model.Detail, caps model.Capabilities, providerName string, generatedAt time.Time) detailDocument {
	doc := detailDocument{
		SchemaVersion: schemaVersion,
		GeneratedAt:   wireTime(generatedAt),
		Provider:      newProviderDoc(providerName, caps),
		TicketID:      d.TicketID,
		Description:   d.Description,
	}

	if caps.Comments && len(d.Comments) > 0 {
		doc.Comments = make([]commentDoc, 0, len(d.Comments))
		for _, c := range d.Comments {
			doc.Comments = append(doc.Comments, commentDoc{
				ID:        c.ID,
				Author:    newUserDoc(c.Author),
				Body:      c.Body,
				CreatedAt: wireTime(c.CreatedAt),
				URL:       c.URL,
			})
		}
	}

	for _, l := range d.Links {
		if !caps.BlockingLinks && l.Kind != model.LinkRelates {
			continue
		}
		doc.Links = append(doc.Links, linkDoc{
			Kind:        l.Kind,
			NativeLabel: l.NativeLabel,
			Target: linkTargetDoc{
				ID:           l.Target.ID,
				Key:          l.Target.Key,
				Title:        l.Target.Title,
				URL:          l.Target.URL,
				Status:       l.Target.Status,
				NativeStatus: l.Target.NativeStatus,
			},
		})
	}

	return doc
}

// wireTime normalizes a timestamp for the wire: UTC, second precision. Sitrep
// reports on human-paced work, so sub-second digits are noise in a document
// people and scripts read.
func wireTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

func newProviderDoc(name string, caps model.Capabilities) providerDoc {
	return providerDoc{
		Name: name,
		Capabilities: capabilitiesDoc{
			Hierarchy:     caps.Hierarchy,
			BlockingLinks: caps.BlockingLinks,
			Comments:      caps.Comments,
			PullRequests:  caps.PullRequests,
		},
	}
}

func newProgressDoc(p model.Progress) progressDoc {
	return progressDoc{
		Todo:        p.Todo,
		InProgress:  p.InProgress,
		Done:        p.Done,
		Cancelled:   p.Cancelled,
		Unknown:     p.Unknown,
		Total:       p.Total,
		Denominator: p.Denominator,
		PercentDone: p.PercentDone,
	}
}

func newUserDoc(u model.User) userDoc {
	return userDoc{Login: u.Login, DisplayName: u.DisplayName, AvatarURL: u.AvatarURL}
}

func newTicketDoc(t model.Ticket, caps model.Capabilities) ticketDoc {
	doc := ticketDoc{
		ID:           t.ID,
		Key:          t.Key,
		Title:        t.Title,
		URL:          t.URL,
		Status:       t.Status,
		NativeStatus: t.NativeStatus,
		Repository:   t.Repository,
	}
	if caps.Hierarchy {
		doc.ParentID = t.ParentID
	}
	for _, a := range t.Assignees {
		doc.Assignees = append(doc.Assignees, newUserDoc(a))
	}
	if caps.PullRequests {
		for _, pr := range t.PullRequests {
			doc.PullRequests = append(doc.PullRequests, pullRequestDoc{
				Number:     pr.Number,
				Title:      pr.Title,
				URL:        pr.URL,
				Repository: pr.Repository,
				State:      pr.State,
				Review:     pr.Review,
				Checks:     pr.Checks,
			})
		}
	}
	return doc
}

// encode writes doc as indented JSON with one trailing newline. HTML escaping
// is off so an ampersand in a title stays an ampersand instead of arriving as a
// unicode escape and making the goldens unreadable.
func encode(w io.Writer, doc any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
