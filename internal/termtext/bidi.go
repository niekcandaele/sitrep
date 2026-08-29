package termtext

import "strings"

// The bidirectional formatting characters that open or close a scope. The
// marks LRM (U+200E), RLM (U+200F) and ALM (U+061C) are deliberately absent:
// they are ordinary content that needs no terminator.
const (
	// Embeddings and overrides. PDF pops whichever of the four is innermost.
	leftToRightEmbedding = '\u202A'
	rightToLeftEmbedding = '\u202B'
	popDirectionalFormat = '\u202C'
	leftToRightOverride  = '\u202D'
	rightToLeftOverride  = '\u202E'

	// Isolates. PDI pops whichever of the three is innermost.
	leftToRightIsolate    = '\u2066'
	rightToLeftIsolate    = '\u2067'
	firstStrongIsolate    = '\u2068'
	popDirectionalIsolate = '\u2069'
)

// Balance closes every bidirectional scope a segment opens and drops every
// terminator that closes nothing, so the bytes of a field and the direction a
// terminal draws them in cannot disagree beyond the field's own end. A segment
// is a newline-delimited run: terminators are appended before the "\n" that
// ends the segment that needed them, which keeps an unterminated override on
// one line of a description out of the forty lines below it.
//
// It contains scopes rather than removing them, so legitimate Hebrew and
// Arabic text is unharmed: input whose scopes are already balanced is returned
// byte-identical, and no bidi code point is ever stripped from it. Balance is
// therefore idempotent, and it allocates nothing when the input carries none of
// the nine code points.
//
// Two cross-family rules from UAX #9 decide what a terminator may close, and a
// naive counter gets both wrong:
//
//  1. A PDF (U+202C) closes the innermost embedding opened inside the current
//     isolate. It cannot reach past an isolate initiator to close an embedding
//     opened outside it, so such a PDF closes nothing and is dropped.
//  2. A PDI (U+2069) closes the innermost unmatched isolate initiator and
//     implicitly terminates every embedding opened inside that isolate. Those
//     embeddings need no appended PDF; inserting one would rewrite text the
//     Unicode algorithm already handles and break byte-identity.
func Balance(s string) string {
	if !containsBidiScope(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	segments := strings.Split(s, "\n")
	for i, segment := range segments {
		if i > 0 {
			b.WriteByte('\n')
		}
		balanceSegment(&b, segment)
	}
	return b.String()
}

// balanceSegment writes one newline-free run of text with its scopes balanced.
// The stack holds the opener runes still waiting for a terminator, innermost
// last.
func balanceSegment(b *strings.Builder, segment string) {
	var stack []rune
	for _, r := range segment {
		switch {
		case isEmbeddingOpener(r), isIsolateOpener(r):
			stack = append(stack, r)
			b.WriteRune(r)
		case r == popDirectionalFormat:
			if top := len(stack) - 1; top >= 0 && isEmbeddingOpener(stack[top]) {
				stack = stack[:top]
				b.WriteRune(r)
			}
		case r == popDirectionalIsolate:
			if i := innermostIsolate(stack); i >= 0 {
				stack = stack[:i]
				b.WriteRune(r)
			}
		default:
			b.WriteRune(r)
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if isEmbeddingOpener(stack[i]) {
			b.WriteRune(popDirectionalFormat)
		} else {
			b.WriteRune(popDirectionalIsolate)
		}
	}
}

// innermostIsolate returns the index of the innermost unmatched isolate
// initiator, or -1 when the stack holds none and a PDI would close nothing.
func innermostIsolate(stack []rune) int {
	for i := len(stack) - 1; i >= 0; i-- {
		if isIsolateOpener(stack[i]) {
			return i
		}
	}
	return -1
}

func isEmbeddingOpener(r rune) bool {
	switch r {
	case leftToRightEmbedding, rightToLeftEmbedding, leftToRightOverride, rightToLeftOverride:
		return true
	}
	return false
}

func isIsolateOpener(r rune) bool {
	switch r {
	case leftToRightIsolate, rightToLeftIsolate, firstStrongIsolate:
		return true
	}
	return false
}

// containsBidiScope reports whether s carries any of the nine code points
// Balance acts on, so ordinary text — including plain Hebrew and Arabic, which
// carries no controls at all — is returned unchanged with no allocation.
func containsBidiScope(s string) bool {
	for _, r := range s {
		if isEmbeddingOpener(r) || isIsolateOpener(r) ||
			r == popDirectionalFormat || r == popDirectionalIsolate {
			return true
		}
	}
	return false
}
