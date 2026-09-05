package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/model"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/fake"
	"github.com/niekcandaele/sitrep/internal/provider/github"
	"github.com/niekcandaele/sitrep/internal/provider/gitlab"
	"github.com/niekcandaele/sitrep/internal/provider/jira"
	"github.com/niekcandaele/sitrep/internal/ref"
)

func TestEffectiveInterval(t *testing.T) {
	tests := []struct {
		name    string
		flagSet bool
		flag    time.Duration
		profile time.Duration
		want    time.Duration
	}{
		{
			name: "neither is the default",
			want: defaultRefreshInterval,
		},
		{
			name:    "a profile's cadence beats the default",
			profile: 30 * time.Second,
			want:    30 * time.Second,
		},
		{
			name:    "an explicit flag beats a profile",
			flagSet: true, flag: 15 * time.Second, profile: 30 * time.Second,
			want: 15 * time.Second,
		},
		{
			// The whole reason this takes "was it set": the flag's default is
			// 60s, and a default must not silently outrank what the user wrote
			// down in their config file.
			name:    "an unset flag at its default does not beat a profile",
			flag:    defaultRefreshInterval,
			profile: 30 * time.Second,
			want:    30 * time.Second,
		},
		{
			name:    "an explicit flag equal to the default still beats a profile",
			flagSet: true, flag: defaultRefreshInterval, profile: 30 * time.Second,
			want: defaultRefreshInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveInterval(tt.flagSet, tt.flag, tt.profile); got != tt.want {
				t.Errorf("effectiveInterval(%v, %s, %s) = %s, want %s",
					tt.flagSet, tt.flag, tt.profile, got, tt.want)
			}
		})
	}
}

func TestEffectiveMaxTickets(t *testing.T) {
	tests := []struct {
		name    string
		profile *config.Profile
		want    int
	}{
		{name: "nil Profile uses the shared default", want: provider.DefaultMaxTickets},
		{name: "zero-value test Profile uses the shared default", profile: &config.Profile{}, want: provider.DefaultMaxTickets},
		{name: "positive Profile value wins", profile: &config.Profile{MaxTickets: 1000}, want: 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveMaxTickets(tt.profile); got != tt.want {
				t.Errorf("effectiveMaxTickets(%+v) = %d, want %d", tt.profile, got, tt.want)
			}
		})
	}
}

func TestNewFakeProviderUsesProfileMaxTicketsIndependentlyOfCadence(t *testing.T) {
	profile := &config.Profile{MaxTickets: 2, RefreshInterval: time.Hour}
	p, err := (Deps{}).newProvider(providerFake, connectionRoute{}, profile, "", fakeProviderSettings{})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	snap, err := p.Resolve(context.Background(), provider.QuerySelector{Query: "q"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(snap.Tickets) != 2 || !snap.LimitReached {
		t.Errorf("Query snapshot = %d tickets, LimitReached=%t; want 2/true", len(snap.Tickets), snap.LimitReached)
	}
	if got := effectiveInterval(false, 0, profile.RefreshInterval); got != time.Hour {
		t.Errorf("effective interval = %s, want 1h", got)
	}
}

func TestNewFakeProviderWiresCancellableDelay(t *testing.T) {
	constructed, err := (Deps{}).newProvider(providerFake, connectionRoute{}, nil, "", fakeProviderSettings{
		delay:    time.Hour,
		delaySet: true,
	})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	p := constructed.(*fake.Provider)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := p.Resolve(ctx, provider.QuerySelector{Query: "q"})
		result <- err
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for p.ResolveCalls() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("Resolve did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Resolve error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Resolve did not return after cancellation")
	}
}

// A Profile's won't-do labels have to survive the CLI seam: config decodes them,
// newGitLab hands them to the driver, and a closed issue carrying one comes back
// Cancelled. Proving it here rather than only in the driver's own tests is the
// point — the wiring is what breaks.
func TestNewProviderForwardsProfileWontDoLabelsToTheGitLabDriver(t *testing.T) {
	const issueBody = `{"iid":8509,"state":"closed","labels":["Backend"],` +
		`"references":{"full":"acme/widgets#8509"},` +
		`"web_url":"https://gitlab.com/acme/widgets/-/issues/8509","title":"Retire the shim"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/closed_by") || strings.HasSuffix(r.URL.Path, "/related_merge_requests") {
			_, _ = io.WriteString(w, "[]")
			return
		}
		_, _ = io.WriteString(w, issueBody)
	}))
	t.Cleanup(server.Close)

	resolve := func(t *testing.T, profile *config.Profile) model.Epic {
		t.Helper()
		deps := Deps{GitLabTokenSource: func(context.Context, string) (string, error) { return "test-token", nil }}
		constructed, err := deps.newProvider(providerAuto, connectionRoute{
			tracker: ref.TrackerGitLab, host: "gitlab.com", gitLabPath: profile.Project, raw: "8509",
		}, profile, "", fakeProviderSettings{})
		if err != nil {
			t.Fatalf("newProvider: %v", err)
		}
		p, ok := constructed.(*gitlab.Provider)
		if !ok {
			t.Fatalf("provider = %T, want *gitlab.Provider", constructed)
		}
		gitlab.WithBaseURL(server.URL)(p)

		snap, err := p.Resolve(context.Background(), provider.EpicSelector{Ref: ref.Ref{
			Tracker: ref.TrackerGitLab, Host: "gitlab.com", Number: 8509, Raw: "8509",
		}})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		return snap.Epic
	}

	t.Run("a configured list reaches the driver", func(t *testing.T) {
		epic := resolve(t, gitlabProfileFrom(t, "    wont_do_labels: [backend]\n"))
		// The Native Status is GitLab's spelling of the label, not the Profile's.
		if epic.Status != model.StatusCancelled || epic.NativeStatus != "Backend" {
			t.Errorf("status = (%v, %q), want (cancelled, \"Backend\")", epic.Status, epic.NativeStatus)
		}
	})

	t.Run("a profile writing none keeps the built-in list", func(t *testing.T) {
		epic := resolve(t, gitlabProfileFrom(t, ""))
		if epic.Status != model.StatusDone || epic.NativeStatus != "closed" {
			t.Errorf("status = (%v, %q), want (done, \"closed\")", epic.Status, epic.NativeStatus)
		}
	})
}

// gitlabProfileFrom parses a one-profile config document, so the value under
// test starts as the bytes a user writes rather than as a Go literal.
func gitlabProfileFrom(t *testing.T, extraKeys string) *config.Profile {
	t.Helper()
	doc := "profiles:\n  acme:\n    provider: gitlab\n    host: gitlab.com\n" +
		"    project: acme/widgets\n" + extraKeys
	cfg, err := config.Parse(strings.NewReader(doc), "/tmp/sitrep-test/config.yml")
	if err != nil {
		t.Fatalf("Parse:\n%s\n%v", doc, err)
	}
	profile := cfg.Profiles["acme"]
	return &profile
}

type captureRoundTripper func(*http.Request) (*http.Response, error)

func (f captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestNewProviderForwardsProfileMaxTicketsToConcreteProviders(t *testing.T) {
	const maxTickets = 7
	stop := errors.New("stop after first membership request")
	client := func(capture func(*http.Request)) *http.Client {
		return &http.Client{Transport: captureRoundTripper(func(req *http.Request) (*http.Response, error) {
			capture(req)
			return nil, stop
		})}
	}
	assertStopped := func(t *testing.T, err error, requests int) {
		t.Helper()
		if err == nil {
			t.Fatal("Resolve unexpectedly succeeded")
		}
		if requests != 1 {
			t.Errorf("membership requests = %d, want 1", requests)
		}
	}

	t.Run("GitHub GraphQL first", func(t *testing.T) {
		profile := &config.Profile{MaxTickets: maxTickets}
		deps := Deps{TokenSource: func(context.Context, string) (string, error) { return "test-token", nil }}
		constructed, err := deps.newProvider(providerAuto, connectionRoute{
			tracker: ref.TrackerGitHub, host: "github.com", raw: "--query",
		}, profile, "", fakeProviderSettings{})
		if err != nil {
			t.Fatalf("newProvider: %v", err)
		}
		p, ok := constructed.(*github.Provider)
		if !ok {
			t.Fatalf("provider = %T, want *github.Provider", constructed)
		}
		requests := 0
		github.WithEndpoint("https://github.example.test/graphql")(p)
		github.WithHTTPClient(client(func(req *http.Request) {
			requests++
			var payload struct {
				Variables struct {
					First int `json:"first"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Errorf("decode GraphQL request: %v", err)
			}
			if payload.Variables.First != maxTickets {
				t.Errorf("GraphQL first = %d, want %d", payload.Variables.First, maxTickets)
			}
		}))(p)
		_, err = p.Resolve(context.Background(), provider.QuerySelector{Query: "is:issue"})
		assertStopped(t, err, requests)
	})

	t.Run("GitLab per_page", func(t *testing.T) {
		profile := &config.Profile{MaxTickets: maxTickets}
		deps := Deps{GitLabTokenSource: func(context.Context, string) (string, error) { return "test-token", nil }}
		constructed, err := deps.newProvider(providerAuto, connectionRoute{
			tracker: ref.TrackerGitLab, host: "gitlab.com", raw: "--query",
		}, profile, "", fakeProviderSettings{})
		if err != nil {
			t.Fatalf("newProvider: %v", err)
		}
		p, ok := constructed.(*gitlab.Provider)
		if !ok {
			t.Fatalf("provider = %T, want *gitlab.Provider", constructed)
		}
		requests := 0
		gitlab.WithBaseURL("https://gitlab.example.test")(p)
		gitlab.WithHTTPClient(client(func(req *http.Request) {
			requests++
			if got := req.URL.Query().Get("per_page"); got != "7" {
				t.Errorf("per_page = %q, want 7", got)
			}
		}))(p)
		_, err = p.Resolve(context.Background(), provider.QuerySelector{Query: "state=opened"})
		assertStopped(t, err, requests)
	})

	t.Run("Jira maxResults", func(t *testing.T) {
		profile := &config.Profile{
			MaxTickets: maxTickets,
			Auth:       config.Auth{User: "reader@example.test", TokenEnv: "JIRA_TOKEN"},
		}
		deps := Deps{Env: func(name string) string {
			if name == "JIRA_TOKEN" {
				return "test-token"
			}
			return ""
		}}
		constructed, err := deps.newProvider(providerAuto, connectionRoute{
			tracker: ref.TrackerJira, host: "acme.atlassian.net", raw: "--query",
		}, profile, "/tmp/config.yml", fakeProviderSettings{})
		if err != nil {
			t.Fatalf("newProvider: %v", err)
		}
		p, ok := constructed.(*jira.Provider)
		if !ok {
			t.Fatalf("provider = %T, want *jira.Provider", constructed)
		}
		requests := 0
		jira.WithBaseURL("https://jira.example.test")(p)
		jira.WithHTTPClient(client(func(req *http.Request) {
			requests++
			if got := req.URL.Query().Get("maxResults"); got != "7" {
				t.Errorf("maxResults = %q, want 7", got)
			}
		}))(p)
		_, err = p.Resolve(context.Background(), provider.QuerySelector{Query: "project = ABC"})
		assertStopped(t, err, requests)
	})
}

// parseArgs calls Parse repeatedly, once per positional argument. The claim
// that the flag package remembers explicitly-set flags across those calls is
// what effectiveInterval's precedence rests on, so it is asserted rather than
// trusted.
func TestIsFlagSetSurvivesRepeatedParses(t *testing.T) {
	newSet := func() (*flag.FlagSet, *time.Duration) {
		fs := flag.NewFlagSet("sitrep", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Usage = func() {}
		d := fs.Duration("interval", defaultRefreshInterval, "")
		fs.Bool("json", false, "")
		return fs, d
	}

	t.Run("set before a positional argument", func(t *testing.T) {
		fs, d := newSet()
		if _, err := parseArgs(fs, []string{"--interval", "15s", "111", "--json"}); err != nil {
			t.Fatalf("parseArgs: %v", err)
		}
		if !isFlagSet(fs, "interval") {
			t.Error("isFlagSet = false for a flag typed before the ref")
		}
		if *d != 15*time.Second {
			t.Errorf("interval = %s, want 15s", *d)
		}
	})

	t.Run("set after a positional argument", func(t *testing.T) {
		fs, _ := newSet()
		if _, err := parseArgs(fs, []string{"111", "--interval", "15s"}); err != nil {
			t.Fatalf("parseArgs: %v", err)
		}
		if !isFlagSet(fs, "interval") {
			t.Error("isFlagSet = false for a flag typed after the ref")
		}
	})

	t.Run("not set", func(t *testing.T) {
		fs, _ := newSet()
		if _, err := parseArgs(fs, []string{"111", "--json"}); err != nil {
			t.Fatalf("parseArgs: %v", err)
		}
		if isFlagSet(fs, "interval") {
			t.Error("isFlagSet = true for a flag nobody typed")
		}
	})
}

func TestShouldCopyDeduplicatedRefs(t *testing.T) {
	tests := []struct {
		name     string
		retained int
		capacity int
		want     bool
	}{
		{name: "all unique", retained: 100_000, capacity: 100_000},
		{name: "one duplicate in a large list", retained: 100_000, capacity: 100_001},
		{name: "small shallow reduction", retained: 63, capacity: 64},
		{name: "small duplicate-heavy reduction", retained: 1, capacity: 64, want: true},
		{name: "half the backing is slack", retained: 32, capacity: 64, want: true},
		{name: "material ratio below slot threshold", retained: 4092, capacity: 5115},
		{name: "slot and ratio thresholds met", retained: 4096, capacity: 5120, want: true},
		{name: "slot threshold without material ratio", retained: 100_000, capacity: 101_024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldCopyDeduplicatedRefs(tt.retained, tt.capacity); got != tt.want {
				t.Errorf("shouldCopyDeduplicatedRefs(%d, %d) = %v, want %v",
					tt.retained, tt.capacity, got, tt.want)
			}
		})
	}
}

func TestResolveSelectionDetachesFullURLBeforeProviderRetention(t *testing.T) {
	const duplicates = 64
	raw := "https://github.com/acme/widgets/issues/112"
	backing := strings.Repeat(raw+"\n", duplicates)
	rawRefs := strings.Fields(backing)

	stopAfterHost := errors.New("stop after capturing provider host")
	var providerHost string
	deps := Deps{TokenSource: func(_ context.Context, host string) (string, error) {
		providerHost = host
		return "", stopAfterHost
	}}
	selection, err := deps.resolveSelection(
		context.Background(), config.Config{}, rawRefs, true, false, "", providerAuto, "",
	)
	if err != nil {
		t.Fatalf("resolveSelection: %v", err)
	}

	selected, ok := selection.selector.(provider.RefListSelector)
	if !ok {
		t.Fatalf("selector = %T, want provider.RefListSelector", selection.selector)
	}
	if len(selected.Refs) != 1 || cap(selected.Refs) != 1 {
		t.Fatalf("deduplicated selector len/cap = %d/%d, want 1/1", len(selected.Refs), cap(selected.Refs))
	}

	retained := []struct {
		name string
		ref  ref.Ref
	}{
		{name: "selection.first", ref: selection.first},
		{name: "selector.Refs[0]", ref: selected.Refs[0]},
	}
	for _, item := range retained {
		fields := []struct {
			name  string
			value string
		}{
			{name: "Host", value: item.ref.Host},
			{name: "Owner", value: item.ref.Owner},
			{name: "Repo", value: item.ref.Repo},
			{name: "Key", value: item.ref.Key},
			{name: "Raw", value: item.ref.Raw},
		}
		for _, field := range fields {
			if stringAliases(field.value, backing) {
				t.Errorf("%s.%s still aliases the bulk stdin string", item.name, field.name)
			}
		}
	}

	p, err := deps.newProvider(providerAuto, selection.route, selection.profile, "", fakeProviderSettings{})
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	if _, err := p.Resolve(context.Background(), selection.selector); !errors.Is(err, stopAfterHost) {
		t.Fatalf("provider Resolve error = %v, want %v", err, stopAfterHost)
	}
	if providerHost != "github.com" {
		t.Fatalf("provider host = %q, want github.com", providerHost)
	}
	if stringAliases(providerHost, backing) {
		t.Error("provider-retained Host still aliases the bulk stdin string")
	}
}

func stringAliases(value, backing string) bool {
	if value == "" || backing == "" {
		return false
	}
	valueData := unsafe.StringData(value)
	for i := 0; i < len(backing); i++ {
		if valueData == unsafe.StringData(backing[i:]) {
			return true
		}
	}
	return false
}

func TestResolveQuerySelectionUsesExplicitProfileWithoutOrigin(t *testing.T) {
	tests := []struct {
		name        string
		profile     config.Profile
		wantTracker ref.Tracker
		wantHost    string
		wantPath    string
	}{
		{name: "github", profile: config.Profile{Name: "work", Provider: "github"}, wantTracker: ref.TrackerGitHub, wantHost: "github.com"},
		{name: "jira", profile: config.Profile{Name: "work", Provider: "jira", Host: "acme.atlassian.net", Project: "ABC"}, wantTracker: ref.TrackerJira, wantHost: "acme.atlassian.net", wantPath: "ABC"},
		{name: "gitlab", profile: config.Profile{Name: "work", Provider: "gitlab", Host: "git.acme.test", Project: "group/project"}, wantTracker: ref.TrackerGitLab, wantHost: "git.acme.test", wantPath: "group/project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Profiles: map[string]config.Profile{"work": tt.profile}}
			deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
				panic("origin was read")
			}}
			selection, err := deps.resolveQuerySelection(context.Background(), cfg, " exact query ", providerAuto, "work")
			if err != nil {
				t.Fatalf("resolveQuerySelection: %v", err)
			}
			if selection.route.tracker != tt.wantTracker || selection.route.host != tt.wantHost || selection.route.gitLabPath != tt.wantPath {
				t.Errorf("route = %+v, want tracker=%s host=%q path=%q", selection.route, tt.wantTracker, tt.wantHost, tt.wantPath)
			}
			if selection.profile == nil || selection.profile.Name != "work" {
				t.Errorf("profile = %#v, want work", selection.profile)
			}
			if got := selection.selector.(provider.QuerySelector).Query; got != " exact query " {
				t.Errorf("query = %q, want exact value", got)
			}
		})
	}
}

func TestResolveQuerySelectionHonorsExplicitProfileWithInjectedProvider(t *testing.T) {
	profile := config.Profile{
		Name: "work", Provider: providerGitHub, Host: "ghe.acme.test",
		RefreshInterval: 30 * time.Second,
	}
	cfg := config.Config{Profiles: map[string]config.Profile{"work": profile}}
	deps := Deps{
		Provider: fake.New(),
		RemoteLookup: func(context.Context, string, string) (string, error) {
			panic("origin was read")
		},
	}

	selection, err := deps.resolveQuerySelection(context.Background(), cfg, "q", providerAuto, "work")
	if err != nil {
		t.Fatalf("resolveQuerySelection: %v", err)
	}
	if selection.profile == nil || selection.profile.Name != "work" {
		t.Fatalf("profile = %#v, want explicit work Profile", selection.profile)
	}
	if selection.route.tracker != ref.TrackerGitHub || selection.route.host != "ghe.acme.test" {
		t.Errorf("route = %+v, want explicit Profile route", selection.route)
	}
	if got := profileInterval(selection.profile); got != 30*time.Second {
		t.Errorf("profile interval = %s, want 30s", got)
	}
}

func TestResolveQuerySelectionRejectsProviderProfileMismatch(t *testing.T) {
	profile := config.Profile{Name: "work", Provider: "jira", Host: "acme.atlassian.net", Project: "ABC"}
	cfg := config.Config{Profiles: map[string]config.Profile{"work": profile}}
	_, err := (Deps{}).resolveQuerySelection(context.Background(), cfg, "q", providerGitHub, "work")
	if err == nil || !strings.Contains(err.Error(), `provider "github"`) || !strings.Contains(err.Error(), `provider "jira"`) {
		t.Fatalf("error = %v, want both provider values", err)
	}
}

func TestResolveQuerySelectionRoutesFromOriginOnce(t *testing.T) {
	tests := []struct {
		name        string
		remote      string
		provider    string
		wantTracker ref.Tracker
		wantHost    string
		wantPath    string
	}{
		{name: "github known host", remote: "git@github.com:acme/widgets.git", provider: providerAuto, wantTracker: ref.TrackerGitHub, wantHost: "github.com"},
		{name: "gitlab known host", remote: "https://gitlab.com/acme/platform/widgets.git", provider: providerAuto, wantTracker: ref.TrackerGitLab, wantHost: "gitlab.com", wantPath: "acme/platform/widgets"},
		{name: "unknown host forced github", remote: "ssh://git@git.acme.test/acme/widgets.git", provider: providerGitHub, wantTracker: ref.TrackerGitHub, wantHost: "git.acme.test"},
		{name: "unknown host forced gitlab", remote: "ssh://git@git.acme.test/acme/widgets.git", provider: providerGitLab, wantTracker: ref.TrackerGitLab, wantHost: "git.acme.test", wantPath: "acme/widgets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			deps := Deps{RemoteLookup: func(_ context.Context, dir, remote string) (string, error) {
				calls++
				if remote != "origin" {
					t.Errorf("remote = %q, want origin", remote)
				}
				return tt.remote, nil
			}}
			selection, err := deps.resolveQuerySelection(context.Background(), config.Config{}, "q", tt.provider, "")
			if err != nil {
				t.Fatalf("resolveQuerySelection: %v", err)
			}
			if calls != 1 {
				t.Errorf("origin lookups = %d, want 1", calls)
			}
			if selection.route.tracker != tt.wantTracker || selection.route.host != tt.wantHost || selection.route.gitLabPath != tt.wantPath {
				t.Errorf("route = %+v, want tracker=%s host=%q path=%q", selection.route, tt.wantTracker, tt.wantHost, tt.wantPath)
			}
		})
	}
}

func TestResolveQuerySelectionUsesMatchingProfileForKnownOrigin(t *testing.T) {
	tests := []struct {
		name        string
		remote      string
		profile     config.Profile
		wantTracker ref.Tracker
		wantHost    string
		wantPath    string
	}{
		{
			name:   "GitHub",
			remote: "git@github.com:acme/widgets.git",
			profile: config.Profile{
				Name: "work-github", Provider: providerGitHub,
				Auth:            config.Auth{TokenEnv: "GITHUB_TOKEN_REF"},
				RefreshInterval: 45 * time.Second, MaxTickets: 17,
			},
			wantTracker: ref.TrackerGitHub, wantHost: "github.com",
		},
		{
			name:   "GitLab",
			remote: "https://gitlab.com/checkout/widgets.git",
			profile: config.Profile{
				Name: "work-gitlab", Provider: providerGitLab, Host: "gitlab.com",
				Project:         "configured/group/widgets",
				Auth:            config.Auth{TokenEnv: "GITLAB_TOKEN_REF"},
				RefreshInterval: 45 * time.Second, MaxTickets: 17,
			},
			wantTracker: ref.TrackerGitLab, wantHost: "gitlab.com", wantPath: "configured/group/widgets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Profiles: map[string]config.Profile{tt.profile.Name: tt.profile}}
			deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
				return tt.remote, nil
			}}
			selection, err := deps.resolveQuerySelection(context.Background(), cfg, "q", providerAuto, "")
			if err != nil {
				t.Fatalf("resolveQuerySelection: %v", err)
			}
			if selection.profile == nil || !reflect.DeepEqual(*selection.profile, tt.profile) {
				t.Fatalf("profile = %#v, want complete matching Profile %#v", selection.profile, tt.profile)
			}
			if selection.route.tracker != tt.wantTracker || selection.route.host != tt.wantHost ||
				selection.route.gitLabPath != tt.wantPath {
				t.Errorf("route = %+v, want tracker=%s host=%q path=%q",
					selection.route, tt.wantTracker, tt.wantHost, tt.wantPath)
			}
			if got := effectiveInterval(false, 0, profileInterval(selection.profile)); got != 45*time.Second {
				t.Errorf("effective interval = %s, want 45s", got)
			}
			if got := effectiveMaxTickets(selection.profile); got != 17 {
				t.Errorf("effective max_tickets = %d, want 17", got)
			}
		})
	}
}

func TestResolveQuerySelectionRedactsCredentialBearingOrigin(t *testing.T) {
	const (
		credential = "github_pat_SECRET_47"
		query      = "SECRET_NATIVE_QUERY_47"
	)

	t.Run("provider contradiction", func(t *testing.T) {
		deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
			return "https://x-access-token:" + credential + "@github.com/acme/widgets.git", nil
		}}
		_, err := deps.resolveQuerySelection(context.Background(), config.Config{}, query, providerGitLab, "")
		if err == nil || !strings.Contains(err.Error(), "github.com") || !strings.Contains(err.Error(), "not GitLab") {
			t.Fatalf("error = %v, want credential-free provider contradiction", err)
		}
		for _, secret := range []string{credential, "x-access-token", query} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error = %q, leaked %q", err, secret)
			}
		}
	})

	t.Run("malformed origin", func(t *testing.T) {
		deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
			return "https://x-access-token:" + credential + "@github.com", nil
		}}
		_, err := deps.resolveQuerySelection(context.Background(), config.Config{}, query, providerAuto, "")
		if err == nil || !strings.Contains(err.Error(), "cannot parse git origin") {
			t.Fatalf("error = %v, want sanitized origin parse failure", err)
		}
		for _, secret := range []string{credential, "x-access-token", query} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error = %q, leaked %q", err, secret)
			}
		}
	})
}

func TestResolveQuerySelectionInfersUniqueProfileForUnknownOrigin(t *testing.T) {
	profile := config.Profile{Name: "work", Provider: "gitlab", Host: "git.acme.test", Project: "configured/path"}
	cfg := config.Config{Profiles: map[string]config.Profile{"work": profile}}
	deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
		return "git@git.acme.test:checkout/repository.git", nil
	}}
	selection, err := deps.resolveQuerySelection(context.Background(), cfg, "repo:ignored", providerAuto, "")
	if err != nil {
		t.Fatalf("resolveQuerySelection: %v", err)
	}
	if selection.profile == nil || selection.profile.Name != "work" {
		t.Fatalf("profile = %#v, want work", selection.profile)
	}
	if selection.route.tracker != ref.TrackerGitLab || selection.route.gitLabPath != "configured/path" {
		t.Errorf("route = %+v, want inferred GitLab Profile route", selection.route)
	}
}

func TestResolveQuerySelectionReportsAmbiguousOrMissingOrigin(t *testing.T) {
	t.Run("unknown host without interpretation", func(t *testing.T) {
		deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@git.acme.test:acme/widgets.git", nil
		}}
		_, err := deps.resolveQuerySelection(context.Background(), config.Config{}, "looks like JQL", providerAuto, "")
		if err == nil || !strings.Contains(err.Error(), "pass --profile or --provider") {
			t.Fatalf("error = %v, want disambiguation", err)
		}
	})

	t.Run("multiple matching profiles", func(t *testing.T) {
		cfg := config.Config{Profiles: map[string]config.Profile{
			"github": {Name: "github", Provider: "github", Host: "git.acme.test"},
			"gitlab": {Name: "gitlab", Provider: "gitlab", Host: "git.acme.test"},
		}}
		deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
			return "git@git.acme.test:acme/widgets.git", nil
		}}
		_, err := deps.resolveQuerySelection(context.Background(), cfg, "q", providerAuto, "")
		if err == nil || !strings.Contains(err.Error(), "pass --profile to choose") {
			t.Fatalf("error = %v, want profile ambiguity", err)
		}
	})

	t.Run("origin lookup failure", func(t *testing.T) {
		deps := Deps{RemoteLookup: func(context.Context, string, string) (string, error) {
			return "", errors.New("no such remote")
		}}
		_, err := deps.resolveQuerySelection(context.Background(), config.Config{}, "q", providerGitHub, "")
		want := "--query needs --profile or an unambiguous git origin in the current directory"
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("error = %v, want prefix %q", err, want)
		}
	})
}

func TestProfileTokenSource(t *testing.T) {
	fallbackErr := errors.New("the driver's own discovery ran")
	fallback := func(context.Context, string) (string, error) { return "", fallbackErr }
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}

	t.Run("an injected source wins", func(t *testing.T) {
		injected := github.TokenSource(func(context.Context, string) (string, error) {
			return "injected", nil
		})
		prof := &config.Profile{Name: "work", Auth: config.Auth{TokenEnv: "GHE_TOKEN"}}
		ts := profileTokenSource(injected, prof, env(map[string]string{"GHE_TOKEN": "from-env"}), fallback)
		if got, _ := ts(context.Background(), "github.com"); got != "injected" {
			t.Errorf("token = %q, want the injected source's", got)
		}
	})

	t.Run("no profile means the driver's own default", func(t *testing.T) {
		if ts := profileTokenSource(nil, nil, env(nil), fallback); ts != nil {
			t.Error("a run with no Profile must leave the driver its own default")
		}
	})

	t.Run("a profile with no token_env means the driver's own default", func(t *testing.T) {
		prof := &config.Profile{Name: "work"}
		if ts := profileTokenSource(nil, prof, env(nil), fallback); ts != nil {
			t.Error("a Profile that names no variable must leave the driver its own default")
		}
	})

	t.Run("the named variable is used when it is set", func(t *testing.T) {
		prof := &config.Profile{Name: "work", Auth: config.Auth{TokenEnv: "GHE_TOKEN"}}
		ts := profileTokenSource(nil, prof, env(map[string]string{"GHE_TOKEN": " from-env "}), fallback)
		got, err := ts(context.Background(), "ghe.acme.test")
		if err != nil {
			t.Fatalf("token source: %v", err)
		}
		if got != "from-env" {
			t.Errorf("token = %q, want the named variable's value, trimmed", got)
		}
	})

	// Naming a variable is a preference, not a demand: writing a Profile to set
	// a host or an interval must never break a working GitHub setup.
	t.Run("an unset variable falls back to the driver's own discovery", func(t *testing.T) {
		prof := &config.Profile{Name: "work", Auth: config.Auth{TokenEnv: "GHE_TOKEN"}}
		ts := profileTokenSource(nil, prof, env(nil), fallback)
		if _, err := ts(context.Background(), "ghe.acme.test"); !errors.Is(err, fallbackErr) {
			t.Errorf("error = %v, want the fallback's", err)
		}
	})
}
