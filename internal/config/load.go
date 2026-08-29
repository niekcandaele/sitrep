package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/niekcandaele/sitrep/internal/provider"
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
		return Config{}, cfg.decodeError(err)
	}
	// A YAML stream may hold several documents. sitrep reads one, so a second
	// "---" would silently discard Profiles the user believes are configured —
	// the worst thing a config file can do.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, cfg.errorf("this config file has more than one YAML document; sitrep reads one")
	}
	cfg.Path = path

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// unknownField matches the one message shape yaml.v3 produces for a key that
// strict decoding does not recognise: "line 5: field token_env not found in
// type config.Profile".
var unknownField = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)

// yamlSection names one of sitrep's config sections in the user's vocabulary
// and lists the keys it accepts, so an unknown key can be answered with the
// keys that would have worked.
type yamlSection struct {
	label string
	keys  []string
}

// yamlSections maps the Go types yaml.v3 names in its own errors onto sitrep's
// vocabulary. auth.token is deliberately absent from auth's key list: it
// decodes so that writing a literal token gets a message saying not to, not
// because it is a key anyone should use.
var yamlSections = map[string]yamlSection{
	"config.Config":  {label: "the top level", keys: []string{"profiles"}},
	"config.Profile": {label: "a profile", keys: []string{"provider", "host", "project", "auth", "refresh_interval", "max_tickets", "wont_do_labels"}},
	"config.Auth":    {label: "auth", keys: []string{"token_env", "user", "user_env"}},
}

// yamlHomes names the section a misplaced key actually belongs to, for the
// keys whose home is obvious enough to guess.
var yamlHomes = map[string]string{
	"token_env": "auth",
	"user":      "auth",
	"user_env":  "auth",
}

// decodeError turns a decode failure into sitrep's own prose. yaml.v3 reports
// every unrecognised key in one *yaml.TypeError, and its own wording names Go
// types the user has never heard of and no key that would have worked — so
// each entry is rewritten and all of them are reported. Anything that is not a
// TypeError is a genuine YAML syntax error, which is the library's business
// and whose message is already good.
func (c Config) decodeError(err error) error {
	var terr *yaml.TypeError
	if !errors.As(err, &terr) || len(terr.Errors) == 0 {
		return c.errorf("%s", err)
	}
	// Joined with "; " rather than newlines: every other error this package
	// raises is one line, and the CLI prints them on one line regardless.
	lines := make([]string, 0, len(terr.Errors))
	for _, e := range terr.Errors {
		lines = append(lines, rewriteTypeError(e))
	}
	return c.errorf("%s", strings.Join(lines, "; "))
}

// rewriteTypeError rewrites one yaml.v3 type-error entry, or returns it
// unchanged when it is not the unknown-key shape.
func rewriteTypeError(entry string) string {
	m := unknownField.FindStringSubmatch(entry)
	if m == nil {
		return entry
	}
	line, field, goType := m[1], m[2], m[3]
	section, ok := yamlSections[goType]
	if !ok {
		return entry
	}
	msg := fmt.Sprintf("line %s: unknown key %q in %s — valid keys are %s",
		line, field, section.label, strings.Join(section.keys, ", "))
	if home, ok := yamlHomes[field]; ok && home != section.label {
		msg += fmt.Sprintf(" (%s belongs under %s:)", field, home)
	}
	return msg
}

// validate checks the decoded document and fills in the fields that are derived
// from it — a Profile's Name, parsed RefreshInterval, and effective MaxTickets.
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
	if err := c.validateWontDoLabels(*p); err != nil {
		return err
	}
	if err := c.validateAuth(*p); err != nil {
		return err
	}
	if err := c.validateInterval(p); err != nil {
		return err
	}
	return c.validateMaxTickets(p)
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
	// A self-hosted instance may listen on a non-default port, and after
	// credential scoping a Profile is the only way such a host can get a token
	// at all — so "acme.example:8443" has to be nameable. Anything else carrying
	// a ":" or a "/" is a URL.
	if !isHostname(p.Host) {
		return c.profileErrorf(p.Name,
			`host must be a hostname like "acme.atlassian.net", not a URL`)
	}
	return nil
}

// isHostname reports whether s is a bare host with an optional numeric port,
// which is the shape a Profile's host must have.
func isHostname(s string) bool {
	if strings.Contains(s, "/") {
		return false
	}
	host, port, found := strings.Cut(s, ":")
	if !found {
		return true
	}
	if host == "" || port == "" || strings.Contains(port, ":") {
		return false
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (c Config) validateProject(p Profile) error {
	if p.Provider == providerGitHub && p.Project != "" {
		// Honouring it would mean deciding what a default repository means for
		// every shape a GitHub Ref can take. Rejecting it is honest;
		// accepting and ignoring it is the worst thing a config file can do.
		return c.profileErrorf(p.Name,
			"project is not used by a github profile (a GitHub Ref carries its own owner/repo)")
	}
	if p.Provider == providerGitLab {
		// "groups/" is how a GitLab path declares it names a group; with nothing
		// after it, it names nothing at all. Every other spelling is a path
		// sitrep cannot check offline and does not try to.
		group, prefixed := strings.CutPrefix(strings.TrimLeft(strings.TrimSpace(p.Project), "/"), "groups/")
		if prefixed && strings.Trim(group, "/") == "" {
			return c.profileErrorf(p.Name, "project %q names no group — write groups/<group path>", p.Project)
		}
		return nil
	}
	if p.Provider != providerJira {
		return nil
	}
	if p.Project == "" {
		return c.profileErrorf(p.Name,
			"project is required for a jira profile: it is the key prefix Refs match on")
	}
	// The project key is the prefix of every Ref key in the project, so it
	// has to be a shape the Ref grammar can produce.
	if ref.KeyPrefix(p.Project+"-1") != strings.ToUpper(p.Project) {
		return c.profileErrorf(p.Name, "project %q is not a Jira project key", p.Project)
	}
	return nil
}

// validateWontDoLabels checks the GitLab won't-do label list. A Profile that
// writes nothing keeps sitrep's built-in list; a Profile that writes the key has
// to mean something by it.
func (c Config) validateWontDoLabels(p Profile) error {
	if p.WontDoLabels == nil {
		return nil
	}
	if p.Provider != providerGitLab {
		// Accepting and ignoring it is the worst thing a config file can do, and
		// there is nothing to honour: GitHub's not_planned and Jira's resolution
		// say outright what a label can only imply.
		return c.profileErrorf(p.Name, "wont_do_labels is only used by a gitlab profile "+
			"(GitHub and Jira report cancellation natively)")
	}
	if len(p.WontDoLabels) == 0 {
		// Honouring an empty list would silently turn the inference off and
		// flatter every Epic containing abandoned work.
		return c.profileErrorf(p.Name,
			"wont_do_labels names no labels — remove the key to use sitrep's built-in list")
	}
	for _, label := range p.WontDoLabels {
		if strings.TrimSpace(label) == "" {
			return c.profileErrorf(p.Name, "wont_do_labels contains an empty label name")
		}
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

	// Both env-name checks run before any provider-specific rule below: a
	// non-Jira Profile needs no token_env, but if it names one anyway — or names
	// a user_env — that name still has to be a name rather than a pasted secret,
	// or Profile.Credential will echo the secret back in "$X is not set".
	if p.Auth.TokenEnv != "" && !isEnvName(p.Auth.TokenEnv) {
		return c.profileErrorf(p.Name, "auth.token_env must be the upper-case NAME of an "+
			"environment variable (e.g. JIRA_API_TOKEN), not a token")
	}
	if p.Auth.UserEnv != "" && !isEnvName(p.Auth.UserEnv) {
		return c.profileErrorf(p.Name, "auth.user_env must be the upper-case NAME of an "+
			"environment variable (e.g. JIRA_USER), not a value")
	}

	if p.Auth.TokenEnv == "" && p.Provider == providerJira {
		// GitHub and GitLab can find a token on their own (`gh auth token`,
		// `glab auth login`); Jira has no other way in.
		return c.profileErrorf(p.Name, "auth.token_env is required for a %s profile", p.Provider)
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

func (c Config) validateMaxTickets(p *Profile) error {
	if p.RawMaxTickets == "" {
		p.MaxTickets = provider.DefaultMaxTickets
		return nil
	}
	maxTickets, err := strconv.Atoi(p.RawMaxTickets)
	if err != nil {
		return c.profileErrorf(p.Name, "max_tickets %q is not a positive integer", p.RawMaxTickets)
	}
	if maxTickets < 1 {
		return c.profileErrorf(p.Name, "max_tickets must be at least 1")
	}
	p.MaxTickets = maxTickets
	return nil
}

// validateNoDuplicateKeyPrefixes rejects two Jira Profiles claiming one project
// key on one site. Two Atlassian sites may each have a project called ABC, and
// a Ref that names its site picks between them at selection time; two Profiles
// for the same key on the same site name no such difference, so "which Profile
// served this?" would have no answer.
func (c Config) validateNoDuplicateKeyPrefixes() error {
	type claim struct{ host, key string }
	claimed := map[claim]string{}
	for _, name := range c.Names() {
		p := c.Profiles[name]
		if p.Provider != providerJira {
			continue
		}
		k := claim{host: normalizeHost(p.effectiveHost()), key: strings.ToUpper(p.Project)}
		if first, ok := claimed[k]; ok {
			return c.errorf("profiles %q and %q both claim the Jira project key %q", first, name, k.key)
		}
		claimed[k] = name
	}
	return nil
}

// isEnvName reports whether s is the name of an environment variable rather
// than a value. It is also the whole of sitrep's "that looks like a token"
// check: a real token contains characters a variable name cannot. There is no
// secret detection beyond this.
//
// The shape is the POSIX-conventional upper-case one — [A-Z_][A-Z0-9_]* —
// which every example in the README and in sitrep's own error messages already
// uses. Lower case is excluded on purpose: real GitHub, GitLab and Atlassian
// tokens carry lower-case letters, so accepting them here is what lets a pasted
// token pass validation and then be echoed back by Profile.Credential's "$X is
// not set" error. Loosening this reopens that.
func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c == '_':
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
