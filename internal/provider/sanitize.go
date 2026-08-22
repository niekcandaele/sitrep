package provider

import (
	"context"
	"strings"

	"github.com/niekcandaele/sitrep/internal/model"
)

// Sanitized wraps a Provider so that every piece of tracker-controlled text it
// returns is free of terminal control sequences before anything renders it.
//
// The seam is here rather than in the renderers because this is the one place
// all of that text crosses. Lip Gloss and sitrep's ANSI-aware wrapping preserve
// escape sequences they recognize, and internal/render/plain writes strings
// with fmt.Fprintf, so a title of "hi\x1b[2J" clears the reader's screen and a
// comment body carrying OSC 52 writes their clipboard — and on a public tracker
// anyone who can comment can write one. Sanitizing at the four renderers would
// be four places to remember and one more for every renderer added after.
//
// Control characters are removed rather than escaped: sitrep is a report, and
// rendering "^[[2J" in a title is noise where dropping it is not. Everything
// else — every printable rune, every multi-byte UTF-8 sequence — is left
// exactly as the tracker returned it.
func Sanitized(p Provider) Provider {
	if p == nil {
		return nil
	}
	if _, already := p.(sanitized); already {
		return p
	}
	return sanitized{Provider: p}
}

type sanitized struct{ Provider }

func (s sanitized) Resolve(ctx context.Context, selector Selector) (model.WatchlistSnapshot, error) {
	snap, err := s.Provider.Resolve(ctx, selector)
	return sanitizeSnapshot(snap), err
}

func (s sanitized) FetchDetail(ctx context.Context, id model.TicketID) (model.Detail, error) {
	detail, err := s.Provider.FetchDetail(ctx, id)
	return sanitizeDetail(detail), err
}

func sanitizeSnapshot(snap model.WatchlistSnapshot) model.WatchlistSnapshot {
	snap.Header.Key = SanitizeLine(snap.Header.Key)
	snap.Header.Title = SanitizeLine(snap.Header.Title)
	snap.Header.URL = SanitizeLine(snap.Header.URL)
	snap.Epic = sanitizeEpic(snap.Epic)
	snap.Parent = sanitizeParent(snap.Parent)
	for i := range snap.Tickets {
		snap.Tickets[i] = sanitizeTicket(snap.Tickets[i])
	}
	return snap
}

func sanitizeEpic(e model.Epic) model.Epic {
	e.Key = SanitizeLine(e.Key)
	e.Title = SanitizeLine(e.Title)
	e.URL = SanitizeLine(e.URL)
	e.NativeStatus = SanitizeLine(e.NativeStatus)
	e.Repository = SanitizeLine(e.Repository)
	e.Assignees = sanitizeUsers(e.Assignees)
	e.PullRequests = sanitizePullRequests(e.PullRequests)
	return e
}

func sanitizeTicket(t model.Ticket) model.Ticket {
	t.Key = SanitizeLine(t.Key)
	t.Title = SanitizeLine(t.Title)
	t.URL = SanitizeLine(t.URL)
	t.NativeStatus = SanitizeLine(t.NativeStatus)
	t.Repository = SanitizeLine(t.Repository)
	t.Assignees = sanitizeUsers(t.Assignees)
	t.PullRequests = sanitizePullRequests(t.PullRequests)
	return t
}

func sanitizeParent(p model.Parent) model.Parent {
	p.Key = SanitizeLine(p.Key)
	p.Title = SanitizeLine(p.Title)
	p.URL = SanitizeLine(p.URL)
	return p
}

func sanitizeUsers(users []model.User) []model.User {
	for i, u := range users {
		users[i] = model.User{
			Login:       SanitizeLine(u.Login),
			DisplayName: SanitizeLine(u.DisplayName),
			AvatarURL:   SanitizeLine(u.AvatarURL),
		}
	}
	return users
}

func sanitizePullRequests(prs []model.PullRequest) []model.PullRequest {
	for i := range prs {
		prs[i].Title = SanitizeLine(prs[i].Title)
		prs[i].URL = SanitizeLine(prs[i].URL)
		prs[i].Repository = SanitizeLine(prs[i].Repository)
	}
	return prs
}

func sanitizeDetail(d model.Detail) model.Detail {
	d.Description = sanitizeText(d.Description)
	for i := range d.Comments {
		d.Comments[i].ID = SanitizeLine(d.Comments[i].ID)
		d.Comments[i].Author = sanitizeUsers([]model.User{d.Comments[i].Author})[0]
		d.Comments[i].Body = sanitizeText(d.Comments[i].Body)
		d.Comments[i].URL = SanitizeLine(d.Comments[i].URL)
	}
	for i := range d.Links {
		d.Links[i].NativeLabel = SanitizeLine(d.Links[i].NativeLabel)
		d.Links[i].Target.Key = SanitizeLine(d.Links[i].Target.Key)
		d.Links[i].Target.Title = SanitizeLine(d.Links[i].Target.Title)
		d.Links[i].Target.URL = SanitizeLine(d.Links[i].Target.URL)
		d.Links[i].Target.NativeStatus = SanitizeLine(d.Links[i].Target.NativeStatus)
	}
	return d
}

// SanitizeLine cleans a field that is displayed on one line: a key, a title, a
// URL, a native status, a login, a repository path, a link label. C0 controls
// (including ESC), DEL and C1 are removed; a tab or a newline becomes a single
// space, so a title that arrived with one still occupies the width the renderer
// measured.
func SanitizeLine(s string) string {
	return sanitize(s, false)
}

// sanitizeText cleans a field that is legitimately multi-line — a Ticket's
// description and a comment body. Newlines and tabs survive because they are
// the text's own structure; "\r\n" is normalized to "\n" so a tracker's line
// endings do not reach a terminal as carriage returns. Everything else
// SanitizeLine removes is removed here too.
func sanitizeText(s string) string {
	return sanitize(s, true)
}

func sanitize(s string, keepLayout bool) string {
	if !needsSanitizing(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			if keepLayout {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		case r == '\r':
			// A bare carriage return rewrites the line already drawn; as half of
			// a CRLF it is redundant with the newline beside it. Either way it is
			// removed, which is what normalizes "\r\n" to "\n".
			continue
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsSanitizing reports whether s carries anything sanitize would change, so
// the overwhelmingly common case — text that is already clean — allocates
// nothing.
func needsSanitizing(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}
