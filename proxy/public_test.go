// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/outbound"
)

// The client a tenant's address is fetched with is outbound's: a server on this
// network, and every spelling of the metadata endpoint, are refused with its
// sentinel before a byte is sent. The table that decides is outbound's own.
func TestPublicIsTheOutboundRule(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer srv.Close()
	for _, u := range []string{
		srv.URL,
		"http://localhost/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[2002:a9fe:a9fe::1]/",
		"http://198.18.0.1/",
		"http://240.0.0.1/",
	} {
		if _, err := Public.Get(u); !errors.Is(err, outbound.ErrRefused) {
			t.Errorf("GET %s: err = %v, want outbound.ErrRefused", u, err)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the server on this network was reached %d times", n)
	}
}
