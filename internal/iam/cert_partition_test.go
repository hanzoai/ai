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

// capture is an iam.HttpClient that records the URL and answers a minimal body.
type capture struct {
	urls []string
	body string
}

func (c *capture) Do(r *http.Request) (*http.Response, error) {
	c.urls = append(c.urls, r.URL.String())
	body := c.body
	if body == "" {
		body = `{"status":"ok","data":{"name":"x"}}`
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

	for i, want := range []string{"id=admin%2Fhanzo-cloud", "id=admin%2Fcert-hanzo"} {
		if !strings.Contains(cap.urls[i], want) {
			t.Errorf("request %d asked %q, want it to contain %q — the cert and its\n"+
				"application must be addressed in the same (platform) partition", i, cap.urls[i], want)
		}
	}
	// The tenant org must appear in NEITHER: a cert addressed under the caller's
	// own org is the exact miss this pins.
	for i, u := range cap.urls {
		if strings.Contains(u, "hanzo%2F") || strings.Contains(u, "id=hanzo/") {
			t.Errorf("request %d (%q) addressed the TENANT org; certs and apps are platform-owned", i, u)
		}
	}
}
