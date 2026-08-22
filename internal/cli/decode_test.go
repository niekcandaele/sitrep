package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// decoder serves a Ref that names the fixture Ticket #112 rather than the
// fixture Epic.
func decoder(opts ...fake.Option) *fake.Provider {
	return fake.New(append([]fake.Option{fake.WithSnapshot(fake.FixtureTicketSnapshot())}, opts...)...)
}

// The headline decoder document: the Ticket, the Epic it belongs to, and its
// Detail — description, comments and links — in one JSON document.
func TestJSONDecodedTicketDocument(t *testing.T) {
	p := decoder()

	got := run([]string{"112", "--json"}, p)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "ticket_decoded.golden.json", []byte(got.stdout))

	// One batched fetch to decode, one Detail read for what was decoded, and
	// nothing else (ADR-0003).
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1", n)
	}
	if n := p.DetailCalls(); n != 1 {
		t.Errorf("DetailCalls() = %d, want exactly 1", n)
	}
}

// The text twin of the same document.
func TestPlainDecodedTicketReport(t *testing.T) {
	got := run([]string{"112", "--plain"}, decoder())

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "ticket_decoded.golden.txt", []byte(got.stdout))

	if strings.ContainsRune(got.stdout, '\x1b') {
		t.Errorf("the report contains an escape byte: %q", got.stdout)
	}
}

// A Ticket that hangs off nothing decodes with no parent at all: no key in the
// document, no Epic line in the report, no error and no placeholder.
func TestDecodedTicketWithNoParent(t *testing.T) {
	asJSON := run([]string{"115", "--json"}, decoder(fake.WithSnapshot(fake.FixtureOrphanTicketSnapshot())))
	if asJSON.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", asJSON.code, asJSON.stderr)
	}
	checkGolden(t, "ticket_decoded_no_parent.golden.json", []byte(asJSON.stdout))

	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(asJSON.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the ticket document: %v", err)
	}
	if _, ok := doc["parent"]; ok {
		t.Error("the document carries a parent key for a Ticket that has none")
	}

	asPlain := run([]string{"115", "--plain"}, decoder(fake.WithSnapshot(fake.FixtureOrphanTicketSnapshot())))
	if asPlain.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", asPlain.code, asPlain.stderr)
	}
	checkGolden(t, "ticket_decoded_no_parent.golden.txt", []byte(asPlain.stdout))
	if strings.Contains(asPlain.stdout, "Epic ") {
		t.Errorf("the report drew a breadcrumb for a Ticket with no parent:\n%s", asPlain.stdout)
	}
}

// Silent absence, in the one-shot report: a Provider declaring neither Comments
// nor BlockingLinks emits no COMMENTS heading and no blocking links, with no
// explanation of what is missing. The Relates link survives.
func TestDecodedTicketWithoutTheDetailCapabilities(t *testing.T) {
	p := decoder(fake.WithCapabilities(model.Capabilities{Hierarchy: true, PullRequests: true}))

	got := run([]string{"112", "--plain"}, p)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	checkGolden(t, "ticket_decoded_no_capabilities.golden.txt", []byte(got.stdout))
	for _, absent := range []string{"COMMENT", "is blocked by", "#115", "#116"} {
		if strings.Contains(got.stdout, absent) {
			t.Errorf("the report mentions %q without the Capability behind it:\n%s", absent, got.stdout)
		}
	}
	if !strings.Contains(got.stdout, "is duplicated by") {
		t.Errorf("the Relates link went with the blocking ones:\n%s", got.stdout)
	}
}

// Every form of the Ref grammar decodes to the same report. The bare
// number is resolved through an injected remote lookup: no test may depend on
// the ambient git remote.
func TestEveryRefFormDecodes(t *testing.T) {
	lookup := func(context.Context, string, string) (string, error) {
		return "git@github.com:acme/widgets.git", nil
	}

	want := runWith([]string{"112", "--plain"}, cli.Deps{Provider: decoder(), RemoteLookup: lookup})
	if want.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", want.code, want.stderr)
	}

	for _, raw := range []string{"acme/widgets#112", "https://github.com/acme/widgets/issues/112"} {
		got := runWith([]string{raw, "--plain"}, cli.Deps{Provider: decoder(), RemoteLookup: lookup})
		if got.code != 0 {
			t.Errorf("%s: exit code = %d, want 0 (stderr: %q)", raw, got.code, got.stderr)
		}
		if got.stdout != want.stdout {
			t.Errorf("%s decoded to a different report:\n%s", raw, got.stdout)
		}
	}
}

// The decoded identity alone is not worth printing as a success, and a
// half-written report would poison whatever is reading it.
func TestDecodedTicketWhenTheDetailFetchFails(t *testing.T) {
	for _, mode := range []string{"--json", "--plain"} {
		p := decoder(fake.WithDetailError(errors.New("boom")))

		got := run([]string{"112", mode}, p)

		if got.code != 1 {
			t.Errorf("%s: exit code = %d, want 1", mode, got.code)
		}
		if got.stdout != "" {
			t.Errorf("%s: stdout = %q, want empty on failure", mode, got.stdout)
		}
		if !strings.Contains(got.stderr, "boom") {
			t.Errorf("%s: stderr = %q, want it to name the failure", mode, got.stderr)
		}
	}
}

// A Ref that names an Epic is untouched by any of this: same document,
// same one batched fetch, and still not one Detail read.
func TestARefThatNamesAnEpicIsUnchanged(t *testing.T) {
	for _, mode := range []string{"--json", "--plain"} {
		p := fake.New()

		got := run([]string{"111", mode}, p)

		if got.code != 0 {
			t.Fatalf("%s: exit code = %d, want 0 (stderr: %q)", mode, got.code, got.stderr)
		}
		if n := p.DetailCalls(); n != 0 {
			t.Errorf("%s: DetailCalls() = %d, want 0: an Epic is not decoded", mode, n)
		}
		if n := p.ResolveCalls(); n != 1 {
			t.Errorf("%s: ResolveCalls() = %d, want exactly 1", mode, n)
		}
	}
}

// --version and --help answer before a Provider is ever asked anything: a
// pre-flight fetch on the way to the usage text would be a Tracker request for
// a run that reads nothing.
func TestVersionAndHelpFetchNothing(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}, {"111", "--json", "--plain"}} {
		p := fake.New()

		run(args, p)

		if n := p.ResolveCalls(); n != 0 {
			t.Errorf("%v: ResolveCalls() = %d, want 0", args, n)
		}
	}
}
