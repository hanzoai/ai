// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// A listing answers a page size it cannot read, rather than crashing on it.
//
// pageSize arrives on the request and was handed to a parser that panicked, so
// "?pageSize=abc" reached the router's recover and came back a 500 with a stack
// trace — on every paged listing in the module, HTTP and ZAP alike. This drives
// the ZAP side because it is reachable without a router.
func TestAListingSurvivesAPageSizeItCannotRead(t *testing.T) {
	withStore(t)
	seedDefaultStore(t)
	iamd := withIAM(t)
	admin := iamd.asUser(t, &iam.User{Owner: "acme", Name: "boss", IsAdmin: true})

	for _, body := range []string{
		`{"pageSize":"abc","p":"1"}`,
		`{"pageSize":"10","p":"xyz"}`,
		`{"pageSize":"-1","p":"-1"}`,
		`{"pageSize":"1.5","p":"1"}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, ok := lookupCloudHandler("forms.list")
			if !ok {
				t.Fatal("forms.list is not registered")
			}
			msg, err := h(context.Background(), admin, []byte(body))
			if err != nil {
				t.Fatalf("answered an error rather than a page: %v", err)
			}
			if got := msg.Root().Uint32(object.CloudRespStatus); got != 200 {
				t.Errorf("status = %d, want 200 — an unreadable page size is not a failure", got)
			}
		})
	}
}
