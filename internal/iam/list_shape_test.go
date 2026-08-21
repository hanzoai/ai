// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// A LIST ARRIVES NAMED, and reading it as a bare array fails silently.
//
// IAM's noun routes answer a listing with the collection under its own key —
// {"users":[…]}, {"providers":[…]}, {"permissions":[…]} — where the retired verb
// surface put a bare array in an envelope's `data`. Decoding the new body into a
// []*T is not an error: json finds no array at the top level, leaves the slice
// nil, and returns success. Every caller then reads an organization with no
// users, no storage provider and no permissions, and nothing anywhere reports a
// failure.
//
// That is the same defect the envelope change makes possible everywhere, caught
// at the one place it cannot announce itself. The single-record reads are safe by
// comparison — a record decoded from the wrong shape comes back zero-valued, and
// its callers already check the fields they need.

// answer is an iam.HttpClient that replies to every request with one body.
type answer struct{ body string }

func (a answer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(a.body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestListsDecodeTheNamedCollection(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		read func(*Client) (int, error)
	}{
		{
			"users", `{"users":[{"name":"a"},{"name":"b"}],"total":2}`,
			func(c *Client) (int, error) { u, err := c.GetUsers(); return len(u), err },
		},
		{
			"providers", `{"providers":[{"name":"s3","category":"Storage"}]}`,
			func(c *Client) (int, error) { p, err := c.GetProviders(); return len(p), err },
		},
		{
			"permissions", `{"permissions":[{"name":"p"}]}`,
			func(c *Client) (int, error) { p, err := c.GetPermissions(); return len(p), err },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetHttpClient(answer{body: tc.body})
			t.Cleanup(func() { SetHttpClient(&http.Client{}) })

			c := NewClient("http://iam.test", "hanzo-cloud", "secret", "", "hanzo", "hanzo-cloud")
			got, err := tc.read(c)
			if err != nil {
				t.Fatalf("read %s: %v", tc.name, err)
			}
			if got == 0 {
				t.Fatalf("%s decoded to an EMPTY list from %s — a named collection read as a\n"+
					"bare array yields nil with no error, and the caller concludes the\n"+
					"organization has none", tc.name, tc.body)
			}
		})
	}
}

// A refusal is a failure, not an empty collection. The verb surface answered 200
// whatever happened and carried its verdict in the body, so this client used to
// ignore the status entirely; against the noun routes that would turn a 403 into
// a confidently empty list.
func TestARefusalIsNotAnEmptyList(t *testing.T) {
	SetHttpClient(refuse{})
	t.Cleanup(func() { SetHttpClient(&http.Client{}) })

	c := NewClient("http://iam.test", "hanzo-cloud", "secret", "", "hanzo", "hanzo-cloud")
	users, err := c.GetUsers()
	if err == nil {
		t.Fatalf("a 403 read back as %d users and no error", len(users))
	}
	if !bytes.Contains([]byte(err.Error()), []byte("forbidden")) {
		t.Errorf("the failure must carry what IAM said, got: %v", err)
	}
}

type refuse struct{}

func (refuse) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":403,"error":"forbidden"}`))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}
