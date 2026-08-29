package ref_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/ref"
)

// stubLookup is how every test in this package resolves a bare number: no test
// shells out to git or needs a real clone.
func stubLookup(url string, err error) ref.RemoteLookup {
	return func(context.Context, string, string) (string, error) { return url, err }
}

func TestParseAcceptedForms(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want ref.Ref
	}{
		{
			name: "issue URL",
			raw:  "https://github.com/acme/widgets/issues/111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name: "issue URL over http with a trailing slash",
			raw:  "http://github.com/acme/widgets/issues/111/",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name: "issue URL with query and fragment",
			raw:  "https://github.com/acme/widgets/issues/111?foo=bar#issuecomment-9",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name: "pull request URL",
			raw:  "https://github.com/acme/widgets/pull/111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name: "GitHub Enterprise host",
			raw:  "https://ghe.corp.example/acme/widgets/issues/111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "ghe.corp.example", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			// "www." is a spelling of github.com, not a different origin, and the
			// GitHub driver has to reach api.github.com rather than
			// www.github.com/api/graphql.
			name: "www.github.com canonicalises",
			raw:  "https://www.github.com/acme/widgets/issues/111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			// An unrecognized host keeps its "www.": it is a distinct host, and
			// therefore a distinct credential scope, from the one without it.
			name: "www. on an unknown host is kept",
			raw:  "https://www.ghe.example/acme/widgets/issues/111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "www.ghe.example", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name: "a self-hosted instance on a custom port keeps it",
			raw:  "https://git.acme.test:8443/acme/widgets/-/issues/7",
			want: ref.Ref{Tracker: ref.TrackerGitLab, Host: "git.acme.test:8443", Owner: "acme", Repo: "widgets", Number: 7},
		},
		{
			name: "owner/repo#N",
			raw:  "acme/widgets#111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
		{
			name: "owner/repo/N",
			raw:  "acme/widgets/111",
			want: ref.Ref{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "widgets", Number: 111},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			tt.want.Raw = tt.raw
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseGitLabURLKeepsItsTracker(t *testing.T) {
	got, err := ref.Parse(context.Background(), "https://gitlab.com/acme/widgets/-/issues/7")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tracker != ref.TrackerGitLab {
		t.Errorf("Tracker = %q, want %q", got.Tracker, ref.TrackerGitLab)
	}
	if got.Owner != "acme" || got.Repo != "widgets" || got.Number != 7 {
		t.Errorf("got %+v, want acme/widgets#7", got)
	}
}

func TestParseJiraURLKeepsItsTracker(t *testing.T) {
	got, err := ref.Parse(context.Background(), "https://acme.atlassian.net/browse/PROJ-12")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tracker != ref.TrackerJira {
		t.Errorf("Tracker = %q, want %q", got.Tracker, ref.TrackerJira)
	}
	if got.Key != "PROJ-12" {
		t.Errorf("Key = %q, want %q", got.Key, "PROJ-12")
	}
}

func TestParseRejects(t *testing.T) {
	rejects := []string{
		"",
		"   ",
		"abc",
		"0",
		"#0",
		"-1",
		"1.5",
		"99999999999999999999",
		"acme/widgets",
		"acme/widgets#",
		"acme/widgets#abc",
		"https://example.com/about",
		"https://github.com/acme/widgets",
	}

	for _, raw := range rejects {
		t.Run(raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), raw,
				ref.WithRemoteLookup(stubLookup("git@github.com:acme/widgets.git", nil)))
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", raw, got)
			}
		})
	}
}

func TestParseBareNumberUsesTheOriginRemote(t *testing.T) {
	for _, raw := range []string{"111", "#111"} {
		t.Run(raw, func(t *testing.T) {
			var gotDir, gotRemote string
			lookup := func(_ context.Context, dir, remote string) (string, error) {
				gotDir, gotRemote = dir, remote
				return "git@github.com:acme/widgets.git", nil
			}

			got, err := ref.Parse(context.Background(), raw,
				ref.WithRemoteLookup(lookup), ref.WithDir("/some/clone"))
			if err != nil {
				t.Fatalf("Parse(%q): %v", raw, err)
			}

			want := ref.Ref{
				Tracker: ref.TrackerGitHub, Host: "github.com",
				Owner: "acme", Repo: "widgets", Number: 111, Raw: raw,
			}
			if got != want {
				t.Errorf("Parse(%q) = %+v, want %+v", raw, got, want)
			}
			if gotDir != "/some/clone" || gotRemote != "origin" {
				t.Errorf("looked up remote %q in %q, want origin in /some/clone", gotRemote, gotDir)
			}
		})
	}
}

func TestParseBareNumberInAGitLabClone(t *testing.T) {
	got, err := ref.Parse(context.Background(), "111",
		ref.WithRemoteLookup(stubLookup("git@gitlab.com:acme/widgets.git", nil)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tracker != ref.TrackerGitLab {
		t.Errorf("Tracker = %q, want %q", got.Tracker, ref.TrackerGitLab)
	}
}

func TestParseBareNumberWithoutARemote(t *testing.T) {
	_, err := ref.Parse(context.Background(), "111",
		ref.WithRemoteLookup(stubLookup("", errors.New("not a git repository"))))
	if err == nil {
		t.Fatal("Parse succeeded without a remote, want an error")
	}
	for _, want := range []string{"111", "bare number", "origin remote", "URL", "not a git repository"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// A bare number resolves in a GitLab clone too, so naming GitHub here
	// sends a GitLab user hunting for a remote nobody expected.
	if strings.Contains(err.Error(), "GitHub") {
		t.Errorf("error %q names a tracker the failure has nothing to do with", err)
	}
}

// The two ways this fails are different problems and get different sentences:
// there is no remote at all, or there is one and it names no tracker sitrep
// can address. A local path is the second: a bare clone of /srv/git has an
// origin and no tracker behind it.
func TestParseBareNumberInAnUnrecognisedClone(t *testing.T) {
	_, err := ref.Parse(context.Background(), "111",
		ref.WithRemoteLookup(stubLookup("/srv/git/widgets.git", nil)))
	if err == nil {
		t.Fatal("Parse succeeded against an unrecognised remote, want an error")
	}
	for _, want := range []string{"111", "bare number", "/srv/git/widgets.git", "recognises", "URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "GitHub") {
		t.Errorf("error %q names a tracker the failure has nothing to do with", err)
	}
}

func TestEmptyRefUsesGenericRefVocabulary(t *testing.T) {
	for _, raw := range []string{"", " \t\n"} {
		_, err := ref.Parse(context.Background(), raw)
		if err == nil {
			t.Fatalf("Parse(%q) succeeded, want an error", raw)
		}
		if got, want := err.Error(), "a Ref is required"; got != want {
			t.Errorf("Parse(%q) error = %q, want %q", raw, got, want)
		}
	}
}

// Rule 4 of the prose contract: an error names the fix. "cannot parse this"
// with no accepted form beside it is a dead end.
func TestUnparseableRefNamesTheAcceptedForms(t *testing.T) {
	_, err := ref.Parse(context.Background(), "not a ref at all")
	if err == nil {
		t.Fatal("Parse succeeded on nonsense, want an error")
	}
	const wantMessage = `cannot parse "not a ref at all" as a Ref — pass an issue URL, "owner/repo#123", "PROJ-123" or a bare number inside a clone (run "sitrep --help" for every accepted form)`
	if got := err.Error(); got != wantMessage {
		t.Errorf("error = %q, want exact short diagnostic %q", got, wantMessage)
	}
	for _, want := range []string{"not a ref at all", "owner/repo#123", "PROJ-123", "bare number", "--help"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error %q spans more than one line", err)
	}
}

func TestUnparseableRefDiagnosticIsBoundedAndTerminalSafe(t *testing.T) {
	cutMultibyte := strings.Repeat("a", 79) + "é" + string([]byte{0xff}) + "\x1b\nremaining"
	tests := []struct {
		name        string
		raw         string
		wantPreview string
		wantLength  bool
	}{
		{name: "80 byte boundary", raw: strings.Repeat("a", 80), wantPreview: strings.Repeat("a", 80)},
		{name: "81 byte boundary", raw: strings.Repeat("b", 81), wantPreview: strings.Repeat("b", 80), wantLength: true},
		{name: "cut multibyte and controls", raw: cutMultibyte, wantPreview: strings.Repeat("a", 79) + `\xc3`, wantLength: true},
		{name: "large token", raw: strings.Repeat("z", 100_000), wantPreview: strings.Repeat("z", 80), wantLength: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ref.Parse(context.Background(), tt.raw)
			if err == nil {
				t.Fatal("Parse succeeded on malformed Ref, want an error")
			}
			message := err.Error()
			if !strings.Contains(message, tt.wantPreview) {
				t.Errorf("error = %q, want bounded quoted prefix %q", message, tt.wantPreview)
			}
			trimmedBytes := len(strings.TrimSpace(tt.raw))
			lengthText := fmt.Sprintf("(%d bytes)", trimmedBytes)
			if tt.wantLength != strings.Contains(message, lengthText) {
				t.Errorf("error = %q, length metadata presence = %t, want %t", message, strings.Contains(message, lengthText), tt.wantLength)
			}
			if tt.wantLength && strings.Contains(message, tt.raw) {
				t.Error("error contains the complete oversized Ref")
			}
			if strings.ContainsAny(message, "\n\r\x1b") {
				t.Errorf("error contains a physical line break or terminal escape: %q", message)
			}
			if len(message) >= 1024 {
				t.Errorf("error is %d bytes, want a compact diagnostic", len(message))
			}
			for _, guidance := range []string{"owner/repo#123", "PROJ-123", "bare number", "--help"} {
				if !strings.Contains(message, guidance) {
					t.Errorf("error %q lost guidance %q", message, guidance)
				}
			}
		})
	}
}

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		raw     string
		tracker ref.Tracker
		host    string
		owner   string
		repo    string
		wantErr bool
	}{
		{raw: "https://github.com/acme/widgets.git", tracker: ref.TrackerGitHub, host: "github.com", owner: "acme", repo: "widgets"},
		{raw: "https://user@github.com/acme/widgets", tracker: ref.TrackerGitHub, host: "github.com", owner: "acme", repo: "widgets"},
		{raw: "git@github.com:acme/widgets.git", tracker: ref.TrackerGitHub, host: "github.com", owner: "acme", repo: "widgets"},
		{raw: "ssh://git@github.com/acme/widgets.git", tracker: ref.TrackerGitHub, host: "github.com", owner: "acme", repo: "widgets"},
		{raw: "git://github.com/acme/widgets.git", tracker: ref.TrackerGitHub, host: "github.com", owner: "acme", repo: "widgets"},
		{raw: "https://GitHub.com/Acme/Widgets.git", tracker: ref.TrackerGitHub, host: "github.com", owner: "Acme", repo: "Widgets"},
		{raw: "https://ghe.corp.example/acme/widgets.git", tracker: ref.TrackerGitHub, host: "ghe.corp.example", owner: "acme", repo: "widgets"},
		{raw: "git@gitlab.com:acme/widgets.git", tracker: ref.TrackerGitLab, host: "gitlab.com", owner: "acme", repo: "widgets"},
		{raw: "git@gitlab.com:group/sub/project.git", tracker: ref.TrackerGitLab, host: "gitlab.com", owner: "group", repo: "sub/project"},
		{raw: "https://www.github.com/acme/widgets.git", tracker: ref.TrackerGitHub, host: "github.com", owner: "acme", repo: "widgets"},
		{raw: "https://www.ghe.example/acme/widgets.git", tracker: ref.TrackerGitHub, host: "www.ghe.example", owner: "acme", repo: "widgets"},
		{raw: "ssh://git@git.acme.test:8443/acme/widgets.git", tracker: ref.TrackerGitHub, host: "git.acme.test:8443", owner: "acme", repo: "widgets"},
		{raw: "git@git.acme.test:acme/widgets.git", tracker: ref.TrackerGitHub, host: "git.acme.test", owner: "acme", repo: "widgets"},
		{raw: "/srv/git/bare.git", wantErr: true},
		{raw: "", wantErr: true},
		{raw: "not a url", wantErr: true},
		{raw: "git@github.com:widgets.git", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ref.ParseRemoteURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseRemoteURL(%q) = %+v, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRemoteURL(%q): %v", tt.raw, err)
			}
			want := ref.Ref{Tracker: tt.tracker, Host: tt.host, Owner: tt.owner, Repo: tt.repo, Raw: tt.raw}
			if got != want {
				t.Errorf("ParseRemoteURL(%q) = %+v, want %+v", tt.raw, got, want)
			}
		})
	}
}

// A Ref renders the way a human writes one, which means it can be typed back
// in: the display key of a cross-repo child is a working Ref.
func TestRefStringRoundTrips(t *testing.T) {
	raws := []string{
		"acme/widgets#111",
		"https://github.com/acme/widgets/issues/111",
	}

	for _, raw := range raws {
		t.Run(raw, func(t *testing.T) {
			first, err := ref.Parse(context.Background(), raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", raw, err)
			}
			second, err := ref.Parse(context.Background(), first.String())
			if err != nil {
				t.Fatalf("Parse(%q): %v", first.String(), err)
			}
			if second.Owner != first.Owner || second.Repo != first.Repo || second.Number != first.Number {
				t.Errorf("%q round-tripped to %+v, want %+v", raw, second, first)
			}
		})
	}
}

// HostTracker is what the CLI asks to tell a Ref whose Tracker was read off a
// recognized host from one that was guessed.
func TestHostTracker(t *testing.T) {
	tests := []struct {
		host string
		want ref.Tracker
	}{
		{host: "github.com", want: ref.TrackerGitHub},
		{host: "GitHub.com", want: ref.TrackerGitHub},
		{host: "gitlab.com", want: ref.TrackerGitLab},
		{host: "gitlab.com:8443", want: ref.TrackerGitLab},
		{host: "acme.atlassian.net", want: ref.TrackerJira},
		{host: "ghe.example", want: ref.TrackerUnknown},
		{host: "www.ghe.example", want: ref.TrackerUnknown},
		{host: "git.acme.test:8443", want: ref.TrackerUnknown},
		{host: "", want: ref.TrackerUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := ref.HostTracker(tt.host); got != tt.want {
				t.Errorf("HostTracker(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestRefStringFallsBackToWhatTheUserTyped(t *testing.T) {
	r := ref.Ref{Raw: "111"}
	if r.String() != "111" {
		t.Errorf("String() = %q, want %q", r.String(), "111")
	}
}
