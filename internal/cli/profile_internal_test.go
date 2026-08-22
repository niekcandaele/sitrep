package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/niekcandaele/sitrep/internal/config"
	"github.com/niekcandaele/sitrep/internal/provider"
	"github.com/niekcandaele/sitrep/internal/provider/github"
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
		context.Background(), config.Config{}, rawRefs, true, providerAuto, "",
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

	p, err := deps.newProvider(providerAuto, selection.first, selection.profile, "")
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
