package vlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

const (
	truncatedBody = 1 << 10
	fullBody      = 1 << 20
)

// HTTPLimit is how much of a body to keep at the current level; 0 means none.
func HTTPLimit(component string) int {
	switch n := ComponentLevel(component); {
	case n >= LevelFull:
		return fullBody
	case n >= LevelBodies:
		return truncatedBody
	}
	return 0
}

// Headers renders headers with credentials removed, in a stable order.
func Headers(h http.Header) string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	slices.Sort(names)
	var b strings.Builder
	for i, k := range names {
		if i > 0 {
			b.WriteString("; ")
		}
		v := strings.Join(h[k], ",")
		if IsSecretName(k) {
			v = Redacted
		}
		fmt.Fprintf(&b, "%s: %s", k, Scrub(v))
	}
	return b.String()
}

// Body renders a body for the log: credentials removed, and cut to limit bytes.
func Body(raw []byte, limit int) string {
	if limit <= 0 || len(raw) == 0 {
		return ""
	}
	clean := raw
	var doc any
	// Field names are the reliable signal, so use them whenever the body is JSON.
	if json.Unmarshal(raw, &doc) == nil {
		if out, err := json.Marshal(scrubJSON(doc)); err == nil {
			clean = out
		}
	} else {
		clean = []byte(Scrub(string(raw)))
	}
	if len(clean) > limit {
		return fmt.Sprintf("%s [truncated %d bytes]", clean[:limit], len(clean)-limit)
	}
	return string(clean)
}

func scrubJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if IsSecretName(k) {
				if _, isNumber := val.(float64); !isNumber {
					x[k] = Redacted
					continue
				}
			}
			x[k] = scrubJSON(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = scrubJSON(x[i])
		}
		return x
	case string:
		return Scrub(x)
	}
	return v
}

// Capture records what a handler writes, up to a limit, while still streaming it.
type Capture struct {
	http.ResponseWriter
	Status int
	Bytes  int64
	body   bytes.Buffer
	limit  int
}

// NewCapture wraps a response writer. A limit of 0 records no body.
func NewCapture(w http.ResponseWriter, limit int) *Capture {
	return &Capture{ResponseWriter: w, Status: http.StatusOK, limit: limit}
}

// WriteHeader implements http.ResponseWriter.
func (c *Capture) WriteHeader(code int) {
	c.Status = code
	c.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (c *Capture) Write(p []byte) (int, error) {
	if left := c.limit - c.body.Len(); left > 0 {
		c.body.Write(p[:min(left, len(p))])
	}
	n, err := c.ResponseWriter.Write(p)
	c.Bytes += int64(n)
	return n, err
}

// Flush keeps streaming responses working.
func (c *Capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (c *Capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Recorded returns the body kept so far.
func (c *Capture) Recorded() []byte { return c.body.Bytes() }
