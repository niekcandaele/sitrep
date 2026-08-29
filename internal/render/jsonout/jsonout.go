// Package jsonout renders sitrep's machine-readable output: the --json
// Watchlist document, the matching Ticket detail document, and the decoder's
// Ticket document — the detail document plus the Ticket's own identity and its
// parent, which is what --json emits for a Ref that named a Ticket.
//
// The wire format is a contract, so it lives here as its own layer of DTO
// structs with json tags rather than tags scattered over the domain model.
// Refactoring internal/model then forces a deliberate decision about the wire
// format instead of silently changing it under consumers.
//
// Conventions, all pinned by golden tests:
//
//   - Keys are snake_case; schema_version comes first. Watchlists use schema v3;
//     Detail and decoded-Ticket documents remain schema v1.
//   - Times are RFC 3339 in UTC.
//   - tickets is always present and always an array, never null.
//   - Optional strings, counts and capability-gated arrays are omitted when
//     empty. An undeclared Capability means the key is absent everywhere —
//     absence is the normal, silent way to say "this Tracker does not do that".
//     pull_request_total is omitted at zero too, so an absent key does not
//     distinguish "this Provider cannot tell you how many there are" from a
//     capable Provider reporting none; an absent pull_requests array is the
//     consumer's signal for the second.
//   - The blocking fields — the top-level blocking object and each Ticket's
//     actionable, links_known, in_cycle and unmet_blockers — appear only when
//     the caller computed a BlockingGraph, which takes both the --links flag and
//     the BlockingLinks Capability. Uncomputed, they are absent: never null and
//     never false. With --links a false is a computed answer, and absence must
//     not be mistaken for one.
//
// Tickets are emitted flat, in Provider order, with a status field on each and
// a computed progress block alongside. Grouping is presentation: it belongs to
// the --plain renderer and the TUI, and any consumer can regroup a flat array
// trivially. Do not add grouping here.
package jsonout

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

// Watchlist documents use schema v3; Detail and decoded-Ticket documents use schema v1.
//
// v3 is a deliberate exception to the rule that additive optional fields need no
// bump — the rule that let pull_request_total land inside v2. The Watchlist
// field set is now invocation-dependent: --links adds keys a plain --json run
// omits, so the version is what tells a consumer which fields this binary is
// capable of emitting at all. Left at 2, a consumer could not tell an old binary
// that rejects --links from a new one that was simply not asked for blockers.
const (
	watchlistSchemaVersion = 3
	detailSchemaVersion    = 1
)

type providerDoc struct {
	Name         string          `json:"name"`
	Capabilities capabilitiesDoc `json:"capabilities"`
}

type capabilitiesDoc struct {
	Hierarchy     bool                     `json:"hierarchy"`
	BlockingLinks bool                     `json:"blocking_links"`
	Comments      bool                     `json:"comments"`
	PullRequests  bool                     `json:"pull_requests"`
	Selectors     *selectorCapabilitiesDoc `json:"selectors,omitempty"`
}

type selectorCapabilitiesDoc struct {
	Epic    bool `json:"epic"`
	RefList bool `json:"ref_list"`
	Query   bool `json:"query"`
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
	// PullRequestTotal is how many pull requests the Tracker says the Ticket
	// has. Zero is omitted along with an unsupplied total: a genuine zero from
	// a capable Provider is indistinguishable on the wire, and pull_requests
	// being absent is what tells the consumer there are none. It stays an int
	// rather than becoming a *int because that would be a wire change.
	PullRequestTotal int `json:"pull_request_total,omitempty"`
	// Actionable, LinksKnown and InCycle are pointers so that a computed false
	// still encodes as false while an uncomputed field is omitted entirely. A
	// plain bool with omitempty would collapse those two into one absent key.
	Actionable    *bool        `json:"actionable,omitempty"`
	LinksKnown    *bool        `json:"links_known,omitempty"`
	InCycle       *bool        `json:"in_cycle,omitempty"`
	UnmetBlockers []blockerDoc `json:"unmet_blockers,omitempty"`
}

// blockerDoc is one Ticket standing between another Ticket and being picked up.
// Satisfied blockers are not emitted: an agent's question is what is holding
// this Ticket.
type blockerDoc struct {
	linkTargetDoc
	// Member is false for a Ghost Ticket and for an anonymous Link target.
	Member bool `json:"member"`
	// StatusKnown is false when the blocker's Status could not be read. It is
	// emitted even though it is currently derivable from status != "unknown":
	// the wire contract should not make consumers re-derive sitrep's own
	// fail-closed rule.
	StatusKnown bool `json:"status_known"`
}

// blockingDoc is the Watchlist-level blocking result. Ghost Tickets get no
// listing of their own: a Ghost is fully described inline in the unmet_blockers
// entry that names it, and a second listing would only invite the two to
// disagree.
type blockingDoc struct {
	// Cycles is always an array, [] when there are none, like tickets. A cycle
	// is reported rather than silently rendered as permanently-blocked rows.
	Cycles [][]model.TicketID `json:"cycles"`
}

type selectorDoc struct {
	Kind  string   `json:"kind"`
	Ref   string   `json:"ref,omitempty"`
	Refs  []string `json:"refs,omitempty"`
	Query *string  `json:"query,omitempty"`
}

type watchlistDoc struct {
	Selector     selectorDoc `json:"selector"`
	Epic         *epicDoc    `json:"epic,omitempty"`
	LimitReached bool        `json:"limit_reached,omitempty"`
}

type watchlistDocument struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Provider      providerDoc  `json:"provider"`
	Watchlist     watchlistDoc `json:"watchlist"`
	Progress      progressDoc  `json:"progress"`
	Blocking      *blockingDoc `json:"blocking,omitempty"`
	Tickets       []ticketDoc  `json:"tickets"`
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
	// Parent is the Ticket's parent. The zero Parent emits no parent key at all.
	Parent model.Parent
	// Detail is the expensive per-ticket data, exactly as the Provider returned
	// it.
	Detail model.Detail
	// Capabilities decide which optional sections appear at all.
	Capabilities model.Capabilities
	// ProviderName is the serving Provider's Name.
	ProviderName string
	// GeneratedAt is the caller's clock reading for the wire document.
	GeneratedAt time.Time
}

// WatchlistDocument is everything the Watchlist renderer needs. It is a struct
// rather than a growing argument list for the same reason TicketDocument is: the
// optional half belongs in a named field, and a second entry point taking the
// optional half would let the two drift.
type WatchlistDocument struct {
	// Snapshot is the resolved Watchlist.
	Snapshot model.WatchlistSnapshot
	// Selector records how membership was chosen.
	Selector provider.Selector
	// ProviderName identifies the serving Provider.
	ProviderName string
	// Blocking is the Watchlist's blocking graph, present only when the caller
	// was asked for it with --links and the Provider declares BlockingLinks.
	// nil means "not computed", which is why every blocking key is then absent
	// rather than null: absence is honest, null invites a wrong reading.
	Blocking *model.BlockingGraph
}

// RenderWatchlist writes the schema-v3 Watchlist document for one snapshot.
//
// This package renders; it never computes. It takes a finished BlockingGraph or
// nothing, and neither builds one nor fetches the Details one would need.
func RenderWatchlist(w io.Writer, doc WatchlistDocument) error {
	snap := doc.Snapshot
	watchlist, err := newWatchlistDoc(snap, doc.Selector)
	if err != nil {
		return err
	}
	out := watchlistDocument{
		SchemaVersion: watchlistSchemaVersion,
		GeneratedAt:   wireTime(snap.FetchedAt),
		Provider:      newWatchlistProviderDoc(doc.ProviderName, snap.Capabilities),
		Watchlist:     watchlist,
		Progress:      newProgressDoc(model.ComputeProgress(snap.Tickets)),
		Tickets:       make([]ticketDoc, 0, len(snap.Tickets)),
	}

	// Members are walked positionally against snap.Tickets rather than looked up
	// by ID: a Ref-list Watchlist may legitimately contain the same Ticket
	// twice, and BlockingGraph.For would collapse those rows onto one another.
	var members []model.Actionability
	var byID map[model.TicketID]model.Ticket
	if doc.Blocking != nil {
		members = doc.Blocking.Members()
		if len(members) != len(snap.Tickets) {
			return fmt.Errorf("json: blocking graph has %d members for %d Tickets",
				len(members), len(snap.Tickets))
		}
		out.Blocking = &blockingDoc{Cycles: cyclesDoc(doc.Blocking.Cycles())}
		byID = make(map[model.TicketID]model.Ticket, len(snap.Tickets))
		for _, t := range snap.Tickets {
			byID[t.ID] = t
		}
	}

	for i, t := range snap.Tickets {
		ticket := newTicketDoc(t, snap.Capabilities)
		if members != nil {
			// Positional pairing only holds while the graph's members are the
			// snapshot's Tickets in order. A length check alone claims more
			// than it checks, so the identity is compared too.
			if members[i].TicketID != t.ID {
				return fmt.Errorf("json: blocking graph member %d is %s, want %s",
					i, members[i].TicketID, t.ID)
			}
			addBlocking(&ticket, members[i], byID)
		}
		out.Tickets = append(out.Tickets, ticket)
	}
	return encode(w, out)
}

// addBlocking writes one member's computed blocking state onto its wire Ticket.
// The three booleans are always written, false included: with --links, false is
// an answer and absence would mean the run never looked.
//
// unmet_blockers is exactly Actionability.Unmet(), nothing added and nothing
// held back. A member whose own Links were unreadable usually has no blockers to
// list, but it can still carry one discovered through another member's Blocks
// Link — that blocker was genuinely read, so suppressing it would hide real
// information. links_known: false is what tells the consumer the list may be
// incomplete; it never means the list was invented.
//
// A blocker's status is Blocker.Status, the authoritative one BuildBlockingGraph
// resolved, not the second-hand copy the Link that named the blocker carried:
// the same Ticket must not appear twice in one document with two statuses. The
// native word is resolved the same way, from byID, which keys the snapshot's
// Tickets — duplicate rows in a Ref-list Watchlist carry identical status, so
// the lookup is safe for this field. Where no authoritative native word is
// available and the Link's copy belongs to a different Status Category, the key
// is omitted rather than emitted as a contradiction.
func addBlocking(doc *ticketDoc, a model.Actionability, byID map[model.TicketID]model.Ticket) {
	doc.Actionable = &a.Actionable
	doc.LinksKnown = &a.LinksKnown
	doc.InCycle = &a.InCycle
	for _, b := range a.Unmet() {
		target := newLinkTargetDoc(b.Target)
		target.Status = b.Status
		target.NativeStatus = blockerNativeStatus(b, byID)
		doc.UnmetBlockers = append(doc.UnmetBlockers, blockerDoc{
			linkTargetDoc: target,
			Member:        b.Member,
			StatusKnown:   b.StatusKnown,
		})
	}
}

// blockerNativeStatus resolves the Tracker's own word for a blocker: the
// member's own Ticket when the blocker is a member, and otherwise the word the
// Link carried, kept only while it agrees with the authoritative Category.
func blockerNativeStatus(b model.Blocker, byID map[model.TicketID]model.Ticket) string {
	if b.Member {
		if t, ok := byID[b.Target.ID]; ok {
			return t.NativeStatus
		}
	}
	if b.Target.Status != b.Status {
		return ""
	}
	return b.Target.NativeStatus
}

// cyclesDoc normalizes the graph's cycles for the wire, where the key is always
// an array rather than null.
func cyclesDoc(cycles [][]model.TicketID) [][]model.TicketID {
	if cycles == nil {
		return [][]model.TicketID{}
	}
	return cycles
}

func newWatchlistDoc(snap model.WatchlistSnapshot, selector provider.Selector) (watchlistDoc, error) {
	switch s := selector.(type) {
	case provider.EpicSelector:
		epic := epicDoc{
			ID:           snap.Epic.ID,
			Key:          snap.Epic.Key,
			Title:        snap.Epic.Title,
			URL:          snap.Epic.URL,
			Status:       snap.Epic.Status,
			NativeStatus: snap.Epic.NativeStatus,
		}
		return watchlistDoc{
			Selector: selectorDoc{Kind: "epic", Ref: selectorRef(s.Ref)},
			Epic:     &epic,
		}, nil
	case provider.RefListSelector:
		refs := make([]string, 0, len(s.Refs))
		for _, r := range s.Refs {
			refs = append(refs, selectorRef(r))
		}
		return watchlistDoc{Selector: selectorDoc{Kind: "ref_list", Refs: refs}}, nil
	case provider.QuerySelector:
		query := s.Query
		return watchlistDoc{
			Selector:     selectorDoc{Kind: "query", Query: &query},
			LimitReached: snap.LimitReached,
		}, nil
	default:
		return watchlistDoc{}, fmt.Errorf("json: unsupported Watchlist Selector %T", selector)
	}
}

func selectorRef(r ref.Ref) string {
	if raw := strings.TrimSpace(r.Raw); raw != "" {
		return raw
	}
	return r.String()
}

// RenderDetail writes the detail document for one Ticket to w. caps decides
// which optional sections appear at all, and generatedAt is the caller's clock
// reading for the wire document.
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
		SchemaVersion: detailSchemaVersion,
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

	for _, l := range model.VisibleLinks(d.Links, caps) {
		doc.Links = append(doc.Links, linkDoc{
			Kind:        l.Kind,
			NativeLabel: l.NativeLabel,
			Target:      newLinkTargetDoc(l.Target),
		})
	}

	return doc
}

// newLinkTargetDoc is the one wire shape for a Ticket named by a Link, whether
// it is the target of a links entry or an unmet blocker. An anonymous target
// keeps its empty identity: dropping it would make a blocked Ticket look
// actionable.
func newLinkTargetDoc(t model.LinkTarget) linkTargetDoc {
	return linkTargetDoc{
		ID:           t.ID,
		Key:          t.Key,
		Title:        t.Title,
		URL:          t.URL,
		Status:       t.Status,
		NativeStatus: t.NativeStatus,
	}
}

// wireTime normalizes a timestamp for the wire: UTC, second precision. Sitrep
// reports on human-paced work, so sub-second digits are noise in a document
// people and scripts read.
func wireTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

func newWatchlistProviderDoc(name string, caps model.Capabilities) providerDoc {
	doc := newProviderDoc(name, caps)
	doc.Capabilities.Selectors = &selectorCapabilitiesDoc{
		Epic:    caps.Selectors.Epic,
		RefList: caps.Selectors.RefList,
		Query:   caps.Selectors.Query,
	}
	return doc
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
		doc.PullRequestTotal = t.PullRequestTotal
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
