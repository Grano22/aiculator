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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

// Claims is the signed content of a token.
type Claims struct {
	Agent   string   `json:"agent"`
	Tier    int      `json:"tier,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Admin   bool     `json:"admin,omitempty"`
	Planner bool     `json:"planner,omitempty"`
	Viewer  bool     `json:"viewer,omitempty"` // May read dashboards and change nothing.
	// Control lets a viewer stop agents and clear gone sessions, and nothing else: it is
	// what the dashboard's buttons need, and far less than admin.
	Control bool      `json:"control,omitempty"`
	Expires time.Time `json:"exp,omitzero"`
}

// Authority signs and verifies tokens.
type Authority struct {
	key ed25519.PrivateKey
	now func() time.Time

	// verified remembers tokens whose signature checked out. Checking one costs about 30µs,
	// far more than the call it guards, and every agent sends the same token on every call.
	// A token's signature cannot stop being valid under the same key, and there is no
	// revocation, so this changes no answer. Only tokens that verified are kept, so forged
	// ones cannot fill it, and expiry is still checked each time.
	mu       sync.Mutex
	verified map[string]Claims
}

// maxVerified bounds the cache. When full it is emptied, which costs one check per token.
const maxVerified = 4096

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
	a.mu.Lock()
	c, hit := a.verified[token]
	a.mu.Unlock()
	if hit {
		c.Tags = slices.Clone(c.Tags)
		return a.checkExpiry(c)
	}
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
	a.mu.Lock()
	if a.verified == nil || len(a.verified) >= maxVerified {
		a.verified = make(map[string]Claims, 64)
	}
	cached := c
	cached.Tags = slices.Clone(c.Tags)
	a.verified[token] = cached
	a.mu.Unlock()
	return a.checkExpiry(c)
}

func (a *Authority) checkExpiry(c Claims) (Claims, error) {
	if !c.Expires.IsZero() && a.now().After(c.Expires) {
		return c, fmt.Errorf("%w: expired", ErrInvalidToken)
	}
	return c, nil
}
