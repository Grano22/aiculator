// Package meta holds what a task carries besides its working state: properties, blobs and
// references to things that live elsewhere, such as a Jira attachment.
//
// It is stored apart from the task record on purpose. That record is rewritten on every
// heartbeat, so anything large there would slow every commit. Nothing on the claim path
// reads this package.
package meta

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// Limits. A property too large for these belongs in a blob.
const (
	MaxBytes = 256 << 10
	MaxProps = 512
)

// ErrInvalid marks a rejected change.
var ErrInvalid = errors.New("invalid metadata")

// BlobRef points at bytes held by the swarm. The hash is the identity, so storing the same
// file twice costs nothing, and what is read back can be checked.
type BlobRef struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// Resource is a foreign object: something that lives elsewhere, and is only referred to.
// Its bytes are fetched on request, once, and kept as a blob.
type Resource struct {
	// Kind says what this is, such as http, jira.attachment or notion.file.
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size,omitempty"`
	// Connection names the configured source whose credentials can read this.
	Connection string `json:"connection,omitempty"`
	// Blob is set once the bytes have been fetched.
	Blob      *BlobRef  `json:"blob,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
}

// Prop is one property: exactly one of a value, a blob or a foreign resource.
type Prop struct {
	Value    any       `json:"value,omitempty"`
	Blob     *BlobRef  `json:"blob,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
}

// Meta is everything extra about one task.
type Meta struct {
	Props     map[string]Prop `json:"props,omitempty"`
	UpdatedAt time.Time       `json:"updated_at,omitzero"`
}

// Patch changes some properties. A null removes one, so that two writers changing
// different properties cannot undo each other.
type Patch struct {
	Props map[string]*Prop `json:"props"`
}

var (
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	shaRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidSHA reports whether s is a lowercase SHA-256 in hex.
func ValidSHA(s string) bool { return shaRE.MatchString(s) }

func (p Prop) validate() error {
	n := 0
	if p.Value != nil {
		n++
	}
	if p.Blob != nil {
		n++
		if !ValidSHA(p.Blob.SHA256) {
			return fmt.Errorf("blob hash %q", p.Blob.SHA256)
		}
	}
	if r := p.Resource; r != nil {
		n++
		if r.Kind == "" {
			return errors.New("a resource needs a kind")
		}
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("resource URL %q: only http and https are allowed", r.URL)
		}
	}
	if n != 1 {
		return errors.New("a property must hold exactly one of value, blob or resource")
	}
	return nil
}

// Apply returns m with the patch applied, or an error that leaves m as it was.
func (m Meta) Apply(p Patch, now time.Time) (Meta, error) {
	out := Meta{Props: make(map[string]Prop, len(m.Props)+len(p.Props)), UpdatedAt: now}
	for k, v := range m.Props {
		out.Props[k] = v
	}
	for k, v := range p.Props {
		if !nameRE.MatchString(k) {
			return m, fmt.Errorf("%w: property name %q", ErrInvalid, k)
		}
		if v == nil {
			delete(out.Props, k)
			continue
		}
		if err := v.validate(); err != nil {
			return m, fmt.Errorf("%w: property %s: %v", ErrInvalid, k, err)
		}
		out.Props[k] = *v
	}
	if len(out.Props) > MaxProps {
		return m, fmt.Errorf("%w: %d properties, over the limit of %d", ErrInvalid, len(out.Props), MaxProps)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return m, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(b) > MaxBytes {
		return m, fmt.Errorf("%w: %d bytes of properties, over the limit of %d: use a blob", ErrInvalid, len(b), MaxBytes)
	}
	return out, nil
}
