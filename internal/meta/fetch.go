package meta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// Downloader reads a foreign object's bytes.
type Downloader interface {
	Download(ctx context.Context, r Resource) (body io.ReadCloser, mediaType string, err error)
}

// Fetcher reads plain URLs. A URL can come from outside, such as a Jira attachment or a
// Notion page, so the server must not be talked into reaching its own network: it refuses
// private addresses at connect time, after DNS, which also defeats DNS rebinding.
type Fetcher struct{ client *http.Client }

// ErrBlockedAddress means a URL resolved to an address the server will not contact.
var ErrBlockedAddress = errors.New("address is not reachable from the swarm")

// NewFetcher returns a fetcher. allowPrivate turns the address check off, for an
// on-premises service that a person has deliberately configured.
func NewFetcher(allowPrivate bool) *Fetcher {
	d := &net.Dialer{Timeout: 15 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
			}
			if !publicAddr(ap.Addr()) {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, ap.Addr())
			}
			return nil
		}
	}
	return &Fetcher{client: &http.Client{
		Transport: &http.Transport{
			// No proxy: a proxy would make the connection for us, past the check above.
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}}
}

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	// Carrier-grade NAT, which IsPrivate leaves out, and the cloud metadata range.
	for _, p := range []string{"100.64.0.0/10", "169.254.0.0/16", "fc00::/7"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}

// Download implements Downloader.
func (f *Fetcher) Download(ctx context.Context, r Resource) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("fetching %s: status %d", r.Name, resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}
