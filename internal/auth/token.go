// Package auth issues and verifies ed25519-signed agent tokens. The token carries
// the agent's identity and tier, so an agent can neither claim a tier it was not
// given nor act under another agent's name.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

// Claims is the signed content of a token.
type Claims struct {
	Agent   string    `json:"agent"`
	Tier    int       `json:"tier,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	Admin   bool      `json:"admin,omitempty"`
	Planner bool      `json:"planner,omitempty"`
	Viewer  bool      `json:"viewer,omitempty"` // May read dashboards and change nothing.
	Expires time.Time `json:"exp,omitzero"`
}

// Authority signs and verifies tokens.
type Authority struct {
	key ed25519.PrivateKey
	now func() time.Time
}

// ErrInvalidToken means a token is malformed, forged or expired.
var ErrInvalidToken = errors.New("invalid token")

var agentRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// New returns an authority with a fresh key.
func New() (*Authority, error) {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Authority{key: k, now: time.Now}, nil
}

// LoadOrCreate reads the signing key from path, creating it with mode 0600 if it
// does not exist.
func LoadOrCreate(path string) (*Authority, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("signing key %s: wrong size", path)
		}
		return &Authority{key: ed25519.PrivateKey(b), now: time.Now}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	a, err := New()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, a.key, 0o600); err != nil {
		return nil, err
	}
	return a, nil
}

// Issue signs a token for an agent. System identities cannot be issued, and the
// character check rules out the ":" that the system prefix needs.
func (a *Authority) Issue(c Claims) (string, error) {
	if !agentRE.MatchString(c.Agent) || core.IsSystem(c.Agent) {
		return "", fmt.Errorf("invalid agent name %q", c.Agent)
	}
	if !c.Admin && !c.Viewer && c.Tier < 1 {
		return "", fmt.Errorf("agent %s needs a tier", c.Agent)
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(ed25519.Sign(a.key, body)), nil
}

// Verify checks the signature and expiry and returns the claims.
func (a *Authority) Verify(token string) (Claims, error) {
	var c Claims
	bodyB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		return c, ErrInvalidToken
	}
	enc := base64.RawURLEncoding
	body, err1 := enc.DecodeString(bodyB64)
	sig, err2 := enc.DecodeString(sigB64)
	if err1 != nil || err2 != nil {
		return c, ErrInvalidToken
	}
	if !ed25519.Verify(a.key.Public().(ed25519.PublicKey), body, sig) {
		return c, ErrInvalidToken
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return c, ErrInvalidToken
	}
	if !c.Expires.IsZero() && a.now().After(c.Expires) {
		return c, fmt.Errorf("%w: expired", ErrInvalidToken)
	}
	return c, nil
}
