package connect

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/vlog"
)

var log = vlog.For("connect")

// PullOptions narrows a pull.
type PullOptions struct {
	Query string    // Replaces the configured query, for this pull only.
	Since time.Time // Only items changed since then. Zero means everything.
}

// Connector reads items from another system. It is all an import needs.
type Connector interface {
	Pull(ctx context.Context, opts PullOptions, yield func(Item) error) error
}

// Getter reads one item. A webhook names what changed, and this fetches it once.
type Getter interface {
	Get(ctx context.Context, key string) (Item, error)
}

// Pusher writes a status back to the other system.
type Pusher interface {
	SetStatus(ctx context.Context, key, status string) error
}

// HookReader understands a system's webhooks.
type HookReader interface {
	// VerifyHook checks the signature, over the exact bytes received.
	VerifyHook(secret string, h http.Header, body []byte) error
	// HookKeys says which items a verified delivery is about.
	HookKeys(body []byte) ([]string, error)
}

// ErrPermanent marks a failure that trying again cannot fix, such as a status the other
// system's workflow does not allow right now.
var ErrPermanent = errors.New("permanent failure")

// Open returns the connector for a source.
func (c *Config) Open(s *Source) (Connector, error) {
	switch s.Kind {
	case "xlsx", "csv":
		return &fileSource{src: s, file: c.AbsPath(s)}, nil
	case "jira":
		return newJira(s)
	case "notion":
		return newNotion(s)
	}
	return nil, fmt.Errorf("unknown kind %q", s.Kind)
}

// webClient is the HTTP side of a remote connector. Credentials are read from the
// environment here, and go only to the configured host.
type webClient struct {
	base      string
	host      string
	source    string
	authorize func(*http.Request)
	client    *http.Client
	fetcher   *meta.Fetcher
}

func envValue(name, what string) (string, error) {
	if name == "" {
		return "", nil
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s: environment variable %s is not set", what, name)
	}
	return v, nil
}

func newWebClient(s *Source, base string, authorize func(*http.Request)) (*webClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad url %q", base)
	}
	if u.Scheme != "https" && !s.AllowPrivate {
		return nil, fmt.Errorf("%s: credentials would be sent unencrypted: use https", base)
	}
	return &webClient{
		base: strings.TrimRight(base, "/"), host: u.Host, source: s.Name, authorize: authorize,
		client:  &http.Client{Timeout: time.Minute},
		fetcher: meta.NewFetcher(s.AllowPrivate),
	}, nil
}

const maxResponse = 64 << 20

// do sends one JSON request. It retries what the other side says is temporary and honours
// its Retry-After, because a sync that ignores a rate limit gets locked out.
func (w *webClient) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	delay := 500 * time.Millisecond
	var last error
	for attempt := range 6 {
		if attempt > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			delay = min(delay*2, 30*time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, method, w.base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w.authorize(req)
		resp, err := w.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		resp.Body.Close()
		if v := log.V(vlog.LevelRequests); v.Enabled() {
			v.Info("connector request", "source", w.source, "method", method, "path", path, "status", resp.StatusCode)
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				delay = min(time.Duration(s)*time.Second, 2*time.Minute)
			}
			continue
		case resp.StatusCode/100 != 2:
			return fmt.Errorf("%w: %s %s: status %d: %s", ErrPermanent, method, path, resp.StatusCode, snippet(data))
		}
		if rerr != nil {
			last = rerr
			continue
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("%s %s: %w", method, path, err)
			}
		}
		return nil
	}
	return last
}

// snippet keeps an error useful without pulling a whole response into the log.
func snippet(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(bytes.ToValidUTF8(b, []byte("?")))
}

// Download implements meta.Downloader. The token goes only to the configured host, so a
// link edited to point elsewhere gets nothing. Go also drops it on a redirect to another
// domain, which is what a Jira attachment does on its way to its file store.
func (w *webClient) Download(ctx context.Context, r meta.Resource) (io.ReadCloser, string, error) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != w.host {
		// Unauthenticated, through the guarded fetcher.
		return w.fetcher.Download(ctx, r)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, "", err
	}
	w.authorize(req)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("downloading %s: status %d", r.Name, resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// verifySHA256 checks a signature of the form sha256=HEX, in constant time.
func verifySHA256(secret, header string, body []byte) error {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return errors.New("missing or unsupported signature")
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return errors.New("malformed signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return errors.New("wrong signature")
	}
	return nil
}
