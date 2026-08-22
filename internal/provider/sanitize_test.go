package provider_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
)

func TestSanitizeLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ordinary text is untouched", in: "Add the widget", want: "Add the widget"},
		{name: "an erase-display sequence", in: "hi\x1b[2Jthere", want: "hi[2Jthere"},
		{name: "a window-title sequence", in: "hi\x1b]0;pwned\athere", want: "hi]0;pwnedthere"},
		{
			name: "OSC 52, which writes the clipboard",
			in:   "\x1b]52;c;cHduZWQ=\x07ok",
			want: "]52;c;cHduZWQ=ok",
		},
		{name: "a bare carriage return", in: "real\rfake", want: "realfake"},
		{name: "CRLF", in: "one\r\ntwo", want: "one two"},
		{name: "a newline becomes a space", in: "one\ntwo", want: "one two"},
		{name: "a tab becomes a space", in: "one\ttwo", want: "one two"},
		{name: "DEL", in: "a\x7fb", want: "ab"},
		{name: "a C1 control", in: "a\u009bb", want: "ab"},
		{name: "a NUL", in: "a\x00b", want: "ab"},
		{name: "multi-byte UTF-8 survives", in: "héllo — 世界 ✓", want: "héllo — 世界 ✓"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.SanitizeLine(tt.in); got != tt.want {
				t.Errorf("SanitizeLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The decorator's field-by-field test asserts what the multi-line helper does
// through the two fields that use it.
func TestSanitizedKeepsTheStructureOfMultiLineText(t *testing.T) {
	p := provider.Sanitized(&hostileProvider{})

	detail, err := p.FetchDetail(context.Background(), "t1")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	want := "line one\nline two\n\tindented[2J"
	if detail.Description != want {
		t.Errorf("Description = %q, want %q", detail.Description, want)
	}
	if got, want := detail.Comments[0].Body, "hello\nworld]52;c;cHduZWQ="; got != want {
		t.Errorf("Comment body = %q, want %q", got, want)
	}
}

// hostile is the string every tracker-controlled field of the fake below
// carries: an escape sequence, a bare CR, a C1 byte and a DEL.
const hostile = "x\x1b[2J\x1b]0;pwned\ay\rz\u009b\x7f"

// hostileProvider answers with hostile text in every field of a WatchlistSnapshot
// and a Detail. Asserting field by field is the point: a field added later and
// not sanitized fails this test rather than shipping.
type hostileProvider struct{}

func (*hostileProvider) Name() string { return "hostile" }

func (*hostileProvider) Capabilities() model.Capabilities {
	return model.Capabilities{Hierarchy: true, BlockingLinks: true, Comments: true, PullRequests: true}
}

func (*hostileProvider) Resolve(context.Context, provider.Selector) (model.WatchlistSnapshot, error) {
	user := model.User{Login: hostile, DisplayName: hostile, AvatarURL: hostile}
	pr := model.PullRequest{Number: 1, Title: hostile, URL: hostile, Repository: hostile}
	return model.WatchlistSnapshot{
		Header: model.WatchlistHeader{Key: hostile, Title: hostile, URL: hostile},
		Epic: model.Epic{
			ID: "e1", Key: hostile, Title: hostile, URL: hostile, NativeStatus: hostile,
			Repository: hostile, Assignees: []model.User{user}, PullRequests: []model.PullRequest{pr},
		},
		Tickets: []model.Ticket{{
			ID: "t1", Key: hostile, Title: hostile, URL: hostile, NativeStatus: hostile,
			Repository: hostile, Assignees: []model.User{user}, PullRequests: []model.PullRequest{pr},
		}},
		Parent: model.Parent{ID: "p1", Key: hostile, Title: hostile, URL: hostile},
	}, nil
}

func (*hostileProvider) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	return model.Detail{
		TicketID:    "t1",
		Description: "line one\r\nline two\n\tindented\x1b[2J",
		Comments: []model.Comment{{
			ID:     hostile,
			Author: model.User{Login: hostile, DisplayName: hostile, AvatarURL: hostile},
			Body:   "hello\r\nworld\x1b]52;c;cHduZWQ=\x07",
			URL:    hostile,
		}},
		Links: []model.Link{{
			Kind:        model.LinkBlockedBy,
			NativeLabel: hostile,
			Target: model.LinkTarget{
				ID: "t2", Key: hostile, Title: hostile, URL: hostile, NativeStatus: hostile,
			},
		}},
	}, nil
}

// Every string a Provider hands back is walked, so a field added later without
// sanitizing fails here.
func TestSanitizedCleansEveryField(t *testing.T) {
	p := provider.Sanitized(&hostileProvider{})

	snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: ref.Ref{}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	detail, err := p.FetchDetail(context.Background(), "t1")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	checkClean(t, "snapshot", reflect.ValueOf(snap), false)
	checkClean(t, "detail", reflect.ValueOf(detail), true)
}

type selectorRecorder struct {
	selector provider.Selector
}

func (*selectorRecorder) Name() string                     { return "recorder" }
func (*selectorRecorder) Capabilities() model.Capabilities { return model.Capabilities{} }
func (*selectorRecorder) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	return model.Detail{}, nil
}
func (p *selectorRecorder) Resolve(_ context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	p.selector = selector
	return model.WatchlistSnapshot{Tickets: []model.Ticket{}}, nil
}

func TestSanitizedForwardsRefListSelectorUnchanged(t *testing.T) {
	inner := &selectorRecorder{}
	wrapped := provider.Sanitized(inner)
	want := provider.RefListSelector{Refs: []ref.Ref{
		{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "one", Number: 1},
		{Tracker: ref.TrackerGitHub, Host: "github.com", Owner: "acme", Repo: "two", Number: 2},
	}}

	if _, err := wrapped.Resolve(context.Background(), want); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(inner.selector, want) {
		t.Errorf("selector = %+v, want %+v", inner.selector, want)
	}
}

// checkClean walks every string in a value and asserts it carries no control
// character. multiline says whether "\n" and "\t" are allowed, which is true of
// a Detail and false of a snapshot.
func checkClean(t *testing.T, path string, v reflect.Value, multiline bool) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		for _, r := range v.String() {
			if multiline && (r == '\n' || r == '\t') {
				continue
			}
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%s = %q, which carries the control character %U", path, v.String(), r)
				return
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			checkClean(t, path+"."+v.Type().Field(i).Name, v.Field(i), multiline)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			checkClean(t, path+"[]", v.Index(i), multiline)
		}
	}
}

// A driver's own prose may quote a server-supplied message, so the one funnel
// every classified error passes through cleans it.
//
// The verb is %s, not %q: %q escapes a control character into printable text
// before SanitizeLine ever sees it, which leaves the sanitization branch
// unexercised and the test green with the sanitization deleted.
func TestErrorfSanitizesTheMessage(t *testing.T) {
	const dirty = "boom\x1b[2J\nnext"
	const format = "gitlab: %s said %s"

	err := provider.Errorf(provider.KindUnavailable, format, "git.acme.test", dirty)
	if strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("error = %q, want no escape character", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error = %q, want one line", err)
	}
	// Deleting the sanitization from Errorf has to fail this test, so assert
	// the message is not what fmt alone would have produced.
	if raw := fmt.Sprintf(format, "git.acme.test", dirty); err.Error() == raw {
		t.Errorf("error = %q, want it to differ from the raw formatted string", err)
	}
	if provider.KindOf(err) != provider.KindUnavailable {
		t.Errorf("KindOf = %v, want KindUnavailable", provider.KindOf(err))
	}
}

// Replacing an error's rendered text must not sever what it wrapped: that is
// sanitizedMessage.Unwrap's whole reason to exist.
func TestErrorfKeepsWrappingThroughASanitizedMessage(t *testing.T) {
	err := provider.Errorf(provider.KindUnavailable, "gitlab: %s: %w", "boom\x1b[2J", context.Canceled)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(%q, context.Canceled) = false, want true", err)
	}
	if got := provider.KindOf(err); got != provider.KindUnavailable {
		t.Errorf("KindOf = %v, want KindUnavailable", got)
	}
	if strings.ContainsRune(err.Error(), 0x1b) || strings.Contains(err.Error(), "\n") {
		t.Errorf("error = %q, want one clean line", err)
	}
}
