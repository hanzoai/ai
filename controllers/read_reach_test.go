// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// These reads name a row by id and sit behind the coarse filter, which asks only
// whether the caller administers SOME organization — a thing every customer's own
// admin is. It never asks which one, so the id was the whole address. Each read
// asks its own row whose it is now, and answers a row it does not reach the way
// it answers a row that is not there.
func TestAByIdReadStaysInsideTheCallersOrg(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	mark := "kind-of-private-marker"
	for _, seed := range []struct {
		name  string
		write func() error
	}{
		{"template", func() error {
			_, e := object.AddTemplate(&object.Template{Owner: "victim", Name: "r", Manifest: mark})
			return e
		}},
		{"asset", func() error {
			_, e := object.AddAsset(&object.Asset{Owner: "victim", Name: "r", DisplayName: mark})
			return e
		}},
		{"form", func() error {
			_, e := object.AddForm(&object.Form{Owner: "victim", Name: "r", DisplayName: mark})
			return e
		}},
		{"graph", func() error { _, e := object.AddGraph(&object.Graph{Owner: "victim", Name: "r", Text: mark}); return e }},
		{"scan", func() error {
			_, e := object.AddScan(&object.Scan{Owner: "victim", Name: "r", DisplayName: mark})
			return e
		}},
		{"session", func() error { _, e := object.AddSession(&object.Session{Owner: "victim", Name: "r"}); return e }},
		{"vector", func() error {
			_, e := object.AddVector(&object.Vector{Owner: "victim", Name: "r", Text: mark})
			return e
		}},
		{"workflow", func() error {
			_, e := object.AddWorkflow(&object.Workflow{Owner: "victim", Name: "r", Text: mark}, "en")
			return e
		}},
		{"article", func() error {
			_, e := object.AddArticle(&object.Article{Owner: "victim", Name: "r", DisplayName: mark})
			return e
		}},
	} {
		if err := seed.write(); err != nil {
			t.Fatalf("seeding a %s: %v", seed.name, err)
		}
	}

	// An admin — of a DIFFERENT organization.
	mallory := people.signedIn(t, &iam.User{Owner: "acme", Name: "mallory", IsAdmin: true})

	for _, read := range []struct {
		route string
		call  func(*ApiController)
	}{
		{"get-template", (*ApiController).GetTemplate},
		{"get-asset", (*ApiController).GetAsset},
		{"get-form", (*ApiController).GetForm},
		{"get-graph", (*ApiController).GetGraph},
		{"get-scan", (*ApiController).GetScan},
		{"get-session", (*ApiController).GetSingleSession},
		{"get-vector", (*ApiController).GetVector},
		{"get-workflow", (*ApiController).GetWorkflow},
		{"get-article", (*ApiController).GetArticle},
	} {
		c := as(visit("GET", "/v1/ai/"+read.route+"?id=victim/r"), mallory)
		read.call(c)
		body := sent(c)
		if strings.Contains(body, mark) || strings.Contains(body, `"owner":"victim"`) {
			t.Errorf("%s answered another organization's row: %s", read.route, body)
		}
	}

	// Its own organization still reads it.
	owner := people.signedIn(t, &iam.User{Owner: "victim", Name: "val", IsAdmin: true})
	c := as(visit("GET", "/v1/ai/get-template?id=victim/r"), owner)
	c.GetTemplate()
	if !strings.Contains(sent(c), mark) {
		t.Errorf("the organization that owns it was refused: %s", sent(c))
	}
}
