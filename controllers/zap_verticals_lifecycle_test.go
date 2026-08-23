// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"encoding/json"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// Forms, articles and scales are three of the same shape — a row owned by an
// org, added, listed, renamed and removed — so they are asserted as one table
// rather than three near-identical tests that drift apart.
//
// Every step is checked against the NEXT read: a write that reports success and
// stores nothing answers exactly like one that works, and the listing returning
// to empty is what makes a delete a delete.
func TestTheVerticalRowsLiveAndDieThroughTheirHandlers(t *testing.T) {
	for _, tc := range []struct {
		what                          string
		list, get, add, update, del   string
		row, renamed                  string
	}{
		{
			what: "a form",
			list: "forms.list", get: "form.get", add: "form.add", update: "form.update", del: "form.delete",
			row:     `{"owner":"acme","name":"f1","displayName":"First form"}`,
			renamed: `{"owner":"acme","name":"f1","displayName":"Renamed form"}`,
		},
		{
			what: "an article",
			list: "articles.list", get: "article.get", add: "article.add", update: "article.update", del: "article.delete",
			row:     `{"owner":"acme","name":"a1","displayName":"First article"}`,
			renamed: `{"owner":"acme","name":"a1","displayName":"Renamed article"}`,
		},
		{
			what: "a scale",
			list: "scales.list", get: "scale.get", add: "scale.add", update: "scale.update", del: "scale.delete",
			row:     `{"owner":"acme","name":"sc1","displayName":"First scale"}`,
			renamed: `{"owner":"acme","name":"sc1","displayName":"Renamed scale"}`,
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			withStore(t)
			iamd := withIAM(t)
			// Writes here are admin-gated, so the lifecycle is driven by an admin
			// of the org and the refusal for everyone else is asserted first.
			plain := iamd.asUser(t, &iam.User{Owner: "acme", Name: "alice"})
			if status, _ := call(t, tc.add, plain, tc.row); status != 403 {
				t.Errorf("%s answered %d to a non-admin, want 403", tc.add, status)
			}

			auth := iamd.asUser(t, &iam.User{Owner: "acme", Name: "boss", IsAdmin: true})

			_, env := call(t, tc.add, auth, tc.row)
			env.ok(t, "add")

			_, env = call(t, tc.list, auth, `{}`)
			env.ok(t, "list")
			var rows []map[string]any
			if err := json.Unmarshal(env.Data, &rows); err != nil {
				t.Fatalf("list data: %v (%s)", err, env.Data)
			}
			if len(rows) != 1 {
				t.Fatalf("list returned %d rows (%s), want the one just added", len(rows), env.Data)
			}
			// It was filed into the caller's own org, which is the org the listing reads.
			if rows[0]["owner"] != "acme" {
				t.Errorf("owner = %v, want the caller's own org", rows[0]["owner"])
			}

			_, env = call(t, tc.update, auth, tc.renamed)
			env.ok(t, "update")
			_, env = call(t, tc.list, auth, `{}`)
			env.ok(t, "list after update")
			rows = nil
			_ = json.Unmarshal(env.Data, &rows)
			if len(rows) != 1 || rows[0]["displayName"] == "First form" {
				t.Errorf("after update the stored row is %v", rows)
			}

			_, env = call(t, tc.del, auth, tc.row)
			env.ok(t, "delete")
			_, env = call(t, tc.list, auth, `{}`)
			env.ok(t, "list after delete")
			rows = nil
			_ = json.Unmarshal(env.Data, &rows)
			if len(rows) != 0 {
				t.Errorf("after delete the listing still holds %d: %v", len(rows), rows)
			}
		})
	}
}
