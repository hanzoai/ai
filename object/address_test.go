// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/outbound"
)

// A document address that arrives on a request is the tenant's, and this process
// reads it from inside the cluster. These are the two entries that take one —
// /v1/ai/rag/embed and /v1/ai/rag/ingest's files[].url — asked to read this
// machine's neighbours and this machine's disk.

// inside is a server on this network: the stand-in for a sibling service or the
// metadata endpoint. It counts every request that reaches it.
func inside(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte("the secret of this network"))
	}))
	t.Cleanup(srv.Close)
	return srv, &reached
}

// embedAddress and ingestAddress hand the address to each tenant entry and report
// what came back and what got indexed.
func embedAddress(t *testing.T, address string) (error, string) {
	t.Helper()
	fake := newFakeIndex()
	t.Cleanup(fake.install())
	_, err := RagEmbedFile("acme", &RagEmbedRequest{FileID: "f1", Filename: "doc.txt", URL: address}, "en")
	return err, indexed(fake)
}

func ingestAddress(t *testing.T, address string) (*IngestStats, error, string) {
	t.Helper()
	fake := newFakeIndex()
	t.Cleanup(fake.install())
	stats, err := IngestSource("acme", &IngestRequest{Files: []IngestFile{{Name: "doc.txt", URL: address}}}, "en")
	return stats, err, indexed(fake)
}

func indexed(f *fakeIndex) string {
	var b strings.Builder
	for _, docs := range f.docs {
		for _, d := range docs {
			b.WriteString(d.Content)
		}
	}
	return b.String()
}

func TestATenantAddressOnThisNetworkIsRefused(t *testing.T) {
	srv, reached := inside(t)
	err, got := embedAddress(t, srv.URL+"/doc.txt")
	if !errors.Is(err, outbound.ErrRefused) {
		t.Errorf("rag embed of %s: err = %v, want outbound.ErrRefused", srv.URL, err)
	}
	stats, err, got2 := ingestAddress(t, srv.URL+"/doc.txt")
	if err == nil && (stats == nil || len(stats.Errors) == 0 || !strings.Contains(strings.Join(stats.Errors, " "), outbound.ErrRefused.Error())) {
		t.Errorf("rag ingest of %s was not refused: stats=%+v", srv.URL, stats)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the server on this network was reached %d times", n)
	}
	if strings.Contains(got+got2, "secret of this network") {
		t.Error("what the server on this network said was indexed for the tenant")
	}
}

// Readers that take a URL read a string with no scheme as a path on this
// machine's disk. A tenant's address is never one.
func TestATenantAddressIsNeverAPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(file, []byte("the secret on this disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{file, "file://" + file, "../" + filepath.Base(file)} {
		err, got := embedAddress(t, address)
		if err == nil {
			t.Errorf("rag embed of %q succeeded", address)
		}
		_, _, got2 := ingestAddress(t, address)
		if strings.Contains(got+got2, "secret on this disk") {
			t.Errorf("rag read %q off this machine's disk", address)
		}
	}
}

// The metadata endpoint and every IPv6 spelling of it and of this network are
// refused before a byte is sent; the table that decides is outbound's.
func TestATenantAddressNamingTheMetadataEndpointIsRefused(t *testing.T) {
	for _, address := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[2002:a9fe:a9fe::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://100.100.100.200/latest/meta-data/",
		"http://192.0.0.192/opc/v2/instance/",
	} {
		if err, _ := embedAddress(t, address); !errors.Is(err, outbound.ErrRefused) {
			t.Errorf("rag embed of %s: err = %v, want outbound.ErrRefused", address, err)
		}
	}
}
