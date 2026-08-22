package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/cli"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
)

// fixedClock is the clock every golden in this package is generated against.
var fixedClock = func() time.Time {
	return time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
}

type result struct {
	code   int
	stdout string
	stderr string
}

func run(args []string, p *fake.Provider) result {
	var stdout, stderr bytes.Buffer
	code := cli.RunWith(args, &stdout, &stderr, cli.Deps{Provider: p, Now: fixedClock})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// The headline test: the whole program, run against the fake Provider, emits a
// document that is byte-for-byte what the golden says.
func TestJSONEpicDocument(t *testing.T) {
	p := fake.New()

	got := run([]string{"111", "--json"}, p)

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	checkGolden(t, "epic.golden.json", []byte(got.stdout))

	// ADR-0003: the epic path is one batched fetch and never touches Detail.
	if n := p.ResolveCalls(); n != 1 {
		t.Errorf("ResolveCalls() = %d, want exactly 1 batched fetch", n)
	}
	if n := p.DetailCalls(); n != 0 {
		t.Errorf("DetailCalls() = %d, want 0: rendering a list must not fetch detail", n)
	}
}

// The flag may come before or after the ref; both orders are how people type.
func TestJSONFlagOrderDoesNotMatter(t *testing.T) {
	before := run([]string{"--json", "111"}, fake.New())
	after := run([]string{"111", "--json"}, fake.New())

	if before.stdout != after.stdout || before.code != after.code {
		t.Errorf("flag order changed the run: %d/%d bytes, codes %d and %d",
			len(before.stdout), len(after.stdout), before.code, after.code)
	}
}

// An undeclared Capability is silently absent, never an error: with pull
// requests off no ticket carries the key at all.
func TestJSONOmitsUndeclaredCapabilities(t *testing.T) {
	p := fake.New(fake.WithCapabilities(model.Capabilities{Hierarchy: true}))

	got := run([]string{"111", "--json"}, p)

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got.code, got.stderr)
	}
	// Absence is silent: no section, and no complaint about its absence either.
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty: an undeclared Capability is not a problem", got.stderr)
	}
	checkGolden(t, "epic_no_pr_capability.golden.json", []byte(got.stdout))

	var doc struct {
		Tickets []map[string]json.RawMessage `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v", err)
	}
	if len(doc.Tickets) == 0 {
		t.Fatal("the document has no tickets")
	}
	for _, ticket := range doc.Tickets {
		if _, ok := ticket["pull_requests"]; ok {
			t.Errorf("ticket %s carries pull_requests without the capability", ticket["key"])
		}
	}
}

// A half-written document followed by an error would poison a consuming
// script, so a failed fetch leaves stdout untouched.
func TestJSONProviderFailure(t *testing.T) {
	p := fake.New(fake.WithResolveError(errors.New("boom")))

	got := run([]string{"111", "--json"}, p)

	if got.code != 1 {
		t.Errorf("exit code = %d, want 1", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", got.stdout)
	}
	if !strings.Contains(got.stderr, "boom") {
		t.Errorf("stderr = %q, want it to contain the provider's error", got.stderr)
	}
	if !strings.HasPrefix(got.stderr, "sitrep: ") {
		t.Errorf("stderr = %q, want it prefixed with the program name", got.stderr)
	}
}

// Run's production path resolves the Provider from --provider, which is how
// sitrep walks end to end today.
func TestRunResolvesTheFakeProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run([]string{"--provider", "fake", "--json", "111"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshalling the epic document: %v", err)
	}
	if got := doc["schema_version"]; got != float64(1) {
		t.Errorf("schema_version = %v, want 1", got)
	}
}
