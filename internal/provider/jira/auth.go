package jira

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/niekcandaele/sitrep/internal/provider"
)

// Credentials are the Atlassian email + API token pair Jira Cloud's documented
// basic-auth flow wants: the account email as the username and the API token as
// the password. The token is a credential — it is never logged, printed,
// rendered, serialized or wrapped into an error.
type Credentials struct {
	// Email is the Atlassian account email. It is an identity, not a secret.
	Email string
	// Token is the API token created at id.atlassian.com.
	Token string
}

// String renders Credentials without their token, so a stray %v in a future
// error cannot leak one.
func (c Credentials) String() string {
	token := "REDACTED"
	if c.Token == "" {
		token = ""
	}
	return fmt.Sprintf("jira.Credentials{Email:%s, Token:%s}", c.Email, token)
}

// header renders the Authorization header value Jira Cloud expects.
func (c Credentials) header() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
}

// CredentialSource returns the Credentials for a host. It is a function rather
// than an interface for the same reason github.TokenSource is: a test injects
// one line and no production code learns it is being tested.
//
// There is deliberately no default source and no environment fallback chain —
// no JIRA_API_TOKEN read here, no .netrc, no keyring. Jira credentials arrive
// through a Profile, resolved in internal/cli; a second source of truth for
// them would be a second place to look when a run cannot authenticate.
type CredentialSource func(ctx context.Context, host string) (Credentials, error)

// missingTokenError and missingEmailError name both halves of the pair,
// because a user who has neither usually does not know which one they are
// missing. Neither ever contains a credential.
func missingTokenError(host string) error {
	return provider.Errorf(provider.KindAuth, "jira: no API token for %s — set the environment variable "+
		"your Profile's auth.token_env names", host)
}

func missingEmailError(host string) error {
	return provider.Errorf(provider.KindAuth, "jira: no Atlassian account email for %s — set auth.user "+
		"(or auth.user_env) in your Profile", host)
}
