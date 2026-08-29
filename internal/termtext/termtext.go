// Package termtext is sitrep's terminal-visible-text boundary: the one place
// that decides what tracker-controlled text may reach a terminal.
//
// # The policy
//
// Malformed UTF-8 is normalized away, and C0 controls (including ESC), DEL and
// C1 are removed. Line folds a tab or a newline to a single space, so a value
// the renderer measured occupies the width it measured; Body keeps newlines and
// tabs because layout is the point of a description or a comment.
//
// Control characters are removed rather than escaped: sitrep is a report, and
// rendering "^[[2J" in a title is noise where dropping it is not. Everything
// else — every printable rune, every multi-byte UTF-8 sequence — is left
// exactly as the tracker returned it. Homoglyph and confusable handling is
// deliberately absent (declined in #33).
//
// # Where it is applied
//
// A model value becomes safe to draw at the moment it enters a screen's state,
// not at the moment it is rendered, and not because of who produced it. Two
// enforcement points call the walkers in model.go, and only two:
// provider.Sanitized for the one-shot renderers, and internal/tui's intake for
// every screen. One policy, one implementation, two sinks (ADR-0006).
//
// # Where a policy change lands
//
// Here, in Line and Body, and nowhere else. A rule that has to hold for every
// terminal-visible field — balancing unterminated bidirectional overrides, say
// — belongs in these two functions, because every such field crosses one of
// them exactly once.
package termtext

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Line cleans a field that is displayed on one line: a key, a title, a URL, a
// native status, a login, a repository path, a link label. Malformed UTF-8 is
// normalized away, C0 controls (including ESC), DEL and C1 are removed, a tab
// or a newline becomes a single space, and a carriage return is dropped.
//
// Normalizing UTF-8 is part of the policy rather than a caller's chore: a raw
// 0x9b byte is not a rune the control scan can classify, so it would otherwise
// reach the terminal as CSI.
//
// Line is idempotent.
func Line(s string) string {
	return clean(s, false)
}

// Body cleans a field that is legitimately multi-line — a Ticket's description
// and a comment body. It is the terminal-control boundary for body text; an
// eventual Markdown or HTML sanitizer is only a second, different boundary.
// Complete terminal sequences and their payloads are removed before malformed
// UTF-8 is discarded. Newlines and tabs survive, CRLF becomes LF, and printable
// Unicode and Markdown punctuation remain unchanged.
//
// Body is idempotent.
func Body(s string) string {
	// ANSI parsers recognize the byte form of C1 controls. Canonicalize their
	// valid UTF-8 spelling first so OSC, DCS, CSI, and their ST terminators have
	// the same terminal semantics whichever spelling a caller supplied.
	s = ansi.Strip(c1Bytes(s))
	s = strings.ToValidUTF8(s, "")
	return clean(s, true)
}

// Err returns an error whose rendered message has crossed Line, keeping
// everything it wrapped reachable so errors.Is, errors.As and provider.KindOf
// still see through it. An error that is already clean is returned unchanged.
//
// It exists because the TUI's Source and DetailSource are plain functions: a
// caller that never went near a Provider can return any error, and the footer
// draws its text.
func Err(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if clean := Line(message); clean != message {
		return &cleanedMessage{msg: clean, err: err}
	}
	return err
}

// cleanedMessage replaces an error's rendered text while keeping everything it
// wrapped reachable.
type cleanedMessage struct {
	msg string
	err error
}

func (e *cleanedMessage) Error() string { return e.msg }
func (e *cleanedMessage) Unwrap() error { return e.err }

// c1Bytes converts valid UTF-8 C1 code points to the single-byte form consumed
// by the ANSI state machine. All resulting invalid bytes are either part of a
// stripped sequence or discarded by strings.ToValidUTF8 immediately after it.
func c1Bytes(s string) string {
	if !containsC1Rune(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if size > 1 && r >= 0x80 && r <= 0x9f {
			b.WriteByte(byte(r))
		} else {
			b.WriteString(s[:size])
		}
		s = s[size:]
	}
	return b.String()
}

func containsC1Rune(s string) bool {
	for _, r := range s {
		if r >= 0x80 && r <= 0x9f {
			return true
		}
	}
	return false
}

// clean drops malformed UTF-8 and control characters in one pass. keepLayout
// says whether a tab and a newline are content (a body) or width (a line).
func clean(s string, keepLayout bool) string {
	if !needsCleaning(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		chunk := s[:size]
		s = s[size:]
		switch {
		case r == utf8.RuneError && size == 1:
			// A byte that decodes to nothing: an unpaired continuation, a
			// truncated sequence, or a raw C1 byte the rune scan below could
			// never have classified.
			continue
		case r == '\n' || r == '\t':
			if keepLayout {
				b.WriteString(chunk)
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
			b.WriteString(chunk)
		}
	}
	return b.String()
}

// needsCleaning reports whether s carries anything clean would change, so the
// overwhelmingly common case — text that is already clean — allocates nothing.
func needsCleaning(s string) bool {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
		s = s[size:]
	}
	return false
}
