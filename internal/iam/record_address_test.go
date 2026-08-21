// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"net/http"
	"testing"
)

// Every record operation is pinned to its exact request line, because IAM's
// surface is NOT uniform across collections and cannot be derived from one of
// them.
//
// What IAM registers: applications, permissions and users answer /get on a GET
// with the key in the query; certs answers ITS /get on a POST with the key in
// the body; organizations have no per-record route at all, only the older
// get-organization, which replies inside a {status, msg, data} envelope. The
// writes are POSTs to /update and /delete.
//
// A spelling this client invents for itself does not fail loudly. Addressing a
// record as path segments — certs/admin/cert-hanzo — 404s, and that is the good
// case: address the COLLECTION with parameters nothing reads and IAM answers
// about every record at 200, which no status check catches. So this asserts
// equality, not a substring: a partial match is exactly what each wrong spelling
// would still satisfy.
func TestEachRecordOperationAddressesItsKeyByMethodAndPath(t *testing.T) {
	for _, tc := range []struct {
		what   string
		call   func(*Client) error
		method string
		url    string
		reply  string
	}{
		{
			"read a cert", func(c *Client) error { _, err := c.GetCert("cert-hanzo"); return err },
			http.MethodPost, "http://iam.test/v1/iam/certs/get", "",
		},
		{
			"read an application", func(c *Client) error { _, err := c.GetApplication("hanzo-cloud"); return err },
			http.MethodGet, "http://iam.test/v1/iam/applications/get?name=hanzo-cloud&owner=admin", "",
		},
		{
			// The only read that is not a native route, and the only one whose
			// verdict is in the body: a miss is 200 with a null data.
			"read an organization", func(c *Client) error { _, err := c.GetOrganization("hanzo"); return err },
			http.MethodGet, "http://iam.test/v1/iam/get-organization?id=admin%2Fhanzo",
			`{"status":"ok","data":{"owner":"admin","name":"hanzo"}}`,
		},
		{
			"read a user", func(c *Client) error { _, err := c.GetUser("z"); return err },
			http.MethodGet, "http://iam.test/v1/iam/users/get?name=z&owner=hanzo", "",
		},
		{
			"read a permission", func(c *Client) error { _, err := c.GetPermission("p"); return err },
			http.MethodGet, "http://iam.test/v1/iam/permissions/get?name=p&owner=hanzo", "",
		},
		{
			"create a permission", func(c *Client) error { _, err := c.AddPermission(&Permission{Name: "p"}); return err },
			http.MethodPost, "http://iam.test/v1/iam/permissions", "",
		},
		{
			"replace a permission", func(c *Client) error { _, err := c.UpdatePermission(&Permission{Name: "p"}); return err },
			http.MethodPost, "http://iam.test/v1/iam/permissions/update", "",
		},
		{
			"remove a permission", func(c *Client) error { _, err := c.DeletePermission(&Permission{Name: "p"}); return err },
			http.MethodPost, "http://iam.test/v1/iam/permissions/delete", "",
		},
		{
			"write a user", func(c *Client) error { return c.UpdateUser(&User{Owner: "acme", Name: "z"}) },
			http.MethodPost, "http://iam.test/v1/iam/users/update", "",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			cap := &capture{body: tc.reply}
			SetHttpClient(cap)
			t.Cleanup(func() { SetHttpClient(&http.Client{}) })

			c := NewClient("http://iam.test", "hanzo-cloud", "secret", "", "hanzo", "hanzo-cloud")
			if err := tc.call(c); err != nil {
				t.Fatalf("%s: %v", tc.what, err)
			}
			if len(cap.urls) != 1 {
				t.Fatalf("want 1 request, got %d: %v", len(cap.urls), cap.urls)
			}
			if cap.methods[0] != tc.method {
				t.Errorf("%s used %s, want %s — IAM registers this route on %s only,\n"+
					"so the other verb is a 404", tc.what, cap.methods[0], tc.method, tc.method)
			}
			if cap.urls[0] != tc.url {
				t.Errorf("%s asked %q, want %q", tc.what, cap.urls[0], tc.url)
			}
		})
	}
}
