// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"net/http"
	"testing"
)

// THREE THINGS MOVE TOGETHER, and only the first is a rename.
//
// IAM retired the verb segments from its paths: `certs/get` became
// GET /v1/iam/certs/<owner>/<name>, `permissions/update` became a PUT there,
// `permissions/delete` a DELETE. Converting the ADDRESS alone leaves two live
// defects, and neither shows up as a failure:
//
// THE QUERY IT WAS SCOPED BY. `?owner=&name=` used to say which record. These
// routes do not read it, so a caller that keeps sending it addresses the
// COLLECTION — and gets answered, at 200, about every record instead of the one
// it named. There is no status to check.
//
// THE METHOD. IAM decides read-from-write BY METHOD. A read shaped as a POST is
// weighed as a write, so a read-scoped grant does not fire and the answer is
// 403 — which reads like a permissions regression while the only thing wrong is
// the verb.
//
// So this asserts the exact request line for every record operation. Equality,
// not a substring: a partial match is precisely what each of the wrong spellings
// above would still satisfy.
func TestEachRecordOperationAddressesItsKeyByMethodAndPath(t *testing.T) {
	for _, tc := range []struct {
		what   string
		call   func(*Client) error
		method string
		url    string
	}{
		{
			"read a cert", func(c *Client) error { _, err := c.GetCert("cert-hanzo"); return err },
			http.MethodGet, "http://iam.test/v1/iam/certs/admin/cert-hanzo",
		},
		{
			"read an application", func(c *Client) error { _, err := c.GetApplication("hanzo-cloud"); return err },
			http.MethodGet, "http://iam.test/v1/iam/applications/admin/hanzo-cloud",
		},
		{
			"read an organization", func(c *Client) error { _, err := c.GetOrganization("hanzo"); return err },
			http.MethodGet, "http://iam.test/v1/iam/organizations/admin/hanzo",
		},
		{
			"read a user", func(c *Client) error { _, err := c.GetUser("z"); return err },
			http.MethodGet, "http://iam.test/v1/iam/users/hanzo/z",
		},
		{
			"read a permission", func(c *Client) error { _, err := c.GetPermission("p"); return err },
			http.MethodGet, "http://iam.test/v1/iam/permissions/hanzo/p",
		},
		{
			"create a permission", func(c *Client) error { _, err := c.AddPermission(&Permission{Name: "p"}); return err },
			http.MethodPost, "http://iam.test/v1/iam/permissions",
		},
		{
			"replace a permission", func(c *Client) error { _, err := c.UpdatePermission(&Permission{Name: "p"}); return err },
			http.MethodPut, "http://iam.test/v1/iam/permissions/hanzo/p",
		},
		{
			"remove a permission", func(c *Client) error { _, err := c.DeletePermission(&Permission{Name: "p"}); return err },
			http.MethodDelete, "http://iam.test/v1/iam/permissions/hanzo/p",
		},
		{
			// The exception IAM states, not one taken here: its update input nests
			// the record under `user`, and a path segment binds only onto a
			// top-level field. Pinned so the day that input carries the key at top
			// level, this is the one line that has to move.
			"write a user", func(c *Client) error { return c.UpdateUser(&User{Name: "z"}) },
			http.MethodPost, "http://iam.test/v1/iam/users/update",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			cap := &capture{}
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
				t.Errorf("%s used %s, want %s — IAM authorizes on the method, so the wrong\n"+
					"one is a 403 that reads like a permissions regression",
					tc.what, cap.methods[0], tc.method)
			}
			if cap.urls[0] != tc.url {
				t.Errorf("%s asked %q, want %q", tc.what, cap.urls[0], tc.url)
			}
		})
	}
}
