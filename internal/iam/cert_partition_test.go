// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// An application and the certificate it points at are read from the SAME
// partition, and that partition is the platform one.
//
// THE BUG THIS LOCKS OUT. GetApplication resolved admin/<app> while GetCert
// resolved <IAM_ORG>/<cert>. An application's `cert` field is a bare name and the
// cert row is written owner=admin, so any deployment whose IAM_ORG was not
// "admin" read the app fine and then could not find its cert — "the entity does
// not exist" for a row that was sitting right there. The signing key every bearer
// token is validated against never got established, and the failure named a
// missing certificate rather than a misaddressed lookup.
//
// It is asserted on the REQUEST rather than on a response, because the defect was
// never in what IAM returned — it was in which tenant we asked. The two reads
// carry their key differently (the application in the query, the cert in the
// body), so both are checked where that route actually reads it.

// capture is an iam.HttpClient that records each request and answers a minimal
// body. It keeps the method and the sent body alongside the URL, because on
// these routes the key rides in the query or in the body depending on which.
type capture struct {
	urls    []string
	methods []string
	sent    []string
	body    string
}

func (c *capture) Do(r *http.Request) (*http.Response, error) {
	c.urls = append(c.urls, r.URL.String())
	c.methods = append(c.methods, r.Method)
	out := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		out = string(b)
	}
	c.sent = append(c.sent, out)
	body := c.body
	if body == "" {
		body = `{"name":"x"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestCertAndApplicationShareThePlatformPartition(t *testing.T) {
	cap := &capture{}
	SetHttpClient(cap)
	t.Cleanup(func() { SetHttpClient(&http.Client{}) })

	// A tenant org that is deliberately NOT the platform one — this is the shape
	// every real deployment has (IAM_ORG=hanzo, apps under admin).
	InitConfig("http://iam.test", "hanzo-cloud", "secret", "", "hanzo", "hanzo-cloud")

	if _, err := GetApplication("hanzo-cloud"); err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if _, err := GetCert("cert-hanzo"); err != nil {
		t.Fatalf("GetCert: %v", err)
	}
	if len(cap.urls) != 2 {
		t.Fatalf("want 2 requests, got %d: %v", len(cap.urls), cap.urls)
	}

	for i, want := range []string{
		"http://iam.test/v1/iam/applications/get?name=hanzo-cloud&owner=admin",
		"http://iam.test/v1/iam/certs/get",
	} {
		if cap.urls[i] != want {
			t.Errorf("request %d asked %q, want %q", i, cap.urls[i], want)
		}
	}

	// The application names its key in the query; the cert names its own in the
	// body. Both must say the PLATFORM owner — a cert asked for under the
	// caller's own org is the exact miss this pins.
	if got := cap.urls[0]; got != "http://iam.test/v1/iam/applications/get?name=hanzo-cloud&owner="+PlatformOwner {
		t.Errorf("the application read asked %q; apps are platform-owned", got)
	}
	var ref Ref
	if err := json.Unmarshal([]byte(cap.sent[1]), &ref); err != nil {
		t.Fatalf("the cert read sent %q, which is not a key: %v", cap.sent[1], err)
	}
	if ref.Owner != PlatformOwner {
		t.Errorf("the cert read asked owner %q, want %q — certs are platform-owned, not the tenant's",
			ref.Owner, PlatformOwner)
	}
	if ref.Name != "cert-hanzo" {
		t.Errorf("the cert read asked name %q, want %q", ref.Name, "cert-hanzo")
	}
}
