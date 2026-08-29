package ref_test

import (
	"context"
	"errors"
	"testing"

	"github.com/niekcandaele/sitrep/internal/ref"
)

// noLookup fails loudly: the key form must be decided from the text alone, so a
// test of it that reaches git has found a bug.
func noLookup() ref.Option {
	return ref.WithRemoteLookup(func(context.Context, string, string) (string, error) {
		return "", errors.New("the key form must not consult a git remote")
	})
}

// The Jira-style key form: the third shape of the Ref grammar. A key
// carries no host — only a Profile knows the site — and no number.
func TestParseJiraStyleKey(t *testing.T) {
	tests := []struct {
		raw     string
		wantKey string
	}{
		{raw: "ABC-123", wantKey: "ABC-123"},
		{raw: "abc-123", wantKey: "ABC-123"},
		{raw: "AbC-123", wantKey: "ABC-123"},
		{raw: "A1_B-9", wantKey: "A1_B-9"},
		{raw: "  ABC-7  ", wantKey: "ABC-7"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), tt.raw, noLookup())
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.raw, err)
			}
			want := ref.Ref{Tracker: ref.TrackerJira, Key: tt.wantKey, Raw: tt.raw}
			if got != want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.raw, got, want)
			}
			if got.String() != tt.wantKey {
				t.Errorf("String() = %q, want %q", got.String(), tt.wantKey)
			}
		})
	}
}

func TestParseRejectsKeyShapedNonKeys(t *testing.T) {
	rejects := []string{"ABC-0", "ABC-", "-123", "ABC-1.5", "1AB-2", "_AB-2", "AB C-2", "ABC--1"}

	for _, raw := range rejects {
		t.Run(raw, func(t *testing.T) {
			got, err := ref.Parse(context.Background(), raw, noLookup())
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", raw, got)
			}
		})
	}
}

// A bare number is still a git-remote lookup: adding the key form must not
// swallow the form that was there first.
func TestParseBareNumberStillReachesTheLookup(t *testing.T) {
	var asked bool
	got, err := ref.Parse(context.Background(), "111", ref.WithRemoteLookup(
		func(context.Context, string, string) (string, error) {
			asked = true
			return "git@github.com:acme/widgets.git", nil
		}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !asked {
		t.Error("the bare number did not consult the git remote")
	}
	if got.Tracker != ref.TrackerGitHub || got.Number != 111 || got.Key != "" {
		t.Errorf(`Parse("111") = %+v, want the GitHub form`, got)
	}
}

// Both roads to a key produce the same value, which is what Profile prefix
// matching compares.
func TestParseJiraURLUpperCasesItsKey(t *testing.T) {
	got, err := ref.Parse(context.Background(), "https://acme.atlassian.net/browse/proj-12", noLookup())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Key != "PROJ-12" {
		t.Errorf("Key = %q, want %q", got.Key, "PROJ-12")
	}
	if got.Host != "acme.atlassian.net" {
		t.Errorf("Host = %q, want the URL's host", got.Host)
	}
}

func TestKeyPrefix(t *testing.T) {
	tests := []struct{ key, want string }{
		{key: "ABC-123", want: "ABC"},
		{key: "abc-123", want: "ABC"},
		{key: "A1_B-9", want: "A1_B"},
		{key: "", want: ""},
		{key: "111", want: ""},
		{key: "ABC-0", want: ""},
		{key: "acme/widgets#111", want: ""},
	}

	for _, tt := range tests {
		if got := ref.KeyPrefix(tt.key); got != tt.want {
			t.Errorf("KeyPrefix(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}
