package termtext_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/termtext"
)

// The nine code points that open or close a bidirectional scope, named so a
// test reads as the rule it asserts rather than as invisible characters.
const (
	lre = "\u202a" // left-to-right embedding
	rle = "\u202b" // right-to-left embedding
	pdf = "\u202c" // pop directional formatting
	lro = "\u202d" // left-to-right override
	rlo = "\u202e" // right-to-left override
	lri = "\u2066" // left-to-right isolate
	rli = "\u2067" // right-to-left isolate
	fsi = "\u2068" // first-strong isolate
	pdi = "\u2069" // pop directional isolate
)

// The marks, which are content and not scopes: they need no terminator, and
// this boundary must leave them alone.
const (
	lrm = "\u200e" // left-to-right mark
	rlm = "\u200f" // right-to-left mark
	alm = "\u061c" // Arabic letter mark
)

func TestLineBalancesBidiScopes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "an unterminated override is closed", in: "a" + rlo + "b", want: "a" + rlo + "b" + pdf},
		{name: "an unterminated isolate is closed", in: "a" + rli + "b", want: "a" + rli + "b" + pdi},
		{name: "a stray PDF is dropped", in: "a" + pdf + "b", want: "ab"},
		{name: "a stray PDI is dropped", in: "a" + pdi + "b", want: "ab"},
		{name: "a balanced override is untouched", in: rlo + "ab" + pdf, want: rlo + "ab" + pdf},
		{
			name: "a PDI implicitly terminates the embedding inside it",
			in:   rli + rlo + "X" + pdi,
			want: rli + rlo + "X" + pdi,
		},
		{
			name: "a PDF cannot cross an isolate barrier",
			in:   lri + "a" + pdf + "b" + pdi,
			want: lri + "ab" + pdi,
		},
		{
			name: "terminators are appended innermost first",
			in:   lre + rli + "x",
			want: lre + rli + "x" + pdi + pdf,
		},
		{
			name: "one terminator per unmatched opener, no depth cap",
			in:   strings.Repeat(rlo, 3) + "x",
			want: strings.Repeat(rlo, 3) + "x" + strings.Repeat(pdf, 3),
		},
		{
			name: "an embedding opened outside an isolate is closed after it",
			in:   rle + "a" + lri + "b" + pdi + "c",
			want: rle + "a" + lri + "b" + pdi + "c" + pdf,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := termtext.Line(tt.in); got != tt.want {
				t.Errorf("Line(%+q) = %+q, want %+q", tt.in, got, tt.want)
			}
		})
	}
}

// Body balances a segment at a time, so a defect on one line of a description
// is contained by that line's own newline rather than by the end of the field.
func TestBodyBalancesEachLine(t *testing.T) {
	in := "opened " + rlo + "one\nplain two\nthree " + pdi + "tail"
	want := "opened " + rlo + "one" + pdf + "\nplain two\nthree tail"
	if got := termtext.Body(in); got != want {
		t.Errorf("Body(%+q) = %+q, want %+q", in, got, want)
	}
}

// The headline guarantee: containment, not rewriting. Real RTL text whose
// scopes are already closed comes out byte for byte as it went in.
func TestBalancedRTLTextIsByteIdentical(t *testing.T) {
	singleLine := []string{
		"שלום עולם",
		"مرحبا بالعالم",
		"Ticket " + rli + "مرحبا بالعالم" + pdi + " is blocked",
		rle + "שלום" + pdf + " done",
		lri + "a " + rli + "ب" + pdi + " c" + pdi,
		rli + rlo + "ب" + pdi,
		fsi + "مرحبا" + pdi,
		lro + "id-42" + pdf,
		rlm + lrm + alm,
	}
	for i, in := range singleLine {
		t.Run(fmt.Sprintf("line-%d", i), func(t *testing.T) {
			if got := termtext.Line(in); got != in {
				t.Errorf("Line(%+q) = %+q, want it unchanged", in, got)
			}
			if got := termtext.Body(in); got != in {
				t.Errorf("Body(%+q) = %+q, want it unchanged", in, got)
			}
		})
	}

	body := "The blocker:\n" + rli + "مرحبا بالعالم" + pdi + "\n" + rli + "שלום עולם" + pdi + "\n"
	if got := termtext.Body(body); got != body {
		t.Errorf("Body(%+q) = %+q, want it unchanged", body, got)
	}

	// Every assertion above would hold if Balance were a no-op, so a known
	// defect has to come back changed.
	if hostile := "title " + rlo + "NORMAL"; termtext.Line(hostile) == hostile {
		t.Errorf("Line(%+q) returned it unchanged, so the table above proves nothing", hostile)
	}
}

// A "simplification" that stripped the controls instead of terminating them
// would render legitimate RTL wrong. Every one of the nine survives.
func TestBalancedInputKeepsEveryBidiCodePoint(t *testing.T) {
	balanced := map[string]string{
		"LRE": lre + "x" + pdf,
		"RLE": rle + "x" + pdf,
		"LRO": lro + "x" + pdf,
		"RLO": rlo + "x" + pdf,
		"LRI": lri + "x" + pdi,
		"RLI": rli + "x" + pdi,
		"FSI": fsi + "x" + pdi,
	}
	for name, in := range balanced {
		t.Run(name, func(t *testing.T) {
			got := termtext.Line(in)
			for _, r := range in {
				if !strings.ContainsRune(got, r) {
					t.Errorf("Line(%+q) = %+q, which lost %U", in, got, r)
				}
			}
		})
	}
}
