package provider_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/ref"
	"github.com/niekcandaele/sitrep/internal/termtext/termtexttest"
)

// The decorator's field-by-field test asserts what the multi-line helper does
// through the two fields that use it.
func TestSanitizedKeepsTheStructureOfMultiLineText(t *testing.T) {
	p := provider.Sanitized(&hostileProvider{})

	detail, err := p.FetchDetail(context.Background(), "t1")
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	want := "line one\nline two\n\tindented"
	if detail.Description != want {
		t.Errorf("Description = %q, want %q", detail.Description, want)
	}
	if got, want := detail.Comments[0].Body, "hello\nworld"; got != want {
		t.Errorf("Comment body = %q, want %q", got, want)
	}
}

// hostile is the string every tracker-controlled field of the fake below
// carries: an escape sequence, a bare CR, a C1 byte, a DEL, an unterminated
// right-to-left override and a pop directional isolate that closes nothing.
const hostile = "x\x1b[2J\x1b]0;pwned\ay\rz\u009b\x7f\u202eoverride\u2069"

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
		LimitReached: true,
		Parent:       model.Parent{ID: "p1", Key: hostile, Title: hostile, URL: hostile},
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

func (p *hostileProvider) FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	return provider.FetchDetailsDefault(ctx, ids, p.FetchDetail)
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

	if !snap.LimitReached {
		t.Error("sanitation dropped LimitReached")
	}
	checkClean(t, "snapshot", reflect.ValueOf(snap), false)
	checkClean(t, "detail", reflect.ValueOf(detail), true)

	// The walk above would pass on a title that lost its override entirely, so
	// the headline shape is asserted once outright: the code point is still
	// there and it is closed. --plain, --json and the decoded one-shot path all
	// read what this one call returns.
	title := snap.Tickets[0].Title
	if !strings.ContainsRune(title, 0x202E) || !strings.HasSuffix(title, "\u202c") {
		t.Errorf("Ticket title = %+q, want the U+202E kept and terminated by U+202C", title)
	}
}

func TestSanitizedFetchDetailsCleansSuccessesAndPreservesPartialFailures(t *testing.T) {
	boom := errors.New("boom")
	p := provider.Sanitized(&pluralHostileProvider{err: boom})
	details, err := p.FetchDetails(context.Background(), []model.TicketID{"t1", "t2"})
	if !errors.Is(err, boom) {
		t.Fatalf("FetchDetails error = %v, want to wrap boom", err)
	}
	var failures *provider.DetailFailures
	if !errors.As(err, &failures) || !errors.Is(failures.Failures["t2"], boom) {
		t.Fatalf("FetchDetails failures = %+v, want t2 wrapping boom", failures)
	}
	checkClean(t, "detail", reflect.ValueOf(details["t1"]), true)
}

type pluralHostileProvider struct {
	hostileProvider
	err error
}

func (p pluralHostileProvider) FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	detail, err := p.FetchDetail(ctx, "t1")
	if err != nil {
		return nil, err
	}
	return map[model.TicketID]model.Detail{"t1": detail}, &provider.DetailFailures{Failures: map[model.TicketID]error{"t2": p.err}}
}

func TestSanitizedFetchDetailsDoesNotMutateProviderResults(t *testing.T) {
	inner := &sharedPluralProvider{details: map[model.TicketID]model.Detail{
		"t1": {TicketID: "t1", Description: hostile},
	}}
	got, err := provider.Sanitized(inner).FetchDetails(context.Background(), []model.TicketID{"t1"})
	if err != nil {
		t.Fatalf("FetchDetails: %v", err)
	}
	if inner.details["t1"].Description != hostile {
		t.Errorf("underlying Detail = %q, want unsanitized shared value", inner.details["t1"].Description)
	}
	if got["t1"].Description == hostile {
		t.Errorf("sanitized Detail = %q, want terminal-safe copy", got["t1"].Description)
	}
}

type sharedPluralProvider struct {
	hostileProvider
	details map[model.TicketID]model.Detail
}

func (p *sharedPluralProvider) FetchDetails(context.Context, []model.TicketID) (map[model.TicketID]model.Detail, error) {
	return p.details, nil
}

func TestStampSnapshotPreservesLimitReached(t *testing.T) {
	p := &hostileProvider{}
	now := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	snap := provider.StampSnapshot(p, model.WatchlistSnapshot{LimitReached: true}, now)
	if !snap.LimitReached {
		t.Error("StampSnapshot dropped LimitReached")
	}
	if !snap.FetchedAt.Equal(now) || snap.Capabilities != p.Capabilities() {
		t.Errorf("stamp = FetchedAt %v Capabilities %+v", snap.FetchedAt, snap.Capabilities)
	}
}

type selectorRecorder struct {
	selector provider.Selector
}

func (*selectorRecorder) Name() string                     { return "recorder" }
func (*selectorRecorder) Capabilities() model.Capabilities { return model.Capabilities{} }
func (*selectorRecorder) FetchDetail(context.Context, model.TicketID) (model.Detail, error) {
	return model.Detail{}, nil
}
func (p *selectorRecorder) FetchDetails(ctx context.Context, ids []model.TicketID) (map[model.TicketID]model.Detail, error) {
	return provider.FetchDetailsDefault(ctx, ids, p.FetchDetail)
}
func (p *selectorRecorder) Resolve(_ context.Context, selector provider.Selector) (model.WatchlistSnapshot, error) {
	p.selector = selector
	snap := model.WatchlistSnapshot{Tickets: []model.Ticket{}}
	if query, ok := selector.(provider.QuerySelector); ok {
		snap.Header = provider.QueryHeader(query.Query)
	}
	return snap, nil
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

func TestSanitizedForwardsQuerySelectorUnchangedAndCleansItsHeader(t *testing.T) {
	inner := &selectorRecorder{}
	wrapped := provider.Sanitized(inner)
	want := provider.QuerySelector{Query: "  labels=backend&search=%0Dsecret\r\nline  "}

	snap, err := wrapped.Resolve(context.Background(), want)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(inner.selector, want) {
		t.Errorf("selector = %+v, want %+v", inner.selector, want)
	}
	if got, want := snap.Header.Title, "  labels=backend&search=%0Dsecret line  "; got != want {
		t.Errorf("Header.Title = %q, want %q", got, want)
	}
}

// checkClean walks every string in a value and asserts it carries no control
// character and leaves no bidirectional scope unbalanced. multiline says
// whether "\n" and "\t" are allowed, which is true of a Detail and false of a
// snapshot.
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
		for _, segment := range strings.Split(v.String(), "\n") {
			if unterminated, stray := termtexttest.Unbalanced(segment); unterminated != 0 || stray != 0 {
				t.Errorf("%s = %+q, which is not bidi-balanced (unterminated %U, stray %U)",
					path, v.String(), unterminated, stray)
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
// before the sanitizer ever sees it, which leaves the sanitization branch
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
