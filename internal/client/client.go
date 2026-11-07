// Package client is a small typed HTTP client for the swarm API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Grano22/aiculator/internal/vlog"
)

var vl = vlog.For("client")

// Client talks to one server with one token.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
	// Header is added to every request, such as the secret a local helper asks for.
	Header map[string]string
}

// New returns a client with no overall timeout, because wait calls hold open.
func New(url, token string) *Client {
	return &Client{URL: url, Token: token, HTTP: &http.Client{}}
}

// StatusError is a non-2xx answer.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Msg) }

// Do sends in as JSON, when it is not nil, and decodes the answer into out.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range c.Header {
		req.Header.Set(k, v)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if v := vl.V(vlog.LevelHeaders); v.Enabled() {
		v.Info("request headers", "method", method, "path", path, "headers", vlog.Headers(req.Header))
	}
	if limit := vlog.HTTPLimit("client"); limit > 0 && in != nil {
		if b, err := json.Marshal(in); err == nil {
			vl.V(vlog.LevelBodies).Info("request body", "method", method, "path", path, "body", vlog.Body(b, limit))
		}
	}
	began := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if v := vl.V(vlog.LevelRequests); v.Enabled() {
			v.Info("request failed", "method", method, "url", c.URL+path, "took", time.Since(began), "err", err)
		}
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if v := vl.V(vlog.LevelRequests); v.Enabled() {
		v.Info("request", "method", method, "url", c.URL+path, "status", resp.StatusCode, "took", time.Since(began), "bytes", len(data))
	}
	if v := vl.V(vlog.LevelHeaders); v.Enabled() {
		v.Info("response headers", "path", path, "headers", vlog.Headers(resp.Header))
	}
	if limit := vlog.HTTPLimit("client"); limit > 0 {
		vl.V(vlog.LevelBodies).Info("response body", "path", path, "status", resp.StatusCode, "body", vlog.Body(data, limit))
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = string(data)
		}
		return &StatusError{resp.StatusCode, e.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Post sends a POST.
func (c *Client) Post(ctx context.Context, path string, in, out any) error {
	return c.Do(ctx, http.MethodPost, path, in, out)
}

// Get sends a GET.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// Upload sends raw bytes and decodes the JSON answer.
func (c *Client) Upload(ctx context.Context, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &StatusError{resp.StatusCode, string(data)}
	}
	return json.Unmarshal(data, out)
}

// Download streams a raw answer. The caller closes it.
func (c *Client) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, &StatusError{resp.StatusCode, string(data)}
	}
	return resp.Body, nil
}
