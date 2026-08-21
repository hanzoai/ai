// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"bytes"
	"io"
	"net/http"
	"strings"
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
// It is asserted on the REQUEST URL rather than on a response, because the defect
// was never in what IAM returned — it was in which tenant we asked.
//
// The pair now travels as two SEPARATE parameters. IAM's noun routes take
// `?owner=&name=`; the joined `?id=<owner>/<name>` the verb surface accepted is
// not a parameter they have, so a request still spelling it states no owner at
// all and is refused for the missing key rather than for the old spelling.

// capture is an iam.HttpClient that records each request and answers a minimal
// body. The method is recorded alongside the URL because it is half of what
// IAM authorizes on.
type capture struct {
	urls    []string
	methods []string
	body    string
}

func (c *capture) Do(r *http.Request) (*http.Response, error) {
	c.urls = append(c.urls, r.URL.String())
	c.methods = append(c.methods, r.Method)
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

	for i, want := range [][]string{
		{"applications/get", "owner=admin", "name=hanzo-cloud"},
		{"certs/get", "owner=admin", "name=cert-hanzo"},
	} {
		for _, part := range want {
			if !strings.Contains(cap.urls[i], part) {
				t.Errorf("request %d asked %q, want it to contain %q — the cert and its\n"+
					"application must be addressed in the same (platform) partition", i, cap.urls[i], part)
			}
		}
	}
	// The tenant org must appear as NEITHER owner: a cert addressed under the
	// caller's own org is the exact miss this pins.
	for i, u := range cap.urls {
		if strings.Contains(u, "owner=hanzo") {
			t.Errorf("request %d (%q) addressed the TENANT org; certs and apps are platform-owned", i, u)
		}
	}
	// And the joined key is gone. It is not merely unused: `id` is not a
	// parameter these routes read, so sending one states no owner at all.
	for i, u := range cap.urls {
		if strings.Contains(u, "id=") {
			t.Errorf("request %d (%q) still carries the retired joined ?id= key", i, u)
		}
	}

	// Both reads are GETs. IAM weighs a request as a READ from its method, so the
	// same lookup posted is weighed as a write and the self-read grant an
	// application relies on to fetch its own signing cert does not fire — a 403
	// on bootstrap that reads like a permissions regression.
	for i, m := range cap.methods {
		if m != http.MethodGet {
			t.Errorf("request %d used %s; a record read must be a GET or it is authorized as a write", i, m)
		}
	}
}
