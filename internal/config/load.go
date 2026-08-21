package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/niekcandaele/sitrep/internal/ref"
)

// The one file, and the one alternative spelling of it. config.yml is the
// documented name and wins when both exist; config.yaml is accepted when it
// does not, because a config file that is silently ignored is the worst failure
// a config file has.
const (
	fileName    = "config.yml"
	altFileName = "config.yaml"
	// dirName is the directory sitrep's config lives in, under the XDG config
	// home.
	dirName = "sitrep"
	// pathEnv is the escape hatch for people with unusual layouts: it names the
	// config file outright and skips every other rule.
	pathEnv = "SITREP_CONFIG"
)

// DefaultPath returns the path of sitrep's single global config file:
// $SITREP_CONFIG when set, else $XDG_CONFIG_HOME/sitrep/config.yml, else
// $HOME/.config/sitrep/config.yml. There is deliberately no per-repository
// config and no search: one file, one place, one answer to "where does this
// setting come from".
//
// When the resolved directory holds no config.yml but does hold a config.yaml,
// the latter's path is returned instead.
//
// env is injected — os.Getenv in production — so path resolution is testable
// without touching the process environment.
func DefaultPath(env func(string) string) (string, error) {
	if env == nil {
		env = os.Getenv
	}

	if path := strings.TrimSpace(env(pathEnv)); path != "" {
		return path, nil
	}

	dir := strings.TrimSpace(env("XDG_CONFIG_HOME"))
	if dir == "" {
		home := strings.TrimSpace(env("HOME"))
		if home == "" {
			return "", errors.New("cannot tell where sitrep's config file lives: " +
				"neither $XDG_CONFIG_HOME nor $HOME is set (set $SITREP_CONFIG to name it outright)")
		}
		dir = filepath.Join(home, ".config")
	}

	path := filepath.Join(dir, dirName, fileName)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		alt := filepath.Join(dir, dirName, altFileName)
		if _, altErr := os.Stat(alt); altErr == nil {
			return alt, nil
		}
	}
	return path, nil
}

// Load reads and validates the config file at path. A file that does not exist
// is not an error: it yields a Config with no Profiles, because a GitHub user
// is expected never to write one. A file that exists but cannot be read — wrong
// permissions, a directory where the file should be — is an error naming the
// path, because that is a config the user believes is in effect.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A missing file is silence, not an error — but sitrep still knows
			// which file it looked for, and an error telling the user where to
			// add a Profile has to name that file rather than the documented
			// default. $SITREP_CONFIG pointing somewhere empty is exactly the
			// case that got this wrong.
			return Config{Path: path}, nil
		}
		return Config{}, fmt.Errorf("reading sitrep's config file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
		return Config{}, fmt.Errorf("reading sitrep's config file %s: it is a directory", path)
	}

	return Parse(f, path)
}

// Parse reads a config document from r and validates it. path is used only in
// error messages.
func Parse(r io.Reader, path string) (Config, error) {
	cfg := Config{Path: path}

	dec := yaml.NewDecoder(r)
	// Strict decoding turns a typo into an error naming the line and the field,
	// which is half of "an error that names the file, the Profile and the
	// problem" for free.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, cfg.errorf("%s", err)
	}
	cfg.Path = path

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate checks the decoded document and fills in the fields that are derived
// from it — a Profile's Name, its parsed RefreshInterval.
//
// It is a pure function of the document: no environment is read, no file, no
// network. In particular a Profile whose token_env is unset in the environment
// is a valid Profile — the credential is only demanded when the Profile is
// actually used.
func (c *Config) validate() error {
	for _, name := range c.Names() {
		p := c.Profiles[name]
		p.Name = name
		if err := c.validateProfile(&p); err != nil {
			return err
		}
		c.Profiles[name] = p
	}
	return c.validateNoDuplicateKeyPrefixes()
}

func (c Config) validateProfile(p *Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return c.errorf("profile name must not be empty")
	}

	if err := c.validateProvider(*p); err != nil {
		return err
	}
	if err := c.validateHost(*p); err != nil {
		return err
	}
	if err := c.validateProject(*p); err != nil {
		return err
	}
	if err := c.validateAuth(*p); err != nil {
		return err
	}
	return c.validateInterval(p)
}

func (c Config) validateProvider(p Profile) error {
	switch p.Provider {
	case "":
		return c.profileErrorf(p.Name, "provider is required (%s)", orList(providerList))
	case providerGitHub, providerGitLab, providerJira:
		return nil
	default:
		return c.profileErrorf(p.Name, "unknown provider %q (use %s)", p.Provider, orList(providerList))
	}
}

func (c Config) validateHost(p Profile) error {
	if p.Host == "" {
		// GitHub is the one Provider whose host has a default worth assuming.
		if p.Provider == providerGitHub {
			return nil
		}
		return c.profileErrorf(p.Name, "host is required for a %s profile", p.Provider)
	}
	if strings.ContainsAny(p.Host, ":/") {
		return c.profileErrorf(p.Name,
			`host must be a hostname like "acme.atlassian.net", not a URL`)
	}
	return nil
}

func (c Config) validateProject(p Profile) error {
	if p.Provider != providerJira {
		return nil
	}
	if p.Project == "" {
		return c.profileErrorf(p.Name,
			"project is required for a jira profile: it is the key prefix Epic Refs match on")
	}
	// The project key is the prefix of every Epic Ref key in the project, so it
	// has to be a shape the Epic Ref grammar can produce.
	if ref.KeyPrefix(p.Project+"-1") != strings.ToUpper(p.Project) {
		return c.profileErrorf(p.Name, "project %q is not a Jira project key", p.Project)
	}
	return nil
}

func (c Config) validateAuth(p Profile) error {
	if p.Auth.Token != "" {
		return c.profileErrorf(p.Name, "auth.token must not be set — sitrep never stores tokens "+
			"in the config file; put the token in an environment variable and name it with auth.token_env")
	}
	if p.Auth.User != "" && p.Auth.UserEnv != "" {
		return c.profileErrorf(p.Name, "set auth.user or auth.user_env, not both")
	}

	if p.Auth.TokenEnv == "" {
		// GitHub can find a token on its own (`gh auth token`); the others have
		// no other way in.
		if p.Provider == providerGitHub {
			return nil
		}
		return c.profileErrorf(p.Name, "auth.token_env is required for a %s profile", p.Provider)
	}
	if !isEnvName(p.Auth.TokenEnv) {
		return c.profileErrorf(p.Name, "auth.token_env must be the NAME of an environment "+
			"variable (e.g. JIRA_API_TOKEN), not a token")
	}
	if p.Auth.UserEnv != "" && !isEnvName(p.Auth.UserEnv) {
		return c.profileErrorf(p.Name, "auth.user_env must be the NAME of an environment "+
			"variable (e.g. JIRA_USER), not a value")
	}
	return nil
}

func (c Config) validateInterval(p *Profile) error {
	if p.RawRefreshInterval == "" {
		return nil
	}
	d, err := time.ParseDuration(p.RawRefreshInterval)
	if err != nil {
		return c.profileErrorf(p.Name, `refresh_interval %q is not a duration (try "30s" or "2m")`,
			p.RawRefreshInterval)
	}
	if d < MinRefreshInterval {
		return c.profileErrorf(p.Name, "refresh_interval must be at least %s", MinRefreshInterval)
	}
	p.RefreshInterval = d
	return nil
}

// validateNoDuplicateKeyPrefixes rejects two Jira Profiles claiming one project
// key. A Jira Profile serves exactly one project key, which keeps prefix
// matching a single equality with no precedence rules — and keeps "which
// Profile served this?" answerable.
func (c Config) validateNoDuplicateKeyPrefixes() error {
	claimed := map[string]string{}
	for _, name := range c.Names() {
		p := c.Profiles[name]
		if p.Provider != providerJira {
			continue
		}
		key := strings.ToUpper(p.Project)
		if first, ok := claimed[key]; ok {
			return c.errorf("profiles %q and %q both claim the Jira project key %q", first, name, key)
		}
		claimed[key] = name
	}
	return nil
}

// isEnvName reports whether s is the name of an environment variable rather
// than a value. It is also the whole of sitrep's "that looks like a token"
// check: a real token contains characters a variable name cannot. There is no
// secret detection beyond this.
func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// orList renders a set of allowed values the way an error message reads them
// aloud: "github, gitlab or jira".
func orList(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	default:
		return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
	}
}
