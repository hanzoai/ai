// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hanzoai/outbound"

	"github.com/hanzoai/ai/proxy"
)

// admitted stands a loopback server in for a public one: requests to its address
// skip the rule, and every other request — each redirect hop included — goes
// through outbound's own transport. It is a swap of this module's client, never a
// switch inside outbound.
type admitted struct {
	host string
	next http.RoundTripper
}

func (a admitted) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == a.host {
		return http.DefaultTransport.RoundTrip(r)
	}
	return a.next.RoundTrip(r)
}

// A public address that redirects inward is refused at the hop, because the hop is
// a new dial and the dial is where the address is judged.
func TestATenantAddressThatRedirectsInwardIsRefused(t *testing.T) {
	srv, reached := inside(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	}))
	t.Cleanup(front.Close)
	rule := outbound.Client(outbound.Options{Timeout: 5 * time.Second, Redirects: 5})
	prev := proxy.Public
	proxy.Public = &http.Client{
		Timeout:       rule.Timeout,
		CheckRedirect: rule.CheckRedirect,
		Transport:     admitted{host: front.Listener.Addr().String(), next: rule.Transport},
	}
	t.Cleanup(func() { proxy.Public = prev })

	for _, to := range []string{
		srv.URL + "/doc.txt",
		"http://localhost:" + srv.URL[len("http://127.0.0.1:"):] + "/doc.txt",
		"http://169.254.169.254/latest/meta-data/",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"file:///etc/passwd",
	} {
		address := front.URL + "/doc.txt?to=" + url.QueryEscape(to)
		if err, got := embedAddress(t, address); !errors.Is(err, outbound.ErrRefused) || got != "" {
			t.Errorf("a redirect to %s: err = %v, indexed %q; want outbound.ErrRefused and nothing", to, err, got)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the server on this network was reached %d times through a redirect", n)
	}
}
