// Package termtexttest is the test support for sitrep's terminal-visible-text
// boundary: one hostile string, a filler that reaches every string field of a
// value through reflection, and an assertion that no control character,
// malformed UTF-8 or unbalanced bidirectional scope survived.
//
// It is exported so a screen can make the same assertion about its own input
// structs that internal/termtext makes about the model — three lines, and a
// string field added later is covered without anyone remembering to cover it.
package termtexttest

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Hostile is the string every filled field carries. It holds, in order: a NUL,
// an erase-display CSI, an OSC 52 clipboard write with a BEL terminator, an
// OSC 8 open/close pair pointing at an attacker's host, raw single-byte C1
// controls (0x9b CSI, 0x9d OSC and 0x9c ST — the byte spellings, which are the
// ones a rune scan alone never classifies), a lone 0xff, a truncated multi-byte
// sequence, CR, LF, TAB and DEL, an unterminated right-to-left override
// (U+202E), an unterminated right-to-left isolate (U+2067) and a stray pop
// directional isolate (U+2069) — plus printable multi-byte text that must
// survive it all.
const Hostile = "visible\x00\x1b[2J" +
	"\x1b]52;c;cHduZWQ=\a" +
	"\x1b]8;;https://evil.example.test/\ascope\x1b]8;;\x1b\\" +
	"\x9b31m\x9d52;c;cHduZWQ=\x9c" +
	"\xff\xe2\x82" +
	"\u202eoverride\u2069\u2067isolate" +
	"\r\n\tx\x7f café 東京"

// Fill writes Hostile into every string field reachable from v, which must be a
// non-nil pointer. Empty slices are given one filled element, so a walker that
// forgets a slice's element type is still caught. Named string types are filled
// too: a field is exempted from AssertClean explicitly or not at all.
func Fill(v any) {
	value := reflect.ValueOf(v)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		panic("termtexttest.Fill needs a non-nil pointer")
	}
	fill(value.Elem())
}

func fill(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(Hostile)
	case reflect.Struct:
		for i := range v.NumField() {
			fill(v.Field(i))
		}
	case reflect.Slice:
		if v.Len() == 0 {
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		for i := range v.Len() {
			fill(v.Index(i))
		}
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i))
		}
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fill(v.Elem())
	}
}

// Option narrows what AssertClean demands of one field path.
type Option func(*options)

type options struct {
	multiline map[string]bool
	exempt    map[string]bool
}

// Multiline marks paths where a newline and a tab are legitimate content rather
// than an injected control — a description, a comment body.
func Multiline(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.multiline[p] = true
		}
	}
}

// Exempt marks paths the boundary deliberately does not clean. Every use is a
// decision that belongs in a comment at the call site: the only fields that
// qualify today are identities, which are never drawn.
func Exempt(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.exempt[p] = true
		}
	}
}

// AssertClean fails t when any string reachable from v carries a control
// character (below 0x20, 0x7f, or 0x80–0x9f) or malformed UTF-8, or when one
// of its newline-delimited segments leaves a bidirectional scope unterminated
// or carries a terminator that closes nothing. It names the field path that
// carried it; label names the value in the failure.
//
// Paths are relative to v: "Title", "Tickets[].Assignees[].Login",
// "Comments[].Body".
func AssertClean(t testing.TB, label string, v any, opts ...Option) {
	t.Helper()
	o := options{multiline: map[string]bool{}, exempt: map[string]bool{}}
	for _, opt := range opts {
		opt(&o)
	}
	assertClean(t, label, "", reflect.ValueOf(v), &o)
}

func assertClean(t testing.TB, label, path string, v reflect.Value, o *options) {
	t.Helper()
	if o.exempt[path] {
		return
	}
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if !utf8.ValidString(s) {
			t.Errorf("%s: %s = % x, which is not valid UTF-8", label, describe(path), []byte(s))
			return
		}
		for _, r := range s {
			if o.multiline[path] && (r == '\n' || r == '\t') {
				continue
			}
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%s: %s = %q, which carries the control character %U",
					label, describe(path), s, r)
				return
			}
		}
		unterminated, stray := Unbalanced(s)
		if stray != 0 {
			t.Errorf("%s: %s = %+q, which carries a stray %U",
				label, describe(path), s, stray)
			return
		}
		if unterminated != 0 {
			t.Errorf("%s: %s = %+q, which leaves %U unterminated",
				label, describe(path), s, unterminated)
			return
		}
	case reflect.Struct:
		for i := range v.NumField() {
			assertClean(t, label, join(path, v.Type().Field(i).Name), v.Field(i), o)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			assertClean(t, label, path+"[]", v.Index(i), o)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			assertClean(t, label, path, v.Elem(), o)
		}
	}
}

// Unbalanced reports the first bidirectional defect in s: the outermost opener
// left without a terminator, and the first terminator that closes nothing.
// Both are zero when every newline-delimited segment of s is balanced, which
// is what the boundary guarantees — a segment is checked per line whichever
// policy produced it, because Line folds its newlines away and Body balances
// each of its segments on its own.
//
// It is exported so a test with a rendered frame or a terminal stream rather
// than a model value can make the same assertion.
func Unbalanced(s string) (unterminated, stray rune) {
	for _, segment := range strings.Split(s, "\n") {
		if u, st := scanBidi(segment); u != 0 || st != 0 {
			return u, st
		}
	}
	return 0, 0
}

// scanBidi answers whether one segment is bidi-balanced: it returns the
// outermost opener left without a terminator and the first terminator that
// closes nothing, either being zero when the segment has none.
//
// It is a second, deliberately simple implementation of the rules in
// termtext.Balance rather than a call to it: an assertion written in terms of
// the code it asserts is vacuous, and this one only needs a yes or no.
func scanBidi(segment string) (unterminated, stray rune) {
	var stack []rune
	for _, r := range segment {
		switch r {
		case 0x202A, 0x202B, 0x202D, 0x202E, // LRE, RLE, LRO, RLO
			0x2066, 0x2067, 0x2068: // LRI, RLI, FSI
			stack = append(stack, r)
		case 0x202C: // PDF closes an embedding, never across an isolate
			top := len(stack) - 1
			if top < 0 || stack[top] >= 0x2066 {
				return 0, r
			}
			stack = stack[:top]
		case 0x2069: // PDI closes the innermost isolate and all it contains
			isolate := -1
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] >= 0x2066 {
					isolate = i
					break
				}
			}
			if isolate < 0 {
				return 0, r
			}
			stack = stack[:isolate]
		}
	}
	if len(stack) > 0 {
		return stack[0], 0
	}
	return 0, 0
}

func join(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

func describe(path string) string {
	if path == "" {
		return "the value itself"
	}
	return path
}

// IsClean reports whether v would pass AssertClean with the same options. It is
// how a test proves its own filler reached something: an unsanitized filled
// value must not be clean, or the suite is vacuously green.
func IsClean(v any, opts ...Option) bool {
	recorder := &recordingTB{}
	AssertClean(recorder, "probe", v, opts...)
	return !recorder.failed
}

// recordingTB records a failure instead of reporting one. AssertClean calls
// only Helper and Errorf; the embedded nil testing.TB makes any other call a
// loud panic rather than a silently ignored assertion.
type recordingTB struct {
	testing.TB
	failed bool
}

func (*recordingTB) Helper()                 {}
func (r *recordingTB) Errorf(string, ...any) { r.failed = true }
