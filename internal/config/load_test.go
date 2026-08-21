package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niekcandaele/sitrep/internal/config"
)

// envOf builds the injected environment reader every path-resolution test uses,
// so no test in this package reads the developer's real environment.
func envOf(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()

	t.Run("SITREP_CONFIG wins outright", func(t *testing.T) {
		got, err := config.DefaultPath(envOf(map[string]string{
			"SITREP_CONFIG":   "/elsewhere/sitrep.yml",
			"XDG_CONFIG_HOME": xdg,
			"HOME":            home,
		}))
		if err != nil {
			t.Fatalf("DefaultPath: %v", err)
		}
		if got != "/elsewhere/sitrep.yml" {
			t.Errorf("DefaultPath = %q, want the SITREP_CONFIG path", got)
		}
	})

	t.Run("XDG_CONFIG_HOME beats HOME", func(t *testing.T) {
		got, err := config.DefaultPath(envOf(map[string]string{"XDG_CONFIG_HOME": xdg, "HOME": home}))
		if err != nil {
			t.Fatalf("DefaultPath: %v", err)
		}
		if want := filepath.Join(xdg, "sitrep", "config.yml"); got != want {
			t.Errorf("DefaultPath = %q, want %q", got, want)
		}
	})

	t.Run("HOME is the fallback", func(t *testing.T) {
		got, err := config.DefaultPath(envOf(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("DefaultPath: %v", err)
		}
		if want := filepath.Join(home, ".config", "sitrep", "config.yml"); got != want {
			t.Errorf("DefaultPath = %q, want %q", got, want)
		}
	})

	t.Run("neither is an error naming the escape hatch", func(t *testing.T) {
		_, err := config.DefaultPath(envOf(nil))
		if err == nil {
			t.Fatal("DefaultPath with no HOME and no XDG_CONFIG_HOME must be an error")
		}
		if !strings.Contains(err.Error(), "SITREP_CONFIG") {
			t.Errorf("error = %q, want it to name $SITREP_CONFIG", err)
		}
	})
}

// A user who typed the other extension gets their config honoured rather than
// silently ignored; config.yml is the documented name and wins when both exist.
func TestDefaultPathAcceptsTheYamlExtension(t *testing.T) {
	xdg := t.TempDir()
	dir := filepath.Join(xdg, "sitrep")
	env := envOf(map[string]string{"XDG_CONFIG_HOME": xdg})

	writeFile(t, filepath.Join(dir, "config.yaml"), "profiles: {}\n")
	got, err := config.DefaultPath(env)
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, "config.yaml"); got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}

	writeFile(t, filepath.Join(dir, "config.yml"), "profiles: {}\n")
	got, err = config.DefaultPath(env)
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, "config.yml"); got != want {
		t.Errorf("DefaultPath = %q, want %q: config.yml wins when both exist", got, want)
	}
}

// The "GitHub with gh logged in works with no config file at all" criterion, at
// its own seam.
func TestLoadMissingFileIsSilence(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "sitrep", "config.yml"))
	if err != nil {
		t.Fatalf("Load of a missing file must not be an error: %v", err)
	}
	if len(cfg.Profiles) != 0 || cfg.Path != "" {
		t.Errorf("Load = %+v, want the empty Config", cfg)
	}
}

func TestLoadReadsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	writeFile(t, path, "profiles:\n  work:\n    provider: github\n    host: ghe.acme.test\n")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	if got := cfg.Profiles["work"].Host; got != "ghe.acme.test" {
		t.Errorf("host = %q, want ghe.acme.test", got)
	}
}

// A file that exists but cannot be read is a config the user believes is in
// effect. Only "not there" is silence.
func TestLoadPresentButUnreadableIsAnError(t *testing.T) {
	dir := t.TempDir()

	t.Run("a directory where the file should be", func(t *testing.T) {
		path := filepath.Join(dir, "config.yml")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("creating %s: %v", path, err)
		}
		if _, err := config.Load(path); err == nil {
			t.Fatal("a directory in the config's place must be an error")
		} else if !strings.Contains(err.Error(), path) {
			t.Errorf("error = %q, want it to name %q", err, path)
		}
	})

	t.Run("malformed YAML", func(t *testing.T) {
		path := filepath.Join(dir, "bad.yml")
		writeFile(t, path, "profiles:\n  x:\n   provider: github\n    host: nope\n")
		if _, err := config.Load(path); err == nil {
			t.Fatal("malformed YAML must be an error")
		} else if !strings.Contains(err.Error(), path) {
			t.Errorf("error = %q, want it to name %q", err, path)
		}
	})
}
